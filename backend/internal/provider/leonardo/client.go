// Package leonardo implements the Leonardo.ai (app.leonardo.ai) provider client.
// Unlike chatgpt/runway (whose JWT IS the stored credential), Leonardo's durable
// credential is the browser COOKIE (better-auth session): the bearer access token
// it mints lives only ~1h. So every call here takes the cookie and derives a
// fresh JWT on the fly via /api/auth/get-session. The browser session itself is a
// sliding, finite-lived session: periodically calling get-session extends it and
// may return replacement Set-Cookie values, which callers must persist. There is
// no separate refresh token/profile. tls-client gives a Chrome JA3/JA4 fingerprint
// so the requests aren't flagged.
package leonardo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"golang.org/x/sync/singleflight"
)

const (
	appBase       = "https://app.leonardo.ai"
	graphqlURL    = "https://api.leonardo.ai/v1/graphql"
	schemaVersion = "1.187.0"
	chromeVersion = "146"
	userAgent     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + chromeVersion + ".0.0.0 Safari/537.36"
	secCHUA       = `"Not;A=Brand";v="8", "Chromium";v="146", "Google Chrome";v="146"`
)

var (
	ErrAuth              = errors.New("leonardo auth failed")
	ErrQuotaExhausted    = errors.New("leonardo quota exhausted")
	ErrTemporaryUpstream = errors.New("leonardo upstream temporary error")
)

// AuthError preserves the safe, actionable part of a get-session failure while
// still matching ErrAuth through errors.Is. It never includes Cookie or bearer
// values, so callers can persist Error() for the admin abnormal-reason column.
type AuthError struct {
	Code   string
	Detail string
}

func (e *AuthError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		return ErrAuth.Error()
	}
	return ErrAuth.Error() + ": " + detail
}

func (e *AuthError) Is(target error) bool {
	return target == ErrAuth
}

type Client struct {
	proxy string
	// sessions caches the short-lived access token per cookie so we don't hit
	// /api/auth/get-session on every call — Leonardo rate-limits that endpoint
	// (429) hard, so re-using the ~1h JWT is essential.
	mu             sync.Mutex
	sessions       map[string]*Session
	refreshFlights singleflight.Group
	// fetchSessionHook is only used by package tests to exercise refresh
	// coalescing without reaching Leonardo.
	fetchSessionHook func(context.Context, string, bool) (*Session, []*http.Cookie, error)
}

func NewClient(proxy string) *Client {
	return &Client{proxy: strings.TrimSpace(proxy), sessions: map[string]*Session{}}
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

// IsLeonardoCookie reports whether a pasted credential is a Leonardo cookie: it
// carries the better-auth session cookie name. This is what disambiguates it from
// an Adobe cookie at import time.
func IsLeonardoCookie(value string) bool {
	for _, part := range strings.Split(value, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && isSessionTokenCookie(name) {
			return true
		}
	}
	return false
}

// Session is the result of /api/auth/get-session: the short-lived bearer plus the
// ids the GraphQL API needs (cognitoSub for the quota query, userId for the feed
// and the CDN image path) and the human-facing account fields.
type Session struct {
	AccessToken string
	CognitoSub  string
	UserID      string
	Email       string
	Name        string
	ExpiresAt   int64
}

// GetSession exchanges the cookie for a fresh access token + account ids. A 401/403
// (or a response with no access token) means the cookie/session is dead → ErrAuth.
func (c *Client) GetSession(ctx context.Context, cookie string) (*Session, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return nil, ErrAuth
	}
	// Re-use a cached, still-valid access token (keep a 60s safety margin) instead
	// of hitting the heavily rate-limited get-session endpoint again.
	c.mu.Lock()
	cs, cached := c.sessions[cookie]
	if cached && cs.ExpiresAt-60 > time.Now().Unix() {
		c.mu.Unlock()
		return cs, nil
	}
	c.mu.Unlock()

	// A newly imported browser cookie normally carries Better Auth's current
	// session_data cache. Read it exactly like the website on the first call; doing
	// an immediate forced refresh after the browser has just polled get-session can
	// trip Leonardo's Cloudflare 429. Once our cached bearer expires, explicitly
	// bypass Better Auth's cookie cache and mint a new one.
	var sess *Session
	var err error
	if cached {
		sess, _, err = c.fetchFreshSession(ctx, cookie)
	} else {
		sess, _, err = c.fetchSession(ctx, cookie, false)
	}
	if err != nil {
		return nil, err
	}
	if sess.ExpiresAt <= time.Now().Unix()+60 && !cached {
		// A copied session_data cookie has no browser Max-Age metadata. If it contains
		// an already-expired bearer (common after a process restart), retry once via
		// the documented cache-bypass query instead of replaying it forever.
		sess, _, err = c.fetchFreshSession(ctx, cookie)
		if err != nil {
			return nil, err
		}
	}
	c.cacheSession(cookie, sess)
	return sess, nil
}

// RefreshCookie force-calls get-session even when the ~1h bearer is cached. That
// request is the Better Auth keepalive: once its update-age is reached, Leonardo
// extends the durable server-side session and can reissue session_token and/or
// session_data cookies. changed only describes whether the raw Cookie header
// changed; a successful unchanged result still renewed/validated the server-side
// session and must count as a successful keepalive.
func (c *Client) RefreshCookie(ctx context.Context, cookie string) (fresh string, changed bool, err error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return "", false, ErrAuth
	}
	sess, setCookies, err := c.fetchFreshSession(ctx, cookie)
	if err != nil {
		return "", false, err
	}
	fresh, changed = mergeAuthCookies(cookie, setCookies)
	if fresh == "" {
		return "", false, ErrAuth
	}
	c.cacheSession(fresh, sess)
	return fresh, changed, nil
}

type freshSessionResult struct {
	session    *Session
	setCookies []*http.Cookie
}

// fetchFreshSession coalesces every forced get-session request by the durable
// session_token. Scheduled keepalive, bearer-expiry renewal and GraphQL auth
// retry can therefore overlap without multiplying Leonardo's rate-limited auth
// calls. Each waiter keeps its own cancellation semantics, while the one shared
// upstream call is allowed to finish for the other waiters.
func (c *Client) fetchFreshSession(ctx context.Context, cookie string) (*Session, []*http.Cookie, error) {
	key, ok := sessionAccountKey(cookie)
	if !ok {
		return nil, nil, ErrAuth
	}
	resultCh := c.refreshFlights.DoChan(key, func() (any, error) {
		sharedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 65*time.Second)
		defer cancel()
		sess, setCookies, err := c.fetchSession(sharedCtx, cookie, true)
		if err != nil {
			return nil, err
		}
		return freshSessionResult{session: sess, setCookies: setCookies}, nil
	})

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, nil, result.Err
		}
		fresh, ok := result.Val.(freshSessionResult)
		if !ok || fresh.session == nil {
			return nil, nil, ErrTemporaryUpstream
		}
		return fresh.session, fresh.setCookies, nil
	}
}

func (c *Client) cacheSession(cookie string, sess *Session) {
	if sess == nil || sess.ExpiresAt <= time.Now().Unix() {
		return
	}
	c.mu.Lock()
	for key, cached := range c.sessions {
		if cached == nil || cached.ExpiresAt <= time.Now().Unix() {
			delete(c.sessions, key)
		}
	}
	c.sessions[cookie] = sess
	c.mu.Unlock()
}

// fetchSession performs the browser-cookie exchange. forceFresh uses Better
// Auth's supported disableCookieCache query while retaining its complete auth
// cookie set; deleting session_data client-side changed the request shape and
// caused Leonardo's Cloudflare edge to return an HTML 429 after Release-v1.3.
func (c *Client) fetchSession(ctx context.Context, cookie string, forceFresh bool) (*Session, []*http.Cookie, error) {
	if c.fetchSessionHook != nil {
		return c.fetchSessionHook(ctx, cookie, forceFresh)
	}
	// Honor the configured provider proxy for the auth exchange. Leonardo applies
	// strict per-IP limits to get-session; the other direct calls are uploads,
	// polling and downloads rather than this rate-limited auth endpoint.
	client, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	endpoint := sessionEndpoint(forceFresh)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":          {"*/*"},
		"accept-encoding": {"gzip, deflate, br, zstd"},
		"accept-language": {"en-US,en;q=0.9"},
		// Cloudflare's bot-management cookies are bound to the real browser's
		// TLS/HTTP fingerprint. Replaying a Chrome 150 __cf_bm value from this
		// server while presenting a different client profile makes an otherwise
		// valid Better Auth session look forged. get-session only needs these
		// durable/cache cookies, so keep the credential and drop browser-bound
		// analytics/edge state from this backend request.
		"cookie":             {authSessionCookies(cookie)},
		"dnt":                {"1"},
		"priority":           {"u=1, i"},
		"referer":            {appBase + "/"},
		"sec-ch-ua":          {secCHUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"macOS"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"same-origin"},
		"user-agent":         {userAgent},
		http.HeaderOrderKey: {
			"accept", "accept-encoding", "accept-language", "cookie", "dnt", "priority", "referer",
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "user-agent",
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, nil, &AuthError{
			Code:   fmt.Sprintf("get_session_http_%d", resp.StatusCode),
			Detail: fmt.Sprintf("get-session 返回 HTTP %d，session cookie 已失效或被拒绝", resp.StatusCode),
		}
	}
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("%w: get-session http %d: %s", ErrTemporaryUpstream, resp.StatusCode, clip(body, 160))
	}
	var raw struct {
		Session struct {
			AccessToken  string `json:"accessToken"`
			CognitoSub   string `json:"cognitoSub"`
			UserID       string `json:"userId"`
			HasuraUserID string `json:"hasuraUserId"`
			TokenExpiry  int64  `json:"accessTokenExpiry"`
		} `json:"session"`
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("%w: get-session non-json", ErrTemporaryUpstream)
	}
	if strings.TrimSpace(raw.Session.AccessToken) == "" {
		// No bearer despite 200 → the cookie no longer authenticates.
		return nil, nil, &AuthError{
			Code:   "get_session_missing_access_token",
			Detail: "get-session 返回 HTTP 200，但响应中缺少 accessToken",
		}
	}
	uid := raw.Session.UserID
	if uid == "" {
		uid = raw.Session.HasuraUserID
	}
	if uid == "" {
		uid = raw.User.ID
	}
	sess := &Session{
		AccessToken: raw.Session.AccessToken,
		CognitoSub:  raw.Session.CognitoSub,
		UserID:      uid,
		Email:       strings.TrimSpace(raw.User.Email),
		Name:        strings.TrimSpace(raw.User.Name),
		ExpiresAt:   accessTokenExpiry(raw.Session.AccessToken, raw.Session.TokenExpiry),
	}
	return sess, resp.Cookies(), nil
}

func sessionEndpoint(forceFresh bool) string {
	endpoint := appBase + "/api/auth/get-session"
	if forceFresh {
		return endpoint + "?disableCookieCache=true"
	}
	return endpoint
}

// accessTokenExpiry prefers the JWT's standard exp claim and only falls back to
// Leonardo's accessTokenExpiry field. The website currently serializes that
// field as JavaScript epoch milliseconds; treating it as Unix seconds makes a
// one-hour bearer appear valid for thousands of years, so the next GraphQL 401
// would be mistaken for a dead browser cookie.
func accessTokenExpiry(token string, raw int64) int64 {
	parts := strings.Split(token, ".")
	if len(parts) >= 2 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims map[string]any
			if json.Unmarshal(payload, &claims) == nil {
				if exp := int64(intValue(claims["exp"])); exp > 0 {
					return exp
				}
			}
		}
	}
	// Normalize millisecond/microsecond/nanosecond epochs to seconds. Unix
	// seconds remain below 1e11 until the year 5138.
	for raw >= 100_000_000_000 {
		raw /= 1000
	}
	return raw
}

func isSessionTokenCookie(name string) bool {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "__Secure-")
	name = strings.TrimPrefix(name, "__Host-")
	return name == "better-auth.session_token"
}

func isSessionDataCookie(name string) bool {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "__Secure-")
	name = strings.TrimPrefix(name, "__Host-")
	return name == "better-auth.session_data" || strings.HasPrefix(name, "better-auth.session_data.")
}

// sessionAccountKey intentionally hashes only the durable account credential.
// Browser-bound Cloudflare/analytics cookies and short-lived session_data chunks
// may differ between otherwise identical copies of the same Leonardo account.
func sessionAccountKey(cookie string) (string, bool) {
	for _, pair := range parseCookiePairs(cookie) {
		if isSessionTokenCookie(pair.name) && pair.value != "" {
			sum := sha256.Sum256([]byte(pair.value))
			return hex.EncodeToString(sum[:]), true
		}
	}
	return "", false
}

func authSessionCookies(cookie string) string {
	pairs := parseCookiePairs(cookie)
	kept := make([]cookiePair, 0, 3)
	for _, pair := range pairs {
		if isSessionTokenCookie(pair.name) || isSessionDataCookie(pair.name) {
			kept = append(kept, pair)
		}
	}
	return formatCookiePairs(kept)
}

type cookiePair struct {
	name  string
	value string
}

// mergeAuthCookies applies replacement Better Auth cookies to the stored browser
// Cookie header. session_data is retained for browser parity; forced renewal uses
// disableCookieCache=true rather than deleting these fingerprint-relevant chunks.
func mergeAuthCookies(cookie string, setCookies []*http.Cookie) (string, bool) {
	pairs := parseCookiePairs(cookie)
	updates := make([]cookiePair, 0, len(setCookies))
	tokenTouched := false
	dataTouched := false
	for _, item := range setCookies {
		if item == nil {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if isSessionTokenCookie(name) {
			tokenTouched = true
		} else if isSessionDataCookie(name) {
			dataTouched = true
		} else {
			// Tracking/edge cookies are copied from the browser and are not part of
			// Better Auth's credential rotation, so don't overwrite them here.
			continue
		}
		deleted := item.MaxAge < 0 || (!item.Expires.IsZero() && item.Expires.Before(time.Now()))
		if !deleted && item.Value != "" {
			updates = append(updates, cookiePair{name: name, value: item.Value})
		}
	}
	kept := make([]cookiePair, 0, len(pairs)+len(updates))
	for _, pair := range pairs {
		if tokenTouched && isSessionTokenCookie(pair.name) {
			continue
		}
		if dataTouched && isSessionDataCookie(pair.name) {
			continue
		}
		kept = append(kept, pair)
	}
	kept = append(kept, updates...)
	fresh := formatCookiePairs(kept)
	return fresh, fresh != formatCookiePairs(pairs)
}

func parseCookiePairs(cookie string) []cookiePair {
	var pairs []cookiePair
	for _, part := range strings.Split(cookie, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		pairs = append(pairs, cookiePair{name: name, value: strings.TrimSpace(value)})
	}
	return pairs
}

func formatCookiePairs(pairs []cookiePair) string {
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair.name+"="+pair.value)
	}
	return strings.Join(parts, "; ")
}

const qGetTokens = `query GetUserTokensFromSub($sub: String) {
  user_details(where: {cognitoId: {_eq: $sub}}) {
    id
    plan
    subscriptionTokens
    paidTokens
    rolloverTokens
    tokenRenewalDate
    __typename
  }
}`

// FetchCreditsBalance derives a JWT from the cookie then reads the account's image
// token balance. Returns a normalized map mirroring the other providers so the
// TokenService quota plumbing is uniform. remaining = subscription+paid+rollover
// (the spendable image tokens); available_until carries the daily renewal time so
// the maintenance sweep can auto-recover a 限额 account.
func (c *Client) FetchCreditsBalance(ctx context.Context, cookie string) (map[string]any, error) {
	sess, err := c.GetSession(ctx, cookie)
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return nil, ErrAuth
		}
		return unknownBalance(err.Error()), nil
	}
	if sess.CognitoSub == "" {
		return unknownBalance("no cognitoSub"), nil
	}

	payload, _ := json.Marshal(map[string]any{
		"operationName": "GetUserTokensFromSub",
		"variables":     map[string]any{"sub": sess.CognitoSub},
		"query":         qGetTokens,
	})
	body, status, err := c.graphqlP(ctx, sess.AccessToken, payload, false)
	if err != nil {
		return unknownBalance("network: " + err.Error()), nil
	}
	if status == 401 || status == 403 {
		return nil, ErrAuth
	}
	if status != 200 {
		return unknownBalance(fmt.Sprintf("http %d: %s", status, clip(body, 160))), nil
	}
	var result struct {
		Data struct {
			UserDetails []struct {
				Plan               string `json:"plan"`
				SubscriptionTokens int    `json:"subscriptionTokens"`
				PaidTokens         int    `json:"paidTokens"`
				RolloverTokens     int    `json:"rolloverTokens"`
				TokenRenewalDate   string `json:"tokenRenewalDate"`
			} `json:"user_details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return unknownBalance("non-json"), nil
	}
	if len(result.Data.UserDetails) == 0 {
		return unknownBalance("no user_details"), nil
	}
	ud := result.Data.UserDetails[0]
	remaining := ud.SubscriptionTokens + ud.PaidTokens + ud.RolloverTokens
	return map[string]any{
		"remaining":       remaining,
		"used":            nil,
		"total":           nil,
		"unknown":         false,
		"error":           nil,
		"plan":            ud.Plan,
		"available_until": strings.TrimSpace(ud.TokenRenewalDate),
		"email":           emptyStringNil(sess.Email),
		"display_name":    emptyStringNil(sess.Name),
		"user_id":         emptyStringNil(sess.UserID),
	}, nil
}

// graphql runs a GraphQL call through the proxy. graphqlP lets callers pick the
// egress: only the generate submit uses the proxy; reference-image upload and
// polling run direct (local IP).
func (c *Client) graphql(ctx context.Context, accessToken string, payload []byte) ([]byte, int, error) {
	return c.graphqlP(ctx, accessToken, payload, true)
}

func (c *Client) graphqlP(ctx context.Context, accessToken string, payload []byte, useProxy bool) ([]byte, int, error) {
	client, err := c.newTLSClientP(useProxy)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, graphqlURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"content-type":         {"application/json"},
		"accept":               {"*/*"},
		"accept-language":      {"en-US,en;q=0.9"},
		"origin":               {appBase},
		"referer":              {appBase + "/"},
		"user-agent":           {userAgent},
		"authorization":        {"Bearer " + accessToken},
		"x-leo-schema-version": {schemaVersion},
		"sec-fetch-dest":       {"empty"},
		"sec-fetch-mode":       {"cors"},
		"sec-fetch-site":       {"same-site"},
		http.HeaderOrderKey: {
			"content-type", "accept", "accept-language", "origin", "referer",
			"user-agent", "authorization", "x-leo-schema-version",
			"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site",
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func unknownBalance(reason string) map[string]any {
	return map[string]any{
		"remaining": nil,
		"used":      nil,
		"total":     nil,
		"unknown":   true,
		"error":     reason,
	}
}

func (c *Client) newTLSClient() (tlsclient.HttpClient, error) { return c.newTLSClientP(true) }

// newDirectTLSClient egresses on the local IP (never the proxy). Used for
// reference-image upload, polling and result download.
func (c *Client) newDirectTLSClient() (tlsclient.HttpClient, error) { return c.newTLSClientP(false) }

func (c *Client) newTLSClientP(useProxy bool) (tlsclient.HttpClient, error) {
	// TLS profile, User-Agent, Client Hints and OS must describe one coherent
	// browser. The copied cookies came from Chrome on macOS; Chrome_146 is the
	// newest profile supported by the current tls-client release and is much
	// closer to that browser than the old Chrome_120/133 Windows mixture.
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(60),
		tlsclient.WithClientProfile(profiles.Chrome_146),
	}
	if useProxy && c.proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(c.proxy))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

// downloadImage fetches a generated image (cdn.leonardo.ai) and returns the bytes.
func (c *Client) downloadImage(ctx context.Context, imageURL string) ([]byte, error) {
	if _, err := url.Parse(imageURL); err != nil {
		return nil, err
	}
	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":     {"image/avif,image/webp,image/png,image/*,*/*;q=0.8"},
		"user-agent": {userAgent},
		"referer":    {appBase + "/"},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: image download http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	return body, nil
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

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func emptyStringNil(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}

func clip(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
