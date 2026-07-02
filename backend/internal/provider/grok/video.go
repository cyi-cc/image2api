package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
)

// assetBase is where generated media artifacts live (the stream returns a
// path like "users/<uid>/generated/<id>/generated_video.mp4").
const assetBase = "https://assets.grok.com/"

var videoURLRe = regexp.MustCompile(`"videoUrl":"([^"]+)"`)

// GenerateVideo runs grok's imagine video pipeline:
//  1. POST /rest/media/post/create  -> a media post id (parentPostId)
//  2. POST /rest/app-chat/conversations/new with modelName "imagine-video-gen"
//     and a videoGenModelConfig referencing that post id; the streaming response
//     reports progress and, at completion, the artifact videoUrl.
//
// frames (optional, up to the model's max) enable image-to-video: each image is
// uploaded to grok and referenced as an imageReference. aspectRatio is passed
// through ("9:16" etc.); resolution is the tier ("720p"); seconds is the clip
// length (6 or 10). When downloadResult is false, returns nil bytes and the
// artifact URL in meta["video_url"]; otherwise downloads the mp4.
func (c *Client) GenerateVideo(ctx context.Context, token, prompt, aspectRatio, resolution string, seconds int, frames [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, nil, ErrAuth
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, nil, fmt.Errorf("grok: prompt required")
	}
	if strings.TrimSpace(aspectRatio) == "" {
		aspectRatio = "16:9"
	}
	if strings.TrimSpace(resolution) == "" {
		resolution = "720p"
	}
	if seconds != 6 && seconds != 10 && seconds != 15 {
		seconds = 10
	}

	client, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}

	// Image-to-video: upload each reference frame and collect its asset URL.
	var imageRefs []string
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		url, upErr := c.uploadImage(ctx, client, token, f)
		if upErr != nil {
			return nil, nil, upErr
		}
		imageRefs = append(imageRefs, url)
	}

	// grok occasionally accepts the conversation (HTTP 200) but closes the stream
	// after only the conversation object — no progress events, no videoUrl. This
	// is transient, so retry the whole create-post + stream a few times before
	// giving up. Real out-of-credits / auth errors are returned immediately.
	const maxAttempts = 3
	var (
		postID   string
		artifact string
		lastBody string
		lastErr  error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		pid, cpErr := c.createPost(ctx, client, token, prompt)
		if cpErr != nil {
			lastErr = cpErr
			continue
		}
		postID = pid

		videoCfg := map[string]any{
			"parentPostId":       postID,
			"aspectRatio":        aspectRatio,
			"videoLength":        seconds,
			"resolutionName":     resolution,
			"isReferenceToVideo": len(imageRefs) > 0,
		}
		if len(imageRefs) > 0 {
			videoCfg["imageReferences"] = imageRefs
		}
		payload := map[string]any{
			"temporary":        true,
			"modelName":        "imagine-video-gen",
			"message":          prompt + " --mode=custom",
			"enableSideBySide": true,
			"responseMetadata": map[string]any{
				"modelConfigOverride": map[string]any{
					"modelMap": map[string]any{"videoGenModelConfig": videoCfg},
				},
			},
		}

		body, psErr := c.postStream(ctx, client, token, "/rest/app-chat/conversations/new", payload)
		if psErr != nil {
			// Transient HTTP/2 stream resets etc. — retry.
			lastErr = psErr
			continue
		}
		if dir := strings.TrimSpace(os.Getenv("GROK_DUMP")); dir != "" {
			_ = os.WriteFile(dir+"/grok_body_"+postID+".txt", []byte(body), 0o644)
		}
		// Out-of-credits surfaces as a stream error (HTTP is still 200) — not retryable.
		if strings.Contains(body, "usagePoolExhausted") || strings.Contains(body, "media generation credits") {
			return nil, nil, fmt.Errorf("%w: media generation credits exhausted", ErrQuotaExhausted)
		}
		// The artifact path appears as "videoUrl":"users/.../generated_video.mp4".
		lastBody = body
		artifact = ""
		for _, m := range videoURLRe.FindAllStringSubmatch(body, -1) {
			if v := strings.TrimSpace(m[1]); v != "" {
				artifact = v // keep the last (progress=100) one
			}
		}
		if artifact != "" {
			break
		}
		// No artifact: grok closed the stream early. Retry.
		lastErr = nil
	}
	if artifact == "" {
		if lastErr != nil {
			return nil, nil, lastErr
		}
		return nil, nil, fmt.Errorf("%w: no video artifact in response: %s", ErrTemporaryUpstream, clip([]byte(lastBody), 200))
	}
	fullURL := artifact
	if !strings.HasPrefix(fullURL, "http") {
		fullURL = assetBase + strings.TrimPrefix(artifact, "/")
	}

	meta := map[string]any{
		"provider":  "grok",
		"post_id":   postID,
		"video_url": fullURL,
	}
	if !downloadResult {
		return nil, meta, nil
	}
	data, err := c.download(ctx, client, token, fullURL)
	if err != nil {
		return nil, nil, err
	}
	return data, meta, nil
}

// uploadImage uploads one reference frame via /rest/app-chat/upload-file (JSON
// with base64 content) and returns its asset content URL for imageReferences.
func (c *Client) uploadImage(ctx context.Context, client tlsclient.HttpClient, token string, img []byte) (string, error) {
	res, err := c.postJSON(ctx, client, token, "/rest/app-chat/upload-file", map[string]any{
		"fileName":     "ref.png",
		"fileMimeType": "image/png",
		"content":      base64.StdEncoding.EncodeToString(img),
	})
	if err != nil {
		return "", err
	}
	fileURI := strings.TrimSpace(stringValue(res["fileUri"]))
	if fileURI == "" {
		return "", fmt.Errorf("%w: upload missing fileUri", ErrTemporaryUpstream)
	}
	if strings.HasPrefix(fileURI, "http") {
		return fileURI, nil
	}
	return assetBase + strings.TrimPrefix(fileURI, "/"), nil
}

// createPost registers a video media post and returns its id (parentPostId).
func (c *Client) createPost(ctx context.Context, client tlsclient.HttpClient, token, prompt string) (string, error) {
	res, err := c.postJSON(ctx, client, token, "/rest/media/post/create", map[string]any{
		"mediaType": "MEDIA_POST_TYPE_VIDEO",
		"prompt":    prompt,
	})
	if err != nil {
		return "", err
	}
	post, _ := res["post"].(map[string]any)
	id := strings.TrimSpace(stringValue(post["id"]))
	if id == "" {
		return "", fmt.Errorf("%w: media post missing id", ErrTemporaryUpstream)
	}
	return id, nil
}

// postJSON does an authed JSON POST and parses a single JSON object response.
func (c *Client) postJSON(ctx context.Context, client tlsclient.HttpClient, token, path string, body any) (map[string]any, error) {
	raw, status, err := c.doPost(ctx, client, token, path, body)
	if err != nil {
		return nil, err
	}
	if e := mapStatus(path, status, raw); e != nil {
		return nil, e
	}
	var out map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s non-json: %s", ErrTemporaryUpstream, path, clip(raw, 120))
	}
	return out, nil
}

// postStream does an authed JSON POST and returns the full (streamed) text body.
func (c *Client) postStream(ctx context.Context, client tlsclient.HttpClient, token, path string, body any) (string, error) {
	raw, status, err := c.doPost(ctx, client, token, path, body)
	if err != nil {
		return "", err
	}
	if e := mapStatus(path, status, raw); e != nil {
		return "", e
	}
	return string(raw), nil
}

func (c *Client) doPost(ctx context.Context, client tlsclient.HttpClient, token, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(http.MethodPost, apiBase+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req = req.WithContext(ctx)
	c.applyHeaders(req, token, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// OpenAsset streams a grok asset (e.g. a generated video) authenticated with the
// account token — used by the async /v1/videos /content proxy. The caller MUST
// close the returned ReadCloser.
func (c *Client) OpenAsset(ctx context.Context, token, url string) (io.ReadCloser, string, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, "", ErrAuth
	}
	client, err := c.newTLSClient()
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"user-agent": {userAgent},
		"referer":    {origin + "/"},
		"cookie":     {"sso=" + token + "; sso-rw=" + token},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, "", fmt.Errorf("%w: asset %d", ErrAuth, resp.StatusCode)
		}
		return nil, "", fmt.Errorf("%w: asset %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "video/mp4"
	}
	return resp.Body, ct, nil
}

func (c *Client) download(ctx context.Context, client tlsclient.HttpClient, token, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"user-agent": {userAgent},
		"referer":    {origin + "/"},
		"cookie":     {"sso=" + token + "; sso-rw=" + token},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: download %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty video download", ErrTemporaryUpstream)
	}
	return data, nil
}

// mapStatus maps an HTTP status to the shared provider error sentinels.
func mapStatus(path string, status int, raw []byte) error {
	switch {
	case status == 200:
		return nil
	case status == 403 && strings.Contains(strings.ToLower(string(raw)), "anti-bot"):
		// grok bot-detection (proxy/TLS fingerprint), NOT a dead token — transient,
		// so a good account isn't killed by an IP/anti-bot hiccup.
		return fmt.Errorf("%w: %s 403 %s", ErrTemporaryUpstream, path, clip(raw, 160))
	case status == 401 || status == 403:
		return fmt.Errorf("%w: %s %d %s", ErrAuth, path, status, clip(raw, 160))
	case status == 429:
		// 429 is grok RATE-LIMITING ("Too many requests") — a transient error that
		// must NOT kill the account. Only a body that names a credit/usage-pool
		// exhaustion is a real quota wall.
		if isCreditError(string(raw)) {
			return fmt.Errorf("%w: %s 429 %s", ErrQuotaExhausted, path, clip(raw, 160))
		}
		return fmt.Errorf("%w: %s 429 %s", ErrTemporaryUpstream, path, clip(raw, 160))
	case status >= 500:
		return fmt.Errorf("%w: %s %d %s", ErrTemporaryUpstream, path, status, clip(raw, 160))
	default:
		if isCreditError(string(raw)) {
			return fmt.Errorf("%w: %s", ErrQuotaExhausted, clip(raw, 160))
		}
		return fmt.Errorf("grok: %s %d %s", path, status, clip(raw, 160))
	}
}

func isCreditError(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "usagepoolexhausted") || strings.Contains(s, "credit") || strings.Contains(s, "insufficient") || strings.Contains(s, "quota")
}
