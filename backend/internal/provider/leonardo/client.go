// Package leonardo implements the Leonardo.ai (app.leonardo.ai) provider client.
// Unlike chatgpt/runway (whose JWT IS the stored credential), Leonardo's durable
// credential is the browser COOKIE (better-auth session): the bearer access token
// it mints lives only ~1h. So every call here takes the cookie and derives a
// fresh JWT on the fly via /api/auth/get-session — there is no long-lived token to
// store or a separate refresh profile to maintain. tls-client gives a Chrome
// JA3/JA4 fingerprint so the requests aren't flagged.
package leonardo

import (
	"bytes"
	"context"
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
)

const (
	appBase       = "https://app.leonardo.ai"
	graphqlURL    = "https://api.leonardo.ai/v1/graphql"
	schemaVersion = "1.255.2"
	userAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
)

var (
	ErrAuth              = errors.New("leonardo auth failed")
	ErrQuotaExhausted    = errors.New("leonardo quota exhausted")
	ErrTemporaryUpstream = errors.New("leonardo upstream temporary error")
)

type Client struct {
	proxy string
	// sessions caches the short-lived access token per cookie so we don't hit
	// /api/auth/get-session on every call — Leonardo rate-limits that endpoint
	// (429) hard, so re-using the ~1h JWT is essential.
	mu       sync.Mutex
	sessions map[string]*Session
	// rotated maps a stored cookie to the freshest value Leonardo handed back via
	// Set-Cookie (better-auth rotates its session_data cookie cache). The service
	// persists it; keeping it here means an unpersisted rotation still works for
	// the rest of the process's life.
	rotated map[string]string
}

func NewClient(proxy string) *Client {
	return &Client{proxy: strings.TrimSpace(proxy), sessions: map[string]*Session{}, rotated: map[string]string{}}
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

// IsLeonardoCookie reports whether a pasted credential is a Leonardo cookie: it
// carries the better-auth session cookie name. This is what disambiguates it from
// an Adobe cookie at import time.
func IsLeonardoCookie(value string) bool {
	return strings.Contains(value, "__Secure-better-auth.session_token") ||
		strings.Contains(value, "better-auth.session_data")
}

// HasSessionData reports whether the cookie carries better-auth's session_data
// cache. Leonardo authenticates get-session off THAT cookie: session_token alone
// answers 200 null (no bearer), which looks exactly like a dead account — so a
// cookie without it must be rejected at import instead of dying later.
func HasSessionData(value string) bool {
	return strings.Contains(value, "better-auth.session_data")
}

// RotatedCookie returns the freshest value for a stored cookie when Leonardo
// rotated its session_data cache, so the caller can persist it.
func (c *Client) RotatedCookie(cookie string) (string, bool) {
	key := strings.TrimSpace(cookie)
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh, ok := c.rotated[key]
	return fresh, ok && fresh != key
}

// mergeCookies applies a response's Set-Cookie pairs onto a request cookie
// string, keeping the original order and appending new names.
func mergeCookies(cookie string, setCookies []string) string {
	if len(setCookies) == 0 {
		return cookie
	}
	updates := map[string]string{}
	order := []string{}
	for _, sc := range setCookies {
		pair := strings.TrimSpace(strings.Split(sc, ";")[0])
		name, value, ok := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		if _, seen := updates[name]; !seen {
			order = append(order, name)
		}
		updates[name] = value
	}
	if len(updates) == 0 {
		return cookie
	}
	var out []string
	used := map[string]bool{}
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, _, _ := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if v, ok := updates[name]; ok {
			used[name] = true
			if v == "" { // a cleared cookie drops out
				continue
			}
			out = append(out, name+"="+v)
			continue
		}
		out = append(out, part)
	}
	for _, name := range order {
		if used[name] || updates[name] == "" {
			continue
		}
		out = append(out, name+"="+updates[name])
	}
	return strings.Join(out, "; ")
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
	// Cookie is the cookie that produced this session, with any Set-Cookie
	// rotation applied — persist it so the account keeps authenticating.
	Cookie string
}

// GetSession exchanges the cookie for a fresh access token + account ids. Only a
// 401 or a 200 carrying no access token means the session is dead → ErrAuth;
// everything else (notably the 403/429 人机校验 page) is a temporary error.
func (c *Client) GetSession(ctx context.Context, cookie string) (*Session, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return nil, ErrAuth
	}
	// Re-use a cached, still-valid access token (keep a 60s safety margin) instead
	// of hitting the heavily rate-limited get-session endpoint again.
	c.mu.Lock()
	if cs, ok := c.sessions[cookie]; ok && cs.ExpiresAt-60 > time.Now().Unix() {
		c.mu.Unlock()
		return cs, nil
	}
	c.mu.Unlock()

	// Use the freshest known value (an earlier response may have rotated the
	// better-auth cookie cache) rather than the possibly stale stored cookie.
	send := cookie
	c.mu.Lock()
	if fresh, ok := c.rotated[cookie]; ok && fresh != "" {
		send = fresh
	}
	c.mu.Unlock()

	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, appBase+"/api/auth/get-session", nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":          {"*/*"},
		"accept-language": {"en-US,en;q=0.9"},
		"cookie":          {send},
		"origin":          {appBase},
		"referer":         {appBase + "/"},
		"user-agent":      {userAgent},
		"sec-fetch-dest":  {"empty"},
		"sec-fetch-mode":  {"cors"},
		"sec-fetch-site":  {"same-origin"},
		http.HeaderOrderKey: {
			"accept", "accept-language", "cookie", "origin", "referer",
			"user-agent", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site",
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if merged := mergeCookies(send, resp.Header["Set-Cookie"]); merged != send {
		c.mu.Lock()
		c.rotated[cookie] = merged
		c.mu.Unlock()
		send = merged
	}
	if resp.StatusCode == 401 {
		return nil, ErrAuth
	}
	if resp.StatusCode != 200 {
		// 403 / 429 here is the Vercel / Cloudflare 人机校验 页，不是 cookie 失效 —
		// 当成临时错误，否则健康的号会被误判死。
		return nil, fmt.Errorf("%w: get-session http %d: %s", ErrTemporaryUpstream, resp.StatusCode, clip(body, 160))
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
		return nil, fmt.Errorf("%w: get-session non-json", ErrTemporaryUpstream)
	}
	if strings.TrimSpace(raw.Session.AccessToken) == "" {
		// No bearer despite 200 → the cookie no longer authenticates.
		return nil, ErrAuth
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
		ExpiresAt:   raw.Session.TokenExpiry,
		Cookie:      send,
	}
	if sess.ExpiresAt > time.Now().Unix() {
		c.mu.Lock()
		c.sessions[cookie] = sess
		c.mu.Unlock()
	}
	return sess, nil
}

// session returns the cookie's access token, optionally forcing a fresh mint
// (dropping the cache) — used when the upstream rejected the current bearer.
func (c *Client) session(ctx context.Context, cookie string, force bool) (*Session, error) {
	if force {
		c.mu.Lock()
		delete(c.sessions, strings.TrimSpace(cookie))
		c.mu.Unlock()
	}
	return c.GetSession(ctx, cookie)
}

// callGraphQL runs one GraphQL call for an account cookie. The bearer only lives
// ~1h, so a rejected token (401/403 or a JWTExpired GraphQL error) is re-minted
// from the cookie and the call retried once. Only a cookie that itself stops
// authenticating yields ErrAuth — an upstream bearer rejection stays temporary so
// the account is never killed for it.
func (c *Client) callGraphQL(ctx context.Context, cookie string, payload []byte, useProxy bool, label string) ([]byte, error) {
	var lastStatus int
	var lastBody []byte
	for attempt := 0; attempt < 2; attempt++ {
		sess, err := c.session(ctx, cookie, attempt > 0)
		if err != nil {
			return nil, err
		}
		body, status, err := c.graphqlP(ctx, sess.AccessToken, payload, useProxy)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %s", ErrTemporaryUpstream, label, err.Error())
		}
		lastStatus, lastBody = status, body
		stale := status == 401 || status == 403
		var gqlErr error
		if !stale && status == 200 {
			gqlErr = graphqlError(body)
			stale = errors.Is(gqlErr, ErrAuth)
		}
		if stale {
			if attempt == 0 {
				continue
			}
			break
		}
		if status != 200 {
			return nil, fmt.Errorf("%w: %s http %d: %s", ErrTemporaryUpstream, label, status, clip(body, 160))
		}
		if gqlErr != nil {
			return nil, gqlErr
		}
		return body, nil
	}
	return nil, fmt.Errorf("%w: %s rejected a freshly minted bearer (http %d): %s",
		ErrTemporaryUpstream, label, lastStatus, clip(lastBody, 160))
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
	body, err := c.callGraphQL(ctx, cookie, payload, false, "credits")
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return nil, ErrAuth
		}
		return unknownBalance(err.Error()), nil
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

// graphqlP runs a GraphQL call; callers pick the egress: only the generate submit
// uses the proxy; reference-image upload and polling run direct (local IP).
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
	// Match the fingerprint proven to work against Leonardo's Cloudflare edge:
	// Chrome_120, fixed extension order. A randomized JA3 (Chrome_133 +
	// WithRandomTLSExtensionOrder) gets flagged and 429'd at get-session.
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(60),
		tlsclient.WithClientProfile(profiles.Chrome_120),
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
