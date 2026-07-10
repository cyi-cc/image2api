// Package runway implements the Runway (runwayml.com) provider client. For now
// it only covers account management — JWT detection, workspace/team id
// extraction and credit-balance probing — mirroring the curl_cffi reference in
// query_credits.py with tls-client so the JA3/JA4 fingerprint matches Chrome.
package runway

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	apiBase = "https://api.runwayml.com"
	origin  = "https://app.runwayml.com"

	// userAgent / secChUA mirror the real browser that produced a successful
	// registration + generation in the reference HAR (Edge 150 on Windows).
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0"
	secChUA   = `"Not;A=Brand";v="8", "Chromium";v="150", "Microsoft Edge";v="150"`
)

// randHex returns n random bytes hex-encoded (2n chars), for sentry trace ids.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

// browserHeaders builds the full Chrome/Edge header set the Runway web app sends
// on its JSON API calls. tls-client only spoofs the TLS/JA3 fingerprint — it does
// NOT inject a User-Agent or the client-hint / fetch-metadata headers. Without
// them a request looks like a headless bot and Runway's anti-abuse revokes the
// account's free credits within minutes ("秒死"). teamID is optional.
func browserHeaders(token, teamID string) http.Header {
	traceID := randHex(16)
	spanID := randHex(8)
	h := http.Header{
		"accept":             {"application/json"},
		"accept-language":    {"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"},
		"authorization":      {"Bearer " + strings.TrimPrefix(token, "Bearer ")},
		"baggage":            {"sentry-environment=production,sentry-public_key=8ea832c064ed4bbcb4b8952c02ba119a,sentry-trace_id=" + traceID},
		"content-type":       {"application/json"},
		"origin":             {origin},
		"priority":           {"u=1, i"},
		"referer":            {origin + "/"},
		"sec-ch-ua":          {secChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"same-site"},
		"sentry-trace":       {traceID + "-" + spanID},
		"user-agent":         {userAgent},
		"x-runway-workspace": {teamID},
		http.HeaderOrderKey: {
			"accept", "accept-language", "authorization", "baggage",
			"content-type", "origin", "priority", "referer",
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site",
			"sentry-trace", "user-agent", "x-runway-workspace",
		},
	}
	if strings.TrimSpace(teamID) == "" {
		h.Del("x-runway-workspace")
	}
	return h
}

// browserAssetHeaders is the lighter header set the browser sends on cross-site
// asset transfers (presigned S3 upload / CloudFront artifact download): a
// User-Agent and client hints, but no Runway authorization.
func browserAssetHeaders() http.Header {
	return http.Header{
		"accept":             {"*/*"},
		"accept-language":    {"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"},
		"origin":             {origin},
		"referer":            {origin + "/"},
		"sec-ch-ua":          {secChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"cross-site"},
		"user-agent":         {userAgent},
	}
}

var (
	ErrAuth              = errors.New("runway auth failed")
	ErrQuotaExhausted    = errors.New("runway quota exhausted")
	ErrTemporaryUpstream = errors.New("runway upstream temporary error")
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

// IsRunwayToken reports whether a JWT looks like a Runway access token: a
// top-level numeric "id" plus an "sso" claim, and crucially NO OpenAI
// (https://api.openai.com/*) claims — that's what disambiguates it from a
// ChatGPT token, which is otherwise also an opaque three-part JWT.
func IsRunwayToken(token string) bool {
	claims := decodeJWTPayload(token)
	if len(claims) == 0 {
		return false
	}
	for k := range claims {
		if strings.HasPrefix(k, "https://api.openai.com/") {
			return false
		}
	}
	_, hasSSO := claims["sso"]
	return hasSSO && claims["id"] != nil
}

// TeamIDFromToken returns the Runway workspace/team id, which equals the JWT
// "id" claim (query_credits.py / gen_video.py both derive teamId this way).
func TeamIDFromToken(token string) string {
	claims := decodeJWTPayload(token)
	switch v := claims["id"].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// ExtractAccountInfo decodes the free (no-network) JWT claims for the accounts
// view: email, team id and expiry.
func ExtractAccountInfo(token string) map[string]any {
	claims := decodeJWTPayload(token)
	return map[string]any{
		"email":      emptyStringNil(strings.TrimSpace(stringValue(claims["email"]))),
		"team_id":    emptyStringNil(TeamIDFromToken(token)),
		"expires_at": claims["exp"],
	}
}

// FetchCreditsBalance probes the account's plan credits via /v1/profile/features
// (query_credits.py). Returns a normalized map mirroring the Adobe client so the
// TokenService quota plumbing can treat all providers uniformly. A 401/403 maps
// to ErrAuth (token dead); any other failure is reported as unknown without
// killing the account.
func (c *Client) FetchCreditsBalance(ctx context.Context, token string) (map[string]any, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return unknownBalance("empty token"), nil
	}
	teamID := TeamIDFromToken(token)
	if teamID == "" {
		return unknownBalance("no team id"), nil
	}

	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	url := apiBase + "/v1/profile/features?asTeamId=" + teamID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = browserHeaders(token, teamID)

	resp, err := client.Do(req)
	if err != nil {
		return unknownBalance("network: " + err.Error()), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Per ops decision a rate-limit (403) is treated as a dead account too, same as
	// a 401 — a throttled Runway token is considered done.
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode != 200 {
		return unknownBalance(fmt.Sprintf("http %d: %s", resp.StatusCode, clip(body, 160))), nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return unknownBalance("non-json"), nil
	}
	features, _ := payload["features"].(map[string]any)
	permitted, _ := features["permitted"].(map[string]any)
	used, _ := features["used"].(map[string]any)
	total := intValue(permitted["numPlanCredits"])
	spent := intValue(used["numPlanCredits"])
	remaining := total - spent
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"remaining": remaining,
		"used":      spent,
		"total":     total,
		"unknown":   false,
		"error":     nil,
	}, nil
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
	options := []tlsclient.HttpClientOption{
		// Whole-request timeout, incl. reading the response body. 30s was too
		// tight for downloading a rendered image/video artifact off a slow CDN,
		// surfacing as "request canceled (Client.Timeout ... while reading body)".
		// The caller's genCtx still bounds the overall generation.
		tlsclient.WithTimeoutSeconds(300),
		tlsclient.WithClientProfile(profiles.Chrome_133),
		tlsclient.WithRandomTLSExtensionOrder(),
	}
	if useProxy && c.proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(c.proxy))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

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
