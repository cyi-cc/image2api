package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"strconv"

	"backend/internal/config"
	"backend/internal/model"
	"backend/internal/provider/adobe"
	"backend/internal/provider/chatgpt"
	"backend/internal/provider/custom"
	"backend/internal/provider/grok"
	"backend/internal/provider/imagine"
	"backend/internal/provider/krea"
	"backend/internal/provider/leonardo"
	"backend/internal/provider/runway"
	"backend/internal/repo"
	"backend/internal/storage"
	"gorm.io/gorm"
)

var (
	ErrMissingAPIKey       = errors.New("missing api key")
	ErrInvalidAPIKey       = errors.New("invalid api key")
	ErrUnknownModel        = errors.New("unknown model")
	ErrUnsupportedParams   = errors.New("unsupported or unpriced parameters for this model")
	ErrInsufficientFunds   = errors.New("insufficient credits")
	ErrGenerationPending   = errors.New("generation executor not implemented yet")
	ErrProviderAuth        = errors.New("provider token invalid or expired")
	ErrNoProviderAccount   = errors.New("no provider account available, please ask an admin to configure one")
	ErrProviderQuota       = errors.New("provider quota exhausted")
	ErrProviderTemporary   = errors.New("provider temporary unavailable")
	ErrProviderExecution   = errors.New("provider request failed")
	ErrProviderUnsupported = errors.New("provider not implemented")
	ErrReferenceTooLarge   = errors.New("reference image too large")
	// ErrConcurrencyFull — every eligible account is busy (each account runs at
	// most ONE generation at a time). English message: surfaced to API / UI.
	ErrConcurrencyFull = errors.New("all accounts are busy (1 concurrent job each), please try again shortly")
	// ErrUserConcurrencyFull — the caller already has their concurrency-group's max
	// generations in flight (画图台 + API key combined). 0 = unlimited.
	ErrUserConcurrencyFull = errors.New("too many generations in progress, please wait for one to finish")
	// ErrVideoJobNotFound / ErrVideoNotReady — /v1/videos async job lookups.
	ErrVideoJobNotFound = errors.New("video job not found")
	ErrVideoNotReady    = errors.New("video is not ready yet")
)

// maxReferenceImageBytes bounds a single decoded reference image. 8 MB
// comfortably covers real photos/screenshots; anything larger is almost
// certainly abuse or a mistake. Mirrors Python core/refs.py.
const maxReferenceImageBytes = 8 * 1024 * 1024

type V1Service struct {
	cfg      *config.Config
	models   *repo.ModelRepository
	users    *repo.UserRepository
	events   *repo.EventRepository
	tokens   *repo.TokenRepository
	settings *repo.SiteSettingRepository
	cgroups  *repo.ConcurrencyGroupRepository
	adobe    *adobe.Client
	chatgpt  *chatgpt.Client
	runway   *runway.Client
	leonardo *leonardo.Client
	krea     *krea.Client
	imagine  *imagine.Client
	grok     *grok.Client
	custom   *custom.Client
	store    *storage.Client
	// refresh re-mints an Adobe access token from its cookie when a request hits a
	// 401 mid-flight (set via SetRefresh — wired after construction to avoid an
	// init cycle). nil for deployments without cookie refresh.
	refresh *RefreshProfileService

	// tokenCursors holds one strict round-robin cursor per pool (key: pool name,
	// value: *uint64). Each pick advances the pool's cursor by one so accounts
	// are used in a fixed, even rotation (acct1→acct2→acct3→acct1…) independent
	// of fails/last_used. The atomic counter also serializes concurrent picks so
	// two simultaneous requests never start on the same account.
	tokenCursors sync.Map

	// inflight maps an in-progress event ID → the cancel func of its generation
	// work context, so the maintenance sweep can stop a stuck generation the
	// moment it abandons the row (instead of letting an orphaned goroutine run on
	// for minutes and surface a late "success" on an already-abandoned event).
	inflight *InflightRegistry

	// conc is the Redis-backed concurrency limiter for BOTH the per-account
	// upstream gate (1+ jobs per account) and the per-user gate (画图台 + API key,
	// capped by the user's concurrency group). Self-healing + fail-open.
	conc *ConcurrencyService
}

// acctAcquire takes one per-account upstream slot (capped at max; 0/1 = single),
// tagged with the generation's eventID (unique per job; a generation only ever
// holds one slot on a given account at a time, so failover reuses it cleanly).
func (s *V1Service) acctAcquire(ctx context.Context, accountID, eventID string, max int) bool {
	if max < 1 {
		max = 1
	}
	return s.conc.Acquire(ctx, "conc:a:"+accountID, max, eventID)
}

func (s *V1Service) acctRelease(ctx context.Context, accountID, eventID string) {
	s.conc.Release(ctx, "conc:a:"+accountID, eventID)
}

// userAcquire takes one per-user generation slot, capped by the user's
// concurrency group (0 = unlimited). Returns false when the user is already at
// their limit. `token` is a unique per-generation tag passed back to userRelease.
func (s *V1Service) userAcquire(ctx context.Context, user *model.User, token string) bool {
	if user == nil {
		return true
	}
	return s.conc.Acquire(ctx, "conc:u:"+user.ID, s.userConcurrencyLimit(ctx, user), token)
}

func (s *V1Service) userRelease(ctx context.Context, userID, token string) {
	s.conc.Release(ctx, "conc:u:"+userID, token)
}

// userConcurrencyLimit resolves the user's concurrency-group cap (0 = unlimited),
// falling back to the default group when unset/missing.
func (s *V1Service) userConcurrencyLimit(ctx context.Context, user *model.User) int {
	if s.cgroups == nil || user == nil {
		return 0
	}
	var g *model.ConcurrencyGroup
	if user.ConcurrencyGroupID != "" {
		g, _ = s.cgroups.Get(ctx, user.ConcurrencyGroupID)
	}
	if g == nil {
		g, _ = s.cgroups.GetDefault(ctx)
	}
	if g == nil {
		return 0
	}
	return g.MaxConcurrency
}

// InflightRegistry tracks the cancel func of every in-progress generation by
// event ID. The generation registers on start and removes on finish; the
// maintenance sweep calls Cancel when it gives up on (abandons) an event.
type InflightRegistry struct {
	m sync.Map // eventID -> context.CancelFunc
}

func (r *InflightRegistry) Add(eventID string, cancel context.CancelFunc) {
	if eventID != "" {
		r.m.Store(eventID, cancel)
	}
}

// Done deregisters an event (called on normal completion).
func (r *InflightRegistry) Done(eventID string) { r.m.Delete(eventID) }

// Cancel stops an in-flight generation by event ID. Returns true if one was
// running and got cancelled. No-op (false) if it already finished.
func (r *InflightRegistry) Cancel(eventID string) bool {
	if v, ok := r.m.LoadAndDelete(eventID); ok {
		v.(context.CancelFunc)()
		return true
	}
	return false
}

type APIPrincipal struct {
	User      *model.User
	TokenType string
}

type V1ImageRequest struct {
	Model  string
	Prompt string
	Size   string
	// Quality is OpenAI's image quality (low|medium|high|auto). For our tiered
	// models it selects the resolution (low→1K, medium→2K, high→4K, auto→default),
	// clamped to whatever tiers the model actually prices. Only used when
	// Resolution is left blank (the strict /v1 OpenAI path); the playground passes
	// Resolution directly and ignores this.
	Quality         string
	AspectRatio     string
	Resolution      string
	N               int
	ReferenceImages []string
	// BaseURL is the scheme+host of the inbound request (e.g. "https://host"),
	// used to build absolute, directly-downloadable output URLs. Empty falls
	// back to a relative "/images/..." path.
	BaseURL string
}

type V1VideoRequest struct {
	Model           string
	Prompt          string
	Duration        string
	AspectRatio     string
	Resolution      string
	ReferenceImages []string
	// BaseURL — see V1ImageRequest.BaseURL.
	BaseURL string
}

func NewV1Service(cfg *config.Config, models *repo.ModelRepository, users *repo.UserRepository, events *repo.EventRepository, tokens *repo.TokenRepository, settings *repo.SiteSettingRepository, cgroups *repo.ConcurrencyGroupRepository, conc *ConcurrencyService, adobeClient *adobe.Client, chatGPTClient *chatgpt.Client, runwayClient *runway.Client, leonardoClient *leonardo.Client, kreaClient *krea.Client, imagineClient *imagine.Client, grokClient *grok.Client, customClient *custom.Client, store *storage.Client) *V1Service {
	return &V1Service{
		cfg:      cfg,
		models:   models,
		users:    users,
		events:   events,
		tokens:   tokens,
		settings: settings,
		cgroups:  cgroups,
		conc:     conc,
		adobe:    adobeClient,
		chatgpt:  chatGPTClient,
		runway:   runwayClient,
		leonardo: leonardoClient,
		krea:     kreaClient,
		imagine:  imagineClient,
		grok:     grokClient,
		custom:   customClient,
		store:    store,
		inflight: &InflightRegistry{},
	}
}

// Inflight exposes the registry so the maintenance sweep can cancel a stuck
// generation when it abandons that event.
func (s *V1Service) Inflight() *InflightRegistry { return s.inflight }

// SetRefresh wires the Adobe cookie-refresh service in after construction
// (RefreshProfileService is built later in bootstrap, so it can't be a ctor arg
// without reordering). Enables refresh-then-retry on a mid-request 401.
func (s *V1Service) SetRefresh(r *RefreshProfileService) { s.refresh = r }

// refreshAdobeToken re-mints an Adobe account's access token from its cookie
// (RefreshNow) and returns the updated row. Used to retry a 401 with a fresh
// token instead of replaying the stale one. Returns false if refresh is
// unavailable or the cookie can no longer mint a token (genuinely dead).
func (s *V1Service) refreshAdobeToken(ctx context.Context, tokenID string) (model.TokenAccount, bool) {
	if s.refresh == nil {
		return model.TokenAccount{}, false
	}
	if err := s.refresh.RefreshNow(ctx, tokenID); err != nil {
		return model.TokenAccount{}, false
	}
	t, err := s.tokens.Get(ctx, "adobe", tokenID)
	if err != nil || t == nil {
		return model.TokenAccount{}, false
	}
	return *t, true
}

func (s *V1Service) Authenticate(ctx context.Context, authHeader string) (*APIPrincipal, error) {
	token := ParseBearer(authHeader)
	if token == "" {
		return nil, ErrMissingAPIKey
	}

	// Only per-user API keys (hashed in the DB) authenticate to /v1. The old
	// global/shared API_KEY backdoor has been removed.
	user, err := s.users.GetByAPIKeyHash(ctx, HashAPIKey(token))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidAPIKey
		}
		return nil, err
	}
	if user.Status != "active" {
		return nil, ErrInvalidAPIKey
	}
	_ = s.users.TouchAPIKeyUsage(ctx, HashAPIKey(token))
	return &APIPrincipal{
		User:      user,
		TokenType: "user",
	}, nil
}

func (s *V1Service) ListModels(ctx context.Context) ([]map[string]any, error) {
	items, err := s.models.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"id":                    item.EffectiveName(),
			"object":                "model",
			"created":               now,
			"owned_by":              item.Provider,
			"kind":                  item.Type,
			"supported_ratios":      repo.JSONStrings(item.Ratios),
			"supported_resolutions": repo.JSONStrings(item.Resolutions),
		})
	}
	return out, nil
}

func (s *V1Service) PrepareImageRequest(ctx context.Context, principal *APIPrincipal, in V1ImageRequest) (map[string]any, error) {
	return s.prepareImageExecution(ctx, principal, in, "v1", true)
}

func (s *V1Service) prepareSessionImage(ctx context.Context, principal *APIPrincipal, in V1ImageRequest) (map[string]any, error) {
	return s.prepareImageExecution(ctx, principal, in, "user", true)
}

func (s *V1Service) prepareAdminTestImage(ctx context.Context, principal *APIPrincipal, in V1ImageRequest) (map[string]any, error) {
	return s.prepareImageExecution(ctx, principal, in, "admin", false)
}

func (s *V1Service) prepareImageExecution(ctx context.Context, principal *APIPrincipal, in V1ImageRequest, source string, charge bool) (map[string]any, error) {
	// Detach the whole execution from the request lifecycle. The frontend tracks
	// progress by polling /jobs/mine, so a client disconnect — or an nginx/CDN
	// gateway timeout on the slow synchronous response — must NOT cancel an
	// in-flight generation. Binding to the request ctx meant a cancelled request
	// (a) spun uselessly in the upstream poll until its 180s timeout and
	// (b) silently dropped the refund + final status write, leaving the row stuck
	// pending until the maintenance sweep mislabeled it "abandoned".
	//
	// `ctx` (WithoutCancel) is durable and used for ALL bookkeeping (status /
	// refund / cleanup) so those always land. `genCtx` is the cancellable WORK
	// context: an 8-min backstop, AND registered in s.inflight so the maintenance
	// sweep can cancel it the instant it abandons the row — stopping a stuck
	// generation from running on for minutes and surfacing a late "success" on an
	// already-abandoned event.
	ctx = context.WithoutCancel(ctx)
	genCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	// Per-user concurrency gate (画图台 + API key combined). Admin model-tests are
	// exempt. Held for the whole generation; released on return.
	if source != "admin" && principal != nil && principal.User != nil {
		slot := randomUpper(12)
		if !s.userAcquire(ctx, principal.User, slot) {
			return nil, ErrUserConcurrencyFull
		}
		defer s.userRelease(ctx, principal.User.ID, slot)
	}

	modelItem, resolution, aspectRatio, price, err := s.prepareImage(ctx, principal, in, charge)
	if err != nil {
		return nil, err
	}
	refCount := len(in.ReferenceImages)
	refFiles := s.saveReferenceImages(ctx, principal, in.ReferenceImages)
	// API-key (source "v1") requests don't persist the output: we return the image
	// as base64 inline (OpenAI gpt-image-1 also returns only b64_json) and never
	// upload to RustFS, so there's no URL. The event is still logged (empty file)
	// for usage; the customer logs page hides source="v1" rows.
	noStore := source == "v1"
	var fileURL, relativePath string
	if !noStore {
		fileURL, relativePath = s.allocateOutput(principal, "png", in.BaseURL)
	}
	eventID, err := s.logPendingEvent(ctx, "image", modelItem, principal, in.Prompt, aspectRatio, resolution, "", refCount, price, relativePath, source, refFiles)
	if err != nil {
		s.cleanupReferenceImages(ctx, "", refFiles)
		return nil, err
	}
	// Register so the maintenance sweep can cancel this generation if it abandons
	// the row; deregister on return.
	s.inflight.Add(eventID, cancel)
	defer s.inflight.Done(eventID)
	// Reference images are transient — remove them (and clear the event's ref
	// paths) once this attempt finishes, whether it succeeds OR fails.
	defer s.cleanupReferenceImages(ctx, eventID, refFiles)
	startedAt := time.Now()

	var imageBytes []byte
	switch s.effectiveProvider(genCtx, modelItem) {
	case "adobe":
		b, execErr := s.generateAdobeImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, adobe.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, adobe.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, adobe.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "chatgpt":
		b, execErr := s.generateChatGPTImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, chatgpt.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, chatgpt.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, chatgpt.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "leonardo":
		b, execErr := s.generateLeonardoImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, leonardo.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, leonardo.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, leonardo.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "krea":
		b, execErr := s.generateKreaImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, krea.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, krea.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, krea.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "imagine":
		b, execErr := s.generateImagineImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, imagine.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, imagine.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, imagine.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "runway":
		b, execErr := s.generateRunwayImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, runway.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, runway.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, runway.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	case "custom":
		b, execErr := s.generateCustomImage(genCtx, eventID, modelItem, in, aspectRatio, resolution)
		if execErr != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
			switch {
			case errors.Is(execErr, custom.ErrAuth):
				return nil, ErrProviderAuth
			case errors.Is(execErr, custom.ErrQuotaExhausted):
				return nil, ErrProviderQuota
			case errors.Is(execErr, custom.ErrTemporaryUpstream):
				return nil, ErrProviderTemporary
			default:
				return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
			}
		}
		imageBytes = b
	default:
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", "provider not implemented", 0)
		return nil, fmt.Errorf("%w: %s", ErrProviderUnsupported, modelItem.Provider)
	}
	if !noStore {
		// Upload to RustFS. On failure the generation fails and credits are
		// refunded — we never fall back to local disk.
		if err := s.store.Put(genCtx, relativePath, imageBytes, "image/png"); err != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", "storage upload failed: "+err.Error(), 0)
			return nil, fmt.Errorf("%w: %v", ErrProviderExecution, err)
		}
		// Best-effort thumbnail for list views; the image serving route falls
		// back to the original when the thumb object is missing.
		if thumb, terr := makeThumbnail(imageBytes); terr == nil {
			_ = s.store.Put(genCtx, ThumbKey(relativePath), thumb, "image/jpeg")
		}
	}
	elapsedMS := int(time.Since(startedAt).Milliseconds())
	if err := s.events.UpdateStatus(ctx, eventID, "success", "", elapsedMS); err != nil {
		return nil, err
	}
	_ = s.models.IncrementGenerationCount(ctx, modelItem.ID)
	if principal != nil && principal.User != nil {
		_ = s.users.IncrementGenerationCount(ctx, principal.User.ID)
	}
	if charge {
		_ = s.maybeGrantInviteReward(ctx, principal)
	}
	if noStore {
		b64 := base64.StdEncoding.EncodeToString(imageBytes)
		return map[string]any{
			"created":    time.Now().Unix(),
			"data":       []map[string]any{{"b64_json": b64}},
			"model":      modelItem.EffectiveName(),
			"provider":   modelItem.Provider,
			"kind":       "image",
			"b64_json":   b64,
			"elapsed_ms": elapsedMS,
			"charged":    price,
			"credits":    principalCredits(principal),
		}, nil
	}
	return map[string]any{
		"created":    time.Now().Unix(),
		"data":       []map[string]any{{"url": fileURL, "b64_json": nil}},
		"model":      modelItem.EffectiveName(),
		"provider":   modelItem.Provider,
		"kind":       "image",
		"url":        fileURL,
		"elapsed_ms": elapsedMS,
		"charged":    price,
		"credits":    principalCredits(principal),
	}, nil
}

func (s *V1Service) PrepareVideoRequest(ctx context.Context, principal *APIPrincipal, in V1VideoRequest) (map[string]any, error) {
	return s.prepareVideoExecution(ctx, principal, in, "v1", true)
}

func (s *V1Service) prepareSessionVideo(ctx context.Context, principal *APIPrincipal, in V1VideoRequest) (map[string]any, error) {
	return s.prepareVideoExecution(ctx, principal, in, "user", true)
}

func (s *V1Service) prepareAdminTestVideo(ctx context.Context, principal *APIPrincipal, in V1VideoRequest) (map[string]any, error) {
	return s.prepareVideoExecution(ctx, principal, in, "admin", false)
}

func (s *V1Service) prepareVideoExecution(ctx context.Context, principal *APIPrincipal, in V1VideoRequest, source string, charge bool) (map[string]any, error) {
	// Detach from the request lifecycle — see prepareImageExecution. `ctx`
	// (WithoutCancel) carries all bookkeeping; `genCtx` is the cancellable work
	// context (12-min backstop — video polls up to 10 min — and registered so the
	// maintenance sweep can cancel a stuck render when it abandons the row).
	ctx = context.WithoutCancel(ctx)
	genCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()

	// Per-user concurrency gate (画图台 + API key combined); admin tests exempt.
	if source != "admin" && principal != nil && principal.User != nil {
		slot := randomUpper(12)
		if !s.userAcquire(ctx, principal.User, slot) {
			return nil, ErrUserConcurrencyFull
		}
		defer s.userRelease(ctx, principal.User.ID, slot)
	}

	modelItem, resolution, aspectRatio, duration, price, err := s.prepareVideo(ctx, principal, in, charge)
	if err != nil {
		return nil, err
	}
	refCount := len(in.ReferenceImages)
	refFiles := s.saveReferenceImages(ctx, principal, in.ReferenceImages)
	// API-key (source "v1") requests return base64 inline and never persist a
	// file — see prepareImageExecution for the rationale.
	noStore := source == "v1"
	var fileURL, relativePath string
	if !noStore {
		fileURL, relativePath = s.allocateOutput(principal, "mp4", in.BaseURL)
	}
	eventID, err := s.logPendingEvent(ctx, "video", modelItem, principal, in.Prompt, aspectRatio, resolution, duration, refCount, price, relativePath, source, refFiles)
	if err != nil {
		s.cleanupReferenceImages(ctx, "", refFiles)
		return nil, err
	}
	// Register so the maintenance sweep can cancel this render if it abandons the
	// row; deregister on return.
	s.inflight.Add(eventID, cancel)
	defer s.inflight.Done(eventID)
	// Frame / reference images are transient — clean up on success OR failure.
	defer s.cleanupReferenceImages(ctx, eventID, refFiles)
	startedAt := time.Now()

	var videoBytes []byte
	var execErr error
	switch s.effectiveProvider(genCtx, modelItem) {
	case "adobe":
		videoBytes, _, execErr = s.generateAdobeVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), true)
	case "runway":
		videoBytes, _, execErr = s.generateRunwayVideo(genCtx, eventID, modelItem, in, aspectRatio, parseDurationSeconds(duration), true)
	case "grok":
		videoBytes, _, execErr = s.generateGrokVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), true)
	case "custom":
		videoBytes, _, execErr = s.generateCustomVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), true)
	default:
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", "provider not implemented", 0)
		return nil, fmt.Errorf("%w: %s", ErrProviderUnsupported, modelItem.Provider)
	}
	if execErr != nil {
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
		switch {
		case errors.Is(execErr, ErrNoProviderAccount):
			return nil, ErrNoProviderAccount
		case errors.Is(execErr, adobe.ErrAuth), errors.Is(execErr, runway.ErrAuth), errors.Is(execErr, grok.ErrAuth), errors.Is(execErr, custom.ErrAuth):
			return nil, ErrProviderAuth
		case errors.Is(execErr, adobe.ErrQuotaExhausted), errors.Is(execErr, runway.ErrQuotaExhausted), errors.Is(execErr, grok.ErrQuotaExhausted), errors.Is(execErr, custom.ErrQuotaExhausted):
			return nil, ErrProviderQuota
		case errors.Is(execErr, adobe.ErrTemporaryUpstream), errors.Is(execErr, runway.ErrTemporaryUpstream), errors.Is(execErr, grok.ErrTemporaryUpstream), errors.Is(execErr, custom.ErrTemporaryUpstream):
			return nil, ErrProviderTemporary
		default:
			return nil, fmt.Errorf("%w: %v", ErrProviderExecution, execErr)
		}
	}
	if !noStore {
		if err := s.store.Put(genCtx, relativePath, videoBytes, "video/mp4"); err != nil {
			_ = s.refundIfNeeded(ctx, principal, eventID, price)
			_ = s.events.UpdateStatus(ctx, eventID, "failed", "storage upload failed: "+err.Error(), 0)
			return nil, fmt.Errorf("%w: %v", ErrProviderExecution, err)
		}
		// Best-effort stills: first frame (downscaled) for list thumbnails and
		// the full-res last frame for 首尾帧 continuation. Missing objects fall
		// back to the video itself at serve time.
		if thumb, last, terr := extractVideoFrames(genCtx, videoBytes); terr == nil {
			if len(thumb) > 0 {
				_ = s.store.Put(genCtx, ThumbKey(relativePath), thumb, "image/jpeg")
			}
			if len(last) > 0 {
				_ = s.store.Put(genCtx, LastFrameKey(relativePath), last, "image/jpeg")
			}
		}
	}
	elapsedMS := int(time.Since(startedAt).Milliseconds())
	if err := s.events.UpdateStatus(ctx, eventID, "success", "", elapsedMS); err != nil {
		return nil, err
	}
	_ = s.models.IncrementGenerationCount(ctx, modelItem.ID)
	if principal != nil && principal.User != nil {
		_ = s.users.IncrementGenerationCount(ctx, principal.User.ID)
	}
	if charge {
		_ = s.maybeGrantInviteReward(ctx, principal)
	}
	if noStore {
		b64 := base64.StdEncoding.EncodeToString(videoBytes)
		return map[string]any{
			"created":    time.Now().Unix(),
			"data":       []map[string]any{{"b64_json": b64}},
			"model":      modelItem.EffectiveName(),
			"provider":   modelItem.Provider,
			"kind":       "video",
			"b64_json":   b64,
			"elapsed_ms": elapsedMS,
			"charged":    price,
			"credits":    principalCredits(principal),
		}, nil
	}
	return map[string]any{
		"created":    time.Now().Unix(),
		"data":       []map[string]any{{"url": fileURL}},
		"model":      modelItem.EffectiveName(),
		"provider":   modelItem.Provider,
		"kind":       "video",
		"url":        fileURL,
		"elapsed_ms": elapsedMS,
		"charged":    price,
		"credits":    principalCredits(principal),
	}, nil
}

// ===== /v1/videos — OpenAI Sora-style async jobs =====
// POST /v1/videos charges + creates a pending event and renders in the
// background; the render captures only the UPSTREAM video URL (no download, no
// RustFS). GET /v1/videos/{id} polls status; /content proxies the upstream URL.

// StartVideoJob validates+charges, creates the job event, kicks the render off in
// the background, and returns the OpenAI video object (status "queued").
func (s *V1Service) StartVideoJob(ctx context.Context, principal *APIPrincipal, in V1VideoRequest) (map[string]any, error) {
	ctx = context.WithoutCancel(ctx)
	modelItem, resolution, aspectRatio, duration, price, err := s.prepareVideo(ctx, principal, in, true)
	if err != nil {
		return nil, err
	}
	refFiles := s.saveReferenceImages(ctx, principal, in.ReferenceImages)
	// Source "v1": no output file is allocated — the result is the upstream URL,
	// stored on the event when the render completes.
	eventID, err := s.logPendingEvent(ctx, "video", modelItem, principal, in.Prompt, aspectRatio, resolution, duration, len(in.ReferenceImages), price, "", "v1", refFiles)
	if err != nil {
		s.cleanupReferenceImages(ctx, "", refFiles)
		return nil, err
	}
	go s.runVideoJob(ctx, principal, in, modelItem, eventID, aspectRatio, resolution, duration, price, refFiles)
	return videoJobObject(eventID, modelItem.EffectiveName(), "queued", 0, duration, sizeFromRatioRes(aspectRatio, resolution), time.Now().Unix(), 0, ""), nil
}

// runVideoJob renders the clip in the background, capturing the upstream URL
// (downloadResult=false → no bytes, no RustFS) and storing it on the event.
func (s *V1Service) runVideoJob(ctx context.Context, principal *APIPrincipal, in V1VideoRequest, modelItem *model.ModelConfig, eventID, aspectRatio, resolution, duration string, price float64, refFiles []string) {
	genCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	s.inflight.Add(eventID, cancel)
	defer s.inflight.Done(eventID)
	defer s.cleanupReferenceImages(ctx, eventID, refFiles)
	startedAt := time.Now()

	// No-store: capture only the UPSTREAM video URL. /content streams it on demand
	// (grok URLs are auth-gated → fetched with the generating account's token).
	var videoURL string
	var execErr error
	switch s.effectiveProvider(genCtx, modelItem) {
	case "adobe":
		_, videoURL, execErr = s.generateAdobeVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), false)
	case "runway":
		_, videoURL, execErr = s.generateRunwayVideo(genCtx, eventID, modelItem, in, aspectRatio, parseDurationSeconds(duration), false)
	case "grok":
		_, videoURL, execErr = s.generateGrokVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), false)
	case "custom":
		_, videoURL, execErr = s.generateCustomVideo(genCtx, eventID, modelItem, in, aspectRatio, resolution, parseDurationSeconds(duration), false)
	default:
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", "provider not implemented", 0)
		return
	}
	if execErr != nil {
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", execErr.Error(), 0)
		return
	}
	if strings.TrimSpace(videoURL) == "" {
		_ = s.refundIfNeeded(ctx, principal, eventID, price)
		_ = s.events.UpdateStatus(ctx, eventID, "failed", "upstream returned no video url", 0)
		return
	}
	// Store the upstream URL as the event's "file"; /content fetches it on demand.
	if err := s.events.MarkVideoReady(ctx, eventID, videoURL, int(time.Since(startedAt).Milliseconds())); err != nil {
		return
	}
	_ = s.models.IncrementGenerationCount(ctx, modelItem.ID)
	if principal != nil && principal.User != nil {
		_ = s.users.IncrementGenerationCount(ctx, principal.User.ID)
	}
	_ = s.maybeGrantInviteReward(ctx, principal)
}

// VideoJob returns the OpenAI video object for a job, scoped to the caller.
func (s *V1Service) VideoJob(ctx context.Context, principal *APIPrincipal, id string) (map[string]any, error) {
	ev, err := s.videoEventForUser(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	status, progress := videoJobStatus(ev)
	completedAt := int64(0)
	if ev.Status == "success" || ev.Status == "failed" {
		completedAt = ev.UpdatedAt.Unix()
	}
	errMsg := ""
	if ev.Status == "failed" {
		errMsg = ev.Error
	}
	modelName := ev.Model
	if nameByID, nerr := s.models.NameMap(ctx); nerr == nil {
		if name, ok := nameByID[ev.Model]; ok && strings.TrimSpace(name) != "" {
			modelName = name
		}
	}
	return videoJobObject(ev.ID, modelName, status, progress, ev.Duration, sizeFromRatioRes(ev.Ratio, ev.Resolution), ev.TS.Unix(), completedAt, errMsg), nil
}

// OpenVideoContent streams a completed job's video by proxying the stored
// upstream URL (downloaded on demand — never persisted).
func (s *V1Service) OpenVideoContent(ctx context.Context, principal *APIPrincipal, id string) (io.ReadCloser, string, error) {
	ev, err := s.videoEventForUser(ctx, principal, id)
	if err != nil {
		return nil, "", err
	}
	if ev.Status != "success" || strings.TrimSpace(ev.File) == "" {
		return nil, "", ErrVideoNotReady
	}
	// grok asset URLs (assets.grok.com) are auth-gated — a plain GET 403s. Stream
	// them through the SAME account that generated the clip, using its token. If
	// that account is gone (grok pools churn often), the clip is unrecoverable.
	if ev.Provider == "grok" && s.grok != nil {
		if s.settings != nil {
			if proxy, perr := s.settings.GetValue(ctx, "proxy.url"); perr == nil {
				s.grok.SetProxy(proxy)
			}
		}
		acct, _ := s.tokens.Get(ctx, "grok", ev.AccountID)
		if acct == nil || strings.TrimSpace(acct.Value) == "" {
			return nil, "", fmt.Errorf("%w: grok account no longer available for this video", ErrProviderTemporary)
		}
		return s.grok.OpenAsset(ctx, acct.Value, ev.File)
	}
	// Other providers return publicly-fetchable URLs — proxy directly.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ev.File, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: fetch upstream video: %v", ErrProviderTemporary, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("%w: upstream video status %d", ErrProviderTemporary, resp.StatusCode)
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = "video/mp4"
	}
	return resp.Body, ct, nil
}

func (s *V1Service) videoEventForUser(ctx context.Context, principal *APIPrincipal, id string) (*model.EventLog, error) {
	ev, err := s.events.GetByID(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if ev == nil || ev.Kind != "video" {
		return nil, ErrVideoJobNotFound
	}
	if principal != nil && principal.User != nil && ev.UserID != principal.User.ID {
		return nil, ErrVideoJobNotFound
	}
	return ev, nil
}

// videoJobStatus maps our event status → OpenAI's (queued|in_progress|completed|
// failed) plus a coarse progress.
func videoJobStatus(ev *model.EventLog) (string, int) {
	switch ev.Status {
	case "success":
		return "completed", 100
	case "failed":
		return "failed", 0
	default:
		if strings.TrimSpace(ev.AccountID) != "" {
			return "in_progress", 50
		}
		return "queued", 0
	}
}

func videoJobObject(id, modelID, status string, progress int, seconds, size string, createdAt, completedAt int64, errMsg string) map[string]any {
	obj := map[string]any{
		"id":         id,
		"object":     "video",
		"model":      modelID,
		"status":     status,
		"progress":   progress,
		"created_at": createdAt,
		"size":       size,
		"seconds":    strings.TrimSuffix(strings.TrimSpace(seconds), "s"),
	}
	if completedAt > 0 {
		obj["completed_at"] = completedAt
	} else {
		obj["completed_at"] = nil
	}
	if errMsg != "" {
		obj["error"] = map[string]any{"message": errMsg}
	} else {
		obj["error"] = nil
	}
	return obj
}

// sizeFromRatioRes reconstructs an OpenAI-style "WxH" label from our stored ratio
// + resolution tier (best-effort; only for display in the job object).
func sizeFromRatioRes(ratio, resolution string) string {
	long := 720
	res := strings.ToUpper(resolution)
	switch {
	case strings.Contains(res, "1080") || strings.Contains(res, "2K"):
		long = 1080
	case strings.Contains(res, "4K") || strings.Contains(res, "2160"):
		long = 2160
	}
	w, h := long, long
	switch strings.TrimSpace(ratio) {
	case "16:9":
		w, h = long, long*9/16
	case "9:16":
		w, h = long*9/16, long
	case "4:3":
		w, h = long, long*3/4
	case "3:4":
		w, h = long*3/4, long
	case "1:1":
		w, h = long, long
	default:
		w, h = long, long*9/16
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// hasActiveProviderToken reports whether the provider pool holds at least one
// usable token for this kind of generation — mirrors the selection filter in
// the generate* paths. Used to fail fast (before charging / creating a job)
// with a clear "no account" error instead of dialing upstream with no token.
func (s *V1Service) hasActiveProviderToken(ctx context.Context, provider, kind string) (bool, error) {
	items, err := s.tokens.ListByPool(ctx, provider)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
			continue
		}
		if provider == "adobe" {
			if kind == "video" && item.VideoLimited {
				continue
			}
			if kind == "image" && item.ImageLimited {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

func (s *V1Service) prepareImage(ctx context.Context, principal *APIPrincipal, in V1ImageRequest, charge bool) (*model.ModelConfig, string, string, float64, error) {
	modelID := strings.TrimSpace(in.Model)
	prompt := strings.TrimSpace(in.Prompt)
	if modelID == "" || prompt == "" {
		return nil, "", "", 0, errors.New("model and prompt required")
	}
	modelItem, err := s.models.Get(ctx, modelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", "", 0, ErrUnknownModel
		}
		return nil, "", "", 0, err
	}
	if !modelItem.Enabled || modelItem.Type != "image" {
		return nil, "", "", 0, ErrUnknownModel
	}
	// Fail fast before charging if the provider has no usable account. Use the
	// effective provider: a custom upstream serving this model id routes to
	// "custom" (effectiveProvider only returns it when such an account exists, so
	// the precheck is satisfied); otherwise check the native provider pool.
	if eff := s.effectiveProvider(ctx, modelItem); eff != "custom" {
		if ok, err := s.hasActiveProviderToken(ctx, eff, "image"); err != nil {
			return nil, "", "", 0, err
		} else if !ok {
			return nil, "", "", 0, ErrNoProviderAccount
		}
	}
	refLimit := 0
	if modelItem.ImageToImage {
		refLimit = modelItem.MaxReferenceImages
		if refLimit <= 0 {
			refLimit = 1
		}
	}
	if len(in.ReferenceImages) > refLimit {
		return nil, "", "", 0, errors.New("too many reference images")
	}
	// Reject oversized reference images before charging (all providers, all paths).
	if err := ensureReferenceSizes(in.ReferenceImages); err != nil {
		return nil, "", "", 0, err
	}
	// `size` (WxH) drives BOTH the aspect ratio AND the resolution tier — its long
	// edge maps to a tier (<1800→1K, 1800–3499→2K, ≥3500→4K). The web path passes
	// an explicit resolution; the OpenAI /v1 path derives it from size. There is no
	// `quality` param — size is the single source of truth for resolution.
	aspectRatio, resolution := parseImageSize(in.Size, in.AspectRatio, in.Resolution)
	// Snap to the nearest ratio the model actually supports — a `size`-derived
	// ratio (e.g. 1:3) must never be passed through to an upstream that rejects
	// it (Runway 400s on ratios outside its list).
	aspectRatio = snapRatio(aspectRatio, repo.JSONStrings(modelItem.Ratios))
	// parseImageSize defaults a blank resolution to "2K" (OpenAI-size parity).
	// For a model that doesn't price that tier — e.g. gpt-image-2 is 1K-only —
	// fall back to its first supported tier so a missing/stale resolution from
	// the client doesn't get rejected as "unsupported or unpriced".
	if _, ok := modelPrice(modelItem, "image", resolution, "", false); !ok {
		if fb := firstPricedResolution(modelItem); fb != "" {
			resolution = fb
		}
	}
	price, err := s.chargeForModel(ctx, principal, modelItem, "image", resolution, "", charge)
	if err != nil {
		return nil, "", "", 0, err
	}
	return modelItem, resolution, aspectRatio, price, nil
}

func (s *V1Service) prepareVideo(ctx context.Context, principal *APIPrincipal, in V1VideoRequest, charge bool) (*model.ModelConfig, string, string, string, float64, error) {
	modelID := strings.TrimSpace(in.Model)
	prompt := strings.TrimSpace(in.Prompt)
	duration := strings.TrimSpace(in.Duration)
	if modelID == "" || prompt == "" {
		return nil, "", "", "", 0, errors.New("model and prompt required")
	}
	if duration == "" {
		return nil, "", "", "", 0, errors.New("duration required")
	}
	modelItem, err := s.models.Get(ctx, modelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", "", "", 0, ErrUnknownModel
		}
		return nil, "", "", "", 0, err
	}
	if !modelItem.Enabled || modelItem.Type != "video" {
		return nil, "", "", "", 0, ErrUnknownModel
	}
	// Fail fast before charging — effective provider (custom upstream by id, else native).
	if eff := s.effectiveProvider(ctx, modelItem); eff == "custom" {
		// custom serves this id (effectiveProvider guaranteed it) — precheck ok
	} else if ok, err := s.hasActiveProviderToken(ctx, eff, "video"); err != nil {
		return nil, "", "", "", 0, err
	} else if !ok {
		return nil, "", "", "", 0, ErrNoProviderAccount
	}
	refLimit := modelItem.MaxReferenceImages
	if refLimit <= 0 {
		refLimit = 10
	}
	if len(in.ReferenceImages) > refLimit {
		return nil, "", "", "", 0, errors.New("too many reference images")
	}
	// Reject oversized reference images before charging (all providers, all paths).
	if err := ensureReferenceSizes(in.ReferenceImages); err != nil {
		return nil, "", "", "", 0, err
	}
	// Runway i2v strictly requires exactly one first-frame image. Enforce it here,
	// BEFORE charging, so a missing/extra frame fails fast instead of charge →
	// upstream reject → refund. generateRunwayVideo keeps its own guard too.
	if modelItem.Provider == "runway" {
		n := 0
		for _, r := range in.ReferenceImages {
			if strings.TrimSpace(r) != "" {
				n++
			}
		}
		if n != 1 {
			return nil, "", "", "", 0, errors.New("runway 图生视频需要且仅需 1 张首帧图")
		}
	}
	aspectRatio := strings.TrimSpace(strings.ReplaceAll(in.AspectRatio, "x", ":"))
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}
	resolution := strings.TrimSpace(in.Resolution)
	if resolution == "" {
		resolution = "720p"
	}
	price, err := s.chargeForModel(ctx, principal, modelItem, "video", resolution, duration, charge)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	return modelItem, resolution, aspectRatio, duration, price, nil
}

func (s *V1Service) chargeForModel(ctx context.Context, principal *APIPrincipal, modelItem *model.ModelConfig, kind, resolution, duration string, charge bool) (float64, error) {
	// 代理用户走代理价(某档未设代理价则回退普通价)。principal.User 即将被扣费的
	// 用户,无论画图台还是 key 调用都从这里取,所以一处即覆盖所有路径。
	agent := principal != nil && principal.User != nil && principal.User.Role == "agent"
	price, ok := modelPrice(modelItem, kind, resolution, duration, agent)
	if !ok {
		return 0, ErrUnsupportedParams
	}
	if !charge || principal == nil || principal.User == nil {
		return 0, nil
	}
	updated, debited, err := s.users.TryDebitCredits(ctx, principal.User.ID, price)
	if err != nil {
		return 0, err
	}
	if !debited {
		if updated != nil {
			principal.User = updated
		}
		return 0, ErrInsufficientFunds
	}
	principal.User = updated
	return price, nil
}

func (s *V1Service) userDir(principal *APIPrincipal) string {
	if principal == nil {
		return "anon"
	}
	return OwnerDir(principal.User)
}

// OwnerDir is the storage directory (= /images/<owner>/ segment) a user's outputs
// live under: sanitized name → sanitized email-local → id → "anon".
func OwnerDir(user *model.User) string {
	if user != nil {
		if d := sanitizeOwnerName(user.Name); d != "" {
			return d
		}
		if d := sanitizeOwnerName(strings.Split(user.Email, "@")[0]); d != "" {
			return d
		}
		if user.ID != "" {
			return user.ID
		}
	}
	return "anon"
}

// saveReferenceImages persists the user's uploaded reference images under the
// media root (same tree as outputs, served cookie-authed via /images) so the
// playground can re-display them after a reload. Best-effort: a save failure
// just drops that thumbnail and never blocks generation. Returns slash paths.
func (s *V1Service) saveReferenceImages(ctx context.Context, principal *APIPrincipal, inputs []string) []string {
	decoded, err := decodeReferenceImages(inputs, len(inputs))
	if err != nil || len(decoded) == 0 {
		return nil
	}
	userDir := s.userDir(principal)
	var paths []string
	for _, data := range decoded {
		ext := imageExtFromBytes(data)
		filename := time.Now().Format("20060102-150405") + "-ref-" + randomUpper(6) + "." + ext
		rel := filepath.ToSlash(filepath.Join(userDir, filename))
		if err := s.store.Put(ctx, rel, data, contentTypeForExt(ext)); err != nil {
			continue
		}
		paths = append(paths, rel)
	}
	return paths
}

// cleanupReferenceImages deletes a generation's reference images from storage and
// clears the event's ref_files paths. Called when an attempt finishes — success
// OR failure — since refs are only needed while generating (no storage bloat, not
// shown in the admin gallery, no dangling回显 URLs). Best-effort: errors ignored.
func (s *V1Service) cleanupReferenceImages(ctx context.Context, eventID string, refFiles []string) {
	if len(refFiles) == 0 {
		return
	}
	for _, rf := range refFiles {
		if strings.TrimSpace(rf) != "" {
			_ = s.store.Delete(ctx, rf)
		}
	}
	if strings.TrimSpace(eventID) != "" {
		_ = s.events.ClearRefFiles(ctx, eventID)
	}
}

// contentTypeForExt maps a file extension to a MIME type for storage uploads.
func contentTypeForExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}

// imageExtFromBytes sniffs a sensible file extension from the magic bytes so the
// saved reference keeps its real type (the /images handler types by extension).
func imageExtFromBytes(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpg"
	case len(b) >= 6 && string(b[0:6]) == "GIF89a", len(b) >= 6 && string(b[0:6]) == "GIF87a":
		return "gif"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	default:
		return "png"
	}
}

// allocateOutput builds the object key (= relative path, user-scoped) and the
// directly-downloadable URL pointing at this site's /images proxy. Nothing is
// written here — the bytes are uploaded to RustFS by the caller.
func (s *V1Service) allocateOutput(principal *APIPrincipal, ext, baseURL string) (string, string) {
	userDir := s.userDir(principal)
	filename := time.Now().Format("20060102-150405") + "-" + randomUpper(8) + "." + strings.TrimPrefix(ext, ".")
	relativePath := filepath.ToSlash(filepath.Join(userDir, filename))
	// OpenAI-style clients need a directly-downloadable absolute URL. When the
	// inbound request's base URL is known, build "{scheme}://{host}/images/...";
	// otherwise fall back to the relative path for backward compatibility.
	if base := strings.TrimRight(strings.TrimSpace(baseURL), "/"); base != "" {
		return base + "/images/" + relativePath, relativePath
	}
	return "/images/" + relativePath, relativePath
}

func (s *V1Service) logPendingEvent(ctx context.Context, kind string, modelItem *model.ModelConfig, principal *APIPrincipal, prompt, ratio, resolution, duration string, refs int, cost float64, file, source string, refFiles []string) (string, error) {
	event := &model.EventLog{
		ID:         "evt-" + randomUpper(12),
		TS:         time.Now(),
		Kind:       kind,
		Status:     "pending",
		Model:      modelItem.ID,
		Provider:   modelItem.Provider,
		Prompt:     prompt,
		Ratio:      ratio,
		Resolution: resolution,
		Duration:   duration,
		Refs:       refs,
		Source:     source,
		Cost:       cost,
		File:       file,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if len(refFiles) > 0 {
		event.RefFiles = jsonArray(refFiles)
	}
	if principal != nil && principal.User != nil {
		event.UserID = principal.User.ID
	}
	if err := s.events.Create(ctx, event); err != nil {
		return "", err
	}
	return event.ID, nil
}

func (s *V1Service) finishUnimplementedEvent(ctx context.Context, eventID string) error {
	return s.events.UpdateStatus(ctx, eventID, "failed", "generation executor not implemented yet", 0)
}

// maxSameAccountAttempts is how many times ONE account is retried for the same
// request before that account is abandoned. Transient/request errors stay on the
// same account; account-level errors (auth/quota) skip straight to the next.
const maxSameAccountAttempts = 3

// grokConcurrencyPerAccount is how many simultaneous generations one grok account
// may run (grok tolerates 10, unlike the 1-per-account default elsewhere).
const grokConcurrencyPerAccount = 10

// maxTempDeadAccounts caps how many accounts the "temporary error = fail over"
// policy (tempFailover, used by adobe) may burn per request before giving up, so
// an upstream-wide blip ("system under load") can't fan a single request out
// across the whole pool. After this many accounts fail this way, the request fails.
const maxTempDeadAccounts = 3

// runPoolWithFailover drives a generation across a round-robin-ordered account
// list with per-error-class behavior, so a bad request never burns the whole
// pool while genuinely limited accounts still fail over:
//   - 额度耗尽 quota → mark the account and FAIL OVER to the next account
//     immediately (same-account retry can't help). Repeats until one succeeds or
//     the pool is exhausted.
//   - 认证失效 auth → refresh the token from its cookie and retry ONCE with the
//     fresh token; if it still auth-fails (or there's nothing to refresh, e.g.
//     chatgpt's JWT IS the credential), mark the account and fail over.
//   - 上游临时 temporary → behavior depends on tempFailover:
//   - tempFailover=false (default): retry the SAME account up to
//     maxSameAccountAttempts times (not counted); if still failing, STOP
//     (no fan-out — an upstream-wide blip fails identically everywhere).
//   - tempFailover=true (adobe): fail over to the next account WITHOUT
//     penalizing this one (rate-limit/overload isn't the account's fault),
//     capped at maxTempDeadAccounts accounts so a pool-wide blip can't fan
//     a single request out across everything.
//   - 参数错 / request-level (anything else) → return immediately, no retry, no
//     account penalty (the account isn't at fault).
//
// Returns the actual upstream error (never a synthetic "retry failed"). On
// success it stamps success_total/fails=0 on the winning account. classify maps
// a provider error to (isAuth, isQuota, isTemporary). refreshOnAuth (nil for
// providers whose token IS the credential) re-mints the account's token so an
// auth retry uses a FRESH token instead of replaying the stale one.
func (s *V1Service) runPoolWithFailover(ctx context.Context, eventID, pool string, active []model.TokenAccount, kind string,
	attempt func(token model.TokenAccount) ([]byte, error),
	classify func(error) (isAuth, isQuota, isTemporary, isDead bool),
	refreshOnAuth func(tokenID string) (model.TokenAccount, bool),
	tempFailover bool,
) ([]byte, error) {
	var lastErr error
	busy := 0
	tempDeadCount := 0
	for _, token := range active {
		// 1 concurrent job per account: skip any account already generating.
		if !s.acctAcquire(ctx, token.ID, eventID, 1) {
			busy++
			continue
		}
		// release via defer so a panic in tryAccount can't leak the 1-job slot.
		data, err, failover, tempDead := func() ([]byte, error, bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			return s.tryAccount(ctx, eventID, pool, token, kind, attempt, classify, refreshOnAuth, tempFailover)
		}()
		if err == nil {
			return data, nil
		}
		lastErr = err
		if tempDead {
			// temp-failover policy: this account hit a temporary upstream error.
			// Cap how many accounts one request may burn before we stop, so an
			// upstream-wide blip doesn't fan out across the whole pool.
			tempDeadCount++
			if tempDeadCount >= maxTempDeadAccounts {
				return nil, lastErr
			}
		}
		if failover {
			continue
		}
		// temporary exhausted or request-level error → surface it, no fan-out.
		return nil, lastErr
	}
	// Nothing ran. If accounts were skipped ONLY because they were all busy
	// (no real failure), tell the caller the pool is at its concurrency cap.
	if lastErr == nil {
		if busy > 0 {
			return nil, ErrConcurrencyFull
		}
		return nil, ErrProviderExecution
	}
	return nil, lastErr
}

// tryAccount runs one account's attempt with the same-account retry policy used
// by the pool: 额度耗尽/认证失效 → mark + failover; 上游临时 → retry ≤3 same account;
// 参数错 → fail fast. Returns (data, err, failover) — failover=true means move on
// to the next account. The per-account concurrency gate is held by the caller.
func (s *V1Service) tryAccount(ctx context.Context, eventID, pool string, token model.TokenAccount, kind string,
	attempt func(token model.TokenAccount) ([]byte, error),
	classify func(error) (isAuth, isQuota, isTemporary, isDead bool),
	refreshOnAuth func(tokenID string) (model.TokenAccount, bool),
	tempFailover bool,
) ([]byte, error, bool, bool) {
	_ = s.events.SetAccount(ctx, eventID, token.ID)
	_ = s.tokens.TouchLastUsed(ctx, token.ID)
	authRefreshed := false
	tempAttempts := 0
	for {
		data, err := attempt(token)
		if err == nil {
			_, _ = s.tokens.Update(ctx, pool, token.ID, map[string]any{
				"last_used_at":  time.Now(),
				"success_total": gorm.Expr("success_total + 1"),
				"fails":         0,
			})
			return data, nil, false, false
		}
		isAuth, isQuota, isTemp, isDead := classify(err)
		if isQuota {
			s.markTokenFailure(ctx, pool, token, kind, false, true)
			return nil, err, true, false
		}
		if isAuth {
			// Refresh from cookie and retry ONCE; otherwise the credential is dead.
			if refreshOnAuth != nil && !authRefreshed {
				if refreshed, ok := refreshOnAuth(token.ID); ok {
					token = refreshed
					authRefreshed = true
					continue
				}
			}
			s.markTokenFailure(ctx, pool, token, kind, true, false)
			return nil, err, true, false
		}
		if isDead {
			s.markTokenDead(ctx, pool, token, kind)
			return nil, err, true, true
		}
		if isTemp {
			if tempFailover {
				// Ops policy (adobe): a temporary upstream error ("system under
				// load" / rate-limit) is NOT the account's fault — record the
				// failure but keep the account active, and fail over to the next
				// account. The pool driver caps how many accounts one request may
				// burn this way (maxTempDeadAccounts).
				_, _ = s.tokens.Update(ctx, pool, token.ID, map[string]any{
					"last_used_at": time.Now(),
					"fail_total":   gorm.Expr("fail_total + 1"),
					"fails":        gorm.Expr("fails + 1"),
				})
				return nil, err, true, true
			}
			tempAttempts++
			if tempAttempts < maxSameAccountAttempts {
				// Short linear backoff (1s, 2s) so an overloaded/rate-limited upstream
				// gets a moment to recover before the same-account retry, instead of
				// hammering it instantly.
				select {
				case <-time.After(time.Duration(tempAttempts) * time.Second):
				case <-ctx.Done():
					return nil, err, false, false
				}
				continue
			}
			return nil, err, false, false // exhausted; no fan-out
		}
		return nil, err, false, false // 参数错 / request-level
	}
}

func adobeErrClass(e error) (bool, bool, bool, bool) {
	return errors.Is(e, adobe.ErrAuth), errors.Is(e, adobe.ErrQuotaExhausted), errors.Is(e, adobe.ErrTemporaryUpstream), errors.Is(e, adobe.ErrDeadUpstream)
}

func (s *V1Service) generateAdobeImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.adobe == nil {
		return nil, errors.New("adobe client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.adobe.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "adobe")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		// Image quota is tracked separately from video — an account whose video
		// quota is exhausted (VideoLimited) is still usable for image as long as
		// its image quota remains. status=="quota" means BOTH kinds are limited
		// (or a legacy/full quota mark), so it's excluded for either kind.
		if item.Status == "active" && !item.Dead && !item.ImageLimited && strings.TrimSpace(item.Value) != "" {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("adobe", active)

	refs, err := decodeReferenceImages(in.ReferenceImages, max(1, modelItem.MaxReferenceImages))
	if err != nil {
		return nil, err
	}

	// Round-robin order. Adobe uses tempFailover=true: a temporary upstream error
	// ("system under load") fails over to the next account without penalizing the
	// current one, capped at maxTempDeadAccounts; auth/quota also fail over
	// (see runPoolWithFailover).
	return s.runPoolWithFailover(ctx, eventID, "adobe", active, "image", func(token model.TokenAccount) ([]byte, error) {
		var blobIDs []string
		for _, ref := range refs {
			id, upErr := s.adobe.UploadImage(ctx, token.Value, ref, "image/png", "")
			if upErr != nil {
				return nil, upErr
			}
			blobIDs = append(blobIDs, id)
		}
		data, _, genErr := s.adobe.GenerateImage(ctx, token.Value, modelItem.ID, in.Prompt, aspectRatio, resolution, blobIDs)
		return data, genErr
	}, adobeErrClass, func(id string) (model.TokenAccount, bool) {
		return s.refreshAdobeToken(ctx, id)
	}, true)
}

func (s *V1Service) generateAdobeVideo(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1VideoRequest, aspectRatio, resolution string, durationSeconds int, downloadResult bool) ([]byte, string, error) {
	if s.adobe == nil {
		return nil, "", errors.New("adobe client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.adobe.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "adobe")
	if err != nil {
		return nil, "", err
	}
	var active []model.TokenAccount
	for _, item := range items {
		// Video quota is tracked separately from image — skip accounts whose
		// video quota is exhausted (VideoLimited), but an image-only limit
		// (ImageLimited) leaves the account usable for video. status=="quota"
		// means BOTH kinds are limited (or a legacy/full quota mark), so it's
		// excluded for either kind.
		if item.Status == "active" && !item.Dead && !item.VideoLimited && strings.TrimSpace(item.Value) != "" {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return nil, "", ErrNoProviderAccount
	}
	s.rotateRoundRobin("adobe", active)

	refLimit := modelItem.MaxReferenceImages
	if refLimit <= 0 {
		refLimit = 10
	}
	refs, err := decodeReferenceImages(in.ReferenceImages, refLimit)
	if err != nil {
		return nil, "", err
	}

	engine, upstreamModel := resolveAdobeVideoEngine(modelItem.ID)
	referenceMode := defaultString(strings.TrimSpace(modelItem.ReferenceMode), "frame")

	// Round-robin order; same-account retry on transient errors, fail over to the
	// next account on auth/quota; temporary upstream errors fail over too without
	// penalizing the account (tempFailover, capped at maxTempDeadAccounts). videoURL is
	// captured from the successful attempt's meta (the upstream presigned URL).
	var videoURL string
	data, err := s.runPoolWithFailover(ctx, eventID, "adobe", active, "video", func(token model.TokenAccount) ([]byte, error) {
		var blobIDs []string
		for _, ref := range refs {
			id, upErr := s.adobe.UploadImage(ctx, token.Value, ref, "image/png", engine)
			if upErr != nil {
				return nil, upErr
			}
			blobIDs = append(blobIDs, id)
		}
		bytes, meta, genErr := s.adobe.GenerateVideo(ctx, token.Value, engine, in.Prompt, aspectRatio, durationSeconds, resolution, referenceMode, upstreamModel, blobIDs, downloadResult)
		if genErr == nil {
			videoURL = strings.TrimSpace(stringValue(meta["video_url"]))
		}
		return bytes, genErr
	}, adobeErrClass, func(id string) (model.TokenAccount, bool) {
		return s.refreshAdobeToken(ctx, id)
	}, true)
	return data, videoURL, err
}

// leonardoMinCredits is the per-generation token cost (one Leonardo image = 30
// tokens). An account with fewer is treated as 限额 and skipped — it can't afford
// a generation. Daily renewal (tokenRenewalDate) drives auto-recovery.
const leonardoMinCredits = 30

func (s *V1Service) generateRunwayVideo(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1VideoRequest, aspectRatio string, durationSeconds int, downloadResult bool) ([]byte, string, error) {
	if s.runway == nil {
		return nil, "", errors.New("runway client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.runway.SetProxy(proxy)
		}
	}

	// Runway i2v strictly requires exactly one first-frame image.
	refs, err := decodeReferenceImages(in.ReferenceImages, 1)
	if err != nil {
		return nil, "", err
	}
	if len(refs) != 1 {
		return nil, "", errors.New("runway 图生视频需要且仅需 1 张首帧图")
	}
	frame := refs[0]

	items, err := s.tokens.ListByPool(ctx, "runway")
	if err != nil {
		return nil, "", err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
			continue
		}
		// No pre-deduct (same policy as the image flow): skip only accounts we KNOW
		// are out of credits (cached remaining <= 0) — those are treated as dead.
		// Unknown balance gets the benefit of the doubt.
		if rem, ok := jsonMapInt(item.Meta, "cached_quota_remaining"); ok && rem <= 0 {
			continue
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return nil, "", ErrNoProviderAccount
	}
	s.rotateRoundRobin("runway", active)

	var lastErr error
	var videoURL string
	busy := 0
	for _, token := range active {
		// 1 concurrent job per account: skip any account already generating.
		if !s.acctAcquire(ctx, token.ID, eventID, 1) {
			busy++
			continue
		}
		var data []byte
		done, failover := func() (bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			_ = s.events.SetAccount(ctx, eventID, token.ID)
			_ = s.tokens.TouchLastUsed(ctx, token.ID)
			teamID := ""
			if token.Meta != nil {
				teamID = strings.TrimSpace(stringValue(token.Meta["team_id"]))
			}
			d, meta, genErr := s.runway.GenerateVideo(ctx, token.Value, teamID, in.Prompt, aspectRatio, durationSeconds, frame, downloadResult)
			if genErr == nil {
				_, _ = s.tokens.Update(ctx, "runway", token.ID, map[string]any{
					"last_used_at":  time.Now(),
					"success_total": gorm.Expr("success_total + 1"),
					"fails":         0,
				})
				data = d
				videoURL = strings.TrimSpace(stringValue(meta["video_url"]))
				return true, false
			}
			lastErr = genErr
			switch {
			case errors.Is(genErr, runway.ErrAuth), errors.Is(genErr, runway.ErrQuotaExhausted):
				// 额度没了 / token 失效 → 当 401 判死(status=disabled, dead),换号。
				s.markTokenFailure(ctx, "runway", token, "video", true, false)
				return false, true
			case errors.Is(genErr, runway.ErrTemporaryUpstream):
				// 上游临时错误 → 直接换下一个号。
				return false, true
			default:
				// 参数级错误(如 prompt 未过审)→ 直接失败,不换号。
				return false, false
			}
		}()
		if done {
			return data, videoURL, nil
		}
		if failover {
			continue
		}
		return nil, "", lastErr
	}
	if lastErr == nil {
		if busy > 0 {
			return nil, "", ErrConcurrencyFull
		}
		lastErr = ErrProviderExecution
	}
	return nil, "", lastErr
}

// customAccountServes reports whether a custom (upstream) account is usable for a
// given model id: active, not dead, has a base_url, and its meta.models list (csv
// of model ids it serves) contains the id. An empty models list serves ALL ids.
func customAccountServes(item model.TokenAccount, modelID string) bool {
	if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
		return false
	}
	if item.Meta == nil || strings.TrimSpace(stringValue(item.Meta["base_url"])) == "" {
		return false
	}
	list := strings.TrimSpace(stringValue(item.Meta["models"]))
	if list == "" {
		return true
	}
	for _, m := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(m), modelID) {
			return true
		}
	}
	return false
}

// customActive returns the custom accounts that serve modelID, ordered by weight
// (higher first; ties by id) so heavier upstreams are preferred.
func (s *V1Service) customActive(ctx context.Context, modelID string) ([]model.TokenAccount, error) {
	items, err := s.tokens.ListByPool(ctx, "custom")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if customAccountServes(item, modelID) {
			active = append(active, item)
		}
	}
	s.rotateRoundRobin("custom", active) // weight priority + round-robin within ties
	return active, nil
}

// accountConcurrency is the per-account simultaneous-job cap. Custom accounts use
// their configured Concurrency (default 1); built-in pools use the system value.
func accountConcurrency(item model.TokenAccount) int {
	if item.Concurrency > 0 {
		return item.Concurrency
	}
	return 1
}

// effectiveProvider routes a model to the "custom" upstream whenever a custom
// account declares it serves that model id (id-based override of the model's
// native provider) — so an upstream can take over any model by matching its id.
// Otherwise the model's own provider is used.
func (s *V1Service) effectiveProvider(ctx context.Context, modelItem *model.ModelConfig) string {
	if s.custom != nil {
		if active, err := s.customActive(ctx, modelItem.ID); err == nil && len(active) > 0 {
			return "custom"
		}
	}
	return modelItem.Provider
}

// generateCustomImage forwards an image generation to an OpenAI-compatible
// upstream. The upstream (custom account) is matched by model id; calls go direct
// (no proxy). Billing uses the local model price.
func (s *V1Service) generateCustomImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.custom == nil {
		return nil, errors.New("custom client not configured")
	}
	refs, err := decodeReferenceImages(in.ReferenceImages, max(1, modelItem.MaxReferenceImages))
	if err != nil {
		return nil, err
	}
	active, err := s.customActive(ctx, modelItem.ID)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	size := upstreamSize(aspectRatio, resolution)
	quality := upstreamQuality(resolution)
	var lastErr error
	busy := 0
	for _, token := range active {
		if !s.acctAcquire(ctx, token.ID, eventID, accountConcurrency(token)) {
			busy++
			continue
		}
		var data []byte
		done, failover := func() (bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			_ = s.events.SetAccount(ctx, eventID, token.ID)
			_ = s.tokens.TouchLastUsed(ctx, token.ID)
			baseURL := stringValue(token.Meta["base_url"])
			d, genErr := s.custom.GenerateImage(ctx, baseURL, token.Value, modelItem.ID, in.Prompt, size, quality, refs)
			if genErr == nil {
				_, _ = s.tokens.Update(ctx, "custom", token.ID, map[string]any{
					"last_used_at": time.Now(), "success_total": gorm.Expr("success_total + 1"), "fails": 0,
				})
				data = d
				return true, false
			}
			lastErr = genErr
			switch {
			case errors.Is(genErr, custom.ErrAuth):
				s.markTokenFailure(ctx, "custom", token, "image", true, false)
				return false, true
			case errors.Is(genErr, custom.ErrQuotaExhausted):
				s.markTokenFailure(ctx, "custom", token, "image", false, true)
				return false, true
			case errors.Is(genErr, custom.ErrTemporaryUpstream):
				return false, true
			default:
				return false, false
			}
		}()
		if done {
			return data, nil
		}
		if failover {
			continue
		}
		return nil, lastErr
	}
	if lastErr == nil {
		if busy > 0 {
			return nil, ErrConcurrencyFull
		}
		lastErr = ErrProviderExecution
	}
	return nil, lastErr
}

// generateCustomVideo forwards a video generation to an OpenAI-compatible
// (Sora-style) upstream, matched by model id. No proxy; local-price billing.
func (s *V1Service) generateCustomVideo(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1VideoRequest, aspectRatio, resolution string, durationSeconds int, downloadResult bool) ([]byte, string, error) {
	if s.custom == nil {
		return nil, "", errors.New("custom client not configured")
	}
	active, err := s.customActive(ctx, modelItem.ID)
	if err != nil {
		return nil, "", err
	}
	if len(active) == 0 {
		return nil, "", ErrNoProviderAccount
	}
	size := upstreamSize(aspectRatio, resolution)
	var lastErr error
	var videoURL string
	busy := 0
	for _, token := range active {
		if !s.acctAcquire(ctx, token.ID, eventID, accountConcurrency(token)) {
			busy++
			continue
		}
		var data []byte
		done, failover := func() (bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			_ = s.events.SetAccount(ctx, eventID, token.ID)
			_ = s.tokens.TouchLastUsed(ctx, token.ID)
			baseURL := stringValue(token.Meta["base_url"])
			d, url, genErr := s.custom.GenerateVideo(ctx, baseURL, token.Value, modelItem.ID, in.Prompt, size, durationSeconds, downloadResult)
			if genErr == nil {
				_, _ = s.tokens.Update(ctx, "custom", token.ID, map[string]any{
					"last_used_at": time.Now(), "success_total": gorm.Expr("success_total + 1"), "fails": 0,
				})
				data = d
				videoURL = url
				return true, false
			}
			lastErr = genErr
			switch {
			case errors.Is(genErr, custom.ErrAuth):
				s.markTokenFailure(ctx, "custom", token, "video", true, false)
				return false, true
			case errors.Is(genErr, custom.ErrQuotaExhausted):
				s.markTokenFailure(ctx, "custom", token, "video", false, true)
				return false, true
			case errors.Is(genErr, custom.ErrTemporaryUpstream):
				return false, true
			default:
				return false, false
			}
		}()
		if done {
			return data, videoURL, nil
		}
		if failover {
			continue
		}
		return nil, "", lastErr
	}
	if lastErr == nil {
		if busy > 0 {
			return nil, "", ErrConcurrencyFull
		}
		lastErr = ErrProviderExecution
	}
	return nil, "", lastErr
}

// upstreamSize maps our (ratio, resolution) to an OpenAI-style "WxH" size string
// for the upstream. The pixel base scales with the tier (1K/2K/4K); the ratio
// sets the shape. Upstreams that key off ratio (our own /v1) read it fine.
func upstreamSize(aspectRatio, resolution string) string {
	base := 1024
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case "2K":
		base = 2048
	case "4K":
		base = 4096
	}
	w, h := 1, 1
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(aspectRatio), "x", ":"), ":")
	if len(parts) == 2 {
		if a, e1 := strconv.Atoi(strings.TrimSpace(parts[0])); e1 == nil && a > 0 {
			if b, e2 := strconv.Atoi(strings.TrimSpace(parts[1])); e2 == nil && b > 0 {
				w, h = a, b
			}
		}
	}
	if w >= h {
		return fmt.Sprintf("%dx%d", base, base*h/w)
	}
	return fmt.Sprintf("%dx%d", base*w/h, base)
}

// upstreamQuality maps a resolution tier to the OpenAI quality enum.
func upstreamQuality(resolution string) string {
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case "2K":
		return "medium"
	case "4K":
		return "high"
	case "1K":
		return "low"
	}
	return ""
}

// generateGrokVideo runs grok's imagine video pipeline across the grok pool.
// Mirrors the runway policy: no pre-deduct, skip accounts known out of credits
// (cached remaining <= 0), and treat an out-of-credits / auth failure as a dead
// account (the grok sso can't be renewed — 失效就失效). Text-to-video only for
// now (grok reference-image upload isn't wired yet).
func (s *V1Service) generateGrokVideo(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1VideoRequest, aspectRatio, resolution string, durationSeconds int, downloadResult bool) ([]byte, string, error) {
	if s.grok == nil {
		return nil, "", errors.New("grok client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.grok.SetProxy(proxy)
		}
	}

	// Optional reference frames (image-to-video), up to the model's max.
	frames, err := decodeReferenceImages(in.ReferenceImages, max(1, modelItem.MaxReferenceImages))
	if err != nil {
		return nil, "", err
	}

	items, err := s.tokens.ListByPool(ctx, "grok")
	if err != nil {
		return nil, "", err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
			continue
		}
		if rem, ok := jsonMapInt(item.Meta, "cached_quota_remaining"); ok && rem <= 0 {
			continue
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return nil, "", ErrNoProviderAccount
	}
	s.rotateRoundRobin("grok", active)

	res := strings.TrimSpace(resolution)
	if res == "" {
		res = "720p"
	}
	var lastErr error
	var videoURL string
	busy := 0
	for _, token := range active {
		// grok allows 10 concurrent jobs per account (unlike the 1-per-account
		// default of the other pools).
		if !s.acctAcquire(ctx, token.ID, eventID, grokConcurrencyPerAccount) {
			busy++
			continue
		}
		var data []byte
		done, failover := func() (bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			_ = s.events.SetAccount(ctx, eventID, token.ID)
			_ = s.tokens.TouchLastUsed(ctx, token.ID)
			d, meta, genErr := s.grok.GenerateVideo(ctx, token.Value, in.Prompt, aspectRatio, res, durationSeconds, frames, downloadResult)
			if genErr == nil {
				_, _ = s.tokens.Update(ctx, "grok", token.ID, map[string]any{
					"last_used_at":  time.Now(),
					"success_total": gorm.Expr("success_total + 1"),
					"fails":         0,
				})
				data = d
				videoURL = strings.TrimSpace(stringValue(meta["video_url"]))
				return true, false
			}
			lastErr = genErr
			switch {
			case errors.Is(genErr, grok.ErrAuth), errors.Is(genErr, grok.ErrQuotaExhausted):
				// 失效 / 额度没了 → 当 401 判死(不续期),换号。
				s.markTokenFailure(ctx, "grok", token, "video", true, false)
				return false, true
			case errors.Is(genErr, grok.ErrTemporaryUpstream):
				return false, true
			default:
				return false, false
			}
		}()
		if done {
			return data, videoURL, nil
		}
		if failover {
			continue
		}
		return nil, "", lastErr
	}
	if lastErr == nil {
		if busy > 0 {
			return nil, "", ErrConcurrencyFull
		}
		lastErr = ErrProviderExecution
	}
	return nil, "", lastErr
}

// generateRunwayImage runs the Runway "Nano Banana 2" (gemini_3_1_flash_image)
// image pipeline across the runway pool. Unlike the video path it does NOT
// pre-deduct credits: it simply round-robins the pool and generates. Per ops
// decision an out-of-credits account is treated like a dead 401 — marked
// dead (status=disabled) and skipped — because Runway credits don't refill
// daily, so a "quota" mark (which the maintenance loop would revive) is wrong.
// Reference images (up to the model's max) are uploaded per attempt.
func (s *V1Service) generateRunwayImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.runway == nil {
		return nil, errors.New("runway client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.runway.SetProxy(proxy)
		}
	}

	refs, err := decodeReferenceImages(in.ReferenceImages, max(1, modelItem.MaxReferenceImages))
	if err != nil {
		return nil, err
	}

	items, err := s.tokens.ListByPool(ctx, "runway")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
			continue
		}
		// No pre-deduct: skip only accounts we KNOW are out of credits
		// (cached remaining <= 0); they're treated as dead. Unknown balance gets
		// the benefit of the doubt — upstream rejects if it's truly empty.
		if rem, ok := jsonMapInt(item.Meta, "cached_quota_remaining"); ok && rem <= 0 {
			continue
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("runway", active)

	imageSize := strings.TrimSpace(resolution)
	if imageSize == "" {
		imageSize = "1K"
	}
	var lastErr error
	busy := 0
	for _, token := range active {
		// 1 concurrent job per account: skip any account already generating.
		if !s.acctAcquire(ctx, token.ID, eventID, 1) {
			busy++
			continue
		}
		var data []byte
		done, failover := func() (bool, bool) {
			defer s.acctRelease(ctx, token.ID, eventID)
			_ = s.events.SetAccount(ctx, eventID, token.ID)
			_ = s.tokens.TouchLastUsed(ctx, token.ID)
			teamID := ""
			if token.Meta != nil {
				teamID = strings.TrimSpace(stringValue(token.Meta["team_id"]))
			}
			d, _, genErr := s.runway.GenerateImage(ctx, token.Value, teamID, in.Prompt, aspectRatio, imageSize, refs)
			if genErr == nil {
				_, _ = s.tokens.Update(ctx, "runway", token.ID, map[string]any{
					"last_used_at":  time.Now(),
					"success_total": gorm.Expr("success_total + 1"),
					"fails":         0,
				})
				data = d
				return true, false
			}
			lastErr = genErr
			switch {
			case errors.Is(genErr, runway.ErrAuth), errors.Is(genErr, runway.ErrQuotaExhausted):
				// 额度没了 / token 失效 → 当 401 判死(status=disabled, dead),换号。
				s.markTokenFailure(ctx, "runway", token, "image", true, false)
				return false, true
			case errors.Is(genErr, runway.ErrTemporaryUpstream):
				// 上游临时错误 → 直接换下一个号。
				return false, true
			default:
				// 参数级错误(如 prompt 未过审)→ 直接失败,不换号。
				return false, false
			}
		}()
		if done {
			return data, nil
		}
		if failover {
			continue
		}
		return nil, lastErr
	}
	if lastErr == nil {
		if busy > 0 {
			return nil, ErrConcurrencyFull
		}
		lastErr = ErrProviderExecution
	}
	return nil, lastErr
}

// reconcileChatGPTQuota re-reads OpenAI's image_gen remaining right after a
// successful generation and writes it back (negative / unknown clamp to 0),
// flipping the account to 限额 when it hits 0 — so accounts limit one-by-one as
// they're used, not all at once on a later batch probe. Runs while the
// per-account concurrency gate is still held. Best-effort (never fails the render).
func (s *V1Service) reconcileChatGPTQuota(ctx context.Context, tokenID, accessToken string) {
	if s.chatgpt == nil {
		return
	}
	data, err := s.chatgpt.FetchImageQuota(ctx, accessToken)
	if err != nil || boolValueWithDefault(data["auth_failed"], false) {
		return
	}
	rem, exhausted := chatgptRemaining(data)
	item, err := s.tokens.Get(ctx, "chatgpt", tokenID)
	if err != nil {
		return
	}
	meta := cloneJSONMap(item.Meta)
	meta["cached_quota_remaining"] = rem
	meta["cached_quota_at"] = int(time.Now().Unix())
	patch := map[string]any{"meta": meta}
	if reset := strings.TrimSpace(stringValue(data["reset_after"])); reset != "" {
		patch["cached_quota_reset_after"] = reset
	} else if strings.TrimSpace(item.CachedQuotaResetAfter) == "" {
		patch["cached_quota_reset_after"] = leonardoResetAfter("")
	}
	if exhausted && item.Status == "active" {
		patch["status"] = "quota"
	}
	_, _ = s.tokens.Update(ctx, "chatgpt", tokenID, patch)
}

func (s *V1Service) generateChatGPTImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.chatgpt == nil {
		return nil, errors.New("chatgpt client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.chatgpt.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "chatgpt")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if item.Status == "active" && !item.Dead && strings.TrimSpace(item.Value) != "" {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("chatgpt", active)

	refLimit := modelItem.MaxReferenceImages
	if refLimit <= 0 {
		refLimit = 1
	}
	refs, err := decodeReferenceImages(in.ReferenceImages, refLimit)
	if err != nil {
		return nil, err
	}

	// Round-robin order; same-account retry on transient errors, fail over to the
	// next account on auth/quota (see runPoolWithFailover).
	return s.runPoolWithFailover(ctx, eventID, "chatgpt", active, "image", func(token model.TokenAccount) ([]byte, error) {
		data, _, genErr := s.chatgpt.GenerateImage(ctx, token.Value, in.Prompt, modelItem.ID, aspectRatio, resolution, refs)
		if genErr == nil {
			// Sync the real OpenAI quota BEFORE the concurrency gate releases, so the
			// freshly-decremented remaining (and 限额 flip at 0) gates the next pick.
			s.reconcileChatGPTQuota(ctx, token.ID, token.Value)
		}
		return data, genErr
	}, func(e error) (bool, bool, bool, bool) {
		return errors.Is(e, chatgpt.ErrAuth), errors.Is(e, chatgpt.ErrQuotaExhausted), errors.Is(e, chatgpt.ErrTemporaryUpstream), false
	}, nil, false) // chatgpt token IS the credential — no cookie to refresh
}

// leonardoResetAfter returns when a Leonardo account's daily free tokens renew.
// Leonardo resets at 08:00 Beijing == 00:00 UTC, so when the upstream gives no
// explicit renewal time we deterministically use the next UTC midnight — this is
// filled at import so 恢复时间 is always populated, not left blank.
func leonardoResetAfter(availableUntil string) string {
	if v := strings.TrimSpace(availableUntil); v != "" {
		return v
	}
	return time.Unix((time.Now().Unix()/86400+1)*86400, 0).UTC().Format(time.RFC3339)
}

// leonardoDimensions maps the catalog's resolution+ratio to Leonardo pixel sizes.
func leonardoDimensions(resolution, aspectRatio string) (int, int) {
	res := strings.ToUpper(strings.TrimSpace(resolution))
	ar := strings.TrimSpace(aspectRatio)
	if res == "4K" {
		switch ar {
		case "2:3":
			return 2000, 3000
		case "16:9":
			return 4096, 2304
		case "4:3":
			return 4096, 3072
		case "4:5":
			return 3264, 4080
		case "9:16":
			return 2160, 3840
		case "2:1":
			return 4096, 2048
		default: // 1:1
			return 4096, 4096
		}
	}
	switch ar { // 2K (default)
	case "2:3":
		return 1664, 2496
	case "16:9":
		return 2560, 1440
	case "4:3":
		return 2304, 1728
	case "4:5":
		return 2432, 3040
	case "9:16":
		return 1440, 2560
	case "2:1":
		return 3232, 1616
	default: // 1:1
		return 2048, 2048
	}
}

func (s *V1Service) generateLeonardoImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.leonardo == nil {
		return nil, errors.New("leonardo client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.leonardo.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "leonardo")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		if item.Status != "active" || item.Dead || strings.TrimSpace(item.Value) == "" {
			continue
		}
		// Skip accounts under the per-generation floor (treated as 限额). Unknown
		// balance gets the benefit of the doubt (upstream rejects if truly empty).
		if rem, ok := jsonMapInt(item.Meta, "cached_quota_remaining"); ok && rem < leonardoMinCredits {
			continue
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("leonardo", active)

	width, height := leonardoDimensions(resolution, aspectRatio)
	// The catalog model id is the upstream Leonardo model name (e.g. seedream-4.5).
	upstreamModel := strings.TrimSpace(modelItem.ID)

	// Optional image-to-image: decode the reference image once up front (Leonardo
	// seedream takes at most one).
	refLimit := modelItem.MaxReferenceImages
	if refLimit <= 0 {
		refLimit = 1
	}
	refs, err := decodeReferenceImages(in.ReferenceImages, refLimit)
	if err != nil {
		return nil, err
	}

	// token.Value is the cookie; GenerateImage mints a fresh JWT each attempt, so an
	// auth failure means the cookie itself is dead — no refresher (nil).
	return s.runPoolWithFailover(ctx, eventID, "leonardo", active, "image", func(token model.TokenAccount) ([]byte, error) {
		// Atomically pre-deduct the per-generation cost so concurrent picks of the
		// same near-empty account can't over-commit it. A known-insufficient
		// balance surfaces as quota → the driver fails over to the next account.
		allowed, deducted, rerr := s.tokens.ReserveQuota(ctx, "leonardo", token.ID, leonardoMinCredits)
		if rerr != nil {
			return nil, fmt.Errorf("%w: reserve: %v", leonardo.ErrTemporaryUpstream, rerr)
		}
		if !allowed {
			return nil, leonardo.ErrQuotaExhausted
		}
		data, _, genErr := s.leonardo.GenerateImage(ctx, token.Value, upstreamModel, in.Prompt, width, height, nil, refs)
		if genErr != nil {
			// Release the hold so a failed render doesn't burn credits.
			if deducted {
				_ = s.tokens.RefundQuota(ctx, "leonardo", token.ID, leonardoMinCredits)
			}
			return nil, genErr
		}
		// Success → overwrite the held value with the REAL upstream balance and
		// sink to 限额 if below the floor (best-effort; never fails a done render).
		s.reconcileLeonardoCredits(ctx, token.ID, token.Value)
		return data, nil
	}, func(e error) (bool, bool, bool, bool) {
		return errors.Is(e, leonardo.ErrAuth), errors.Is(e, leonardo.ErrQuotaExhausted), errors.Is(e, leonardo.ErrTemporaryUpstream), false
	}, nil, false)
}

// reconcileLeonardoCredits re-fetches an account's real token balance after a
// render and writes it back, flipping the account to 限额 when below the per-gen
// floor. Stores the daily renewal time so RecoverQuota can auto-recover it.
func (s *V1Service) reconcileLeonardoCredits(ctx context.Context, tokenID, cookie string) {
	if s.leonardo == nil {
		return
	}
	data, err := s.leonardo.FetchCreditsBalance(ctx, cookie)
	if err != nil {
		return
	}
	rem, ok := data["remaining"].(int)
	if !ok {
		return
	}
	item, err := s.tokens.Get(ctx, "leonardo", tokenID)
	if err != nil {
		return
	}
	meta := cloneJSONMap(item.Meta)
	meta["cached_quota_remaining"] = rem
	meta["cached_quota_at"] = int(time.Now().Unix())
	patch := map[string]any{"meta": meta}
	patch["cached_quota_reset_after"] = leonardoResetAfter(stringValue(data["available_until"]))
	if rem < leonardoMinCredits && item.Status == "active" {
		patch["status"] = "quota"
	}
	_, _ = s.tokens.Update(ctx, "leonardo", tokenID, patch)
}

// kreaRefreshAndPersist ensures the account's Krea cookie has a valid access token
// (refreshing via the rotating refresh_token when expired) and persists the new
// cookie — the refresh_token is single-use, so the rotated value MUST be saved.
func kreaRefreshAndPersist(ctx context.Context, client *krea.Client, tokens *repo.TokenRepository, tokenID, cookie string) (string, error) {
	if client == nil {
		return cookie, nil
	}
	fresh, changed, err := client.RefreshIfNeeded(ctx, cookie)
	if err != nil {
		return "", err
	}
	if changed && tokenID != "" {
		_, _ = tokens.Update(ctx, "krea", tokenID, map[string]any{"value": fresh})
	}
	return fresh, nil
}

// kreaDimensions maps the catalog's resolution+ratio to Krea pixel sizes.
func kreaDimensions(resolution, aspectRatio string) (int, int) {
	res := strings.ToUpper(strings.TrimSpace(resolution))
	ar := strings.TrimSpace(aspectRatio)
	if res == "2K" {
		switch ar {
		case "4:3":
			return 2048, 1536
		case "3:4":
			return 1536, 2048
		case "16:9":
			return 2048, 1152
		case "9:16":
			return 1152, 2048
		default: // 1:1
			return 2048, 2048
		}
	}
	switch ar { // 1K (default)
	case "4:3":
		return 1024, 768
	case "3:4":
		return 768, 1024
	case "16:9":
		return 1024, 576
	case "9:16":
		return 576, 1024
	default: // 1:1
		return 1024, 1024
	}
}

func (s *V1Service) generateKreaImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.krea == nil {
		return nil, errors.New("krea client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.krea.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "krea")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		// No numeric floor — Krea signals 限额 with a 402 at generation time, which
		// the failover driver turns into mark-quota + next account.
		if item.Status == "active" && !item.Dead && strings.TrimSpace(item.Value) != "" {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("krea", active)

	width, height := kreaDimensions(resolution, aspectRatio)
	refLimit := modelItem.MaxReferenceImages
	if refLimit <= 0 {
		refLimit = 1
	}
	refs, err := decodeReferenceImages(in.ReferenceImages, refLimit)
	if err != nil {
		return nil, err
	}

	return s.runPoolWithFailover(ctx, eventID, "krea", active, "image", func(token model.TokenAccount) ([]byte, error) {
		// Refresh the (rotating) Supabase token if expired and persist the new
		// cookie, then generate with the fresh cookie.
		cookie, rerr := kreaRefreshAndPersist(ctx, s.krea, s.tokens, token.ID, token.Value)
		if rerr != nil {
			return nil, rerr
		}
		data, _, genErr := s.krea.GenerateImage(ctx, cookie, in.Prompt, width, height, refs)
		return data, genErr
	}, func(e error) (bool, bool, bool, bool) {
		return errors.Is(e, krea.ErrAuth), errors.Is(e, krea.ErrQuotaExhausted), errors.Is(e, krea.ErrTemporaryUpstream), false
	}, nil, false)
}

// imagineRefreshAndPersist ensures the account's Imagine credential has a valid
// access token (refreshing via the rotating refreshToken when expired) and
// persists the new credential — both tokens rotate, so the value MUST be saved.
func imagineRefreshAndPersist(ctx context.Context, client *imagine.Client, tokens *repo.TokenRepository, tokenID, cred string) (string, error) {
	if client == nil {
		return cred, nil
	}
	fresh, changed, err := client.RefreshIfNeeded(ctx, cred)
	if err != nil {
		return "", err
	}
	if changed && tokenID != "" {
		_, _ = tokens.Update(ctx, "imagine", tokenID, map[string]any{"value": fresh})
	}
	return fresh, nil
}

// imagineStyle maps the catalog model id to its upstream style_id + resolution.
func imagineStyle(modelID string) (int, string) {
	if strings.TrimSpace(modelID) == "imagine-1.5pro" {
		return 41004, "4K"
	}
	return 41001, "2K"
}

func (s *V1Service) generateImagineImage(ctx context.Context, eventID string, modelItem *model.ModelConfig, in V1ImageRequest, aspectRatio, resolution string) ([]byte, error) {
	if s.imagine == nil {
		return nil, errors.New("imagine client not configured")
	}
	if s.settings != nil {
		if proxy, err := s.settings.GetValue(ctx, "proxy.url"); err == nil {
			s.imagine.SetProxy(proxy)
		}
	}

	items, err := s.tokens.ListByPool(ctx, "imagine")
	if err != nil {
		return nil, err
	}
	var active []model.TokenAccount
	for _, item := range items {
		// No numeric floor — Imagine signals 限额 with a 402 at generation time,
		// which the failover driver turns into mark-quota + next account.
		if item.Status == "active" && !item.Dead && strings.TrimSpace(item.Value) != "" {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return nil, ErrNoProviderAccount
	}
	s.rotateRoundRobin("imagine", active)

	// Each model supports exactly one resolution (2K / 4K) — force it per model.
	styleID, res := imagineStyle(modelItem.ID)

	return s.runPoolWithFailover(ctx, eventID, "imagine", active, "image", func(token model.TokenAccount) ([]byte, error) {
		// Refresh the (rotating) access token if expired and persist the new
		// credential, then generate with the fresh token.
		cred, rerr := imagineRefreshAndPersist(ctx, s.imagine, s.tokens, token.ID, token.Value)
		if rerr != nil {
			return nil, rerr
		}
		data, _, genErr := s.imagine.GenerateImage(ctx, cred, styleID, res, aspectRatio, in.Prompt)
		if genErr != nil {
			return nil, genErr
		}
		return data, nil
	}, func(e error) (bool, bool, bool, bool) {
		return errors.Is(e, imagine.ErrAuth), errors.Is(e, imagine.ErrQuotaExhausted), errors.Is(e, imagine.ErrTemporaryUpstream), false
	}, nil, false)
}

func (s *V1Service) refundIfNeeded(ctx context.Context, principal *APIPrincipal, eventID string, price float64) error {
	if principal == nil || principal.User == nil || price <= 0 {
		return nil
	}
	// Exactly-once: claim the refund via the event's `refunded` flag. If another
	// path (e.g. the abandoned-purge sweep) already refunded, MarkRefunded
	// returns false and we skip — no double refund.
	claimed, err := s.events.MarkRefunded(ctx, eventID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	updated, err := s.users.AdjustCredits(ctx, principal.User.ID, price)
	if err == nil {
		principal.User = updated
	}
	return err
}

func (s *V1Service) maybeGrantInviteReward(ctx context.Context, principal *APIPrincipal) error {
	if principal == nil || principal.User == nil || s.settings == nil {
		return nil
	}
	enabledRaw, err := s.settings.GetValue(ctx, "credits.invite_enabled")
	if err != nil {
		return err
	}
	if !parseBoolSetting(enabledRaw, true) {
		return nil
	}
	rewardRaw, err := s.settings.GetValue(ctx, "credits.invite_reward")
	if err != nil {
		return err
	}
	_, err = s.users.GrantInviteReward(ctx, principal.User.ID, parseIntSetting(rewardRaw, 3))
	return err
}

// ensureReferenceSizes rejects any reference image over the byte cap BEFORE
// charging, so an oversized image fails fast (no charge, no pending-log churn)
// across every entry path — session /generate, API-key /v1, and admin /test.
// decodeReferenceImages re-checks at decode time as a backstop; this mirrors its
// base64 length pre-check (decoded ≈ len(b64)*3/4).
func ensureReferenceSizes(inputs []string) error {
	for _, raw := range inputs {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if (len(v)*3)/4 > maxReferenceImageBytes {
			return ErrReferenceTooLarge
		}
	}
	return nil
}

func decodeReferenceImages(inputs []string, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 1
	}
	if len(inputs) > limit {
		return nil, errors.New("too many reference images")
	}
	out := make([][]byte, 0, len(inputs))
	for _, raw := range inputs {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		// Only raw base64 is accepted (no "data:...;base64," URL prefix). A data
		// URL now fails to decode rather than being silently stripped.
		// decoded size ≈ len(b64) * 3 / 4 — reject oversized payloads up front,
		// before allocating the decoded buffer.
		if (len(v)*3)/4 > maxReferenceImageBytes {
			return nil, ErrReferenceTooLarge
		}
		data, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(v)
			if err != nil {
				return nil, errors.New("invalid reference image encoding")
			}
		}
		if len(data) == 0 {
			return nil, errors.New("empty reference image")
		}
		if len(data) > maxReferenceImageBytes {
			return nil, ErrReferenceTooLarge
		}
		out = append(out, data)
	}
	return out, nil
}

func parseImageSize(size, aspectRatio, resolution string) (string, string) {
	ar := strings.TrimSpace(strings.ReplaceAll(aspectRatio, "x", ":"))
	rs := strings.TrimSpace(resolution)
	if size != "" && strings.Contains(strings.ToLower(size), "x") {
		var w, h int
		_, _ = fmt.Sscanf(strings.ToLower(size), "%dx%d", &w, &h)
		if w > 0 && h > 0 {
			if ar == "" {
				ar = guessRatio(w, h)
			}
			if rs == "" {
				maxEdge := w
				if h > maxEdge {
					maxEdge = h
				}
				switch {
				case maxEdge >= 3500:
					rs = "4K"
				case maxEdge >= 1800:
					rs = "2K"
				default:
					rs = "1K"
				}
			}
		}
	}
	if ar == "" {
		ar = "1:1"
	}
	if rs == "" {
		rs = "2K"
	}
	return ar, rs
}

// snapRatio returns the entry in supported closest in value to ar ("W:H").
// ar is returned as-is when it's already supported, unparsable, or the model
// has no ratio list.
func snapRatio(ar string, supported []string) string {
	parse := func(s string) (float64, bool) {
		var w, h int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			return 0, false
		}
		return float64(w) / float64(h), true
	}
	v, ok := parse(ar)
	if !ok || len(supported) == 0 {
		return ar
	}
	best, bestDelta := "", 0.0
	for _, s := range supported {
		if strings.TrimSpace(strings.ReplaceAll(s, "x", ":")) == ar {
			return ar
		}
		sv, sok := parse(strings.ReplaceAll(s, "x", ":"))
		if !sok {
			continue
		}
		if d := absFloat(v - sv); best == "" || d < bestDelta {
			best, bestDelta = strings.TrimSpace(strings.ReplaceAll(s, "x", ":")), d
		}
	}
	if best == "" {
		return ar
	}
	return best
}

func guessRatio(w, h int) string {
	type candidate struct {
		W int
		H int
	}
	// The 17 ratios actually used across our models. Must stay in sync with the
	// custom-model picker (CustomModelModal RATIO_OPTS) and the docs 对照表, so a
	// /v1 `size` maps to exactly one of them. 9:21 is intentionally absent —
	// no image provider accepts it (Runway 400s on it). snapRatio then clamps
	// the guess to the target model's own supported list.
	candidates := []candidate{
		{1, 1},
		{5, 4}, {4, 3}, {3, 2}, {16, 9}, {2, 1}, {21, 9}, {3, 1}, {4, 1}, {8, 1}, // 横
		{4, 5}, {3, 4}, {2, 3}, {9, 16}, {1, 3}, {1, 4}, {1, 8}, // 竖
	}
	best := candidates[0]
	bestDelta := absFloat(float64(w)/float64(h) - float64(best.W)/float64(best.H))
	for _, item := range candidates[1:] {
		delta := absFloat(float64(w)/float64(h) - float64(item.W)/float64(item.H))
		if delta < bestDelta {
			best = item
			bestDelta = delta
		}
	}
	return fmt.Sprintf("%d:%d", best.W, best.H)
}

// firstPricedResolution returns the model's lowest priced image tier (1K/2K/4K
// order), or "" if none is priced. Used to rescue a request whose resolution
// the model doesn't support.
func firstPricedResolution(item *model.ModelConfig) string {
	if item == nil {
		return ""
	}
	for _, r := range []string{"1K", "2K", "4K"} {
		if _, ok := jsonMapFloat(item.Prices, r); ok {
			return r
		}
	}
	return ""
}

// resolutionForQuality maps OpenAI's `quality` to one of the model's priced
// resolution tiers: low→1K, medium→2K, high→4K, auto/blank→the model's lowest
// priced tier. The desired tier is clamped to the nearest tier the model
// actually prices (e.g. seedream is 2K/4K only: low→2K, high→4K).
func resolutionForQuality(item *model.ModelConfig, quality string) string {
	order := []string{"1K", "2K", "4K"}
	var priced []string
	for _, r := range order {
		if _, ok := jsonMapFloat(item.Prices, r); ok {
			priced = append(priced, r)
		}
	}
	if len(priced) == 0 {
		return firstPricedResolution(item)
	}
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	want, ok := rank[strings.ToLower(strings.TrimSpace(quality))]
	if !ok {
		return priced[0] // auto / unknown → model default (lowest priced)
	}
	idxOf := func(r string) int {
		for i, v := range order {
			if v == r {
				return i
			}
		}
		return 0
	}
	best, bestDist := priced[0], 99
	for _, r := range priced {
		d := idxOf(r) - want
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = r, d
		}
	}
	return best
}

// modelPrice returns the charge for (kind, resolution, duration). The set of
// supported tiers is always driven by the NORMAL prices; `agent` only overrides
// the amount with the agent price when one is set for that tier (else it falls
// back to the normal price).
func modelPrice(item *model.ModelConfig, kind, resolution, duration string, agent bool) (float64, bool) {
	if item == nil {
		return 0, false
	}
	// tierPrice: normal price gates support; agent price (if present) overrides.
	tierPrice := func(normal, agentMap map[string]any, key string) (float64, bool) {
		nv, ok := jsonMapFloat(normal, key)
		if !ok {
			return 0, false
		}
		if agent {
			if av, aok := jsonMapFloat(agentMap, key); aok {
				return av, true
			}
		}
		return nv, true
	}
	if kind == "video" {
		rv, rok := tierPrice(item.Prices, item.PricesAgent, resolution)
		dv, dok := tierPrice(item.DurationPrices, item.DurationPricesAgent, duration)
		if !rok || !dok {
			return 0, false
		}
		return rv + dv, true
	}
	return tierPrice(item.Prices, item.PricesAgent, resolution)
}

func jsonMapFloat(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		// datatypes.JSONMap.Scan decodes with UseNumber(), so values loaded from
		// the DB arrive as json.Number — NOT float64. Without this case every
		// price read back from Postgres looked "unpriced".
		if f, err := x.Float64(); err == nil {
			return f, true
		}
	case string:
		var out float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%f", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

func sanitizeOwnerName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseDurationSeconds(raw string) int {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimSuffix(raw, "s")
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return 5
	}
	return n
}

func resolveAdobeVideoEngine(modelID string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "gemini-veo31", "firefly-veo31":
		// Use the fast tier — it's the only Veo 3.1 version this account is
		// entitled to (standard "3.1-generate" returns 403 user_not_entitled).
		// "firefly-veo31" is the legacy id, kept for back-compat with historical
		// rows/logs; the model is branded "gemini-veo31" now.
		return "veo31-fast", ""
	case "firefly-ray":
		return "luma", ""
	case "firefly-video":
		return "firefly-video", ""
	default:
		return "sora2", ""
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func principalCredits(principal *APIPrincipal) float64 {
	if principal == nil || principal.User == nil {
		return 0
	}
	return principal.User.Credits
}

// markTokenFailure applies Python mark_bad semantics for a failed generation
// attempt against a pool token. It always bumps fail counters; the status side
// effects depend on the failure reason and the provider/pool.
//
//   - quota:  status="quota"; when no cached_quota_reset_after is present, set
//     quota_recover_at to next UTC midnight so the maintenance loop can revive it.
//   - auth on chatgpt: status="disabled" + dead=true (the access token IS the
//     credential; a 401 means it's dead).
//   - auth on adobe: NOT disabled/dead — the access token auto-refreshes from the
//     cookie, so rotate for this request and let the refresh loop mint a new one.
//   - other (non-auth/non-quota): NEITHER pool is auto-disabled — accounts stay
//     active/green and fails is tracked only for rotation ordering.
func (s *V1Service) markTokenFailure(ctx context.Context, pool string, token model.TokenAccount, kind string, isAuth, isQuota bool) {
	patch := map[string]any{
		"last_used_at": time.Now(),
		"fail_total":   gorm.Expr("fail_total + 1"),
		"fails":        gorm.Expr("fails + 1"),
	}
	switch {
	case isQuota:
		// Adobe quota is per-kind: a video-quota error must not block image
		// requests (and vice-versa). Flag only the failing kind, and only sink
		// the account into the shared "quota" waiting status once BOTH kinds are
		// limited. Other pools (chatgpt) are single-kind, so they go straight to
		// "quota" as before.
		if pool == "adobe" {
			imageLimited := token.ImageLimited
			videoLimited := token.VideoLimited
			if kind == "video" {
				videoLimited = true
				patch["video_limited"] = true
			} else {
				imageLimited = true
				patch["image_limited"] = true
			}
			if imageLimited && videoLimited {
				patch["status"] = "quota"
			}
		} else {
			patch["status"] = "quota"
		}
		if strings.TrimSpace(token.CachedQuotaResetAfter) == "" {
			recoverAt := time.Unix((time.Now().Unix()/86400+1)*86400, 0).UTC()
			patch["quota_recover_at"] = &recoverAt
		}
	case isAuth:
		// Adobe auth failures are NOT disabling: the access token refreshes from
		// the cookie. chatgpt/runway/leonardo auth means the stored credential is
		// dead — a raw JWT (chatgpt/runway) or a cookie whose session no longer
		// authenticates (leonardo) — there's nothing left to refresh from.
		if pool == "chatgpt" || pool == "runway" || pool == "leonardo" || pool == "krea" || pool == "imagine" || pool == "grok" {
			patch["status"] = "disabled"
			patch["dead"] = true
		}
	default:
		// Neither pool is auto-disabled on generic (non-auth / non-quota) failures
		// — the account usually still works, so it stays active (green). fails is
		// only tracked for rotation ordering. (A chatgpt *auth* failure still marks
		// the token dead in the isAuth case above; that is a genuinely dead token.)
	}
	_, _ = s.tokens.Update(ctx, pool, token.ID, patch)
}

// markTokenDead disables an account and marks it dead on a fatal upstream error
// (a non-overload temporary Adobe failure that ops policy treats as account death).
func (s *V1Service) markTokenDead(ctx context.Context, pool string, token model.TokenAccount, kind string) {
	_, _ = s.tokens.Update(ctx, pool, token.ID, map[string]any{
		"last_used_at": time.Now(),
		"fail_total":   gorm.Expr("fail_total + 1"),
		"fails":        gorm.Expr("fails + 1"),
		"status":       "disabled",
		"dead":         true,
	})
}

// nextCursor returns the pool's current round-robin position and atomically
// advances it by one. Concurrent callers each get a distinct value, so parallel
// picks land on different accounts instead of racing onto the same one. The
// counter is in-memory (per process): it resets on restart, which only shifts
// the rotation's starting point — distribution stays even.
func (s *V1Service) nextCursor(pool string) uint64 {
	v, _ := s.tokenCursors.LoadOrStore(pool, new(uint64))
	return atomic.AddUint64(v.(*uint64), 1) - 1
}

// rotateRoundRobin orders the active accounts by a stable key (ID) and rotates
// the slice in place so iteration begins at the pool's current cursor position,
// then advances the cursor. This is strict round-robin: account selection
// cycles in fixed order regardless of fails or last_used. The fall-through
// retry chain is preserved — on failure the caller's loop simply continues to
// the next account in rotation order.
func (s *V1Service) rotateRoundRobin(pool string, items []model.TokenAccount) {
	if len(items) <= 1 {
		return
	}
	// Weight = priority: higher-weight accounts come first, so the scheduler tries
	// them before lower-weight ones (and only falls through when they're at their
	// concurrency cap). Within the SAME weight all accounts are equal, so they're
	// rotated by the pool cursor for even distribution.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Weight != items[j].Weight {
			return items[i].Weight > items[j].Weight
		}
		return items[i].ID < items[j].ID
	})
	start := int(s.nextCursor(pool))
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && items[j].Weight == items[i].Weight {
			j++
		}
		if g := j - i; g > 1 {
			off := start % g
			if off != 0 {
				grp := items[i:j]
				rot := make([]model.TokenAccount, 0, g)
				rot = append(rot, grp[off:]...)
				rot = append(rot, grp[:off]...)
				copy(grp, rot)
			}
		}
		i = j
	}
}
