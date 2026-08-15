// Package creativefabrica implements the Creative Fabrica Studio
// (studio.creativefabrica.com) video-generation upstream.
//
// One-shot accounts: every account's coins are just enough for exactly one
// generation, so a successful render kills the account. The credential is a
// .creativefabrica.com session cookie; a short-lived JWT is minted from it on
// demand via GraphQL /query/userAuth, and every model request authenticates
// with that JWT (the cookie is sent along as a fallback).
package creativefabrica

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

var (
	ErrAuth              = errors.New("creativefabrica auth failed")
	ErrQuotaExhausted    = errors.New("creativefabrica quota exhausted")
	ErrTemporaryUpstream = errors.New("creativefabrica upstream temporary error")
	ErrDeadUpstream      = errors.New("creativefabrica upstream fatal error")
	ErrRateLimited       = errors.New("creativefabrica rate limited")
	// ErrPaymentRequired marks an account whose payment intent is in a failed
	// state — it can never generate (the studio answers 400 failed_precondition
	// "payment required ... COIN_PAYMENT_INTENT_STATUS_FAILED"). It wraps ErrAuth
	// so the pool kills the account and fails over instead of burning retries.
	ErrPaymentRequired = fmt.Errorf("%w: payment required", ErrAuth)
)

// isPaymentRequired reports whether a non-200 body is the account-level
// "payment required" rejection rather than a request-level parameter error.
//
// Connect unary errors don't always carry the marker in plaintext: the studio
// answers failed_precondition with the real detail base64-protobuf-encoded in
// details[].value ("payment required. payment status: COIN_PAYMENT_INTENT_...").
// Decode those values and scan the decoded bytes, so a failed coin intent still
// kills the account instead of being misread as a request-level 400.
func isPaymentRequired(status int, body string) bool {
	if status != 400 {
		return false
	}
	b := strings.ToLower(body)
	if strings.Contains(b, "payment required") || strings.Contains(b, "coin_payment_intent") {
		return true
	}
	var env struct {
		Details []struct {
			Value string `json:"value"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return false
	}
	for _, d := range env.Details {
		raw, err := base64.StdEncoding.DecodeString(d.Value)
		if err != nil {
			continue
		}
		low := strings.ToLower(string(raw))
		if strings.Contains(low, "payment required") ||
			strings.Contains(low, "coin_payment_intent") ||
			strings.Contains(low, "coin_error_code_payment_required") {
			return true
		}
	}
	return false
}

const (
	graphQLHost       = "https://graphql-gw.creativefabrica.com"
	mediaMatrixHost   = "https://studio-media-matrix.creativefabrica.com"
	userAuthPath      = "/query/userAuth"
	userBalancePath   = "/query/userBalance"
	userPath          = "/query/user"
	initiatePath      = "/creativefabrica.studiomediamatrix.v1.StudioMediaMatrixService/InitiateSession"
	listSessionsPath  = "/creativefabrica.studiomediamatrix.v1.StudioMediaMatrixService/ListSessions"
	origin            = "https://studio.creativefabrica.com"
	pollInterval      = 5 * time.Second
	pollTimeout       = 16 * time.Minute
	downloadTimeout   = 3 * time.Minute
	videoServiceType  = "SERVICE_TYPE_VIDEO_GENERATOR"
	videoFrameRef     = "VIDEO_FRAME_TYPE_REFERENCE"
	visibilityPrivate = "SESSION_VISIBILITY_PRIVATE"
)

// Model is one Creative Fabrica video model: the local catalog id, the upstream
// enum, the fixed duration in seconds, and the upstream resolution label.
type Model struct {
	ID        string // local model_configs id, e.g. "seedance-2.0"
	Enum      string // upstream enum, e.g. VIDEO_GENERATOR_MODEL_BYTEDANCE_SEEDDREAM_2
	Duration  int    // fixed seconds (account plan is fixed-length)
	Resolution string // upstream resolution label, e.g. 720p
}

// Models returns the two Creative Fabrica seedance models. The upstream enum
// differs between the two (SEEDANCE_2_FAST vs SEEDDREAM_2), matching the
// studio frontend's InitiateSession payloads.
func Models() map[string]Model {
	return map[string]Model{
		"seedance-2.0-fast": {
			ID:         "seedance-2.0-fast",
			Enum:       "VIDEO_GENERATOR_MODEL_BYTEDANCE_SEEDANCE_2_FAST",
			Duration:   14,
			Resolution: "720p",
		},
		"seedance-2.0": {
			ID:         "seedance-2.0",
			Enum:       "VIDEO_GENERATOR_MODEL_BYTEDANCE_SEEDDREAM_2",
			Duration:   10,
			Resolution: "720p",
		},
	}
}

// LookupModel resolves a local model id to its upstream config. Returns ok=false
// when the id isn't a Creative Fabrica model.
func LookupModel(modelID string) (Model, bool) {
	m, ok := Models()[modelID]
	return m, ok
}

// Client talks to the Creative Fabrica Studio API through a Chrome-fingerprinted
// TLS client so the Cloudflare-protected Connect endpoints don't reject us.
type Client struct {
	proxy string
}

func NewClient(proxy string) *Client {
	return &Client{proxy: strings.TrimSpace(proxy)}
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

// ExchangeToken mints the short-lived JWT from the account cookie via GraphQL
// /query/userAuth. Returns the token and the user id. A null / missing me means
// the cookie no longer authenticates → ErrAuth.
func (c *Client) ExchangeToken(ctx context.Context, cookie string) (token, userID string, err error) {
	query := `{"query":"\n    query userAuth {\n  me {\n    token\n    user {\n      id\n      isTemporary\n    }\n  }\n}\n    "}`
	var payload struct {
		Data struct {
			Me *struct {
				Token string `json:"token"`
				User  struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"me"`
		} `json:"data"`
	}
	body, err := c.postGraphQL(ctx, cookie, "", userAuthPath, query)
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("%w: bad token response: %s", ErrAuth, clip(body, 300))
	}
	if payload.Data.Me == nil || strings.TrimSpace(payload.Data.Me.Token) == "" {
		return "", "", fmt.Errorf("%w: cookie did not authenticate", ErrAuth)
	}
	return payload.Data.Me.Token, payload.Data.Me.User.ID, nil
}

// FetchBalance reads the coin balance via GraphQL /query/userBalance (the
// request authenticates with the cookie alone). Negative value on error.
func (c *Client) FetchBalance(ctx context.Context, cookie string) (int64, error) {
	query := `{"query":"\nquery userBalance {\n  userBalance {\n    balance\n  }\n}\n\n"}`
	var payload struct {
		Data struct {
			UserBalance *struct {
				Balance json.Number `json:"balance"`
			} `json:"userBalance"`
		} `json:"data"`
	}
	body, err := c.postGraphQL(ctx, cookie, "", userBalancePath, query)
	if err != nil {
		return -1, err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return -1, fmt.Errorf("bad balance response: %s", clip(body, 300))
	}
	if payload.Data.UserBalance == nil {
		return -1, ErrAuth
	}
	b, _ := payload.Data.UserBalance.Balance.Int64()
	return b, nil
}

// FetchUser reads the profile (email, name) via GraphQL /query/user, which the
// studio browser hits on every page load. The request authenticates with the
// cookie alone. Returns the email (empty on error); a null me means the cookie
// no longer authenticates → ErrAuth.
func (c *Client) FetchUser(ctx context.Context, cookie string) (string, error) {
	query := `{"query":"\n    query user {\n  me {\n    token\n    user {\n      id\n      email\n    }\n  }\n}\n    "}`
	var payload struct {
		Data struct {
			Me *struct {
				Token string `json:"token"`
				User  struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"user"`
			} `json:"me"`
		} `json:"data"`
	}
	body, err := c.postGraphQL(ctx, cookie, "", userPath, query)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("%w: bad user response: %s", ErrAuth, clip(body, 300))
	}
	if payload.Data.Me == nil || payload.Data.Me.User.ID == "" {
		return "", ErrAuth
	}
	return strings.TrimSpace(payload.Data.Me.User.Email), nil
}

// GenerateVideo runs the full generation: InitiateSession → PUT reference
// images to the presigned S3 URLs → poll ListSessions until COMPLETED → (when
// downloadResult) fetch the MP4. Returns the bytes (nil when url-only) and the
// previewMediaUrl. durationSeconds is ignored: the plan fixes the length per
// model (LookupModel.Duration).
func (c *Client) GenerateVideo(ctx context.Context, cookie, token, modelID, prompt, aspectRatio string, refs [][]byte, downloadResult bool) ([]byte, string, error) {
	m, ok := LookupModel(modelID)
	if !ok {
		return nil, "", fmt.Errorf("creativefabrica: unknown model %q", modelID)
	}
	sessionID, uploads, err := c.initiateSession(ctx, cookie, token, m, prompt, aspectRatio, refs)
	if err != nil {
		return nil, "", err
	}
	// Each ref uploads to the presigned URL the session returned for it.
	for i, u := range uploads {
		if err := c.putS3(ctx, u, refs[i]); err != nil {
			return nil, "", err
		}
	}
	videoURL, err := c.pollSession(ctx, cookie, token, sessionID)
	if err != nil {
		return nil, "", err
	}
	if !downloadResult {
		return nil, videoURL, nil
	}
	data, err := c.download(ctx, videoURL)
	if err != nil {
		return nil, "", err
	}
	return data, videoURL, nil
}

// initiateSession submits the generation and returns the session id plus the
// presigned upload URLs (one per reference image).
func (c *Client) initiateSession(ctx context.Context, cookie, token string, m Model, prompt, aspectRatio string, refs [][]byte) (string, []string, error) {
	frames := make([]any, 0, len(refs))
	refPrompt := strings.TrimSpace(prompt)
	for i := range refs {
		ref := fmt.Sprintf("img%d", i+1)
		frames = append(frames, map[string]any{
			"type":     videoFrameRef,
			"fileSize": fmt.Sprintf("%d", len(refs[i])),
			"fileName": randomFileName(i + 1),
			"ref":      ref,
		})
		if !strings.Contains(refPrompt, "["+ref+"]") {
			refPrompt += " Use [" + ref + "]"
		}
	}
	reqBody := map[string]any{
		"visibility": visibilityPrivate,
		"sessionRequestPromptToVideoGeneratorContent": map[string]any{
			"serviceType":  videoServiceType,
			"promptContent": map[string]any{"prompt": refPrompt},
			"resolution":   resolutionEnum(m.Resolution),
			"model":        m.Enum,
			"frames":       frames,
			"aspectRatio":  aspectRatioEnum(aspectRatio),
			"directorConfig": map[string]any{
				"filmStock": map[string]any{"color": "DIRECTOR_FILM_STOCK_COLOR_FULL_COLOR"},
			},
			"videoDuration": map[string]any{"inSeconds": m.Duration},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.postConnect(ctx, cookie, token, initiatePath, body, true)
	if err != nil {
		return "", nil, err
	}
	var payload struct {
		Session struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			// The presigned upload URLs are nested under promptToVideoGeneratorContent.frames
			// (each frame echoes the request frame + the S3 presigned `url`), not under a
			// top-level session.frames. Reading the wrong level yields 0 uploads.
			PromptToVideoGeneratorContent struct {
				Frames []struct {
					URL string `json:"url"`
				} `json:"frames"`
			} `json:"promptToVideoGeneratorContent"`
		} `json:"session"`
	}
	if err := json.Unmarshal(resp, &payload); err != nil {
		return "", nil, fmt.Errorf("creativefabrica bad initiate response: %s", clip(resp, 300))
	}
	if strings.TrimSpace(payload.Session.ID) == "" {
		return "", nil, fmt.Errorf("creativefabrica initiate missing session: %s", clip(resp, 300))
	}
	respFrames := payload.Session.PromptToVideoGeneratorContent.Frames
	uploads := make([]string, 0, len(respFrames))
	for _, f := range respFrames {
		if u := strings.TrimSpace(f.URL); u != "" {
			uploads = append(uploads, u)
		}
	}
	if len(uploads) != len(refs) {
		return "", nil, fmt.Errorf("creativefabrica initiate returned %d upload urls for %d refs", len(uploads), len(refs))
	}
	return payload.Session.ID, uploads, nil
}

// pollSession polls ListSessions until the session is COMPLETED / FAILED and
// returns previewMediaUrl on success.
func (c *Client) pollSession(ctx context.Context, cookie, token, sessionID string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"serviceType": videoServiceType,
		"pagination":  map[string]any{"take": 100},
		"surface":     "SURFACE_STUDIO",
	})
	deadline := time.Now().Add(pollTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("creativefabrica generation timed out after %v", pollTimeout)
		}
		resp, err := c.postConnect(ctx, cookie, token, listSessionsPath, reqBody, false)
		if err != nil {
			return "", err
		}
		var payload struct {
			Sessions []map[string]any `json:"sessions"`
		}
		if err := json.Unmarshal(resp, &payload); err != nil {
			return "", fmt.Errorf("creativefabrica bad list response: %s", clip(resp, 300))
		}
		for _, s := range payload.Sessions {
			id, _ := s["id"].(string)
			if id != sessionID {
				continue
			}
			status, _ := s["status"].(string)
			switch status {
			case "SESSION_STATUS_COMPLETED":
				if u := strings.TrimSpace(stringValue(s["previewMediaUrl"])); u != "" {
					return u, nil
				}
				return "", fmt.Errorf("creativefabrica session completed without preview url")
			case "SESSION_STATUS_FAILED", "SESSION_STATUS_CANCELLED", "SESSION_STATUS_ERROR":
				detail := ""
				for _, k := range []string{"errorMessage", "failureReason", "error", "message"} {
					if v, ok := s[k]; ok {
						if d := strings.TrimSpace(stringValue(v)); d != "" {
							detail = d
							break
						}
					}
				}
				return "", fmt.Errorf("creativefabrica session %s%s", status, withDetail(detail))
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// putS3 uploads a reference image to the presigned URL. S3 doesn't care about
// TLS fingerprinting, so a plain client is fine here.
func (c *Client) putS3(ctx context.Context, presigned string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presigned, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", defaultUA())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("creativefabrica s3 upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creativefabrica s3 upload failed: %d %s", resp.StatusCode, clip(body, 200))
	}
	return nil
}

// download fetches the finished MP4 from the public video-v2 URL.
func (c *Client) download(parent context.Context, videoURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", defaultUA())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: download: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("creativefabrica download failed: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrTemporaryUpstream, err)
	}
	return data, nil
}

// postGraphQL calls a graphql-gw endpoint with the raw JSON body. Authenticated
// by cookie (balance) and optionally the JWT too. Direct connection (no proxy).
func (c *Client) postGraphQL(ctx context.Context, cookie, token, path, body string) ([]byte, error) {
	return c.postJSON(ctx, graphQLHost+path, token, cookie, []byte(body), false, true, false)
}

// postConnect calls a Connect unary endpoint on the media-matrix service. The
// Connect-Protocol-Version header marks the POST as a Connect RPC (distinct from
// a plain JSON REST POST) — the studio browser always sends it. Only the
// InitiateSession call (下单) egresses through the proxy; polling runs direct.
func (c *Client) postConnect(ctx context.Context, cookie, token, path string, body []byte, useProxy bool) ([]byte, error) {
	return c.postJSON(ctx, mediaMatrixHost+path, token, cookie, body, true, false, useProxy)
}

func (c *Client) postJSON(ctx context.Context, url, token, cookie string, body []byte, connect, graphql, useProxy bool) ([]byte, error) {
	sess, err := c.newTLSClient(useProxy)
	if err != nil {
		return nil, err
	}
	req, err := fhttp.NewRequest(fhttp.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = fhttp.Header{
		"content-type": {"application/json"},
		"accept":       {"*/*"},
		"origin":       {origin},
		"referer":      {origin + "/"},
		"user-agent":   {defaultUA()},
	}
	if graphql {
		req.Header.Set("accept", "application/json, multipart/mixed")
	}
	if connect {
		req.Header.Set("connect-protocol-version", "1")
	}
	if cookie != "" {
		req.Header.Set("cookie", cookie)
	}
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	resp, err := sess.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("creativefabrica request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, fmt.Errorf("%w (%d: %s)", ErrAuth, resp.StatusCode, clip(data, 300))
	case isPaymentRequired(resp.StatusCode, string(data)):
		return nil, fmt.Errorf("%w (%d: %s)", ErrPaymentRequired, resp.StatusCode, clip(data, 300))
	case resp.StatusCode == 429:
		return nil, fmt.Errorf("%w (429)", ErrRateLimited)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w (%d: %s)", ErrDeadUpstream, resp.StatusCode, clip(data, 300))
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("creativefabrica %d: %s", resp.StatusCode, clip(data, 300))
	}
	return data, nil
}

func resolutionEnum(res string) string {
	switch strings.ToLower(strings.TrimSpace(res)) {
	case "720p":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_RESOLUTION_720P"
	case "1080p":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_RESOLUTION_1080P"
	default:
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_RESOLUTION_720P"
	}
}

func aspectRatioEnum(ratio string) string {
	switch strings.ReplaceAll(strings.TrimSpace(ratio), " ", "") {
	case "16:9":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_16_9"
	case "9:16":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_9_16"
	case "1:1":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_1_1"
	case "4:3":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_4_3"
	case "3:4":
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_3_4"
	default:
		return "PROMPT_TO_VIDEO_GENERATOR_CONTENT_ASPECT_RATIO_16_9"
	}
}

// randomFileName mimics the studio's client-generated reference name
// ("_<base36-ish id>_<n>.png"). The value only needs to be unique per upload.
func randomFileName(n int) string {
	return "_" + randomID() + "_" + fmt.Sprintf("%d", n) + ".png"
}

func randomID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 26)
	for i := range b {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// TLS client plumbing: a Chrome-fingerprinted client so the Cloudflare front of
// the Connect endpoints sees a plausible browser handshake.
var fingerprints = []profiles.ClientProfile{
	profiles.Chrome_146,
	profiles.Chrome_144,
	profiles.Chrome_133,
	profiles.Chrome_131,
}

type tlsSession struct {
	client tlsclient.HttpClient
}

func (c *Client) newTLSClient(useProxy bool) (*tlsSession, error) {
	idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(fingerprints))))
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(60),
		tlsclient.WithClientProfile(fingerprints[idx.Int64()]),
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
	return &tlsSession{client: client}, nil
}

func defaultUA() string {
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
}

func clip(v []byte, n int) string {
	s := strings.TrimSpace(string(v))
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// stringValue coerces a decoded JSON value to its string form.
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// withDetail appends a failure detail to an error message when present.
func withDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}


