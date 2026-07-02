package service

import (
	"context"
	"sync"
	"sync/atomic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"backend/internal/model"
	"backend/internal/provider/adobe"
	"backend/internal/provider/chatgpt"
	"backend/internal/provider/imagine"
	"backend/internal/provider/krea"
	"backend/internal/provider/leonardo"
	"backend/internal/provider/grok"
	"backend/internal/provider/runway"
	"backend/internal/repo"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var validTokenPools = map[string]string{
	"chatgpt":  "openai",
	"adobe":    "adobe",
	"runway":   "runway",
	"leonardo": "leonardo",
	"krea":     "krea",
	"imagine":  "imagine",
	"grok":     "grok",
	"custom":   "custom",
}

type TokenService struct {
	tokens   *repo.TokenRepository
	refresh  *repo.RefreshProfileRepository
	events   *repo.EventRepository
	settings *repo.SiteSettingRepository
	adobe    *adobe.Client
	chatgpt  *chatgpt.Client
	runway   *runway.Client
	leonardo *leonardo.Client
	krea     *krea.Client
	imagine  *imagine.Client
	grok     *grok.Client
	// sem caps concurrent background pending-probe goroutines (mirrors Python's
	// 10-worker _quota_check_pool) so a big paste doesn't fire hundreds of
	// simultaneous upstream requests.
	sem chan struct{}
	// kreaActivating guards the once-per-day krea /app activation sweep so the 60s
	// maintenance tick can't pile up overlapping sweeps.
	kreaActivating atomic.Bool
}

func NewTokenService(tokens *repo.TokenRepository, refresh *repo.RefreshProfileRepository, events *repo.EventRepository, settings *repo.SiteSettingRepository, adobeClient *adobe.Client, chatGPTClient *chatgpt.Client, runwayClient *runway.Client, leonardoClient *leonardo.Client, kreaClient *krea.Client, imagineClient *imagine.Client, grokClient *grok.Client) *TokenService {
	return &TokenService{
		tokens:   tokens,
		refresh:  refresh,
		events:   events,
		settings: settings,
		adobe:    adobeClient,
		chatgpt:  chatGPTClient,
		runway:   runwayClient,
		leonardo: leonardoClient,
		krea:     kreaClient,
		imagine:  imagineClient,
		grok:     grokClient,
		sem:      make(chan struct{}, 10),
	}
}

// applyProxy snapshots the configured outbound proxy onto a provider client right
// before an upstream call. Adobe/ChatGPT/Runway/Leonardo all egress through it;
// without this the import/quota probes would dial from the bare server IP (which
// Leonardo rate-limits with a 429).
func (s *TokenService) applyProxy(ctx context.Context) {
	if s.settings == nil {
		return
	}
	proxy, err := s.settings.GetValue(ctx, "proxy.url")
	if err != nil {
		return
	}
	if s.leonardo != nil {
		s.leonardo.SetProxy(proxy)
	}
	if s.krea != nil {
		s.krea.SetProxy(proxy)
	}
	if s.imagine != nil {
		s.imagine.SetProxy(proxy)
	}
	if s.grok != nil {
		s.grok.SetProxy(proxy)
	}
}

// RefreshExpiringTokens proactively renews krea/imagine sessions ~10min before
// the access token expires (the providers' refreshLeadSeconds gate), so a dormant
// account's rotating refresh_token never lapses. A dead token can't be recovered
// and — for krea — also means the daily free-credit meter can't be re-created, so
// keeping it perpetually fresh is what lets额度 auto-recover each day. Called by
// the maintenance sweep; refresh only hits the network for near-expiry accounts.
func (s *TokenService) RefreshExpiringTokens(ctx context.Context) {
	items, err := s.tokens.List(ctx)
	if err != nil {
		return
	}
	s.applyProxy(ctx)
	for i := range items {
		it := items[i]
		if it.Dead || it.Status == "disabled" {
			continue
		}
		switch it.Pool {
		case "krea":
			if s.krea == nil {
				continue
			}
			if _, rerr := kreaRefreshAndPersist(ctx, s.krea, s.tokens, it.ID, it.Value); rerr != nil && errors.Is(rerr, krea.ErrAuth) {
				_, _ = s.tokens.Update(ctx, "krea", it.ID, map[string]any{"status": "disabled", "dead": true})
			}
		case "imagine":
			if s.imagine == nil {
				continue
			}
			if _, rerr := imagineRefreshAndPersist(ctx, s.imagine, s.tokens, it.ID, it.Value); rerr != nil && errors.Is(rerr, imagine.ErrAuth) {
				_, _ = s.tokens.Update(ctx, "imagine", it.ID, map[string]any{"status": "disabled", "dead": true})
			}
		}
	}
}

// ActivateKreaDue loads /app (Activate) for each krea account that hasn't been
// synced since the most recent daily reset, then re-syncs its balance. Krea only
// grants the daily free balance after the SSR app page loads, so without this an
// always-active account (one that never went 限额, hence never recovered) would
// read 0 / 402 after the reset. Runs in the background off the maintenance tick,
// guarded so sweeps never overlap, bounded concurrency to avoid a reset burst.
func (s *TokenService) ActivateKreaDue(ctx context.Context) {
	if s.krea == nil {
		return
	}
	if !s.kreaActivating.CompareAndSwap(false, true) {
		return // a sweep is already running
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		defer s.kreaActivating.Store(false)
		items, err := s.tokens.ListByPool(bg, "krea")
		if err != nil {
			return
		}
		s.applyProxy(bg)
		// Most recent UTC midnight (== last Beijing-08:00 reset). An account whose
		// last sync (cached_quota_at) predates this hasn't been activated today.
		lastReset := (time.Now().Unix() / 86400) * 86400
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i := range items {
			it := items[i]
			if it.Dead || it.Status == "disabled" || strings.TrimSpace(it.Value) == "" {
				continue
			}
			if at, ok := jsonMapInt(it.Meta, "cached_quota_at"); ok && int64(at) >= lastReset {
				continue // already activated/synced since the last reset
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(it model.TokenAccount) {
				defer wg.Done()
				defer func() { <-sem }()
				actx, cancel := context.WithTimeout(bg, 90*time.Second)
				defer cancel()
				s.krea.Activate(actx, it.Value) // load /app → grant daily balance
				_, _ = s.Quota(actx, "krea", it.ID)
			}(it)
		}
		wg.Wait()
	}()
}

func (s *TokenService) List(ctx context.Context) (map[string][]ginToken, error) {
	items, err := s.tokens.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]ginToken{}
	for _, item := range items {
		out[item.Pool] = append(out[item.Pool], ginToken{
			ID:           item.ID,
			ValuePreview: previewSecret(item.Value),
			Status:       item.Status,
			Fails:        item.Fails,
			AddedAt:      item.AddedAt,
		})
	}
	return out, nil
}

func (s *TokenService) Add(ctx context.Context, pool, value, tokenID string) (*model.TokenAccount, error) {
	pool = normalizePool(pool)
	if pool == "" {
		return nil, errors.New("unknown pool")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("pool and value required")
	}
	if tokenID == "" {
		tokenID = newTokenID(pool)
	}
	return s.createToken(ctx, pool, tokenID, value, "active", nil)
}

func (s *TokenService) ImportChatGPTToken(ctx context.Context, accessToken, tokenID string) (*model.TokenAccount, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("access_token required")
	}
	// Land as pending and return instantly; a background worker probes quota and
	// flips the row active/dead (Python import_chatgpt_token). pending tokens are
	// not schedulable — the pool only hands out status=="active".
	meta := datatypes.JSONMap{"pending_check": true}
	info := chatgpt.ExtractAccountInfo(accessToken)
	_, exp := parseJWTEmailExpiry(accessToken)
	// Identity is (pool, email): reuse the existing row for this email, else mint a
	// fresh id — never trust the caller's id (it can collide with an unrelated row
	// → a spurious 23505/400 for a brand-new account).
	email := strings.TrimSpace(stringValue(info["email"]))
	if existing, _ := s.tokens.GetByPoolEmail(ctx, "chatgpt", email); existing != nil {
		tokenID = existing.ID
	} else if email != "" || tokenID == "" {
		tokenID = newTokenID("chatgpt")
	}
	item, err := s.createToken(ctx, "chatgpt", tokenID, accessToken, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if item, err = s.tokens.Update(ctx, "chatgpt", tokenID, map[string]any{
				"value": accessToken, "status": "pending", "meta": meta,
			}); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	// JWT-derived fields are free (no network) — hydrate them up front. OpenAI
	// tokens carry the email under the nested "https://api.openai.com/profile"
	// claim, so read it from ExtractAccountInfo — parseJWTEmailExpiry only sees
	// top-level claims and returns "" for ChatGPT tokens.
	patch := map[string]any{}
	if email := strings.TrimSpace(stringValue(info["email"])); email != "" {
		patch["account_email"] = email
	}
	if display := strings.TrimSpace(stringValue(info["plan_type"])); display != "" {
		patch["account_display_name"] = display
	}
	if exp != nil {
		patch["cached_quota_reset_after"] = exp.Format(time.RFC3339)
	}
	if len(patch) > 0 {
		if updated, uerr := s.tokens.Update(ctx, "chatgpt", tokenID, patch); uerr == nil {
			item = updated
		}
	}
	go s.checkPendingChatGPT(tokenID, accessToken)
	return item, nil
}

// ImportRunwayToken lands a Runway JWT as a pending account and probes its
// credit balance off-thread (mirrors ImportChatGPTToken). The workspace/team id
// (= the JWT "id" claim) is stashed in meta["team_id"] so generation can send it
// as x-runway-workspace later. Recovery time == the JWT expiry.
func (s *TokenService) ImportRunwayToken(ctx context.Context, accessToken, tokenID string) (*model.TokenAccount, error) {
	accessToken = strings.TrimSpace(strings.TrimPrefix(accessToken, "Bearer "))
	if accessToken == "" {
		return nil, errors.New("access_token required")
	}
	if !runway.IsRunwayToken(accessToken) {
		return nil, errors.New("not a runway token")
	}
	teamID := runway.TeamIDFromToken(accessToken)
	// JWT-derived fields are free (no network); email + exp are top-level claims.
	email, exp := parseJWTEmailExpiry(accessToken)
	// Identity is (pool, email): reuse the existing row for this email, else mint a
	// fresh id — never trust the caller's id (it can collide → spurious 400).
	if existing, _ := s.tokens.GetByPoolEmail(ctx, "runway", email); existing != nil {
		tokenID = existing.ID
	} else if email != "" || tokenID == "" {
		tokenID = newTokenID("runway")
	}
	meta := datatypes.JSONMap{"pending_check": true}
	if teamID != "" {
		meta["team_id"] = teamID
	}
	item, err := s.createToken(ctx, "runway", tokenID, accessToken, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if item, err = s.tokens.Update(ctx, "runway", tokenID, map[string]any{
				"value": accessToken, "status": "pending", "meta": meta,
			}); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	patch := map[string]any{}
	if email != "" {
		patch["account_email"] = email
	}
	if exp != nil {
		patch["cached_quota_reset_after"] = exp.Format(time.RFC3339)
	}
	if len(patch) > 0 {
		if updated, uerr := s.tokens.Update(ctx, "runway", tokenID, patch); uerr == nil {
			item = updated
		}
	}
	go s.checkPendingRunway(tokenID, accessToken)
	return item, nil
}

// ImportLeonardoCookie imports a Leonardo account. Unlike Adobe (which keeps a
// refresh profile to re-mint a bearer), Leonardo's stored credential IS the
// cookie — the bearer is derived on demand at generation time via get-session —
// so there's no refresh profile. Lands a pending row, then the worker validates
// the cookie + hydrates email/quota off-thread.
func (s *TokenService) ImportLeonardoCookie(ctx context.Context, cookie, tokenID string) (*model.TokenAccount, error) {
	cookie = cleanAdobeCookie(cookie) // same paste-cleanup (JSON / "Cookie:" prefix)
	if cookie == "" {
		return nil, errors.New("cookie required")
	}
	if !leonardo.IsLeonardoCookie(cookie) {
		return nil, errors.New("not a leonardo cookie")
	}
	if tokenID == "" {
		tokenID = newTokenID("leonardo")
	}
	meta := datatypes.JSONMap{"pending_check": true}
	item, err := s.createToken(ctx, "leonardo", tokenID, cookie, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			item, err = s.tokens.Update(ctx, "leonardo", tokenID, map[string]any{
				"value":  cookie,
				"status": "pending",
				"meta":   meta,
			})
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	// Fill 恢复时间 synchronously at import (next 08:00 Beijing) so it appears
	// alongside 创建时间 immediately — not only after the background quota probe.
	if updated, uerr := s.tokens.Update(ctx, "leonardo", tokenID, map[string]any{
		"cached_quota_reset_after": leonardoResetAfter(""),
	}); uerr == nil {
		item = updated
	}
	go s.checkPendingLeonardo(tokenID, cookie)
	return item, nil
}

// checkPendingLeonardo validates a freshly imported Leonardo cookie off-thread:
// get-session must succeed (else the cookie is dead → disabled), then it hydrates
// email/display-name + the token balance and the daily renewal time (so the
// maintenance sweep can auto-recover a 限额 account).
func (s *TokenService) checkPendingLeonardo(tokenID, cookie string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: leonardo pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.leonardo == nil {
		s.finishPending(ctx, "leonardo", tokenID, "active", false, nil)
		return
	}
	s.applyProxy(ctx)
	data, err := s.leonardo.FetchCreditsBalance(ctx, cookie)
	if err != nil {
		if errors.Is(err, leonardo.ErrAuth) {
			s.finishPending(ctx, "leonardo", tokenID, "disabled", true, nil)
			return
		}
		// network/proxy blip — benefit of the doubt, activate.
		s.finishPending(ctx, "leonardo", tokenID, "active", false, nil)
		return
	}
	seed := map[string]any{}
	if em := strings.TrimSpace(stringValue(data["email"])); em != "" {
		seed["account_email"] = em
	}
	if dn := strings.TrimSpace(stringValue(data["display_name"])); dn != "" {
		seed["account_display_name"] = dn
	}
	// Always fill 恢复时间: use upstream's renewal time if present, else the next
	// daily reset (08:00 Beijing == next UTC midnight).
	seed["cached_quota_reset_after"] = leonardoResetAfter(stringValue(data["available_until"]))
	if len(seed) > 0 {
		_, _ = s.tokens.Update(ctx, "leonardo", tokenID, seed)
	}
	quotaMeta := map[string]any{}
	if rem, ok := data["remaining"].(int); ok {
		quotaMeta["cached_quota_remaining"] = rem
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
	}
	if uid := strings.TrimSpace(stringValue(data["user_id"])); uid != "" {
		quotaMeta["user_id"] = uid
	}
	s.finishPending(ctx, "leonardo", tokenID, "active", false, quotaMeta)
}

// ImportKreaCookie imports a Krea account. Like Leonardo the stored credential
// IS the cookie (Supabase session); quota/generation forward it directly.
func (s *TokenService) ImportKreaCookie(ctx context.Context, cookie, tokenID string) (*model.TokenAccount, error) {
	cookie = cleanAdobeCookie(cookie) // same paste-cleanup (JSON / "Cookie:" prefix)
	if cookie == "" {
		return nil, errors.New("cookie required")
	}
	if !krea.IsKreaCookie(cookie) {
		return nil, errors.New("not a krea cookie")
	}
	// Identity is (pool, email), NOT the caller-supplied id — a colliding id from
	// the upstream importer would otherwise raise a spurious 23505/400 for a brand
	// new account. Reuse the existing row for this email; else mint a fresh unique
	// id (never trust the caller's id for a new row).
	email := krea.EmailFromCookie(cookie)
	if existing, _ := s.tokens.GetByPoolEmail(ctx, "krea", email); existing != nil {
		tokenID = existing.ID
	} else {
		tokenID = newTokenID("krea")
	}
	meta := datatypes.JSONMap{"pending_check": true}
	item, err := s.createToken(ctx, "krea", tokenID, cookie, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			item, err = s.tokens.Update(ctx, "krea", tokenID, map[string]any{
				"value":  cookie,
				"status": "pending",
				"meta":   meta,
			})
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	// Email is in the cookie (no network) — hydrate up front, plus 恢复时间.
	seed := map[string]any{"cached_quota_reset_after": leonardoResetAfter("")}
	if email != "" {
		seed["account_email"] = email
	}
	if updated, uerr := s.tokens.Update(ctx, "krea", tokenID, seed); uerr == nil {
		item = updated
	}
	go s.checkPendingKrea(tokenID, cookie)
	return item, nil
}

// checkPendingKrea validates a freshly imported Krea cookie off-thread and
// hydrates the credit balance. A 401 from billing-data → the cookie is dead.
func (s *TokenService) checkPendingKrea(tokenID, cookie string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: krea pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.krea == nil {
		s.finishPending(ctx, "krea", tokenID, "active", false, nil)
		return
	}
	s.applyProxy(ctx)
	cookie, rerr := kreaRefreshAndPersist(ctx, s.krea, s.tokens, tokenID, cookie)
	if rerr != nil {
		if errors.Is(rerr, krea.ErrAuth) {
			s.finishPending(ctx, "krea", tokenID, "disabled", true, nil)
			return
		}
		s.finishPending(ctx, "krea", tokenID, "active", false, nil)
		return
	}
	data, err := s.krea.FetchCreditsBalance(ctx, cookie)
	if err != nil {
		if errors.Is(err, krea.ErrAuth) {
			s.finishPending(ctx, "krea", tokenID, "disabled", true, nil)
			return
		}
		s.finishPending(ctx, "krea", tokenID, "active", false, nil)
		return
	}
	if em := strings.TrimSpace(stringValue(data["email"])); em != "" {
		_, _ = s.tokens.Update(ctx, "krea", tokenID, map[string]any{"account_email": em})
	}
	quotaMeta := map[string]any{}
	if rem, ok := data["remaining"].(int); ok {
		quotaMeta["cached_quota_remaining"] = rem
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
	}
	s.finishPending(ctx, "krea", tokenID, "active", false, quotaMeta)
}

// ImportImagineToken imports an Imagine.art account. The stored credential IS the
// JSON {"token","refreshToken"}; quota/generation forward it (refreshing the
// access token from the refreshToken when expired).
func (s *TokenService) ImportImagineToken(ctx context.Context, cred, tokenID string) (*model.TokenAccount, error) {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return nil, errors.New("credential required")
	}
	if !imagine.IsImagineToken(cred) {
		return nil, errors.New("not an imagine token")
	}
	// Identity is (pool, email=userId), NOT the caller-supplied id — reuse the
	// existing row for this account; else mint a fresh unique id.
	email := imagine.EmailFromCred(cred)
	if existing, _ := s.tokens.GetByPoolEmail(ctx, "imagine", email); existing != nil {
		tokenID = existing.ID
	} else {
		tokenID = newTokenID("imagine")
	}
	meta := datatypes.JSONMap{"pending_check": true}
	item, err := s.createToken(ctx, "imagine", tokenID, cred, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			item, err = s.tokens.Update(ctx, "imagine", tokenID, map[string]any{
				"value":  cred,
				"status": "pending",
				"meta":   meta,
			})
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	// userId is in the token (no network) — hydrate up front, plus 恢复时间.
	// Imagine free credits renew daily like Krea (next UTC midnight = 08:00 北京).
	seed := map[string]any{"cached_quota_reset_after": leonardoResetAfter("")}
	if email != "" {
		seed["account_email"] = email
	}
	if updated, uerr := s.tokens.Update(ctx, "imagine", tokenID, seed); uerr == nil {
		item = updated
	}
	go s.checkPendingImagine(tokenID, cred)
	return item, nil
}

// checkPendingImagine validates a freshly imported Imagine token off-thread and
// hydrates the credit balance. A 401 from /v1/credit → the token is dead.
func (s *TokenService) checkPendingImagine(tokenID, cred string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: imagine pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.imagine == nil {
		s.finishPending(ctx, "imagine", tokenID, "active", false, nil)
		return
	}
	s.applyProxy(ctx)
	cred, rerr := imagineRefreshAndPersist(ctx, s.imagine, s.tokens, tokenID, cred)
	if rerr != nil {
		if errors.Is(rerr, imagine.ErrAuth) {
			s.finishPending(ctx, "imagine", tokenID, "disabled", true, nil)
			return
		}
		s.finishPending(ctx, "imagine", tokenID, "active", false, nil)
		return
	}
	data, err := s.imagine.FetchCreditsBalance(ctx, cred)
	if err != nil {
		if errors.Is(err, imagine.ErrAuth) {
			s.finishPending(ctx, "imagine", tokenID, "disabled", true, nil)
			return
		}
		s.finishPending(ctx, "imagine", tokenID, "active", false, nil)
		return
	}
	if em := strings.TrimSpace(stringValue(data["email"])); em != "" {
		_, _ = s.tokens.Update(ctx, "imagine", tokenID, map[string]any{"account_email": em})
	}
	quotaMeta := map[string]any{}
	if rem, ok := data["remaining"].(int); ok {
		quotaMeta["cached_quota_remaining"] = rem
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
	}
	s.finishPending(ctx, "imagine", tokenID, "active", false, quotaMeta)
}

func (s *TokenService) ImportAdobeCookie(ctx context.Context, cookie, tokenID string) (*model.TokenAccount, *model.RefreshProfile, error) {
	cookie = cleanAdobeCookie(cookie)
	if cookie == "" {
		return nil, nil, errors.New("cookie required")
	}
	if tokenID == "" {
		tokenID = newTokenID("adobe")
	}
	now := time.Now()
	// Register the cookie refresh profile up front. Push next_retry_at out a full
	// interval so the maintenance loop doesn't race the import worker on the first
	// exchange — the worker below owns the initial hydrate.
	nextRetry := now.Add(54000 * time.Second)
	profile := &model.RefreshProfile{
		ID:              tokenID,
		Name:            tokenID,
		Pool:            "adobe",
		Kind:            "adobe_cookie",
		Cookie:          cookie,
		Enabled:         true,
		IntervalSeconds: 54000,
		ImportedAt:      &now,
		NextRetryAt:     &nextRetry,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.refresh.Create(ctx, profile); err != nil && !errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, nil, err
	}
	// Land a placeholder pending token (value filled in by the worker). NOT
	// schedulable — the pool only hands out status=="active". The import returns
	// instantly; the row flips active/dead once the worker finishes the three
	// Adobe round-trips. Mirrors Python import_adobe_cookie.
	meta := datatypes.JSONMap{"pending_check": true}
	item, err := s.createToken(ctx, "adobe", tokenID, "", "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			item, err = s.tokens.Update(ctx, "adobe", tokenID, map[string]any{
				"status": "pending",
				"meta":   meta,
			})
			if err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	}
	go s.checkPendingAdobe(tokenID, cookie)
	return item, profile, nil
}

// checkPendingAdobe runs the three Adobe round-trips off-thread for a freshly
// imported cookie so the import request returns instantly (Python
// _check_pending_adobe). Step 1 (exchange) is authoritative — a bad/expired
// cookie can't mint a token, so failure marks the row dead. Steps 2-3 (credits /
// profile) are best-effort hydration.
func (s *TokenService) checkPendingAdobe(tokenID, cookie string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: adobe pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if s.adobe == nil {
		s.finishPending(ctx, "adobe", tokenID, "disabled", true, nil)
		return
	}
	result, err := s.adobe.ExchangeCookie(ctx, cookie)
	if err != nil {
		s.finishPending(ctx, "adobe", tokenID, "disabled", true, nil)
		_, _ = s.refresh.Update(ctx, tokenID, map[string]any{
			"last_attempt_at":      time.Now(),
			"last_error":           err.Error(),
			"consecutive_failures": 1,
		})
		return
	}
	// Seed the real access token, then activate so the pool can schedule it.
	seed := map[string]any{"value": result.AccessToken}
	email, exp := parseJWTEmailExpiry(result.AccessToken)
	if email != "" {
		seed["account_email"] = email
	}
	if exp != nil {
		seed["cached_quota_reset_after"] = exp.Format(time.RFC3339)
	}
	_, _ = s.tokens.Update(ctx, "adobe", tokenID, seed)

	quotaMeta := map[string]any{}
	if cb, e := s.adobe.FetchCreditsBalance(ctx, result.AccessToken); e == nil {
		if ra := strings.TrimSpace(stringValue(cb["available_until"])); ra != "" {
			_, _ = s.tokens.Update(ctx, "adobe", tokenID, map[string]any{"cached_quota_reset_after": ra})
		}
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
		if rem, ok := cb["remaining"].(int); ok {
			quotaMeta["cached_quota_remaining"] = rem
		}
	}
	if prof, e := s.adobe.FetchAccountProfile(ctx, result.AccessToken); e == nil {
		p := map[string]any{}
		if em := strings.TrimSpace(stringValue(prof["email"])); em != "" {
			p["account_email"] = em
		}
		if dn := strings.TrimSpace(stringValue(prof["display_name"])); dn != "" {
			p["account_display_name"] = dn
		}
		if len(p) > 0 {
			_, _ = s.tokens.Update(ctx, "adobe", tokenID, p)
		}
	}

	s.finishPending(ctx, "adobe", tokenID, "active", false, quotaMeta)
	_, _ = s.refresh.Update(ctx, tokenID, map[string]any{
		"last_attempt_at":      time.Now(),
		"last_success_at":      time.Now(),
		"last_error":           "",
		"consecutive_failures": 0,
		"next_retry_at":        time.Now().Add(54000 * time.Second),
	})
}

// checkPendingChatGPT probes a freshly imported ChatGPT token's quota off-thread
// (Python _check_pending_chatgpt). 401 → dead; a non-auth error gets the benefit
// of the doubt and activates so a transient blip can't sideline a good account.
func (s *TokenService) checkPendingChatGPT(tokenID, accessToken string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: chatgpt pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.chatgpt == nil {
		s.finishPending(ctx, "chatgpt", tokenID, "active", false, nil)
		return
	}
	data, err := s.chatgpt.FetchImageQuota(ctx, accessToken)
	if err != nil {
		// network/proxy error — benefit of the doubt, activate.
		s.finishPending(ctx, "chatgpt", tokenID, "active", false, nil)
		return
	}
	if boolValueWithDefault(data["auth_failed"], false) {
		s.finishPending(ctx, "chatgpt", tokenID, "disabled", true, nil)
		return
	}
	rem, exhausted := chatgptRemaining(data)
	quotaMeta := map[string]any{
		"cached_quota_remaining": rem,
		"cached_quota_at":        int(time.Now().Unix()),
	}
	// reset 时间:优先用 OpenAI 的 reset_after,缺失则默认次日重置,保证限额号能被
	// RecoverQuota 到点自动复活、重新探测,而不会永久搁置。
	reset := strings.TrimSpace(stringValue(data["reset_after"]))
	if reset == "" {
		reset = leonardoResetAfter("")
	}
	_, _ = s.tokens.Update(ctx, "chatgpt", tokenID, map[string]any{"cached_quota_reset_after": reset})
	// remaining<=0(0 / 负数 / 未知)→ 置「限额」,池子不再调度,到点自动恢复。
	status := "active"
	if exhausted {
		status = "quota"
	}
	s.finishPending(ctx, "chatgpt", tokenID, status, false, quotaMeta)
}

// chatgptRemaining normalizes OpenAI's image_gen remaining: the raw rate-limit
// counter can go NEGATIVE on over-used accounts, and "—"(absent)means unknown —
// both clamp to 0, and 0 counts as exhausted (→ 限额). Returns (remaining≥0,
// exhausted).
func chatgptRemaining(data map[string]any) (int, bool) {
	raw, ok := data["remaining"]
	if !ok || raw == nil {
		return 0, true
	}
	rem := intValue(raw)
	if rem < 0 {
		rem = 0
	}
	return rem, rem <= 0
}

// checkPendingRunway probes a freshly imported Runway token's credit balance
// off-thread (mirrors checkPendingChatGPT). ErrAuth → dead; any other error
// gets the benefit of the doubt and activates so a transient blip can't sideline
// a good account.
func (s *TokenService) checkPendingRunway(tokenID, accessToken string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: runway pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.runway == nil {
		s.finishPending(ctx, "runway", tokenID, "active", false, nil)
		return
	}
	data, err := s.runway.FetchCreditsBalance(ctx, accessToken)
	if err != nil {
		if errors.Is(err, runway.ErrAuth) {
			s.finishPending(ctx, "runway", tokenID, "disabled", true, nil)
			return
		}
		// network/proxy error — benefit of the doubt, activate.
		s.finishPending(ctx, "runway", tokenID, "active", false, nil)
		return
	}
	quotaMeta := map[string]any{}
	if rem, ok := data["remaining"].(int); ok {
		quotaMeta["cached_quota_remaining"] = rem
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
	}
	if used, ok := data["used"].(int); ok {
		quotaMeta["cached_quota_used"] = used
	}
	if total, ok := data["total"].(int); ok {
		quotaMeta["cached_quota_total"] = total
	}
	s.finishPending(ctx, "runway", tokenID, "active", false, quotaMeta)
}

// ImportGrokToken lands a Grok website "sso" cookie (a JWT carrying only a
// session_id) as a pending account and probes its credit balance off-thread.
// Identity is the session id (grok sso has no email/exp claim). No refresh: a
// dead session just dies (失效就失效).
func (s *TokenService) ImportGrokToken(ctx context.Context, ssoToken, tokenID string) (*model.TokenAccount, error) {
	ssoToken = strings.TrimSpace(strings.TrimPrefix(ssoToken, "Bearer "))
	ssoToken = strings.TrimPrefix(ssoToken, "sso=")
	if ssoToken == "" {
		return nil, errors.New("sso token required")
	}
	if !grok.IsGrokToken(ssoToken) {
		return nil, errors.New("not a grok sso token")
	}
	sid := grok.SessionIDFromToken(ssoToken)
	// Resolve the real account email up front (GET /api/auth/session) for dedup +
	// display — the sso session_id rotates per login, so email is the stable id.
	email := ""
	if s.grok != nil {
		s.applyProxy(ctx)
		// Best-effort email resolution. A dead/blocked probe must NOT block import:
		// land the account and let the async checkPendingGrok disable it if the
		// session is truly dead. Empty email just falls back to the session id.
		if e, _, ferr := s.grok.FetchSession(ctx, ssoToken); ferr == nil {
			email = e
		}
	}
	idKey := email
	if idKey == "" {
		idKey = sid
	}
	// Identity is (pool, email): reuse the row for this account, else mint.
	if existing, _ := s.tokens.GetByPoolEmail(ctx, "grok", idKey); existing != nil {
		tokenID = existing.ID
	} else if idKey != "" || tokenID == "" {
		tokenID = newTokenID("grok")
	}
	meta := datatypes.JSONMap{"pending_check": true}
	if sid != "" {
		meta["session_id"] = sid
	}
	item, err := s.createToken(ctx, "grok", tokenID, ssoToken, "pending", meta)
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if item, err = s.tokens.Update(ctx, "grok", tokenID, map[string]any{
				"value": ssoToken, "status": "pending", "meta": meta,
			}); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	if idKey != "" {
		if updated, uerr := s.tokens.Update(ctx, "grok", tokenID, map[string]any{"account_email": idKey}); uerr == nil {
			item = updated
		}
	}
	go s.checkPendingGrok(tokenID, ssoToken)
	return item, nil
}


func (s *TokenService) checkPendingGrok(tokenID, ssoToken string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("token import: grok pending check panicked for %s: %v", tokenID, r)
		}
	}()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.grok == nil {
		s.finishPending(ctx, "grok", tokenID, "active", false, nil)
		return
	}
	s.applyProxy(ctx)
	// Validate the session first: a dead sso answers 200 {"status":"unauthenticated"},
	// which FetchSession now maps to ErrAuth → disable. Also backfill the email if
	// import couldn't resolve it (empty account_email currently shows the session id).
	if email, _, serr := s.grok.FetchSession(ctx, ssoToken); serr != nil {
		if errors.Is(serr, grok.ErrAuth) {
			s.finishPending(ctx, "grok", tokenID, "disabled", true, nil)
			return
		}
	} else if strings.TrimSpace(email) != "" {
		_, _ = s.tokens.Update(ctx, "grok", tokenID, map[string]any{"account_email": strings.TrimSpace(email)})
	}
	data, err := s.grok.FetchCreditsBalance(ctx, ssoToken)
	if err != nil {
		if errors.Is(err, grok.ErrAuth) {
			s.finishPending(ctx, "grok", tokenID, "disabled", true, nil)
			return
		}
		s.finishPending(ctx, "grok", tokenID, "active", false, nil)
		return
	}
	quotaMeta := map[string]any{}
	if rem, ok := data["remaining"].(int); ok {
		quotaMeta["cached_quota_remaining"] = rem
		quotaMeta["cached_quota_at"] = int(time.Now().Unix())
	}
	if used, ok := data["used"].(int); ok {
		quotaMeta["cached_quota_used"] = used
	}
	if total, ok := data["total"].(int); ok {
		quotaMeta["cached_quota_total"] = total
	}
	if reset := strings.TrimSpace(stringValue(data["reset_after"])); reset != "" {
		_, _ = s.tokens.Update(ctx, "grok", tokenID, map[string]any{"cached_quota_reset_after": reset})
	}
	s.finishPending(ctx, "grok", tokenID, "active", false, quotaMeta)
}

// ImportCustomAccount adds an upstream as a custom account: base_url + key, the
// csv list of model ids it serves (empty = all), plus optional weight and
// per-account concurrency. No probe — the account goes active immediately and is
// matched to custom models by id at generation time. Calls go direct (no proxy).
func (s *TokenService) ImportCustomAccount(ctx context.Context, baseURL, apiKey, models, name string, weight, concurrency int, tokenID string) (*model.TokenAccount, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	// Edit mode: tokenID points at an existing custom account. base_url required;
	// a blank key keeps the stored one.
	if strings.TrimSpace(tokenID) != "" {
		existing, gerr := s.tokens.Get(ctx, "custom", tokenID)
		if gerr != nil {
			return nil, gerr
		}
		if baseURL == "" {
			return nil, errors.New("base_url required")
		}
		meta := datatypes.JSONMap{"base_url": baseURL}
		if m := strings.TrimSpace(models); m != "" {
			meta["models"] = m
		}
		patch := map[string]any{"meta": meta, "weight": weight, "concurrency": concurrency, "account_email": strings.TrimSpace(name)}
		if apiKey != "" {
			patch["value"] = apiKey
		}
		item, uerr := s.tokens.Update(ctx, "custom", tokenID, patch)
		if uerr != nil {
			return nil, uerr
		}
		_ = existing
		return item, nil
	}
	if baseURL == "" || apiKey == "" {
		return nil, errors.New("base_url and key required")
	}
	meta := datatypes.JSONMap{"base_url": baseURL}
	if m := strings.TrimSpace(models); m != "" {
		meta["models"] = m
	}
	tokenID = newTokenID("custom")
	item, err := s.createToken(ctx, "custom", tokenID, apiKey, "active", meta)
	if err != nil {
		return nil, err
	}
	patch := map[string]any{}
	if strings.TrimSpace(name) != "" {
		patch["account_email"] = strings.TrimSpace(name)
	}
	if weight != 0 {
		patch["weight"] = weight
	}
	if concurrency > 0 {
		patch["concurrency"] = concurrency
	}
	if len(patch) > 0 {
		if updated, uerr := s.tokens.Update(ctx, "custom", tokenID, patch); uerr == nil {
			item = updated
		}
	}
	return item, nil
}

// finishPending writes the terminal status/dead flag and clears the pending_check
// marker (merging any cached quota) for a background import probe.
func (s *TokenService) finishPending(ctx context.Context, pool, id, status string, dead bool, quotaMeta map[string]any) {
	item, err := s.tokens.Get(ctx, pool, id)
	if err != nil {
		return
	}
	meta := cloneJSONMap(item.Meta)
	meta["pending_check"] = false
	for k, v := range quotaMeta {
		meta[k] = v
	}
	patch := map[string]any{"status": status, "meta": meta}
	if dead {
		patch["dead"] = true
	}
	_, _ = s.tokens.Update(ctx, pool, id, patch)
}

func (s *TokenService) Update(ctx context.Context, pool, id string, body map[string]any) (*model.TokenAccount, error) {
	pool = normalizePool(pool)
	if pool == "" {
		return nil, errors.New("unknown pool")
	}
	patch := map[string]any{}
	if raw, ok := body["status"]; ok {
		status := normalizeTokenStatus(stringValue(raw))
		if status == "" {
			return nil, errors.New("invalid status")
		}
		patch["status"] = status
		if status == "active" {
			patch["dead"] = false
			if _, hasFails := body["fails"]; !hasFails {
				patch["fails"] = 0
			}
		}
	}
	if raw, ok := body["value"]; ok {
		value := strings.TrimSpace(stringValue(raw))
		if value == "" {
			return nil, errors.New("value cannot be empty")
		}
		patch["value"] = value
		patch["dead"] = false
	}
	if raw, ok := body["fails"]; ok {
		patch["fails"] = intValue(raw)
	}
	if len(patch) == 0 {
		return s.tokens.Get(ctx, pool, id)
	}
	return s.tokens.Update(ctx, pool, id, patch)
}

func (s *TokenService) Delete(ctx context.Context, pool, id string) error {
	pool = normalizePool(pool)
	if pool == "" {
		return errors.New("unknown pool")
	}
	rows, err := s.tokens.Delete(ctx, pool, id)
	if err != nil {
		return err
	}
	// Also drop the matching cookie refresh profile (token id == profile id),
	// otherwise the background refresher re-creates the token. Track whether a
	// profile existed so we can mirror Python's 404-when-nothing-removed.
	profileRemoved := false
	if _, getErr := s.refresh.Get(ctx, id); getErr == nil {
		profileRemoved = true
	}
	_ = s.refresh.Delete(ctx, id)
	if rows == 0 && !profileRemoved {
		return ErrNotFound
	}
	return nil
}

// DeleteBulk removes many accounts by id (across pools) plus their cookie
// refresh profiles. Returns how many account rows were removed.
func (s *TokenService) DeleteBulk(ctx context.Context, ids []string) (int, error) {
	seen := make(map[string]struct{}, len(ids))
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		return 0, nil
	}
	rows, err := s.tokens.DeleteByIDs(ctx, clean)
	if err != nil {
		return 0, err
	}
	// Drop matching cookie refresh profiles (id == token id) so the background
	// refresher doesn't re-create the tokens.
	_ = s.refresh.DeleteByIDs(ctx, clean)
	return int(rows), nil
}

func (s *TokenService) Accounts(ctx context.Context) ([]map[string]any, error) {
	items, err := s.tokens.List(ctx)
	if err != nil {
		return nil, err
	}
	inFlight, err := s.events.InFlightByAccount(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, accountRow(item, inFlight[item.ID]))
	}
	return out, nil
}

func (s *TokenService) Quota(ctx context.Context, pool, id string) (map[string]any, error) {
	item, err := s.tokens.Get(ctx, normalizePool(pool), id)
	if err != nil {
		return nil, err
	}
	if poolToType(item.Pool) == "openai" && s.chatgpt != nil {
		data, err := s.chatgpt.FetchImageQuota(ctx, item.Value)
		if err != nil {
			return nil, err
		}
		authFailed := boolValueWithDefault(data["auth_failed"], false)
		if authFailed {
			_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
				"status": "disabled",
				"dead":   true,
				"fails":  gorm.Expr("fails + 1"),
			})
		}
		patch := map[string]any{}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		rem, exhausted := chatgptRemaining(data)
		meta["cached_quota_remaining"] = rem
		patch["meta"] = meta
		resetAfter := strings.TrimSpace(stringValue(data["reset_after"]))
		if resetAfter == "" {
			resetAfter = leonardoResetAfter("")
		}
		patch["cached_quota_reset_after"] = resetAfter
		item.CachedQuotaResetAfter = resetAfter
		// remaining<=0(负数/未知/0)→ 限额;>0 且当前是 quota → 恢复 active。
		// auth 失效已在上面置死,这里不再改它的状态。
		if !authFailed {
			if exhausted {
				patch["status"] = "quota"
			} else if item.Status == "quota" {
				patch["status"] = "active"
			}
		}
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		return map[string]any{
			"supported":       true,
			"remaining":       rem,
			"total":           nil,
			"reset_after":     emptyToNil(item.CachedQuotaResetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "adobe" && s.adobe != nil {
		data, err := s.adobe.FetchCreditsBalance(ctx, item.Value)
		if err != nil {
			if errors.Is(err, adobe.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled",
					"dead":   true,
					"fails":  gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		patch := map[string]any{}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		if remaining, ok := data["remaining"].(int); ok {
			meta["cached_quota_remaining"] = remaining
		}
		if used, ok := data["used"].(int); ok {
			meta["cached_quota_used"] = used
		}
		if total, ok := data["total"].(int); ok {
			meta["cached_quota_total"] = total
		}
		patch["meta"] = meta
		if resetAfter := strings.TrimSpace(stringValue(data["available_until"])); resetAfter != "" {
			patch["cached_quota_reset_after"] = resetAfter
			item.CachedQuotaResetAfter = resetAfter
		}
		if len(patch) > 0 {
			if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
				item = updated
			}
		}
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(item.CachedQuotaResetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "krea" && s.krea != nil {
		s.applyProxy(ctx)
		cookie, rerr := kreaRefreshAndPersist(ctx, s.krea, s.tokens, item.ID, item.Value)
		if rerr != nil {
			if errors.Is(rerr, krea.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled", "dead": true, "fails": gorm.Expr("fails + 1"),
				})
			}
			return nil, rerr
		}
		data, err := s.krea.FetchCreditsBalance(ctx, cookie)
		if err != nil {
			if errors.Is(err, krea.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled", "dead": true, "fails": gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		rem, hasRem := data["remaining"].(int)
		if hasRem {
			meta["cached_quota_remaining"] = rem
		}
		// 每日免费额度在 UTC 零点(北京 08:00)重置 —— 恢复时间始终重算为"下一个零点",
		// 不保留已过期的旧值(否则过了 08:00 还一直显示今天 08:00,不会变明天)。
		resetAfter := leonardoResetAfter("")
		patch := map[string]any{"meta": meta, "cached_quota_reset_after": resetAfter}
		// Krea 限额由生成时的 402 判定;一旦余额恢复(galactus 触发刷新后 >0),把之前
		// 沉下去的 quota 翻回 active,避免有余额却卡在限额。
		if item.Status == "quota" && hasRem && rem > 0 {
			patch["status"] = "active"
		}
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(resetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "imagine" && s.imagine != nil {
		s.applyProxy(ctx)
		cred, rerr := imagineRefreshAndPersist(ctx, s.imagine, s.tokens, item.ID, item.Value)
		if rerr != nil {
			if errors.Is(rerr, imagine.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled", "dead": true, "fails": gorm.Expr("fails + 1"),
				})
			}
			return nil, rerr
		}
		data, err := s.imagine.FetchCreditsBalance(ctx, cred)
		if err != nil {
			if errors.Is(err, imagine.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled", "dead": true, "fails": gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		rem, hasRem := data["remaining"].(int)
		if hasRem {
			meta["cached_quota_remaining"] = rem
		}
		// 每日免费额度在 UTC 零点(北京 08:00)重置 —— 恢复时间始终重算为"下一个零点",
		// 不保留已过期的旧值(否则过了 08:00 还一直显示今天 08:00,不会变明天)。
		resetAfter := leonardoResetAfter("")
		patch := map[string]any{"meta": meta, "cached_quota_reset_after": resetAfter}
		// 余额恢复(>0)→ 把之前因 402 沉下去的 quota 翻回 active。
		if item.Status == "quota" && hasRem && rem > 0 {
			patch["status"] = "active"
		}
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(resetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "leonardo" && s.leonardo != nil {
		s.applyProxy(ctx)
		data, err := s.leonardo.FetchCreditsBalance(ctx, item.Value)
		if err != nil {
			if errors.Is(err, leonardo.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled",
					"dead":   true,
					"fails":  gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		patch := map[string]any{}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		if remaining, ok := data["remaining"].(int); ok {
			meta["cached_quota_remaining"] = remaining
			// Below the per-generation floor → sink to "限额" so it stops being
			// scheduled. The daily renewal time (below) lets the sweep auto-recover.
			if remaining < leonardoMinCredits && item.Status == "active" {
				patch["status"] = "quota"
			}
		}
		if uid := strings.TrimSpace(stringValue(data["user_id"])); uid != "" {
			meta["user_id"] = uid
		}
		patch["meta"] = meta
		// Daily reset (08:00 Beijing == next UTC midnight) unless upstream gives an
		// explicit renewal time. Drives RecoverQuota.
		resetAfter := leonardoResetAfter(stringValue(data["available_until"]))
		patch["cached_quota_reset_after"] = resetAfter
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(resetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "runway" && s.runway != nil {
		data, err := s.runway.FetchCreditsBalance(ctx, item.Value)
		if err != nil {
			if errors.Is(err, runway.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled",
					"dead":   true,
					"fails":  gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		patch := map[string]any{}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		if remaining, ok := data["remaining"].(int); ok {
			// Refresh only updates the displayed balance number — it never flips
			// status. Out-of-credits is judged at generation time (dead/401), so a
			// refresh can't sink a runway account into a revivable "quota" state.
			meta["cached_quota_remaining"] = remaining
		}
		if used, ok := data["used"].(int); ok {
			meta["cached_quota_used"] = used
		}
		if total, ok := data["total"].(int); ok {
			meta["cached_quota_total"] = total
		}
		patch["meta"] = meta
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		// Recovery time stays the JWT expiry (cached at import) — Runway credits
		// reset monthly, so the credits endpoint carries no reset timestamp.
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(item.CachedQuotaResetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	if poolToType(item.Pool) == "grok" && s.grok != nil {
		data, err := s.grok.FetchCreditsBalance(ctx, item.Value)
		if err != nil {
			if errors.Is(err, grok.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled",
					"dead":   true,
					"fails":  gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		patch := map[string]any{}
		meta := cloneJSONMap(item.Meta)
		meta["cached_quota_at"] = int(time.Now().Unix())
		if remaining, ok := data["remaining"].(int); ok {
			// Refresh only updates the displayed credit number; never flips status.
			// Out-of-credits is judged at generation time (dead/401, no renewal).
			meta["cached_quota_remaining"] = remaining
		}
		if used, ok := data["used"].(int); ok {
			meta["cached_quota_used"] = used
		}
		if total, ok := data["total"].(int); ok {
			meta["cached_quota_total"] = total
		}
		patch["meta"] = meta
		// Recovery time is the death deadline: the maintenance sweep expires a grok
		// account once this marker passes (grok sso can't be renewed). Only stamp it
		// when still unset (import couldn't resolve it) — never move it forward on a
		// later refresh, so an admin opening 账号管理 can't push the death time out.
		if reset := strings.TrimSpace(stringValue(data["reset_after"])); reset != "" && strings.TrimSpace(item.CachedQuotaResetAfter) == "" {
			patch["cached_quota_reset_after"] = reset
			item.CachedQuotaResetAfter = reset
		}
		if updated, updateErr := s.tokens.Update(ctx, item.Pool, item.ID, patch); updateErr == nil {
			item = updated
		}
		return map[string]any{
			"supported":       true,
			"remaining":       data["remaining"],
			"used":            data["used"],
			"total":           data["total"],
			"reset_after":     emptyToNil(item.CachedQuotaResetAfter),
			"quota_cached_at": meta["cached_quota_at"],
			"unchanged":       false,
			"unknown":         boolValueWithDefault(data["unknown"], false),
			"error":           data["error"],
		}, nil
	}
	remaining, hasRemaining := jsonMapInt(item.Meta, "cached_quota_remaining")
	quotaAt, _ := jsonMapInt(item.Meta, "cached_quota_at")
	typeLabel := poolToType(item.Pool)
	return map[string]any{
		"supported":       typeLabel == "openai" || typeLabel == "adobe" || typeLabel == "runway" || typeLabel == "grok",
		"remaining":       valueOrNil((typeLabel == "openai" || typeLabel == "runway" || typeLabel == "grok") && hasRemaining, remaining),
		"total":           nil,
		"reset_after":     emptyToNil(item.CachedQuotaResetAfter),
		"quota_cached_at": valueOrNil(quotaAt != 0, quotaAt),
		"unchanged":       true,
		"unknown":         false,
		"error":           nil,
	}, nil
}

func (s *TokenService) Email(ctx context.Context, pool, id string) (map[string]any, error) {
	item, err := s.tokens.Get(ctx, normalizePool(pool), id)
	if err != nil {
		return nil, err
	}
	if poolToType(item.Pool) == "openai" {
		email := strings.TrimSpace(item.AccountEmail)
		if email != "" {
			return map[string]any{"email": email, "cached": true}, nil
		}
		info := chatgpt.ExtractAccountInfo(item.Value)
		if extracted := strings.TrimSpace(stringValue(info["email"])); extracted != "" {
			_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{"account_email": extracted})
			return map[string]any{"email": extracted, "cached": false}, nil
		}
		return map[string]any{"email": nil, "cached": false}, nil
	}
	if poolToType(item.Pool) == "runway" {
		email := strings.TrimSpace(item.AccountEmail)
		if email != "" {
			return map[string]any{"email": email, "cached": true}, nil
		}
		// Runway email is a top-level JWT claim — decode it (no network).
		if extracted, _ := parseJWTEmailExpiry(item.Value); extracted != "" {
			_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{"account_email": extracted})
			return map[string]any{"email": extracted, "cached": false}, nil
		}
		return map[string]any{"email": nil, "cached": false}, nil
	}
	if poolToType(item.Pool) != "adobe" {
		return map[string]any{"email": nil}, nil
	}
	email := strings.TrimSpace(item.AccountEmail)
	if email == "" {
		if s.adobe == nil {
			return map[string]any{"email": nil, "cached": false}, nil
		}
		profile, err := s.adobe.FetchAccountProfile(ctx, item.Value)
		if err != nil {
			if errors.Is(err, adobe.ErrAuth) {
				_, _ = s.tokens.Update(ctx, item.Pool, item.ID, map[string]any{
					"status": "disabled",
					"dead":   true,
					"fails":  gorm.Expr("fails + 1"),
				})
			}
			return nil, err
		}
		patch := map[string]any{}
		if profileEmail := strings.TrimSpace(stringValue(profile["email"])); profileEmail != "" {
			patch["account_email"] = profileEmail
			email = profileEmail
		}
		if displayName := strings.TrimSpace(stringValue(profile["display_name"])); displayName != "" {
			patch["account_display_name"] = displayName
		}
		if len(patch) > 0 {
			_, _ = s.tokens.Update(ctx, item.Pool, item.ID, patch)
		}
		return map[string]any{"email": emptyToNil(email), "cached": false}, nil
	}
	return map[string]any{"email": email, "cached": true}, nil
}

func (s *TokenService) createToken(ctx context.Context, pool, tokenID, value, status string, meta datatypes.JSONMap) (*model.TokenAccount, error) {
	now := time.Now()
	item := &model.TokenAccount{
		ID:        tokenID,
		Pool:      pool,
		Value:     value,
		Status:    status,
		AddedAt:   &now,
		Meta:      meta,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.tokens.Create(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func accountRow(item model.TokenAccount, inFlight int64) map[string]any {
	remaining, hasRemaining := jsonMapInt(item.Meta, "cached_quota_remaining")
	quotaAt, _ := jsonMapInt(item.Meta, "cached_quota_at")
	pending, _ := jsonMapBool(item.Meta, "pending_check")
	// OpenAI email lives in the token's JWT (nested profile claim). Decode it at
	// render time like the Python reference (_account_row) so accounts imported
	// before the email was persisted still show a name; fall back to the cached
	// field for adobe (whose email comes from a network profile fetch).
	email := item.AccountEmail
	if poolToType(item.Pool) == "openai" {
		if decoded := strings.TrimSpace(stringValue(chatgpt.ExtractAccountInfo(item.Value)["email"])); decoded != "" {
			email = decoded
		}
	}
	typeLabel := poolToType(item.Pool)
	teamID := ""
	if item.Meta != nil {
		teamID = strings.TrimSpace(stringValue(item.Meta["team_id"]))
	}
	hasQuota := typeLabel == "openai" || typeLabel == "adobe" || typeLabel == "runway" || typeLabel == "leonardo" || typeLabel == "krea" || typeLabel == "imagine" || typeLabel == "grok"
	return map[string]any{
		"id":                item.ID,
		"pool":              item.Pool,
		"type":              typeLabel,
		"email":             emptyToNil(email),
		"team_id":           emptyToNil(teamID),
		"remaining":         valueOrNil(hasQuota && hasRemaining, remaining),
		"reset_after":       emptyToNil(item.CachedQuotaResetAfter),
		"quota_cached_at":   valueOrNil(quotaAt != 0, quotaAt),
		"created_at":        unixOrNil(item.AddedAt),
		"last_used_at":      unixOrNil(item.LastUsedAt),
		"expires_at":        jwtExpiryUnix(item.Value),
		"in_flight":         inFlight,
		"success_total":     item.SuccessTotal,
		"fail_total":        item.FailTotal,
		"fails_streak":      item.Fails,
		"status":            item.Status,
		"dead":              item.Dead,
		"image_limited":     item.ImageLimited,
		"video_limited":     item.VideoLimited,
		"pending":           pending,
		"quota_supported":   hasQuota,
		"needs_reset_fetch": typeLabel == "adobe" && item.Status == "active" && strings.TrimSpace(item.CachedQuotaResetAfter) == "",
		"weight":            item.Weight,
		"concurrency":       item.Concurrency,
		"base_url":          emptyToNil(strings.TrimSpace(stringValue(item.Meta["base_url"]))),
		"models":            strings.TrimSpace(stringValue(item.Meta["models"])),
	}
}

// jwtExpiryUnix returns the access token's exp claim (epoch seconds) for the
// accounts UI, or nil when the token is absent/opaque (e.g. a pending row whose
// value hasn't been minted yet).
func jwtExpiryUnix(token string) any {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	_, exp := parseJWTEmailExpiry(token)
	if exp == nil {
		return nil
	}
	return exp.Unix()
}

// cleanAdobeCookie mirrors the Python admin import preprocessing
// (api/admin.py import_adobe_cookie): tolerate JSON array/object pastes,
// unwrap a one-level {"cookie": "..."} wrapper, strip a leading "Cookie:"
// prefix and collapse stray whitespace/newlines.
func cleanAdobeCookie(cookie string) string {
	cookieStr := strings.TrimSpace(cookie)

	// ① A JSON array/object paste -> turn into a cookie string up front.
	if strings.HasPrefix(cookieStr, "[") {
		if converted := cookieStringFromInput(cookieStr); converted != "" {
			cookieStr = converted
		}
	}

	// ② Tolerate the whole JSON object `{"cookie": "..."}` pasted into the
	// textarea. Unwrap one level of JSON before validating.
	if strings.HasPrefix(cookieStr, "{") && strings.HasSuffix(cookieStr, "}") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(cookieStr), &parsed); err == nil {
			inner, ok := parsed["cookie"]
			if !ok {
				inner = parsed["value"]
			}
			switch v := inner.(type) {
			case string:
				cookieStr = strings.TrimSpace(v)
			case []any, map[string]any:
				if converted := cookieStringFromInputValue(v); converted != "" {
					cookieStr = converted
				}
			}
		}
	}

	// ③ Strip a leading "Cookie: " prefix (case-insensitive).
	if len(cookieStr) >= 7 && strings.EqualFold(cookieStr[:7], "cookie:") {
		cookieStr = strings.TrimSpace(cookieStr[7:])
	}

	// ④ Collapse stray newlines and excess whitespace.
	cookieStr = strings.Join(strings.Fields(cookieStr), " ")
	return cookieStr
}

// cookieStringFromInput parses a JSON string (array or object) describing
// browser cookies into a "name=value; name=value" cookie string, mirroring
// providers/adobe/_auth.py _cookie_string_from_input.
func cookieStringFromInput(raw string) string {
	raw = strings.TrimSpace(raw)
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return cookieStringFromInputValue(parsed)
}

func cookieStringFromInputValue(raw any) string {
	switch v := raw.(type) {
	case string:
		text := strings.TrimSpace(v)
		if len(text) >= 7 && strings.EqualFold(text[:7], "cookie:") {
			text = strings.TrimSpace(text[7:])
		}
		return text
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(stringValue(m["name"]))
			value := stringValue(m["value"])
			if name != "" {
				parts = append(parts, name+"="+value)
			}
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		if cookies, ok := v["cookies"].([]any); ok {
			return cookieStringFromInputValue(cookies)
		}
		if inner, ok := v["cookie"]; ok {
			switch inner.(type) {
			case string, []any:
				return cookieStringFromInputValue(inner)
			}
		}
		return ""
	default:
		return ""
	}
}

func previewSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 16 {
		return "***"
	}
	return v[:6] + "…" + v[len(v)-4:]
}

func normalizePool(pool string) string {
	pool = strings.ToLower(strings.TrimSpace(pool))
	if _, ok := validTokenPools[pool]; ok {
		return pool
	}
	return ""
}

func normalizeTokenStatus(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "active", "disabled", "quota", "pending":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func newTokenID(pool string) string {
	prefix := "TK"
	if pool == "adobe" {
		prefix = "AD"
	}
	if pool == "chatgpt" {
		prefix = "OA"
	}
	if pool == "runway" {
		prefix = "RW"
	}
	if pool == "leonardo" {
		prefix = "LN"
	}
	if pool == "krea" {
		prefix = "KR"
	}
	if pool == "imagine" {
		prefix = "IM"
	}
	return prefix + randomUpper(10)
}

func poolToType(pool string) string {
	if mapped, ok := validTokenPools[pool]; ok {
		return mapped
	}
	return pool
}

func parseJWTEmailExpiry(token string) (string, *time.Time) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return "", nil
	}
	payload := parts[1]
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", nil
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", nil
	}
	email := strings.TrimSpace(stringValue(claims["email"]))
	switch v := claims["exp"].(type) {
	case float64:
		t := time.Unix(int64(v), 0)
		return email, &t
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			t := time.Unix(n, 0)
			return email, &t
		}
	}
	return email, nil
}

func jsonMapInt(m datatypes.JSONMap, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	return intValue(v), true
}

func jsonMapBool(m datatypes.JSONMap, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	default:
		return boolValueWithDefault(x, false), true
	}
}

func cloneJSONMap(in datatypes.JSONMap) datatypes.JSONMap {
	out := datatypes.JSONMap{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func unixOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func emptyToNil(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}

func valueOrNil(ok bool, v any) any {
	if !ok {
		return nil
	}
	return v
}

type ginToken struct {
	ID           string     `json:"id"`
	ValuePreview string     `json:"value_preview"`
	Status       string     `json:"status"`
	Fails        int        `json:"fails"`
	AddedAt      *time.Time `json:"added_at"`
}

var _ = fmt.Sprint
