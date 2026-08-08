package adobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	submitURL       = "https://firefly-3p.ff.adobe.io/v2/3p-images/generate-async"
	image5SubmitURL = "https://image-v5.ff.adobe.io/v1/images/generate-async"
	videoSubmitURL  = "https://firefly-3p.ff.adobe.io/v2/3p-videos/generate-async"
	// Firefly-native video model (project id "firefly-video"): distinct host,
	// submit path and storage host from the 3p (veo/luma) video flow.
	fireflyVideoSubmitURL = "https://video-v1.ff.adobe.io/v2/videos/generate"
	fireflyVideoUploadURL = "https://video-v1.ff.adobe.io/v2/storage/image"
	uploadURL             = "https://firefly-3p.ff.adobe.io/v2/storage/image"
	// Seedance reference video/audio go to their own storage endpoints; posting
	// them to /v2/storage/image is rejected.
	uploadVideoURL = "https://firefly-3p.ff.adobe.io/v2/storage/video"
	uploadAudioURL = "https://firefly-3p.ff.adobe.io/v2/storage/audio"
	creditsURL     = "https://firefly.adobe.io/v1/credits/balance"
	creditsAPIKey  = "SunbreakWebUI1"
)

var (
	ErrAuth              = errors.New("adobe auth failed")
	ErrQuotaExhausted    = errors.New("adobe quota exhausted")
	ErrTemporaryUpstream = errors.New("adobe upstream temporary error")
	ErrDeadUpstream      = errors.New("adobe upstream fatal error")
	ErrRateLimited       = errors.New("adobe rate limited")
	// ErrContentRejected is Adobe's content-safety filter refusing the prompt or
	// the generated image (HTTP 451 image_unsafe). It is the prompt's fault, not
	// the account's — every account rejects the same content — so the caller must
	// surface it as-is without penalizing/killing the account or failing over.
	ErrContentRejected = errors.New("adobe content rejected")
	// ErrNotEntitled is a 403 user_not_entitled on submit: the account has no
	// Firefly entitlement at all. It wraps ErrAuth so existing auth handling still
	// applies, but callers can single it out — unlike an expired access token this
	// cannot be fixed by refreshing from the cookie, so the account is done.
	ErrNotEntitled = fmt.Errorf("%w: user not entitled", ErrAuth)
	// errTransport marks a request that never got a response back (EOF / 连接被切 /
	// 超时)。上游没收到就没开始生成，所以原地换条连接重试是安全的。
	errTransport = errors.New("transport failure")
)

// videoSubmitMaxRetries is how many extra in-place attempts a video submit gets
// when the connection dies before any response arrives (EOF/reset/timeout).
const videoSubmitMaxRetries = 3

// isContentRejection reports whether an Adobe response (status + body) is a
// content-safety refusal rather than a genuine upstream/account failure. Adobe
// returns HTTP 451 with an "*_unsafe" error_code when moderation blocks the
// prompt or the produced image, and "reference_image_privacy_error" when a
// reference image contains a real person's face. Both are the request's fault —
// every account rejects them identically — so neither may penalize the account.
func isContentRejection(status int, body string) bool {
	if status != 451 {
		return false
	}
	return strings.Contains(body, "unsafe") || strings.Contains(body, "privacy_error")
}

var profileURLs = []string{
	"https://ims-na1.adobelogin.com/ims/profile/v1",
	"https://adobeid-na1.services.adobe.com/ims/profile/v1",
}

type Client struct {
	apiKey string
	proxy  string
}

func NewClient(apiKey, proxy string) *Client {
	return &Client{
		apiKey: defaultString(apiKey, clientID),
		proxy:  strings.TrimSpace(proxy),
	}
}

// getARPSessionID returns a fresh ARP session id for this token. The value is
// regenerated per call, but the embedded pid stays stable per token; see
// buildARPSessionID for the wire format.
func (c *Client) getARPSessionID(token string) string {
	return buildARPSessionID(token)
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

func (c *Client) ExchangeCookie(ctx context.Context, cookie string) (*CookieExchangeResult, error) {
	sess, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	return exchangeCookieWithTLSClient(ctx, sess, cookie)
}

// uploadMaxRetries is how many extra in-place attempts a transient upload
// failure (transport error / timeout, 429/451/5xx) gets on a fresh connection
// before the error is surfaced.
const uploadMaxRetries = 5

// UploadImage stores a reference image and returns its blob id. Transient
// failures are retried in place (uploadMaxRetries times); the final error keeps
// its original (non-temporary) classification so the account is not penalized
// for a network blip.
func (c *Client) UploadImage(ctx context.Context, token string, content []byte, contentType, engine string) (string, error) {
	// Reference-image upload runs on the local IP (not the proxy).
	body, err, retryable := c.uploadImageOnce(ctx, token, content, contentType, engine)
	for attempt := 0; err != nil && retryable && attempt < uploadMaxRetries && ctx.Err() == nil; attempt++ {
		body, err, retryable = c.uploadImageOnce(ctx, token, content, contentType, engine)
	}
	if err != nil {
		return "", err
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("adobe upload bad response: %w (%s)", err, clip(body, 300))
	}
	if id := blobIDFromUploadPayload(payload); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("adobe upload missing blob id: %s", clip(body, 300))
}

// blobIDFromUploadPayload digs the blob id out of a storage response. The image
// endpoint answers {"images":[{"id":...}]}, video/audio use their own key
// ("videos"/"audios"/"assets"/"files"), and some responses put the id at the top
// level — accept any of them instead of only "images".
func blobIDFromUploadPayload(payload map[string]any) string {
	if id := strings.TrimSpace(stringValue(payload["id"])); id != "" {
		return id
	}
	for _, key := range []string{"images", "videos", "audios", "audio", "assets", "files"} {
		items, ok := payload[key].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		first, ok := items[0].(map[string]any)
		if !ok {
			continue
		}
		if id := strings.TrimSpace(stringValue(first["id"])); id != "" {
			return id
		}
	}
	return ""
}

// uploadImageOnce performs a single upload attempt and returns the raw response
// body plus whether a failure is retryable (transport error / 429/451/5xx).
// Auth failures (401/403) and other non-200s are not retryable.
func (c *Client) uploadImageOnce(ctx context.Context, token string, content []byte, contentType, engine string) ([]byte, error, bool) {
	sess, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err, false
	}

	endpoint := uploadURL
	switch {
	case engine == "firefly-video":
		endpoint = fireflyVideoUploadURL
	case strings.HasPrefix(contentType, "video/"):
		endpoint = uploadVideoURL
	case strings.HasPrefix(contentType, "audio/"):
		endpoint = uploadAudioURL
	}
	// Seedance is a Firefly-native model: its uploads need the clio-playground-web
	// key, the express key is rejected with 403 access_error.
	apiKey := c.apiKey
	if strings.HasPrefix(engine, "seedance-") {
		apiKey = clioClientID
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(content))
	if err != nil {
		return nil, err, false
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"authorization": {"Bearer " + strings.TrimSpace(token)},
		"x-api-key":     {apiKey},
		"content-type":  {defaultString(contentType, "image/png")},
		"accept":        {"*/*"},
		"user-agent":    {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"authorization",
			"x-api-key",
			"content-type",
			"accept",
			"user-agent",
		},
	}

	resp, err := sess.client.Do(req)
	if err != nil {
		// Transport error (incl. Client.Timeout on the storage endpoint) — a
		// network blip, retryable on a fresh connection.
		return nil, fmt.Errorf("adobe upload request: %w", err), true
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err, true
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("%w (upload %d %s: %s)", ErrAuth, resp.StatusCode, resp.Header.Get("x-access-error"), clip(body, 300)), false
	}
	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("%w (upload %d): %s", ErrRateLimited, resp.StatusCode, clip(body, 300)), false
	}
	if resp.StatusCode == 451 || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("adobe upload failed: %d %s", resp.StatusCode, clip(body, 300)), true
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("adobe upload failed: %d %s", resp.StatusCode, clip(body, 300)), false
	}
	return body, nil, true
}

func (c *Client) GenerateImage(ctx context.Context, token, modelID, prompt, aspectRatio, resolution string, blobIDs []string, downloadResult bool) ([]byte, map[string]any, error) {
	// Only the generate submit goes through the proxy; polling + download run on
	// the local IP.
	submitSess, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	pollSess, err := c.newDirectTLSClient()
	if err != nil {
		return nil, nil, err
	}

	var lastBody []byte
	var lastErr error
	// Firefly Image 5 uses a different endpoint + request schema (modelVersion
	// "image5", resolutionLevel, top-level aspectRatio label, no modelId/size).
	endpoint := submitURL
	var candidates []map[string]any
	if modelID == "firefly-image-5" {
		endpoint = image5SubmitURL
		candidates = []map[string]any{buildImage5Payload(prompt, aspectRatio, resolution, blobIDs)}
	} else {
		candidates = BuildImagePayloadCandidates(modelID, prompt, aspectRatio, resolution, blobIDs)
	}
	for _, payload := range candidates {
		respBody, pollURL, err := c.submitImage(ctx, submitSess, token, prompt, endpoint, payload)
		if err == nil {
			meta, data, pollErr := c.pollImage(ctx, pollSess, token, pollURL, downloadResult)
			if pollErr != nil {
				return nil, nil, pollErr
			}
			return data, meta, nil
		}
		lastBody = respBody
		lastErr = err
		if errors.Is(err, ErrAuth) || errors.Is(err, ErrQuotaExhausted) || errors.Is(err, ErrContentRejected) {
			return nil, nil, err
		}
	}
	// Content-safety refusal: the prompt/image is blocked, retrying other payloads
	// or accounts is pointless — surface it as-is so the pool doesn't fail over.
	if errors.Is(lastErr, ErrContentRejected) {
		return nil, nil, fmt.Errorf("%w: adobe submit: %s", ErrContentRejected, submitDetail(lastErr, lastBody))
	}
	// Preserve the temporary classification so the pool retries (overload / 5xx /
	// rate-limit) instead of failing the request outright.
	if errors.Is(lastErr, ErrDeadUpstream) {
		return nil, nil, fmt.Errorf("%w: adobe submit: %s", ErrDeadUpstream, submitDetail(lastErr, lastBody))
	}
	if errors.Is(lastErr, ErrTemporaryUpstream) {
		return nil, nil, fmt.Errorf("%w: adobe submit: %s", ErrTemporaryUpstream, submitDetail(lastErr, lastBody))
	}
	return nil, nil, fmt.Errorf("adobe submit failed: %s", submitDetail(lastErr, lastBody))
}

// submitDetail 拼接上游细节：优先用响应体，响应体为空时（传输层报错，例如超时、
// 连接被切）退回内层错误文本，避免错误只剩一句 "adobe submit:" 无从排查。
func submitDetail(err error, body []byte) string {
	if s := clip(body, 300); strings.TrimSpace(s) != "" {
		return s
	}
	if err != nil {
		return err.Error()
	}
	return "(empty response)"
}

// GenerateVideo renders the clip and (when downloadResult) downloads the MP4.
// With downloadResult=false it returns nil bytes and the upstream presigned URL
// in meta["video_url"] — used by the async /v1/videos job, which proxies that URL
// on /content instead of persisting the file.
func (c *Client) GenerateVideo(ctx context.Context, token, engine, prompt, aspectRatio string, durationSeconds int, resolution, referenceMode, upstreamModel string, blobIDs, videoBlobIDs, audioBlobIDs []string, downloadResult bool) ([]byte, map[string]any, error) {
	// Only the submit goes through the proxy; polling + download run on the local IP.
	submitSess, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	pollSess, err := c.newDirectTLSClient()
	if err != nil {
		return nil, nil, err
	}

	payload := BuildVideoPayload(engine, prompt, aspectRatio, durationSeconds, resolution, referenceMode, upstreamModel, blobIDs, videoBlobIDs, audioBlobIDs)
	endpoint := videoSubmitURL
	if engine == "firefly-video" {
		endpoint = fireflyVideoSubmitURL
	}
	// 连接在拿到响应前就断掉（EOF / reset / 超时）说明上游根本没收到这单，
	// 换一条新连接原地重试，最多 videoSubmitMaxRetries 次。
	respBody, pollURL, err := c.submitVideo(ctx, submitSess, token, endpoint, payload)
	for attempt := 0; err != nil && errors.Is(err, errTransport) && attempt < videoSubmitMaxRetries && ctx.Err() == nil; attempt++ {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
		retrySess, sessErr := c.newTLSClient()
		if sessErr != nil {
			break
		}
		submitSess = retrySess
		respBody, pollURL, err = c.submitVideo(ctx, submitSess, token, endpoint, payload)
	}
	if err != nil {
		return nil, nil, err
	}
	_ = respBody
	meta, data, pollErr := c.pollVideo(ctx, pollSess, token, pollURL, downloadResult)
	if pollErr != nil {
		return nil, nil, pollErr
	}
	return data, meta, nil
}

func (c *Client) FetchAccountProfile(ctx context.Context, token string) (map[string]any, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return map[string]any{}, nil
	}
	sess, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}

	for _, rawURL := range profileURLs {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header = http.Header{
			"authorization": {"Bearer " + token},
			"accept":        {"application/json"},
			"user-agent":    {sess.fp.userAgent},
			http.HeaderOrderKey: {
				"authorization",
				"accept",
				"user-agent",
			},
		}

		resp, err := sess.client.Do(req)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != 200 {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		email := strings.TrimSpace(stringValue(payload["email"]))
		displayName := strings.TrimSpace(stringValue(payload["displayName"]))
		if displayName == "" {
			displayName = strings.TrimSpace(stringValue(payload["name"]))
		}
		if displayName == "" {
			displayName = strings.TrimSpace(stringValue(payload["fullName"]))
		}
		userID := strings.TrimSpace(stringValue(payload["userId"]))
		if userID == "" {
			userID = strings.TrimSpace(stringValue(payload["authId"]))
		}
		if email != "" || displayName != "" || userID != "" {
			return map[string]any{
				"email":        emptyStringNil(email),
				"display_name": emptyStringNil(displayName),
				"user_id":      emptyStringNil(userID),
			}, nil
		}
	}

	return map[string]any{}, nil
}

func (c *Client) FetchCreditsBalance(ctx context.Context, token string) (map[string]any, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return map[string]any{
			"remaining":       nil,
			"used":            nil,
			"total":           nil,
			"available_until": nil,
			"unknown":         true,
			"error":           "empty token",
		}, nil
	}

	accountID := ExtractAccountID(token)
	if accountID == "" {
		return map[string]any{
			"remaining":       nil,
			"used":            nil,
			"total":           nil,
			"available_until": nil,
			"unknown":         true,
			"error":           "no account id",
		}, nil
	}

	sess, err := c.newTLSClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, creditsURL, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"authorization": {"Bearer " + token},
		"x-api-key":     {creditsAPIKey},
		"x-account-id":  {accountID},
		"accept":        {"application/json"},
		"content-type":  {"application/json"},
		"user-agent":    {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"authorization",
			"x-api-key",
			"x-account-id",
			"accept",
			"content-type",
			"user-agent",
		},
	}

	resp, err := sess.client.Do(req)
	if err != nil {
		return map[string]any{
			"remaining":       nil,
			"used":            nil,
			"total":           nil,
			"available_until": nil,
			"unknown":         true,
			"error":           "network: " + err.Error(),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		return nil, ErrAuth
	}
	if resp.StatusCode != 200 {
		return map[string]any{
			"remaining":       nil,
			"used":            nil,
			"total":           nil,
			"available_until": nil,
			"unknown":         true,
			"error":           fmt.Sprintf("http %d: %s", resp.StatusCode, clip(body, 160)),
		}, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{
			"remaining":       nil,
			"used":            nil,
			"total":           nil,
			"available_until": nil,
			"unknown":         true,
			"error":           "non-json",
		}, nil
	}

	totalInfo, _ := payload["total"].(map[string]any)
	quota, _ := totalInfo["quota"].(map[string]any)
	return map[string]any{
		"remaining":       intOrNil(quota["available"]),
		"used":            intOrNil(quota["used"]),
		"total":           intOrNil(quota["total"]),
		"available_until": emptyStringNil(strings.TrimSpace(stringValue(totalInfo["availableUntil"]))),
		"plan":            strings.TrimSpace(stringValue(totalInfo["planCap"])),
		"unknown":         false,
		"error":           nil,
	}, nil
}

func (c *Client) submitImage(ctx context.Context, sess *tlsSession, token, prompt, endpoint string, payload map[string]any) ([]byte, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"authorization":      {"Bearer " + strings.TrimSpace(token)},
		"x-api-key":          {c.apiKey},
		"x-arp-session-id":   {c.getARPSessionID(token)},
		"content-type":       {"application/json"},
		"accept":             {"*/*"},
		"origin":             {"https://new.express.adobe.com"},
		"referer":            {"https://new.express.adobe.com/"},
		"accept-language":    {"en-US,en;q=0.9"},
		"sec-ch-ua":          {sess.fp.secCHUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {sess.fp.platform},
		"sec-fetch-site":     {"cross-site"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"user-agent":         {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"authorization",
			"x-api-key",
			"x-arp-session-id",
			"content-type",
			"accept",
			"origin",
			"referer",
			"accept-language",
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"sec-ch-ua-platform",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-dest",
			"user-agent",
			"x-nonce",
		},
	}
	if nonce := buildSubmitNonce(token, prompt); nonce != "" {
		req.Header.Set("x-nonce", nonce)
	}

	resp, err := sess.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		accessErr := resp.Header.Get("x-access-error")
		if strings.EqualFold(accessErr, "taste_exhausted") {
			return respBody, "", ErrQuotaExhausted
		}
		if strings.EqualFold(accessErr, "user_not_entitled") {
			return respBody, "", fmt.Errorf("%w (submit %d %s: %s)", ErrNotEntitled, resp.StatusCode, accessErr, clip(respBody, 300))
		}
		return respBody, "", fmt.Errorf("%w (submit %d %s: %s)", ErrAuth, resp.StatusCode, accessErr, clip(respBody, 300))
	}
	// "system under load" / timeout_error = adobe rate-limit/overload (can come on a
	// non-5xx) — treat as temporary so the pool retries instead of failing.
	if b := string(respBody); strings.Contains(b, "system under load") || strings.Contains(b, "timeout_error") {
		return respBody, "", ErrTemporaryUpstream
	}
	if isContentRejection(resp.StatusCode, string(respBody)) {
		return respBody, "", ErrContentRejected
	}
	if resp.StatusCode == 429 || resp.StatusCode == 451 || resp.StatusCode >= 500 {
		return respBody, "", fmt.Errorf("%w (submit %d: %s)", ErrDeadUpstream, resp.StatusCode, clip(respBody, 300))
	}
	if resp.StatusCode != 200 {
		return respBody, "", fmt.Errorf("submit rejected: %d %s", resp.StatusCode, clip(respBody, 300))
	}

	var payloadResp map[string]any
	if err := json.Unmarshal(respBody, &payloadResp); err != nil {
		return respBody, "", err
	}
	if override := strings.TrimSpace(resp.Header.Get("x-override-status-link")); override != "" {
		return respBody, normalizePollURL(override), nil
	}
	if links, ok := payloadResp["links"].(map[string]any); ok {
		if result, ok := links["result"].(map[string]any); ok {
			if href := strings.TrimSpace(stringValue(result["href"])); href != "" {
				return respBody, normalizePollURL(href), nil
			}
		}
		if href := strings.TrimSpace(stringValue(links["result"])); href != "" {
			return respBody, normalizePollURL(href), nil
		}
	}
	return respBody, "", errors.New("submit ok but no poll url")
}

func (c *Client) pollImage(ctx context.Context, sess *tlsSession, token, pollURL string, downloadResult bool) (map[string]any, []byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("adobe generation timed out: %w", err)
		}

		req, err := http.NewRequest(http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, nil, err
		}
		req = req.WithContext(ctx)
		req.Header = http.Header{
			"authorization": {"Bearer " + strings.TrimSpace(token)},
			"accept":        {"*/*"},
			"origin":        {"https://new.express.adobe.com"},
			"referer":       {"https://new.express.adobe.com/"},
			"user-agent":    {sess.fp.userAgent},
			http.HeaderOrderKey: {
				"authorization",
				"accept",
				"origin",
				"referer",
				"user-agent",
			},
		}

		resp, err := sess.client.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if b := string(body); strings.Contains(b, "system under load") || strings.Contains(b, "timeout_error") {
			return nil, nil, ErrTemporaryUpstream
		}
		if isContentRejection(resp.StatusCode, string(body)) {
			return nil, nil, fmt.Errorf("%w: %s", ErrContentRejected, clip(body, 300))
		}
		if resp.StatusCode == 429 || resp.StatusCode == 451 || resp.StatusCode >= 500 {
			return nil, nil, fmt.Errorf("%w (poll %d: %s)", ErrDeadUpstream, resp.StatusCode, clip(body, 300))
		}
		if resp.StatusCode != 200 {
			return nil, nil, fmt.Errorf("adobe poll failed: %d %s", resp.StatusCode, clip(body, 300))
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, nil, err
		}
		if outputs, ok := payload["outputs"].([]any); ok && len(outputs) > 0 {
			if first, ok := outputs[0].(map[string]any); ok {
				if image, ok := first["image"].(map[string]any); ok {
					if url := strings.TrimSpace(stringValue(image["presignedUrl"])); url != "" {
						// Expose the presigned URL for callers that want it directly.
						payload["image_url"] = url
						if !downloadResult {
							return payload, nil, nil
						}
						data, err := c.download(ctx, sess, url)
						if err != nil {
							return nil, nil, err
						}
						return payload, data, nil
					}
				}
			}
		}

		status := strings.ToUpper(strings.TrimSpace(stringValue(payload["status"])))
		if status == "FAILED" || status == "CANCELLED" || status == "ERROR" {
			return nil, nil, fmt.Errorf("adobe job failed: %s", clip(body, 300))
		}
		time.Sleep(3 * time.Second)
	}
}

func (c *Client) submitVideo(ctx context.Context, sess *tlsSession, token, endpoint string, payload map[string]any) ([]byte, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(ctx)
	// Seedance is a Firefly-native model and needs the clio-playground-web key;
	// the express key is rejected with 403 access_error. The origin stays on the
	// Express surface for every engine — the upstream only gates on the key and
	// x-arp-session-id, not on origin.
	apiKey := c.apiKey
	origin := "https://new.express.adobe.com"
	if strings.EqualFold(stringValue(payload["modelId"]), "seedance") {
		apiKey = clioClientID
	}
	req.Header = http.Header{
		"authorization":      {"Bearer " + strings.TrimSpace(token)},
		"x-api-key":          {apiKey},
		"x-arp-session-id":   {c.getARPSessionID(token)},
		"content-type":       {"application/json"},
		"accept":             {"*/*"},
		"origin":             {origin},
		"referer":            {origin + "/"},
		"accept-language":    {"en-US,en;q=0.9"},
		"sec-ch-ua":          {sess.fp.secCHUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {sess.fp.platform},
		"sec-fetch-site":     {"cross-site"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"user-agent":         {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"authorization",
			"x-api-key",
			"x-arp-session-id",
			"content-type",
			"accept",
			"origin",
			"referer",
			"accept-language",
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"sec-ch-ua-platform",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-dest",
			"user-agent",
			"x-nonce",
		},
	}
	// The working video submit (HAR) carries x-nonce just like the image submit.
	if prompt, _ := payload["prompt"].(string); prompt != "" {
		if nonce := buildSubmitNonce(token, prompt); nonce != "" {
			req.Header.Set("x-nonce", nonce)
		}
	}

	resp, err := sess.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w: %v", ErrTemporaryUpstream, errTransport, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		accessErr := resp.Header.Get("x-access-error")
		if strings.EqualFold(accessErr, "taste_exhausted") {
			return respBody, "", ErrQuotaExhausted
		}
		if strings.EqualFold(accessErr, "user_not_entitled") {
			return respBody, "", fmt.Errorf("%w (%d %s: %s)", ErrNotEntitled, resp.StatusCode, accessErr, clip(respBody, 300))
		}
		// Surface Adobe's response body — "adobe auth failed" alone hides whether
		// it's a bad token, a missing scope, or a WAF/fingerprint block.
		return respBody, "", fmt.Errorf("%w (%d %s: %s)", ErrAuth, resp.StatusCode, accessErr, clip(respBody, 300))
	}
	if isContentRejection(resp.StatusCode, string(respBody)) {
		return respBody, "", ErrContentRejected
	}
	if resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode == 451 || resp.StatusCode >= 500 {
		return respBody, "", fmt.Errorf("%w (video submit %d: %s)", ErrDeadUpstream, resp.StatusCode, clip(respBody, 300))
	}
	// "system under load" / timeout_error = adobe overload — treat as a temporary
	// error so the tempFailover policy moves to the next account (same as the image path).
	if b := string(respBody); strings.Contains(b, "system under load") || strings.Contains(b, "timeout_error") {
		return respBody, "", ErrTemporaryUpstream
	}
	if resp.StatusCode != 200 {
		return respBody, "", fmt.Errorf("video submit rejected: %d %s", resp.StatusCode, clip(respBody, 300))
	}

	var payloadResp map[string]any
	if err := json.Unmarshal(respBody, &payloadResp); err != nil {
		return respBody, "", err
	}
	if override := strings.TrimSpace(resp.Header.Get("x-override-status-link")); override != "" {
		return respBody, normalizePollURL(override), nil
	}
	if links, ok := payloadResp["links"].(map[string]any); ok {
		if result, ok := links["result"].(map[string]any); ok {
			if href := strings.TrimSpace(stringValue(result["href"])); href != "" {
				return respBody, normalizePollURL(href), nil
			}
		}
		if href := strings.TrimSpace(stringValue(links["result"])); href != "" {
			return respBody, normalizePollURL(href), nil
		}
	}
	return respBody, "", errors.New("video submit ok but no poll url")
}

func (c *Client) pollVideo(ctx context.Context, sess *tlsSession, token, pollURL string, downloadResult bool) (map[string]any, []byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("adobe video generation timed out: %w", err)
		}

		req, err := http.NewRequest(http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, nil, err
		}
		req = req.WithContext(ctx)
		req.Header = http.Header{
			"authorization": {"Bearer " + strings.TrimSpace(token)},
			"accept":        {"*/*"},
			"origin":        {"https://new.express.adobe.com"},
			"referer":       {"https://new.express.adobe.com/"},
			"user-agent":    {sess.fp.userAgent},
			http.HeaderOrderKey: {
				"authorization",
				"accept",
				"origin",
				"referer",
				"user-agent",
			},
		}

		resp, err := sess.client.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, nil, fmt.Errorf("%w (%d %s: %s)", ErrAuth, resp.StatusCode, resp.Header.Get("x-access-error"), clip(body, 300))
		}
		if b := string(body); strings.Contains(b, "system under load") || strings.Contains(b, "timeout_error") {
			return nil, nil, ErrTemporaryUpstream
		}
		if isContentRejection(resp.StatusCode, string(body)) {
			return nil, nil, fmt.Errorf("%w: %s", ErrContentRejected, clip(body, 300))
		}
		if resp.StatusCode == 429 || resp.StatusCode == 451 || resp.StatusCode >= 500 {
			return nil, nil, fmt.Errorf("%w (video poll %d: %s)", ErrDeadUpstream, resp.StatusCode, clip(body, 300))
		}
		if resp.StatusCode != 200 {
			return nil, nil, fmt.Errorf("adobe video poll failed: %d %s", resp.StatusCode, clip(body, 300))
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, nil, err
		}
		if outputs, ok := payload["outputs"].([]any); ok && len(outputs) > 0 {
			if first, ok := outputs[0].(map[string]any); ok {
				if video, ok := first["video"].(map[string]any); ok {
					if raw := strings.TrimSpace(stringValue(video["presignedUrl"])); raw != "" {
						payload["video_url"] = raw
						if !downloadResult {
							return payload, nil, nil
						}
						data, err := c.download(ctx, sess, raw)
						if err != nil {
							return nil, nil, err
						}
						return payload, data, nil
					}
				}
			}
		}

		status := strings.ToUpper(strings.TrimSpace(stringValue(payload["status"])))
		if status == "FAILED" || status == "CANCELLED" || status == "ERROR" {
			return nil, nil, fmt.Errorf("adobe video job failed: %s", clip(body, 300))
		}
		time.Sleep(3 * time.Second)
	}
}

// download fetches the generated artifact from Adobe's presigned S3 URL. The
// asset is already produced at this point, so a transient network hiccup
// (connection EOF/reset mid-body, S3 accelerate 5xx) must not fail the whole
// generation — retry with backoff before giving up.
const downloadTimeout = 3 * time.Minute

func (c *Client) download(parent context.Context, sess *tlsSession, url string) ([]byte, error) {
	// The artifact is already produced; detach from the (often nearly-elapsed)
	// generation ctx deadline/cancel and give the download its own budget so a
	// slow CDN read isn't killed mid-body and the backoff-retries can actually run.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), downloadTimeout)
	defer cancel()
	var data []byte
	var err error
	for _, wait := range []time.Duration{0, 1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second} {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		data, err = c.downloadOnce(ctx, sess, url)
		if err == nil || !errors.Is(err, ErrTemporaryUpstream) {
			break
		}
	}
	return data, err
}

func (c *Client) downloadOnce(ctx context.Context, sess *tlsSession, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":     {"*/*"},
		"user-agent": {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"accept",
			"user-agent",
		},
	}
	resp, err := sess.client.Do(req)
	if err != nil {
		// Network-level failure (EOF, reset, timeout) — transient, worth a retry.
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: adobe download failed: %d %s", ErrTemporaryUpstream, resp.StatusCode, clip(body, 200))
		}
		return nil, fmt.Errorf("adobe download failed: %d %s", resp.StatusCode, clip(body, 200))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		// Body truncated mid-read (EOF) — transient, retry.
		return nil, fmt.Errorf("%w: read body: %v", ErrTemporaryUpstream, err)
	}
	return data, nil
}

// fingerprint bundles a TLS ClientProfile with the browser headers that match
// it, so the JA3/JA4 handshake and the advertised User-Agent / client hints
// stay internally consistent within a single request.
type fingerprint struct {
	profile   profiles.ClientProfile
	userAgent string
	secCHUA   string
	platform  string // quoted sec-ch-ua-platform value, e.g. `"Windows"`
}

const (
	osWindows = iota
	osMac
)

// newChromeFingerprint pairs a TLS ClientProfile with headers for the same
// Chrome major. secCHUA is passed in verbatim rather than templated: the GREASE
// brand entry ("Not(A:Brand", "Not_A Brand", …), its version, and its position
// in the list all rotate per Chrome release, so there is no formula to derive
// it from the major.
func newChromeFingerprint(profile profiles.ClientProfile, major, osKind int, secCHUA string) fingerprint {
	ua := fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
	platform := `"Windows"`
	if osKind == osMac {
		ua = fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
		platform = `"macOS"`
	}
	return fingerprint{
		profile:   profile,
		userAgent: ua,
		secCHUA:   secCHUA,
		platform:  platform,
	}
}

// adobeFingerprints is a small pool of recent desktop Chrome builds across
// Windows/macOS. Each request draws one at random so the outbound TLS
// fingerprint and headers vary from call to call instead of presenting a single
// static signature to Adobe's anti-bot layer.
//
// The majors here are capped by what tls-client ships a TLS profile for
// (Chrome_146 is the newest in v1.15.1). Live Adobe traffic is on Chromium
// 150+, so the UA is deliberately NOT set higher than the pool: advertising a
// newer UA than the JA3/JA4 handshake backs up is a sharper signal than being a
// few releases behind. tls-client has no Edge profile at any version, so the
// pool stays pure Chrome rather than pairing an Edge UA with a Chrome
// handshake.
var adobeFingerprints = []fingerprint{
	newChromeFingerprint(profiles.Chrome_146, 146, osWindows, `"Chromium";v="146", "Google Chrome";v="146", "Not?A_Brand";v="24"`),
	newChromeFingerprint(profiles.Chrome_146, 146, osMac, `"Chromium";v="146", "Google Chrome";v="146", "Not?A_Brand";v="24"`),
	newChromeFingerprint(profiles.Chrome_144, 144, osWindows, `"Chromium";v="144", "Google Chrome";v="144", "Not?A_Brand";v="24"`),
	newChromeFingerprint(profiles.Chrome_144, 144, osMac, `"Chromium";v="144", "Google Chrome";v="144", "Not?A_Brand";v="24"`),
	newChromeFingerprint(profiles.Chrome_133, 133, osWindows, `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`),
	newChromeFingerprint(profiles.Chrome_131, 131, osMac, `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`),
}

func randomFingerprint() fingerprint {
	return adobeFingerprints[rand.Intn(len(adobeFingerprints))]
}

// tlsSession pairs a configured TLS client with the fingerprint it was built
// for, so downstream header builders advertise the matching UA / client hints.
type tlsSession struct {
	client tlsclient.HttpClient
	fp     fingerprint
}

// newTLSClient builds a session that routes through the configured proxy (when
// set). newDirectTLSClient builds one that always uses the local IP. Only the
// image-generation submit goes through the proxy; reference-image upload,
// polling and result download run on the local IP.
func (c *Client) newTLSClient() (*tlsSession, error) {
	return c.newTLSSession(randomFingerprint(), true)
}

func (c *Client) newDirectTLSClient() (*tlsSession, error) {
	return c.newTLSSession(randomFingerprint(), false)
}

func (c *Client) newTLSSession(fp fingerprint, useProxy bool) (*tlsSession, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(60),
		tlsclient.WithClientProfile(fp.profile),
		tlsclient.WithNotFollowRedirects(),
		tlsclient.WithRandomTLSExtensionOrder(),
	}
	if useProxy && c.proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(c.proxy))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	return &tlsSession{client: client, fp: fp}, nil
}

func exchangeCookieWithTLSClient(ctx context.Context, sess *tlsSession, cookie string) (*CookieExchangeResult, error) {
	cookie = normalizeCookie(cookie)
	if cookie == "" {
		return nil, ErrAdobeCookieEmpty
	}

	body := "client_id=" + clientID + "&guest_allowed=true&scope=" + strings.ReplaceAll(scopeValue, ",", "%2C")
	payload, err := doCookieExchange(ctx, sess, cookie, body, "https://new.express.adobe.com")
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(stringValue(payload["access_token"]))
	if token == "" {
		return nil, errors.New("adobe cookie exchange missing access_token")
	}
	// The Express token (projectx_webapp) covers image/veo/luma/sora but is not
	// entitled to the Firefly-native seedance models — those return 403
	// model_not_authorized. firefly.adobe.com mints a second token for
	// client_id=clio-playground-web with the creative_production scope, keyed by
	// user_id, which IS entitled. Mint that here too (best-effort: if it fails we
	// keep the Express token so the other models still work).
	if uid := ExtractAccountID(token); uid != "" {
		clioBody := "client_id=" + clioClientID + "&scope=" + strings.ReplaceAll(clioScopeValue, ",", "%2C") + "&user_id=" + url.QueryEscape(uid)
		if clioPayload, err := doCookieExchange(ctx, sess, cookie, clioBody, "https://firefly.adobe.com"); err == nil {
			if clioToken := strings.TrimSpace(stringValue(clioPayload["access_token"])); clioToken != "" {
				return &CookieExchangeResult{
					AccessToken: clioToken,
					ExpiresIn:   intValue(clioPayload["expires_in"]),
					Raw:         clioPayload,
				}, nil
			}
		}
	}
	return &CookieExchangeResult{
		AccessToken: token,
		ExpiresIn:   intValue(payload["expires_in"]),
		Raw:         payload,
	}, nil
}

func doCookieExchange(ctx context.Context, sess *tlsSession, cookie, body, origin string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, refreshURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":          {"*/*"},
		"accept-language": {"en-US,en;q=0.9"},
		"content-type":    {"application/x-www-form-urlencoded;charset=UTF-8"},
		"cookie":          {cookie},
		"origin":          {origin},
		"referer":         {origin + "/"},
		"user-agent":      {sess.fp.userAgent},
		http.HeaderOrderKey: {
			"accept", "accept-language", "content-type", "cookie",
			"origin", "referer", "user-agent",
		},
	}
	resp, err := sess.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adobe cookie exchange network error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("adobe cookie exchange upstream %d: %s", resp.StatusCode, clip(respBody, 200))
	}
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return nil, fmt.Errorf("adobe cookie exchange invalid json: %w", err)
	}
	return payload, nil
}

func buildSubmitNonce(token, prompt string) string {
	claims := decodeJWTPayload(token)
	userID := strings.TrimSpace(stringValue(claims["user_id"]))
	if userID == "" {
		userID = strings.TrimSpace(stringValue(claims["aa_id"]))
	}
	if userID == "" {
		userID = strings.TrimSpace(stringValue(claims["sub"]))
	}
	prompt = strings.TrimSpace(prompt)
	if userID == "" || prompt == "" {
		return ""
	}
	if len(prompt) > 256 {
		prompt = prompt[:256]
	}
	sum := sha256.Sum256([]byte(userID + "-" + prompt))
	return hex.EncodeToString(sum[:])
}

func ExtractAccountID(token string) string {
	claims := decodeJWTPayload(token)
	userID := strings.TrimSpace(stringValue(claims["user_id"]))
	if userID == "" {
		userID = strings.TrimSpace(stringValue(claims["aa_id"]))
	}
	if userID == "" {
		userID = strings.TrimSpace(stringValue(claims["sub"]))
	}
	return userID
}

func normalizePollURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := parsed.Hostname()
	if !strings.HasPrefix(host, "firefly-epo") {
		return raw
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return raw
	}
	jobID := strings.TrimSpace(parts[len(parts)-1])
	hostSuffix := strings.TrimPrefix(host, "firefly-epo")
	hostSuffix = strings.SplitN(hostSuffix, ".", 2)[0]
	if len(hostSuffix) != 4 {
		return raw
	}
	for _, ch := range hostSuffix {
		if ch < '0' || ch > '9' {
			return raw
		}
	}
	return "https://bks-epo" + hostSuffix + ".adobe.io/v2/jobs/result/" + jobID + "?host=" + parsed.Host + "/"
}

func clip(v []byte, n int) string {
	s := strings.TrimSpace(string(v))
	if len(s) <= n {
		return s
	}
	return s[:n]
}
