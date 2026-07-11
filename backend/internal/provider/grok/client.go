// Package grok implements the Grok (grok.com / xAI) provider client. Auth is the
// website "sso" session cookie (a JWT whose only claim is a session_id — no exp,
// no refresh: when the session dies upstream the account is simply dead, never
// renewed). grok.com gates requests with an x-statsig-id header; its value
// is a 70-byte anti-bot record — header[49] | counter_le32 |
// sha256("METHOD!path!counter"+suffix)[:16] | trailer — XOR-masked with one
// random byte and base64-encoded; we reproduce it exactly in statsigID (the
// build-specific header/suffix/trailer are env-overridable when grok rotates
// them). Uses tls-client so the JA3/JA4 fingerprint matches Chrome.
package grok

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
)

const (
	apiBase   = "https://grok.com"
	origin    = "https://grok.com"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	// fullCredits is the weekly grant — UI shows "100 满额".
	fullCredits = 100
)

var (
	ErrAuth              = errors.New("grok auth failed")
	ErrQuotaExhausted    = errors.New("grok quota exhausted")
	ErrTemporaryUpstream = errors.New("grok upstream temporary error")
)

type Client struct {
	proxy string
}

func NewClient(proxy string) *Client {
	return &Client{proxy: strings.TrimSpace(proxy)}
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

// IsGrokToken reports whether a JWT looks like a Grok website "sso" cookie: a
// payload whose ONLY claim is "session_id". That disambiguates it from a runway
// token (id + sso claims) or a chatgpt token (openai.com claims).
func IsGrokToken(token string) bool {
	claims := decodeJWTPayload(token)
	if len(claims) == 0 {
		return false
	}
	if _, ok := claims["session_id"]; !ok {
		return false
	}
	// Reject tokens that ALSO carry other-provider markers.
	for k := range claims {
		if k == "session_id" {
			continue
		}
		if k == "sso" || k == "id" || strings.HasPrefix(k, "https://api.openai.com/") {
			return false
		}
	}
	return true
}

// SessionIDFromToken returns the sso session id (for dedup / display).
func SessionIDFromToken(token string) string {
	return strings.TrimSpace(stringValue(decodeJWTPayload(token)["session_id"]))
}

// ExtractAccountInfo returns the free (no-network) account view. grok sso has no
// email/exp claim, so identity falls back to the session id.
func ExtractAccountInfo(token string) map[string]any {
	sid := SessionIDFromToken(token)
	return map[string]any{
		"email":      emptyStringNil(sid),
		"session_id": emptyStringNil(sid),
		"expires_at": nil,
	}
}

// FetchCreditsBalance reads the account's live credit balance via the billing
// gRPC-web endpoint GetGrokCreditsConfig (empty request). The response carries
// the remaining credits (field 1, a float32) and the weekly reset timestamp
// (field 5). A 401/403 maps to ErrAuth (the session is dead). Returns the
// normalized map the TokenService quota plumbing expects.
func (c *Client) FetchCreditsBalance(ctx context.Context, token string) (map[string]any, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return unknownBalance("empty token"), nil
	}
	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	c.ensureChallenge(ctx, client, token)
	// gRPC-web empty message frame: 1-byte flag + 4-byte length (both zero).
	body := []byte{0, 0, 0, 0, 0}
	req, err := http.NewRequest(http.MethodPost, apiBase+"/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	c.applyHeaders(req, token, map[string]string{
		"content-type": "application/grpc-web+proto",
		"x-grpc-web":   "1",
		"accept":       "application/grpc-web+proto",
	})

	resp, err := client.Do(req)
	if err != nil {
		return unknownBalance("network: " + err.Error()), nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode != 200 {
		return unknownBalance(fmt.Sprintf("http %d: %s", resp.StatusCode, clip(raw, 160))), nil
	}

	// GetGrokCreditsConfig field #1 is the credits USED this period (not remaining):
	// an exhausted account reads 100, a fresh one reads ~0. Remaining = 100 - used.
	// Field #5 carries the credits' own reset timestamp (weekly grant refill) —
	// this is the 恢复时间 we surface, NOT the subscription's billing-period end.
	used, resetUnix, ok := parseCreditsConfig(raw)
	if !ok {
		return unknownBalance("unparsable credits config"), nil
	}
	if used < 0 {
		used = 0
	}
	if used > fullCredits {
		used = fullCredits
	}
	remaining := fullCredits - used

	// 恢复时间: the credits-config weekly reset (when the free grant refills).
	reset := strings.TrimSpace(resetUnix)
	return map[string]any{
		"remaining":   remaining,
		"used":        used,
		"total":       fullCredits,
		"reset_after": emptyStringNil(reset),
		"unknown":     false,
		"error":       nil,
	}, nil
}

// Subscription is the membership view parsed from GET /rest/subscriptions.
type Subscription struct {
	Member           bool   // a subscription with status ACTIVE exists (INACTIVE entries don't count)
	Tier             string // e.g. SUBSCRIPTION_TIER_GROK_PRO ("" for free)
	Status           string // e.g. SUBSCRIPTION_STATUS_ACTIVE
	BillingPeriodEnd string // RFC3339; when the plan renews / credits reset
	FreeTrial        bool   // currently in a free-trial offer
}

// FetchSubscription reads GET /rest/subscriptions and reports the account's
// membership. Member is true only when an entry with SUBSCRIPTION_STATUS_ACTIVE
// exists: a lapsed membership keeps its entry but flips to
// SUBSCRIPTION_STATUS_INACTIVE, and an empty array means never subscribed —
// both read as Member=false (the entry's tier/status are still surfaced).
// A 401/403 maps to ErrAuth; other transport/HTTP errors are returned so callers
// can treat them as best-effort (they already have the credit balance).
func (c *Client) FetchSubscription(ctx context.Context, token string) (*Subscription, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, ErrAuth
	}
	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	c.ensureChallenge(ctx, client, token)
	req, err := http.NewRequest(http.MethodGet, apiBase+"/rest/subscriptions", nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	c.applyHeaders(req, token, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: subscriptions http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	var body struct {
		Subscriptions []struct {
			Tier             string `json:"tier"`
			Status           string `json:"status"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
			ActiveOffer      struct {
				FreeTrial *struct {
					TrialDays int `json:"trialDays"`
				} `json:"freeTrial"`
			} `json:"activeOffer"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%w: subscriptions non-json", ErrTemporaryUpstream)
	}
	out := &Subscription{}
	// Surface the ACTIVE subscription if any (falling back to the first entry
	// for tier/status info), but only an ACTIVE one sets Member.
	for i, s := range body.Subscriptions {
		active := strings.EqualFold(s.Status, "SUBSCRIPTION_STATUS_ACTIVE")
		if i == 0 || active {
			out.Member = active
			out.Tier = strings.TrimSpace(s.Tier)
			out.Status = strings.TrimSpace(s.Status)
			out.BillingPeriodEnd = strings.TrimSpace(s.BillingPeriodEnd)
			out.FreeTrial = s.ActiveOffer.FreeTrial != nil
			if active {
				break
			}
		}
	}
	return out, nil
}

// FetchSession reads the account profile via GET /api/auth/session and returns
// (email, userID). A 401/403 means the sso session is dead → ErrAuth.
func (c *Client) FetchSession(ctx context.Context, token string) (email, userID string, err error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return "", "", ErrAuth
	}
	client, err := c.newDirectTLSClient()
	if err != nil {
		return "", "", err
	}
	c.ensureChallenge(ctx, client, token)
	req, err := http.NewRequest(http.MethodGet, apiBase+"/api/auth/session", nil)
	if err != nil {
		return "", "", err
	}
	req = req.WithContext(ctx)
	c.applyHeaders(req, token, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", "", ErrAuth
	}
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("%w: session http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	var body struct {
		Status  string `json:"status"`
		Session struct {
			Email  string `json:"email"`
			UserID string `json:"userId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "", fmt.Errorf("%w: session non-json", ErrTemporaryUpstream)
	}
	// A dead sso cookie answers 200 {"status":"unauthenticated"} rather than 401.
	if !strings.EqualFold(body.Status, "authenticated") {
		return "", "", ErrAuth
	}
	return strings.TrimSpace(body.Session.Email), strings.TrimSpace(body.Session.UserID), nil
}

// grok's x-statsig-id is a per-session anti-bot token. Its 3-byte "F" tail is a
// browser fingerprint the server recomputes from the homepage seed + curve set,
// and the byte-indexing that derives it ROTATES on every grok web reship — so any
// hand-ported algorithm goes stale within a day (403 anti-bot). The durable path
// therefore runs grok's OWN signer in goja (statsig_engine.go): we fetch the seed
// + curves from the homepage and let grok's code do all the (rotating) indexing.
// statsigID prefers that engine; the hand-ported computeStatsigTail below and the
// static env-overridable defaults are only a last-resort fallback. See the package
// doc and statsig_engine.go for the full picture.
// statsigEpoch is the challenge epoch (2023-05-01 00:00 UTC).
const (
	statsigEpoch          = 1682924400
	defaultStatsigHeader  = "00a1adb5012bd32f844f4426c62680d91c6129361eb9459a759710e179a888a99b21678e1f0b1e8952de6a6b3ca019f74b"
	defaultStatsigSuffix  = "obfiowerehiringd244100f5c28f5c28f5c047ae147ae147b047ae147ae147b0f5c28f5c28f5c00"
	defaultStatsigTrailer = 3
	statsigSaltPrefix     = "obfiowerehiring"
	statsigAnimDuration   = 4096
	statsigTTL            = 5 * time.Minute
)

var (
	statsigHeader  = resolveStatsigHeader()
	statsigSuffix  = envOr("GROK_STATSIG_SUFFIX", defaultStatsigSuffix)
	statsigTrailer = resolveStatsigTrailer()

	statsigMetaRe = regexp.MustCompile(`name="grok[^"]*verification"[^>]*content="([^"]+)"`)

	statsigMu    sync.Mutex
	statsigCache = map[string]statsigChallenge{} // keyed by sso token
)

// statsigChallenge is a resolved, self-consistent (header, salt) pair for one
// grok session, derived from the homepage seed + curves.
type statsigChallenge struct {
	header    []byte
	suffix    string
	trailer   byte
	fetchedAt time.Time

	// Inputs for the durable goja signer (statsig_js.go): the raw <meta> seed
	// content and the curves JSON. Empty when parsing failed (engine then skipped).
	seedB64    string
	curvesJSON string
}

// statsigCurve is one entry of the per-load curve set injected via the Next.js
// RSC stream; the server uses it (with the seed) to recompute F.
type statsigCurve struct {
	Color  []int `json:"color"`
	Deg    int   `json:"deg"`
	Bezier []int `json:"bezier"`
}

func resolveStatsigHeader() []byte {
	h := envOr("GROK_STATSIG_HEADER_HEX", defaultStatsigHeader)
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 49 {
		b, _ = hex.DecodeString(defaultStatsigHeader)
	}
	return b
}

func resolveStatsigTrailer() byte {
	if v := strings.TrimSpace(os.Getenv("GROK_STATSIG_TRAILER")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 255 {
			return byte(n)
		}
	}
	return defaultStatsigTrailer
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// ensureChallenge refreshes the cached (header, salt) for the session if missing
// or stale. Any failure is non-fatal: statsigID then falls back to the static
// defaults. An explicit env override disables dynamic fetching entirely.
func (c *Client) ensureChallenge(ctx context.Context, client tlsclient.HttpClient, token string) {
	if token == "" || client == nil {
		return
	}
	if os.Getenv("GROK_STATSIG_HEADER_HEX") != "" || os.Getenv("GROK_STATSIG_SUFFIX") != "" {
		return
	}
	statsigMu.Lock()
	cur, ok := statsigCache[token]
	fresh := ok && time.Since(cur.fetchedAt) < statsigTTL
	statsigMu.Unlock()
	if fresh {
		return
	}
	ch, err := fetchStatsigChallenge(ctx, client, token)
	if err != nil {
		// Silent fallback to static defaults is the #1 cause of a recurring
		// "403 anti-bot": the homepage structure changed and we never notice.
		// Surface it so the failure mode (fetch/parse broke vs. offsets rotated)
		// is diagnosable from logs instead of guessing.
		log.Printf("grok statsig: self-heal failed, using stale static defaults (403 likely): %v", err)
		return
	}
	log.Printf("grok statsig: self-heal ok header[:6]=%x suffix=%s", ch.header[:6], ch.suffix)
	statsigMu.Lock()
	statsigCache[token] = ch
	statsigMu.Unlock()
}

// fetchStatsigChallenge does a browser-free homepage GET and derives a
// self-consistent (header, salt) pair: header = 0x00 + seed, salt = prefix + F.
func fetchStatsigChallenge(ctx context.Context, client tlsclient.HttpClient, token string) (statsigChallenge, error) {
	req, err := http.NewRequest(http.MethodGet, apiBase+"/", nil)
	if err != nil {
		return statsigChallenge{}, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":            {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		"accept-language":   {"en-US,en;q=0.9"},
		"user-agent":        {userAgent},
		"cookie":            {"sso=" + token + "; sso-rw=" + token},
		http.HeaderOrderKey: {"accept", "accept-language", "user-agent", "cookie"},
	}
	resp, err := client.Do(req)
	if err != nil {
		return statsigChallenge{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return statsigChallenge{}, err
	}
	html := string(raw)

	mm := statsigMetaRe.FindStringSubmatch(html)
	if mm == nil {
		return statsigChallenge{}, errors.New("statsig: seed meta not found")
	}
	curves, err := parseStatsigCurves(html)
	if err != nil {
		return statsigChallenge{}, err
	}
	curvesJSON, err := json.Marshal(curves)
	if err != nil {
		return statsigChallenge{}, err
	}

	// Primary path is the goja signer (statsig_js.go); set it up / refresh it for
	// this build. Static (header, salt) below is only a last-resort fallback.
	ensureEngine(ctx, client, html)

	ch := statsigChallenge{
		header:     statsigHeader,
		suffix:     statsigSuffix,
		trailer:    statsigTrailer,
		fetchedAt:  time.Now(),
		seedB64:    mm[1],
		curvesJSON: string(curvesJSON),
	}
	// Best-effort static derivation (the old hand-ported algorithm) as fallback.
	if seed, err := decodeStatsigSeed(mm[1]); err == nil {
		if tail, err := computeStatsigTail(seed, curves); err == nil {
			ch.header = append([]byte{0x00}, seed...)
			ch.suffix = statsigSaltPrefix + tail
			ch.trailer = defaultStatsigTrailer
		}
	}
	return ch, nil
}

func decodeStatsigSeed(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 48 {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("statsig: seed decode: %w", err)
	}
	if len(b) != 48 {
		return nil, fmt.Errorf("statsig: seed len %d", len(b))
	}
	return b, nil
}

// parseStatsigCurves extracts the [[{color,deg,bezier}...]...] array from the
// RSC-escaped homepage HTML (the one immediately followed by color/bezier keys).
func parseStatsigCurves(html string) ([][]statsigCurve, error) {
	marker := -1
	for from := 0; ; {
		i := strings.Index(html[from:], "curves")
		if i < 0 {
			break
		}
		i += from
		end := i + 160
		if end > len(html) {
			end = len(html)
		}
		w := html[i:end]
		if strings.Contains(w, "color") && strings.Contains(w, "bezier") {
			marker = i
			break
		}
		from = i + 6
	}
	if marker < 0 {
		return nil, errors.New("statsig: curves not found")
	}
	rel := strings.IndexByte(html[marker:], '[')
	if rel < 0 {
		return nil, errors.New("statsig: curves array start not found")
	}
	start := marker + rel
	depth, stop := 0, -1
	for k := start; k < len(html); k++ {
		switch html[k] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				stop = k + 1
			}
		}
		if stop > 0 {
			break
		}
	}
	if stop < 0 {
		return nil, errors.New("statsig: curves array end not found")
	}
	sub := strings.ReplaceAll(html[start:stop], `\`, "")
	var out [][]statsigCurve
	if err := json.Unmarshal([]byte(sub), &out); err != nil {
		return nil, fmt.Errorf("statsig: curves json: %w", err)
	}
	return out, nil
}

// computeStatsigTail reproduces the browser's 3-byte color + 6-number transform
// matrix "F" tail: it selects a curve by the seed, samples the curve's keyframe
// animation (color lerp + rotate) at a seed-derived paused currentTime, and
// serializes getComputedStyle(color)+getComputedStyle(transform) exactly as the
// signer does (each number -> Number(v.toFixed(2)).toString(16), '.'/'-' stripped).
func computeStatsigTail(seed []byte, curves [][]statsigCurve) (string, error) {
	if len(seed) < 46 {
		return "", errors.New("statsig: short seed")
	}
	if len(curves) == 0 {
		return "", errors.New("statsig: no curves")
	}
	group := int(seed[5]) % len(curves)
	if len(curves[group]) == 0 {
		return "", errors.New("statsig: empty curve group")
	}
	idx := int(seed[45]) % len(curves[group])
	cv := curves[group][idx]
	if len(cv.Color) < 6 || len(cv.Bezier) < 4 {
		return "", errors.New("statsig: malformed curve")
	}

	n := (int(seed[1]) % 16) * (int(seed[6]) % 16) * (int(seed[13]) % 16)
	currentTime := jsRound(float64(n)/10) * 10
	progress := float64(currentTime) / statsigAnimDuration

	x1 := toFixed2(float64(cv.Bezier[0]) / 255)
	y1 := toFixed2(float64(cv.Bezier[1])*2/255 - 1)
	x2 := toFixed2(float64(cv.Bezier[2]) / 255)
	y2 := toFixed2(float64(cv.Bezier[3])*2/255 - 1)
	eased := cubicBezierEase(x1, y1, x2, y2, progress)

	nums := make([]float64, 0, 9)
	for k := 0; k < 3; k++ {
		v := jsRound(float64(cv.Color[k]) + (float64(cv.Color[k+3])-float64(cv.Color[k]))*eased)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		nums = append(nums, float64(v))
	}
	theta := int(math.Floor(float64(cv.Deg)*300/255 + 60))
	rad := float64(theta) * eased * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	nums = append(nums, cos, sin, -sin, cos, 0, 0)

	var b strings.Builder
	for _, v := range nums {
		b.WriteString(jsHex(v))
	}
	out := strings.NewReplacer(".", "", "-", "").Replace(b.String())
	return out, nil
}

// jsRound matches JavaScript Math.round (round half up toward +Inf).
func jsRound(x float64) int {
	return int(math.Floor(x + 0.5))
}

// toFixed2 matches JavaScript Number(v.toFixed(2)).
func toFixed2(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 2, 64), 64)
	return f
}

// jsHex matches JavaScript Number(v.toFixed(2)).toString(16).
func jsHex(v float64) string {
	v = toFixed2(v)
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	ip := int64(math.Floor(v))
	frac := v - float64(ip)
	s := neg + strconv.FormatInt(ip, 16)
	if frac == 0 {
		return s
	}
	const digits = "0123456789abcdef"
	var b strings.Builder
	b.WriteString(s)
	b.WriteByte('.')
	for i := 0; i < 20 && frac != 0; i++ {
		frac *= 16
		d := int(frac)
		b.WriteByte(digits[d])
		frac -= float64(d)
	}
	return b.String()
}

// cubicBezierEase evaluates a CSS cubic-bezier(x1,y1,x2,y2) easing at input
// fraction p: solve X(t)=p for t (bisection), then return Y(t).
func cubicBezierEase(x1, y1, x2, y2, p float64) float64 {
	bez := func(t, a, b float64) float64 {
		mt := 1 - t
		return 3*a*mt*mt*t + 3*b*mt*t*t + t*t*t
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if bez(mid, x1, x2) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return bez((lo+hi)/2, y1, y2)
}

// statsigID reproduces grok.com's x-statsig-id anti-bot token for a request. The
// token binds to the request METHOD and URL path and to a coarse timestamp, so
// it must be regenerated per request. See the package doc for the layout. It uses
// the session's self-healed (header, salt) when available, else static defaults.
func statsigID(path, method, token string) string {
	header, suffix, trailer := statsigHeader, statsigSuffix, statsigTrailer
	statsigMu.Lock()
	ch, ok := statsigCache[token]
	statsigMu.Unlock()
	if ok {
		header, suffix, trailer = ch.header, ch.suffix, ch.trailer
		// Primary: run grok's own signer in goja (durable across reships). Falls
		// through to the static computation below on any failure.
		if ch.seedB64 != "" && ch.curvesJSON != "" {
			if id, err := signWithEngine(ch.seedB64, ch.curvesJSON, path, method); err == nil {
				return id
			} else if !errors.Is(err, errEngineNotReady) {
				log.Printf("grok statsig: js signer failed, using static fallback: %v", err)
			}
		}
	}

	counter := uint32(time.Now().Unix() - statsigEpoch)
	sig := fmt.Sprintf("%s!%s!%d%s", method, path, counter, suffix)
	hash := sha256.Sum256([]byte(sig))

	raw := make([]byte, 0, 70)
	raw = append(raw, header...)
	raw = binary.LittleEndian.AppendUint32(raw, counter)
	raw = append(raw, hash[:16]...)
	raw = append(raw, trailer)

	key := byte(rand.IntN(256))
	for i := range raw {
		raw[i] ^= key
	}
	return base64.RawStdEncoding.EncodeToString(raw)
}

// applyHeaders sets the browser-like header set + sso cookie + spoofed statsig id.
// extra overrides/adds per-request headers (e.g. content-type).
func (c *Client) applyHeaders(req *http.Request, token string, extra map[string]string) {
	h := http.Header{
		"accept":             {"*/*"},
		"accept-language":    {"en-US,en;q=0.9"},
		"content-type":       {"application/json"},
		"origin":             {origin},
		"referer":            {origin + "/"},
		"user-agent":         {userAgent},
		"x-statsig-id":       {statsigID(req.URL.Path, req.Method, token)},
		"x-xai-request-id":   {uuid.NewString()},
		"sec-ch-ua":          {`"Chromium";v="133", "Not(A:Brand";v="99"`},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"same-origin"},
		"cookie":             {"sso=" + token + "; sso-rw=" + token},
	}
	for k, v := range extra {
		h[k] = []string{v}
	}
	h[http.HeaderOrderKey] = []string{
		"accept", "accept-language", "content-type", "origin", "referer",
		"user-agent", "x-statsig-id", "x-xai-request-id", "x-grpc-web",
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "cookie",
	}
	req.Header = h
}

func (c *Client) newTLSClient() (tlsclient.HttpClient, error) { return c.newTLSClientP(true) }

// newDirectTLSClient egresses on the local IP (never the proxy). Used for
// reference-frame upload and result (video) download; only the generate submit
// (/rest/app-chat/conversations/new) uses the proxy.
func (c *Client) newDirectTLSClient() (tlsclient.HttpClient, error) { return c.newTLSClientP(false) }

func (c *Client) newTLSClientP(useProxy bool) (tlsclient.HttpClient, error) {
	options := []tlsclient.HttpClientOption{
		// Video generation streams inline until progress=100; a 15s clip can take
		// several minutes, so allow up to 10m (caller's genCtx caps at 12m).
		tlsclient.WithTimeoutSeconds(600),
		tlsclient.WithClientProfile(profiles.Chrome_133),
		tlsclient.WithRandomTLSExtensionOrder(),
	}
	if useProxy && c.proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(c.proxy))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

// --- gRPC-web / protobuf decoding for GetGrokCreditsConfig ---

// parseCreditsConfig extracts (remaining credits, reset RFC3339-ish unix string)
// from the gRPC-web framed protobuf. Layout (reverse-engineered):
//
//	frame: 1-byte flag + 4-byte big-endian length + payload
//	payload: field 1 (message) {
//	  field 1: float32  -> remaining credits
//	  field 5: message  { field 1: varint -> reset unix seconds }
//	}
func parseCreditsConfig(buf []byte) (remaining int, resetUnix string, ok bool) {
	for len(buf) >= 5 {
		flag := buf[0]
		ln := int(buf[1])<<24 | int(buf[2])<<16 | int(buf[3])<<8 | int(buf[4])
		buf = buf[5:]
		if ln > len(buf) {
			break
		}
		payload := buf[:ln]
		buf = buf[ln:]
		if flag&0x80 != 0 { // trailer frame (grpc-status), skip
			continue
		}
		// payload: expect field 1 (wire type 2) wrapping the config message.
		fn, wt, val, rest, good := readField(payload)
		if !good || fn != 1 || wt != 2 {
			continue
		}
		rem, reset, found := scanConfigMessage(val)
		_ = rest
		if found {
			return rem, reset, true
		}
	}
	return 0, "", false
}

func scanConfigMessage(msg []byte) (used int, resetUnix string, ok bool) {
	var usedF float32
	seen := false
	for len(msg) > 0 {
		fn, wt, val, rest, good := readField(msg)
		if !good {
			break
		}
		msg = rest
		seen = true
		switch {
		case fn == 1 && wt == 5: // float32 credits USED this period
			usedF = float32FromLE(val)
		case fn == 5 && wt == 2: // reset timestamp message { #1 varint=seconds }
			if sec, sok := firstVarint(val); sok {
				resetUnix = strconv.FormatInt(sec, 10)
			}
		}
	}
	// A valid config message may OMIT field #1 when used == 0 (proto3 drops zero
	// scalars) — a full-quota account. So as long as the message had any field,
	// treat it as parsed with used defaulting to 0 (= 100 remaining).
	return int(usedF), resetUnix, seen
}

// readField reads one protobuf field: returns (fieldNum, wireType, value, rest, ok).
// For wt 2 value is the length-delimited bytes; wt 5 the 4 LE bytes; wt 0 the
// raw varint bytes; wt 1 the 8 bytes.
func readField(b []byte) (fn int, wt int, val []byte, rest []byte, ok bool) {
	tag, n := readVarint(b)
	if n == 0 {
		return 0, 0, nil, b, false
	}
	b = b[n:]
	fn = int(tag >> 3)
	wt = int(tag & 7)
	switch wt {
	case 0:
		_, m := readVarint(b)
		if m == 0 {
			return 0, 0, nil, b, false
		}
		return fn, wt, b[:m], b[m:], true
	case 1:
		if len(b) < 8 {
			return 0, 0, nil, b, false
		}
		return fn, wt, b[:8], b[8:], true
	case 2:
		ln, m := readVarint(b)
		if m == 0 || int(ln) > len(b)-m {
			return 0, 0, nil, b, false
		}
		return fn, wt, b[m : m+int(ln)], b[m+int(ln):], true
	case 5:
		if len(b) < 4 {
			return 0, 0, nil, b, false
		}
		return fn, wt, b[:4], b[4:], true
	default:
		return 0, 0, nil, b, false
	}
}

func firstVarint(b []byte) (int64, bool) {
	fn, wt, val, _, ok := readField(b)
	if !ok || fn != 1 || wt != 0 {
		return 0, false
	}
	v, _ := readVarint(val)
	return int64(v), true
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	var s uint
	for i := 0; i < len(b); i++ {
		v |= uint64(b[i]&0x7f) << s
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
		s += 7
	}
	return 0, 0
}

func float32FromLE(b []byte) float32 {
	if len(b) < 4 {
		return 0
	}
	bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return math.Float32frombits(bits)
}

// --- small helpers (mirror the other provider clients) ---

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(token, "Bearer ")), ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return strings.TrimSpace(string(b))
	}
}

func emptyStringNil(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}

func unknownBalance(reason string) map[string]any {
	return map[string]any{
		"remaining": nil, "used": nil, "total": nil,
		"unknown": true, "error": reason,
	}
}

func clip(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
