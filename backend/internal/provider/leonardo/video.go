package leonardo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// qGenerationVideos polls the generation record and reads Leonardo's generated
// motion asset URL. The same field is returned by its REST generation schema.
const qGenerationVideos = `query GenerationVideos($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id
    status
    generated_images {
      id
      motionMP4URL
      __typename
    }
    __typename
  }
}`

// GenerateVideo uses the same browser-cookie -> short-lived bearer flow as the
// Leonardo image provider. Seedance is submitted through Leonardo's universal
// Generate mutation, then polled until the generated MP4 becomes available.
func (c *Client) GenerateVideo(ctx context.Context, cookie, model, prompt, aspectRatio, resolution string, durationSeconds int, referenceMode string, refImages [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
	sess, err := c.GetSession(ctx, cookie)
	if err != nil {
		return nil, nil, err
	}
	model = normalizeSeedanceModel(model)
	if model == "" {
		return nil, nil, errors.New("leonardo: unsupported seedance model")
	}
	if durationSeconds < 4 || durationSeconds > 15 {
		return nil, nil, errors.New("leonardo: seedance duration must be 4-15 seconds")
	}

	width, height := seedanceDimensions(aspectRatio, resolution)
	parameters := map[string]any{
		"prompt":           prompt,
		"quantity":         1,
		"width":            width,
		"height":           height,
		"duration":         durationSeconds,
		"motion_has_audio": true,
	}

	if len(refImages) > 0 {
		refLimit := 4
		if strings.EqualFold(strings.TrimSpace(referenceMode), "frame") {
			refLimit = 2
		}
		refs := refImages[:min(len(refImages), refLimit)]
		uploadIDs := make([]string, 0, len(refs))
		for _, img := range refs {
			if len(img) == 0 {
				continue
			}
			id, upErr := c.uploadInitImage(ctx, sess.AccessToken, img)
			if upErr != nil {
				return nil, nil, upErr
			}
			uploadIDs = append(uploadIDs, id)
		}
		if len(uploadIDs) > 0 {
			parameters["guidances"] = seedanceGuidances(uploadIDs, referenceMode)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"operationName": "Generate",
		"query":         mGenerate,
		"variables": map[string]any{
			"request": map[string]any{
				"model": model,
				// Match Leonardo's existing cookie-backed image flow. Free/web
				// accounts may require community-visible generations.
				"public":     true,
				"parameters": parameters,
			},
		},
	})
	body, status, err := c.graphql(ctx, sess.AccessToken, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: submit: %s", ErrTemporaryUpstream, err.Error())
	}
	if status == 401 || status == 403 {
		return nil, nil, ErrAuth
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("%w: generate video http %d: %s", ErrTemporaryUpstream, status, clip(body, 200))
	}
	if e := graphqlError(body); e != nil {
		return nil, nil, e
	}
	var submitted struct {
		Data struct {
			Generate struct {
				GenerationID string `json:"generationId"`
			} `json:"generate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &submitted); err != nil {
		return nil, nil, fmt.Errorf("%w: generate video non-json", ErrTemporaryUpstream)
	}
	genID := strings.TrimSpace(submitted.Data.Generate.GenerationID)
	if genID == "" {
		return nil, nil, fmt.Errorf("%w: no generationId: %s", ErrTemporaryUpstream, clip(body, 200))
	}

	videoURL, err := c.pollVideo(ctx, sess.AccessToken, genID)
	if err != nil {
		return nil, nil, err
	}
	info := map[string]any{
		"generation_id": genID,
		"video_url":     videoURL,
		"user_id":       sess.UserID,
	}
	if !downloadResult {
		return nil, info, nil
	}
	data, err := c.downloadVideo(ctx, videoURL)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func normalizeSeedanceModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "leonardo-seedance-2.0", "seedance-2.0":
		return "seedance-2.0"
	case "leonardo-seedance-fast", "leonardo-seedance-2.0-fast", "seedance-fast", "seedance-2.0-fast":
		return "seedance-2.0-fast"
	case "leonardo-seedance-mini", "leonardo-seedance-2.0-mini", "seedance-mini", "seedance-2.0-mini":
		return "seedance-2.0-mini"
	default:
		return ""
	}
}

func seedanceDimensions(aspectRatio, resolution string) (int, int) {
	long, short := 1280, 720
	if strings.EqualFold(strings.TrimSpace(resolution), "1080p") {
		long, short = 1920, 1080
	}
	switch strings.TrimSpace(strings.ReplaceAll(aspectRatio, "x", ":")) {
	case "9:16":
		return short, long
	case "1:1":
		return short, short
	default:
		return long, short
	}
}

func seedanceGuidances(uploadIDs []string, referenceMode string) map[string]any {
	if strings.EqualFold(strings.TrimSpace(referenceMode), "frame") {
		out := map[string]any{}
		if len(uploadIDs) > 0 {
			out["start_frame"] = []any{map[string]any{"image": map[string]any{"id": uploadIDs[0], "type": "UPLOADED"}}}
		}
		if len(uploadIDs) > 1 {
			out["end_frame"] = []any{map[string]any{"image": map[string]any{"id": uploadIDs[1], "type": "UPLOADED"}}}
		}
		return out
	}
	refs := make([]any, 0, min(len(uploadIDs), 4))
	for idx, id := range uploadIDs[:min(len(uploadIDs), 4)] {
		refs = append(refs, map[string]any{
			"image":    map[string]any{"id": id, "type": "UPLOADED"},
			"strength": "MID",
			"order":    idx,
		})
	}
	return map[string]any{"image_reference": refs}
}

func (c *Client) pollVideo(ctx context.Context, accessToken, genID string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"operationName": "GenerationVideos",
		"query":         qGenerationVideos,
		"variables": map[string]any{
			"where": map[string]any{"id": map[string]any{"_in": []string{genID}}},
		},
	})
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(8 * time.Minute)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl.Add(-60 * time.Second)
	}

	for {
		body, status, err := c.graphqlP(ctx, accessToken, payload, false)
		if err != nil {
			return "", fmt.Errorf("%w: poll video: %s", ErrTemporaryUpstream, err.Error())
		}
		if status == 401 || status == 403 {
			return "", ErrAuth
		}
		if status == 200 {
			if e := graphqlError(body); e != nil {
				// Schema versions have alternated between motionMP4URL and the
				// snake_case REST field. Retry with the latter when the former is
				// rejected so either deployed Leonardo schema remains usable.
				if strings.Contains(strings.ToLower(e.Error()), "motionmp4url") {
					return c.pollVideoRESTField(ctx, accessToken, genID, deadline)
				}
				return "", e
			}
			var result struct {
				Data struct {
					Generations []struct {
						Status          string `json:"status"`
						GeneratedImages []struct {
							MotionMP4URL string `json:"motionMP4URL"`
						} `json:"generated_images"`
					} `json:"generations"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &result); err == nil && len(result.Data.Generations) > 0 {
				generation := result.Data.Generations[0]
				switch strings.ToUpper(strings.TrimSpace(generation.Status)) {
				case "COMPLETE", "COMPLETED":
					for _, output := range generation.GeneratedImages {
						if u := firstVideoURL(output.MotionMP4URL); u != "" {
							return u, nil
						}
					}
					return "", fmt.Errorf("%w: complete but no video url", ErrTemporaryUpstream)
				case "FAILED", "ERROR", "CANCELED", "CANCELLED":
					return "", fmt.Errorf("%w: video generation failed", ErrTemporaryUpstream)
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: video generation timed out", ErrTemporaryUpstream)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) pollVideoRESTField(ctx context.Context, accessToken, genID string, deadline time.Time) (string, error) {
	const query = `query GenerationVideos($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id
    status
    generated_images {
      id
      motion_mp4_url
      __typename
    }
    __typename
  }
}`
	payload, _ := json.Marshal(map[string]any{
		"operationName": "GenerationVideos",
		"query":         query,
		"variables": map[string]any{
			"where": map[string]any{"id": map[string]any{"_in": []string{genID}}},
		},
	})
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		body, status, err := c.graphqlP(ctx, accessToken, payload, false)
		if err != nil {
			return "", fmt.Errorf("%w: poll video: %s", ErrTemporaryUpstream, err.Error())
		}
		if status == 401 || status == 403 {
			return "", ErrAuth
		}
		if status == 200 {
			if e := graphqlError(body); e != nil {
				return "", e
			}
			var result struct {
				Data struct {
					Generations []struct {
						Status          string `json:"status"`
						GeneratedImages []struct {
							MotionMP4URL string `json:"motion_mp4_url"`
						} `json:"generated_images"`
					} `json:"generations"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &result); err == nil && len(result.Data.Generations) > 0 {
				generation := result.Data.Generations[0]
				switch strings.ToUpper(strings.TrimSpace(generation.Status)) {
				case "COMPLETE", "COMPLETED":
					for _, output := range generation.GeneratedImages {
						if u := firstVideoURL(output.MotionMP4URL); u != "" {
							return u, nil
						}
					}
					return "", fmt.Errorf("%w: complete but no video url", ErrTemporaryUpstream)
				case "FAILED", "ERROR", "CANCELED", "CANCELLED":
					return "", fmt.Errorf("%w: video generation failed", ErrTemporaryUpstream)
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: video generation timed out", ErrTemporaryUpstream)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func firstVideoURL(values ...string) string {
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if parsed, err := url.Parse(raw); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return raw
		}
	}
	return ""
}

func (c *Client) downloadVideo(ctx context.Context, videoURL string) ([]byte, error) {
	if firstVideoURL(videoURL) == "" {
		return nil, errors.New("leonardo: invalid video url")
	}
	client, err := c.newDirectTLSClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":     {"video/mp4,video/*,*/*;q=0.8"},
		"user-agent": {userAgent},
		"referer":    {appBase + "/"},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: video download: %s", ErrTemporaryUpstream, err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: video download http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	return body, nil
}
