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

type videoModelProfile struct {
	UpstreamID       string
	MinDuration      int
	MaxDuration      int
	MaxReferences    int
	SupportsEndFrame bool
	SupportsAudio    bool
}

// GenerateVideo uses the same browser-cookie -> short-lived bearer flow as the
// Leonardo image provider. Video models are submitted through Leonardo's
// universal Generate mutation, then polled until the MP4 becomes available.
func (c *Client) GenerateVideo(ctx context.Context, cookie, model, prompt, aspectRatio, resolution string, durationSeconds int, referenceMode string, refImages [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
	sess, err := c.GetSession(ctx, cookie)
	if err != nil {
		return nil, nil, err
	}
	profile, ok := leonardoVideoProfile(model)
	if !ok {
		return nil, nil, errors.New("leonardo: unsupported video model")
	}
	if durationSeconds < profile.MinDuration || durationSeconds > profile.MaxDuration {
		return nil, nil, fmt.Errorf("leonardo: %s duration must be %d-%d seconds", profile.UpstreamID, profile.MinDuration, profile.MaxDuration)
	}

	width, height := videoDimensions(aspectRatio, resolution)
	parameters := map[string]any{
		"prompt":   prompt,
		"quantity": 1,
		"width":    width,
		"height":   height,
		"duration": durationSeconds,
	}
	if profile.SupportsAudio {
		parameters["motion_has_audio"] = true
	}

	if len(refImages) > 0 {
		refLimit := profile.MaxReferences
		if isFrameReferenceMode(referenceMode) {
			refLimit = 1
			if profile.SupportsEndFrame {
				refLimit = 2
			}
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
			parameters["guidances"] = videoGuidances(uploadIDs, referenceMode, profile)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"operationName": "Generate",
		"query":         mGenerate,
		"variables": map[string]any{
			"request": map[string]any{
				"model": profile.UpstreamID,
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

func leonardoVideoProfile(model string) (videoModelProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "leonardo-seedance-2.0", "seedance-2.0":
		return videoModelProfile{UpstreamID: "seedance-2.0", MinDuration: 4, MaxDuration: 15, MaxReferences: 2, SupportsEndFrame: true, SupportsAudio: true}, true
	case "leonardo-seedance-fast", "leonardo-seedance-2.0-fast", "seedance-fast", "seedance-2.0-fast":
		return videoModelProfile{UpstreamID: "seedance-2.0-fast", MinDuration: 4, MaxDuration: 15, MaxReferences: 2, SupportsEndFrame: true, SupportsAudio: true}, true
	case "leonardo-seedance-mini", "leonardo-seedance-2.0-mini", "seedance-mini", "seedance-2.0-mini":
		return videoModelProfile{UpstreamID: "seedance-2.0-mini", MinDuration: 4, MaxDuration: 15, MaxReferences: 2, SupportsEndFrame: true, SupportsAudio: true}, true
	case "leonardo-minimax-h3", "minimax-h3", "minimax_h3":
		// MiniMax H3 was added to the Leonardo web app before its REST guide.
		// Keep the observed/official model name in this one mapping so a later
		// upstream naming change does not leak through the public API.
		return videoModelProfile{UpstreamID: "minimax-h3", MinDuration: 5, MaxDuration: 15, MaxReferences: 2, SupportsEndFrame: true, SupportsAudio: true}, true
	case "leonardo-happy-horse-1.1", "leonardo-happyhorse-1.1", "happy-horse-1.1", "happyhorse-1.1":
		// Verified against Leonardo's Happy Horse 1.1 REST guide: 3-15s,
		// maximum 9 image references, one start frame, native audio.
		return videoModelProfile{UpstreamID: "happy-horse-1.1", MinDuration: 3, MaxDuration: 15, MaxReferences: 9, SupportsAudio: true}, true
	default:
		return videoModelProfile{}, false
	}
}

func videoDimensions(aspectRatio, resolution string) (int, int) {
	ratio := strings.TrimSpace(strings.ReplaceAll(strings.ToLower(aspectRatio), "x", ":"))
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "2k", "1440p":
		// MiniMax H3's native 2K family, including its 21:9 option.
		switch ratio {
		case "21:9":
			return 2560, 1080
		case "9:16":
			return 1440, 2560
		case "4:3":
			return 1920, 1440
		case "3:4":
			return 1440, 1920
		case "1:1":
			return 1920, 1920
		default:
			return 2560, 1440
		}
	case "1080p":
		// Leonardo's documented Happy Horse 1.1 dimension table.
		switch ratio {
		case "9:16":
			return 1080, 1920
		case "4:3":
			return 1662, 1248
		case "3:4":
			return 1248, 1662
		case "1:1":
			return 1440, 1440
		default:
			return 1920, 1080
		}
	default: // 720p
		switch ratio {
		case "9:16":
			return 720, 1280
		case "4:3":
			return 1108, 832
		case "3:4":
			return 832, 1108
		case "1:1":
			return 960, 960
		default:
			return 1280, 720
		}
	}
}

func isFrameReferenceMode(referenceMode string) bool {
	return strings.EqualFold(strings.TrimSpace(referenceMode), "frame")
}

func videoGuidances(uploadIDs []string, referenceMode string, profile videoModelProfile) map[string]any {
	if isFrameReferenceMode(referenceMode) {
		out := map[string]any{}
		if len(uploadIDs) > 0 {
			out["start_frame"] = []any{map[string]any{"image": map[string]any{"id": uploadIDs[0], "type": "UPLOADED"}}}
		}
		if profile.SupportsEndFrame && len(uploadIDs) > 1 {
			out["end_frame"] = []any{map[string]any{"image": map[string]any{"id": uploadIDs[1], "type": "UPLOADED"}}}
		}
		return out
	}
	limit := min(len(uploadIDs), profile.MaxReferences)
	refs := make([]any, 0, limit)
	for idx, id := range uploadIDs[:limit] {
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
