// Package custom implements a generic OpenAI-compatible upstream client. A
// "custom" model forwards generation to any OpenAI-compatible API: the upstream
// base_url + api_key live on a custom account (pool="custom"), the upstream model
// name on the model config (UpstreamModel). Calls go DIRECT (no tls-client, no
// proxy) — the upstream is a normal API with no anti-bot.
package custom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrAuth              = errors.New("custom upstream auth failed")
	ErrQuotaExhausted    = errors.New("custom upstream quota exhausted")
	ErrTemporaryUpstream = errors.New("custom upstream temporary error")
)

type Client struct{}

func NewClient() *Client { return &Client{} }

// sanitizeErr strips the upstream URL/host from a network error so a user's
// private upstream URL never leaks into the event log / API response.
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "Client.Timeout"), strings.Contains(s, "timeout"):
		return "request timeout"
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "no such host"), strings.Contains(s, "dial tcp"), strings.Contains(s, "lookup "):
		return "cannot reach upstream"
	case strings.Contains(s, "tls"), strings.Contains(s, "TLS"), strings.Contains(s, "certificate"):
		return "TLS error"
	case strings.Contains(s, "EOF"), strings.Contains(s, "reset by peer"), strings.Contains(s, "broken pipe"):
		return "connection reset"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return strings.ToLower(ue.Op) + " upstream failed"
	}
	return "upstream request failed"
}

func httpClient() *http.Client { return &http.Client{Timeout: 10 * time.Minute} }

// GenerateImage calls the upstream OpenAI image API. With reference images it
// uses /v1/images/edits (multipart); otherwise /v1/images/generations. Returns
// the raw image bytes (decoded from b64_json, or downloaded from url).
func (c *Client) GenerateImage(ctx context.Context, baseURL, apiKey, model, prompt, size, quality string, refs [][]byte) ([]byte, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || apiKey == "" {
		return nil, ErrAuth
	}
	var req *http.Request
	var err error
	if len(refs) > 0 {
		body := &bytes.Buffer{}
		w := multipart.NewWriter(body)
		_ = w.WriteField("model", model)
		_ = w.WriteField("prompt", prompt)
		if size != "" {
			_ = w.WriteField("size", size)
		}
		for i, r := range refs {
			fw, e := w.CreateFormFile("image[]", fmt.Sprintf("ref_%d.png", i+1))
			if e != nil {
				return nil, e
			}
			_, _ = fw.Write(r)
		}
		_ = w.Close()
		req, err = http.NewRequest(http.MethodPost, baseURL+"/v1/images/edits", body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
	} else {
		payload := map[string]any{"model": model, "prompt": prompt, "n": 1}
		if size != "" {
			payload["size"] = size
		}
		if quality != "" {
			payload["quality"] = quality
		}
		raw, _ := json.Marshal(payload)
		req, err = http.NewRequest(http.MethodPost, baseURL+"/v1/images/generations", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, sanitizeErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := mapStatus(resp.StatusCode, body); e != nil {
		return nil, e
	}
	return imageBytesFromResponse(ctx, body)
}

// GenerateVideo drives the upstream Sora-style async video API:
// POST /v1/videos → poll GET /v1/videos/{id} → GET /v1/videos/{id}/content.
// When downloadResult is false it returns the upstream content URL instead.
func (c *Client) GenerateVideo(ctx context.Context, baseURL, apiKey, model, prompt, size string, seconds int, downloadResult bool) ([]byte, string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || apiKey == "" {
		return nil, "", ErrAuth
	}
	payload := map[string]any{"model": model, "prompt": prompt}
	if size != "" {
		payload["size"] = size
	}
	if seconds > 0 {
		payload["seconds"] = fmt.Sprintf("%d", seconds)
	}
	raw, _ := json.Marshal(payload)
	created, err := c.doJSON(ctx, http.MethodPost, baseURL+"/v1/videos", apiKey, raw)
	if err != nil {
		return nil, "", err
	}
	jobID := strings.TrimSpace(stringValue(created["id"]))
	if jobID == "" {
		return nil, "", fmt.Errorf("%w: video create missing id", ErrTemporaryUpstream)
	}
	// Poll until terminal.
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		job, err := c.doJSON(ctx, http.MethodGet, baseURL+"/v1/videos/"+jobID, apiKey, nil)
		if err != nil {
			if errors.Is(err, ErrTemporaryUpstream) {
				if sleepCtx(ctx, 5*time.Second) != nil {
					return nil, "", ctx.Err()
				}
				continue
			}
			return nil, "", err
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(job["status"]))) {
		case "completed", "succeeded", "success":
			contentURL := baseURL + "/v1/videos/" + jobID + "/content"
			if !downloadResult {
				return nil, contentURL, nil
			}
			data, err := c.download(ctx, contentURL, apiKey)
			if err != nil {
				return nil, "", err
			}
			return data, contentURL, nil
		case "failed", "error", "canceled", "cancelled":
			reason := stringValue(job["error"])
			if isCreditError(reason) {
				return nil, "", fmt.Errorf("%w: %s", ErrTemporaryUpstream, clip([]byte(reason), 160))
			}
			return nil, "", fmt.Errorf("custom: video %s", clip([]byte(reason), 160))
		}
		if sleepCtx(ctx, 5*time.Second) != nil {
			return nil, "", ctx.Err()
		}
	}
}

func (c *Client) doJSON(ctx context.Context, method, url, apiKey string, body []byte) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, sanitizeErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if e := mapStatus(resp.StatusCode, raw); e != nil {
		return nil, e
	}
	var out map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: non-json: %s", ErrTemporaryUpstream, clip(raw, 120))
	}
	return out, nil
}

func (c *Client) download(ctx context.Context, url, apiKey string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, sanitizeErr(err))
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
		return nil, fmt.Errorf("%w: empty download", ErrTemporaryUpstream)
	}
	return data, nil
}

// imageBytesFromResponse extracts image bytes from an OpenAI images response:
// data[0].b64_json (preferred) or data[0].url (downloaded).
func imageBytesFromResponse(ctx context.Context, body []byte) ([]byte, error) {
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Data) == 0 {
		return nil, fmt.Errorf("%w: bad image response: %s", ErrTemporaryUpstream, clip(body, 160))
	}
	d := out.Data[0]
	if d.B64JSON != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(d.B64JSON))
		if err != nil {
			return nil, fmt.Errorf("%w: bad b64: %v", ErrTemporaryUpstream, err)
		}
		return raw, nil
	}
	if d.URL != "" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
		resp, err := httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, sanitizeErr(err))
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}
	return nil, fmt.Errorf("%w: image response had no b64/url", ErrTemporaryUpstream)
}

func mapStatus(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 401 || status == 403:
		return fmt.Errorf("%w: %d %s", ErrAuth, status, clip(body, 160))
	case status == 429:
		// Custom upstreams have NO "quota exhausted" lock — a 429 is just rate
		// limiting, treated as a temporary error (fail over, account stays active).
		return fmt.Errorf("%w: 429 %s", ErrTemporaryUpstream, clip(body, 160))
	case status >= 500:
		return fmt.Errorf("%w: %d %s", ErrTemporaryUpstream, status, clip(body, 160))
	default:
		if isCreditError(string(body)) {
			return fmt.Errorf("%w: %s", ErrTemporaryUpstream, clip(body, 160))
		}
		return fmt.Errorf("custom: %d %s", status, clip(body, 160))
	}
}

func isCreditError(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "insufficient") || strings.Contains(s, "quota") ||
		strings.Contains(s, "credit") || strings.Contains(s, "balance")
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

func clip(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
