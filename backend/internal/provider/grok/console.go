package grok

// Grok Console (console.x.ai) is xAI's own product console. It serves the same
// grok-imagine media models as grok.com, but over a plain JSON API that is NOT
// anti-bot gated: no x-statsig-id, no media-post + streaming-conversation dance.
// Auth reuses the very same website "sso" cookie — POST /v1/dpop/token exchanges
// it for a short-lived access token bound to a locally generated P-256 key, and
// every call then carries "Authorization: DPoP <token>" plus a per-request ES256
// DPoP proof (RFC 9449) over (method, url, token hash).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/google/uuid"
)

const (
	consoleBase   = "https://console.x.ai"
	consoleOrigin = "https://console.x.ai"

	// Upstream model ids of the Console media catalog. The -quality image model
	// is what the reference-image (edit) path uses.
	ConsoleVideoModel        = "grok-imagine-video"
	ConsoleImageModel        = "grok-imagine-image"
	ConsoleImageQualityModel = "grok-imagine-image-quality"

	// consoleTokenSkew refreshes a DPoP token slightly before it expires.
	consoleTokenSkew = 20 * time.Second
	// consoleMaxEditImages is the upstream cap for image-to-image references
	// (video takes a single 首图).
	consoleMaxEditImages = 3

	consoleVideoPollEvery = 2 * time.Second
	consoleVideoDeadline  = 20 * time.Minute
)

// consoleSession is one minted DPoP token plus the key it is bound to. skew is
// (server time − local time) learned from the mint response Date header; the
// proof iat is shifted by it so a drifting host clock doesn't fail every proof.
type consoleSession struct {
	accessToken string
	key         *ecdsa.PrivateKey
	jwk         consoleJWK
	expiresAt   time.Time
	skew        time.Duration
}

type consoleJWK struct {
	Crv string `json:"crv"`
	Kty string `json:"kty"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

var (
	consoleMu       sync.Mutex
	consoleSessions = map[string]consoleSession{} // keyed by sso token hash
)

// GenerateConsoleVideo runs Console's video pipeline: POST /v1/videos/generations
// returns a request_id, GET /v1/videos/{id} is polled until the clip is rendered
// and reports its vidgen.x.ai URL. frames is optional (image-to-video, one 首图
// max) and is inlined as a data URL — Console takes the image in the request, so
// there is no separate upload step. When downloadResult is false, returns nil
// bytes and the artifact URL in meta["video_url"]; otherwise downloads the mp4.
func (c *Client) GenerateConsoleVideo(ctx context.Context, token, prompt, aspectRatio, resolution string, seconds int, frames [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, nil, ErrAuth
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(frames) == 0 {
		return nil, nil, fmt.Errorf("grok console: prompt required")
	}
	if seconds < 1 || seconds > 15 {
		seconds = 10
	}
	submitClient, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	directClient, err := c.newDirectTLSClient()
	if err != nil {
		return nil, nil, err
	}

	payload := map[string]any{"model": ConsoleVideoModel, "duration": seconds}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if ratio := strings.TrimSpace(aspectRatio); ratio != "" {
		payload["aspect_ratio"] = ratio
	}
	if res := consoleVideoResolution(resolution); res != "" {
		payload["resolution"] = res
	}
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		payload["image"] = map[string]any{"url": dataURL(f)}
		break // 上游只吃 1 张首图
	}

	created, err := c.consoleJSON(ctx, submitClient, token, http.MethodPost, "/v1/videos/generations", payload)
	if err != nil {
		return nil, nil, err
	}
	requestID := strings.TrimSpace(stringValue(created["request_id"]))
	if requestID == "" {
		return nil, nil, fmt.Errorf("%w: video create missing request_id", ErrTemporaryUpstream)
	}

	deadline := time.Now().Add(consoleVideoDeadline)
	videoURL := ""
	for {
		status, pollErr := c.consoleJSON(ctx, submitClient, token, http.MethodGet, "/v1/videos/"+url.PathEscape(requestID), nil)
		if pollErr != nil {
			return nil, nil, pollErr
		}
		url, done, sErr := parseConsoleVideoStatus(status)
		if sErr != nil {
			return nil, nil, sErr
		}
		if done {
			videoURL = url
			break
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("%w: video render did not complete in time", ErrTemporaryUpstream)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(consoleVideoPollEvery):
		}
	}

	meta := map[string]any{
		"provider":   "grok",
		"request_id": requestID,
		"video_url":  videoURL,
	}
	if !downloadResult {
		return nil, meta, nil
	}
	data, err := c.download(ctx, directClient, token, videoURL)
	if err != nil {
		return nil, nil, err
	}
	return data, meta, nil
}

// GenerateConsoleImage runs Console's image pipeline: /v1/images/generations on
// grok-imagine-image for text-to-image, and /v1/images/edits on the
// grok-imagine-image-quality model (up to 3 inlined reference images) as soon as
// references are supplied — 带参考图自动走 quality 那个上游模型. Returns the
// artifact URL in meta["image_url"]; the bytes are downloaded unless urlOnly.
func (c *Client) GenerateConsoleImage(ctx context.Context, token, prompt, aspectRatio, resolution string, refs [][]byte, urlOnly bool) ([]byte, map[string]any, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, nil, ErrAuth
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, nil, fmt.Errorf("grok console: prompt required")
	}
	submitClient, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	directClient, err := c.newDirectTLSClient()
	if err != nil {
		return nil, nil, err
	}

	payload := map[string]any{
		"model": ConsoleImageModel, "prompt": strings.TrimSpace(prompt),
		"n": 1, "response_format": "url",
	}

	if ratio := consoleImageAspectRatio(aspectRatio); ratio != "" {
		payload["aspect_ratio"] = ratio
	}
	if res := consoleImageResolution(resolution); res != "" {
		payload["resolution"] = res
	}
	path := "/v1/images/generations"
	var images []map[string]any
	for _, r := range refs {
		if len(r) == 0 || len(images) >= consoleMaxEditImages {
			continue
		}
		images = append(images, map[string]any{"type": "image_url", "url": dataURL(r)})
	}
	if len(images) > 0 {
		path = "/v1/images/edits"
		payload["model"] = ConsoleImageQualityModel
		if len(images) == 1 {
			payload["image"] = images[0]
		} else {
			payload["images"] = images
		}
	}

	res, err := c.consoleJSON(ctx, submitClient, token, http.MethodPost, path, payload)
	if err != nil {
		return nil, nil, err
	}
	items, _ := res["data"].([]any)
	imageURL := ""
	for _, item := range items {
		entry, _ := item.(map[string]any)
		if entry == nil {
			continue
		}
		if v := strings.TrimSpace(stringValue(entry["url"])); v != "" {
			imageURL = v
			break
		}
	}
	if imageURL == "" {
		return nil, nil, fmt.Errorf("%w: image response missing url", ErrTemporaryUpstream)
	}
	meta := map[string]any{"provider": "grok", "image_url": imageURL}
	if urlOnly {
		return nil, meta, nil
	}
	data, err := c.download(ctx, directClient, token, imageURL)
	if err != nil {
		return nil, nil, err
	}
	return data, meta, nil
}

// consoleJSON does an authed Console JSON call and parses the response object. A
// 401 means the DPoP token went stale (or the sso died): the cached session is
// dropped and the call retried once with a freshly minted token.
func (c *Client) consoleJSON(ctx context.Context, client tlsclient.HttpClient, token, method, path string, body any) (map[string]any, error) {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = encoded
	}
	for attempt := 0; attempt < 2; attempt++ {
		session, err := c.consoleSession(ctx, client, token)
		if err != nil {
			return nil, err
		}
		var reader io.Reader
		if len(payload) > 0 {
			reader = strings.NewReader(string(payload))
		}
		req, err := http.NewRequest(method, consoleBase+path, reader)
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		if err := c.applyConsoleHeaders(req, token, session); err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			consoleForget(token, session.accessToken)
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, readErr)
		}
		if e := consoleMapStatus(path, resp.StatusCode, raw); e != nil {
			return nil, e
		}
		out := map[string]any{}
		if len(raw) == 0 {
			return out, nil
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("%w: %s non-json: %s", ErrTemporaryUpstream, path, clip(raw, 120))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: console retry state invalid", ErrTemporaryUpstream)
}

// consoleSession returns a live DPoP session for the sso token, minting one when
// the cache is empty or the token is about to expire.
func (c *Client) consoleSession(ctx context.Context, client tlsclient.HttpClient, token string) (consoleSession, error) {
	key := consoleCacheKey(token)
	consoleMu.Lock()
	cached, ok := consoleSessions[key]
	consoleMu.Unlock()
	if ok && cached.expiresAt.After(time.Now().Add(consoleTokenSkew)) {
		return cached, nil
	}
	session, err := c.mintConsoleSession(ctx, client, token)
	if err != nil {
		return consoleSession{}, err
	}
	consoleMu.Lock()
	consoleSessions[key] = session
	consoleMu.Unlock()
	return session, nil
}

// consoleForget drops a cached session (only if it is still the one that failed,
// so a concurrent refresh isn't thrown away).
func consoleForget(token, accessToken string) {
	key := consoleCacheKey(token)
	consoleMu.Lock()
	if current, ok := consoleSessions[key]; ok && current.accessToken == accessToken {
		delete(consoleSessions, key)
	}
	consoleMu.Unlock()
}

func consoleCacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

// mintConsoleSession posts the freshly generated public JWK to /v1/dpop/token
// with the sso cookie and keeps the returned DPoP-bound access token.
func (c *Client) mintConsoleSession(ctx context.Context, client tlsclient.HttpClient, token string) (consoleSession, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return consoleSession{}, err
	}
	jwk := consoleJWK{
		Crv: "P-256", Kty: "EC",
		X: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.X.FillBytes(make([]byte, 32))),
		Y: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.Y.FillBytes(make([]byte, 32))),
	}
	body, err := json.Marshal(map[string]any{"jwk": jwk})
	if err != nil {
		return consoleSession{}, err
	}
	req, err := http.NewRequest(http.MethodPost, consoleBase+"/v1/dpop/token", strings.NewReader(string(body)))
	if err != nil {
		return consoleSession{}, err
	}
	req = req.WithContext(ctx)
	c.applyConsoleBrowserHeaders(req, token, nil)
	before := time.Now().UTC()
	resp, err := client.Do(req)
	if err != nil {
		return consoleSession{}, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	after := time.Now().UTC()
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return consoleSession{}, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	if e := consoleMapStatus("/v1/dpop/token", resp.StatusCode, raw); e != nil {
		return consoleSession{}, e
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return consoleSession{}, fmt.Errorf("%w: dpop token non-json: %s", ErrTemporaryUpstream, clip(raw, 120))
	}
	if strings.TrimSpace(out.AccessToken) == "" || !strings.EqualFold(strings.TrimSpace(out.TokenType), "DPoP") {
		return consoleSession{}, fmt.Errorf("%w: dpop token response invalid", ErrTemporaryUpstream)
	}
	if out.ExpiresIn <= 0 || out.ExpiresIn > 3600 {
		out.ExpiresIn = 300
	}
	return consoleSession{
		accessToken: out.AccessToken,
		key:         privateKey,
		jwk:         jwk,
		expiresAt:   time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		skew:        consoleClockSkew(resp.Header.Get("Date"), before, after),
	}, nil
}

// applyConsoleHeaders sets the browser-like header set plus the DPoP
// Authorization header and its per-request proof.
func (c *Client) applyConsoleHeaders(req *http.Request, token string, session consoleSession) error {
	proof, err := consoleProof(session, req)
	if err != nil {
		return err
	}
	c.applyConsoleBrowserHeaders(req, token, map[string]string{
		"authorization": "DPoP " + session.accessToken,
		"dpop":          proof,
	})
	return nil
}

func (c *Client) applyConsoleBrowserHeaders(req *http.Request, token string, extra map[string]string) {
	h := http.Header{
		"accept":             {"*/*"},
		"accept-language":    {"en-US,en;q=0.9"},
		"content-type":       {"application/json"},
		"origin":             {consoleOrigin},
		"referer":            {consoleOrigin + "/"},
		"user-agent":         {userAgent},
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
		"accept", "accept-language", "authorization", "content-type", "dpop",
		"origin", "referer", "user-agent", "sec-ch-ua", "sec-ch-ua-mobile",
		"sec-ch-ua-platform", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "cookie",
	}
	req.Header = h
}

// consoleProof builds the ES256 DPoP proof JWT for one request: it binds the
// method + URL (without query) and the access token hash (ath), signed with the
// key the token was issued against.
func consoleProof(session consoleSession, req *http.Request) (string, error) {
	if session.key == nil || strings.TrimSpace(session.accessToken) == "" || req == nil || req.URL == nil {
		return "", errors.New("grok console: dpop session invalid")
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	ath := sha256.Sum256([]byte(session.accessToken))
	header := map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": session.jwk}
	claims := map[string]any{
		"jti": uuid.NewString(),
		"htm": strings.ToUpper(req.Method),
		"htu": req.URL.Scheme + "://" + req.URL.Host + path,
		"iat": time.Now().Add(session.skew).UTC().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(ath[:]),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, session.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// consoleClockSkew mirrors the console.x.ai frontend: proof iat is local time
// corrected by (server Date − local time) so host clock drift doesn't break
// every proof. before/after bound the RTT around the mint response.
func consoleClockSkew(dateHeader string, before, after time.Time) time.Duration {
	dateHeader = strings.TrimSpace(dateHeader)
	if dateHeader == "" {
		return 0
	}
	serverTime, err := http.ParseTime(dateHeader)
	if err != nil {
		return 0
	}
	if after.Before(before) {
		after = before
	}
	mid := before.Add(after.Sub(before) / 2)
	return serverTime.UTC().Sub(mid.UTC()).Round(time.Second)
}

// parseConsoleVideoStatus reads one poll response: (url, done, error).
func parseConsoleVideoStatus(res map[string]any) (string, bool, error) {
	status := strings.ToLower(strings.TrimSpace(stringValue(res["status"])))
	switch status {
	case "done", "completed", "succeeded", "success", "ready":
		video, _ := res["video"].(map[string]any)
		url := ""
		if video != nil {
			url = strings.TrimSpace(stringValue(video["url"]))
		}
		if url == "" {
			return "", false, fmt.Errorf("%w: video done without content url", ErrTemporaryUpstream)
		}
		return url, true, nil
	case "pending", "processing", "in_progress", "queued", "":
		return "", false, nil
	default:
		message := consoleErrorMessage(res["error"])
		if isCreditError(message) {
			return "", false, fmt.Errorf("%w: %s", ErrQuotaExhausted, message)
		}
		if message == "" {
			message = status
		}
		return "", false, fmt.Errorf("grok console: video generation failed: %s", message)
	}
}

// consoleMapStatus maps a Console HTTP status to the shared provider sentinels.
// Console is not anti-bot gated, so a 403 is a real rejection (dead sso / no
// access) unless the body is a Cloudflare interstitial.
func consoleMapStatus(path string, status int, raw []byte) error {
	body := string(raw)
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 401:
		return fmt.Errorf("%w: %s 401 %s", ErrAuth, path, clip(raw, 160))
	case status == 403:
		if isBotChallenge(body) {
			return fmt.Errorf("%w: %s 403 %s", ErrTemporaryUpstream, path, clip(raw, 160))
		}
		return fmt.Errorf("%w: %s 403 %s", ErrAuth, path, clip(raw, 160))
	case status == 429:
		if isCreditError(body) {
			return fmt.Errorf("%w: %s 429 %s", ErrQuotaExhausted, path, clip(raw, 160))
		}
		return fmt.Errorf("%w: %s 429 %s", ErrTemporaryUpstream, path, clip(raw, 160))
	case status >= 500:
		return fmt.Errorf("%w: %s %d %s", ErrTemporaryUpstream, path, status, clip(raw, 160))
	default:
		if isCreditError(body) {
			return fmt.Errorf("%w: %s", ErrQuotaExhausted, clip(raw, 160))
		}
		return fmt.Errorf("grok console: %s %d %s", path, status, clip(raw, 160))
	}
}

func consoleErrorMessage(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, key := range []string{"message", "code", "type"} {
			if text := strings.TrimSpace(stringValue(v[key])); text != "" {
				return text
			}
		}
	}
	return ""
}

// consoleImageAspectRatio keeps only the ratios Console's image models accept;
// anything else is dropped so the upstream picks its default instead of 400ing.
func consoleImageAspectRatio(value string) string {
	switch strings.TrimSpace(value) {
	case "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2":
		return strings.TrimSpace(value)
	}
	return ""
}

// consoleImageResolution maps our tier to the Console enum (only 1k / 2k exist).
func consoleImageResolution(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1K":
		return "1k"
	case "2K", "4K":
		return "2k"
	}
	return ""
}

// consoleVideoResolution clamps to the two tiers Console accepts.
func consoleVideoResolution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "480p":
		return "480p"
	default:
		return "720p"
	}
}

// dataURL inlines reference image bytes; Console takes images in the request
// body, so no upload endpoint is involved.
func dataURL(img []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
}
