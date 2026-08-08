package leonardo

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// qGenerationVideos polls one video generation's status AND its produced clip in
// a single round-trip (motionMP4URL carries the mp4).
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

// VideoAssets are the decoded reference assets a video request can carry:
// image references (strength MID), one audio track and video references. The
// caller enforces the per-type caps; durations are derived from the bytes here
// because Leonardo requires them for audio/video guidances.
type VideoAssets struct {
	Images [][]byte
	Audios [][]byte
	Videos [][]byte
}

// GenerateVideo runs the Leonardo video pipeline (seedance-2.0 / -fast, hailuo) against
// one account cookie: upload every reference asset, submit the Generate mutation
// as a PRIVATE generation, poll until COMPLETE, then optionally download the mp4.
func (c *Client) GenerateVideo(ctx context.Context, cookie, model, prompt string, width, height, durationSeconds int, refs VideoAssets, downloadResult bool) ([]byte, map[string]any, error) {
	sess, err := c.GetSession(ctx, cookie)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(model) == "" {
		model = "seedance-2.0-fast"
	}

	guidances := map[string]any{}
	var imageRefs []map[string]any
	for _, img := range refs.Images {
		if len(img) == 0 {
			continue
		}
		uploadID, upErr := c.uploadAsset(ctx, cookie, assetExtension(img, "png"), img)
		if upErr != nil {
			return nil, nil, upErr
		}
		imageRefs = append(imageRefs, map[string]any{
			"image":    map[string]any{"id": uploadID, "type": "UPLOADED"},
			"strength": "MID",
		})
	}
	if len(imageRefs) > 0 {
		guidances["image_reference"] = imageRefs
	}
	var audioRefs []map[string]any
	for _, aud := range refs.Audios {
		if len(aud) == 0 {
			continue
		}
		uploadID, upErr := c.uploadAsset(ctx, cookie, assetExtension(aud, "mp3"), aud)
		if upErr != nil {
			return nil, nil, upErr
		}
		audio := map[string]any{"id": uploadID, "type": "UPLOADED"}
		if secs := MediaDurationSeconds(aud); secs > 0 {
			audio["duration"] = secs
		}
		audioRefs = append(audioRefs, map[string]any{"audio": audio})
	}
	if len(audioRefs) > 0 {
		guidances["audio_reference"] = audioRefs
	}
	var videoRefs []map[string]any
	for _, vid := range refs.Videos {
		if len(vid) == 0 {
			continue
		}
		uploadID, upErr := c.uploadAsset(ctx, cookie, assetExtension(vid, "mp4"), vid)
		if upErr != nil {
			return nil, nil, upErr
		}
		video := map[string]any{"id": uploadID, "type": "UPLOADED"}
		if secs := MediaDurationSeconds(vid); secs > 0 {
			video["duration"] = secs
		}
		videoRefs = append(videoRefs, map[string]any{"video": video})
	}
	if len(videoRefs) > 0 {
		guidances["video_reference_base"] = videoRefs
	}

	parameters := map[string]any{
		"height":           height,
		"width":            width,
		"duration":         durationSeconds,
		"motion_has_audio": true,
		"quantity":         1,
		"prompt":           prompt,
		"guidances":        guidances,
	}
	// seedance 走随机种子;hailuo 的生成页 seedEnabled=false,请求里不带 seed。
	if !strings.HasPrefix(model, "hailuo") {
		parameters["seed"] = -1
	}
	genReq := map[string]any{
		"operationName": "Generate",
		"query":         mGenerate,
		"variables": map[string]any{
			"request": map[string]any{
				"model": model,
				// 私有生成：不进公开 feed。
				"public":     false,
				"parameters": parameters,
			},
		},
	}
	payload, _ := json.Marshal(genReq)
	body, err := c.callGraphQL(ctx, cookie, payload, true, "generate-video")
	if err != nil {
		return nil, nil, err
	}
	var genResp struct {
		Data struct {
			Generate struct {
				GenerationID string `json:"generationId"`
			} `json:"generate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &genResp); err != nil {
		return nil, nil, fmt.Errorf("%w: generate non-json", ErrTemporaryUpstream)
	}
	genID := strings.TrimSpace(genResp.Data.Generate.GenerationID)
	if genID == "" {
		return nil, nil, fmt.Errorf("%w: no generationId: %s", ErrTemporaryUpstream, clip(body, 200))
	}

	videoURL, err := c.pollVideo(ctx, cookie, genID)
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
	data, err := c.downloadImage(ctx, videoURL)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// pollVideo polls one generation until COMPLETE (returning the mp4 url) or
// FAILED. Temporary upstream hiccups don't abort the wait — only ctx/deadline do.
func (c *Client) pollVideo(ctx context.Context, cookie, genID string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"operationName": "GenerationVideos",
		"query":         qGenerationVideos,
		"variables": map[string]any{
			"where": map[string]any{"id": map[string]any{"_in": []string{genID}}},
		},
	})

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(10 * time.Minute)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl.Add(-60 * time.Second)
	}

	for {
		body, err := c.callGraphQL(ctx, cookie, payload, false, "poll-video")
		if errors.Is(err, ErrAuth) {
			return "", err
		}
		if err == nil {
			var pr struct {
				Data struct {
					Generations []struct {
						Status          string `json:"status"`
						GeneratedImages []struct {
							MotionMP4URL string `json:"motionMP4URL"`
						} `json:"generated_images"`
					} `json:"generations"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &pr); err == nil && len(pr.Data.Generations) > 0 {
				g := pr.Data.Generations[0]
				switch strings.ToUpper(g.Status) {
				case "COMPLETE":
					for _, img := range g.GeneratedImages {
						if u := strings.TrimSpace(img.MotionMP4URL); u != "" {
							return u, nil
						}
					}
					return "", fmt.Errorf("%w: complete but no video url", ErrTemporaryUpstream)
				case "FAILED":
					return "", fmt.Errorf("%w: generation failed", ErrTemporaryUpstream)
				}
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: generation timed out", ErrTemporaryUpstream)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// assetExtension sniffs the upload extension Leonardo expects for a reference
// asset; fallback is used when the bytes aren't recognized.
func assetExtension(data []byte, fallback string) string {
	n := len(data)
	switch {
	case n >= 8 && string(data[1:4]) == "PNG":
		return "png"
	case n >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpg"
	case n >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case n >= 12 && string(data[4:8]) == "ftyp":
		if n >= 12 && strings.HasPrefix(string(data[8:12]), "qt") {
			return "mov"
		}
		if n >= 12 && strings.HasPrefix(string(data[8:12]), "M4A") {
			return "m4a"
		}
		return "mp4"
	case n >= 4 && data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3:
		return "webm"
	case n >= 3 && string(data[0:3]) == "ID3":
		return "mp3"
	case n >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0:
		return "mp3"
	case n >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav"
	}
	return fallback
}

// MediaDurationSeconds returns a media asset's duration in seconds (0 when it
// can't be determined). Leonardo's audio_reference / video_reference_base
// guidances carry the duration, and the caller validates video lengths with it,
// so it's derived from the uploaded bytes: mp4/mov via the mvhd box, wav via the
// fmt byte rate, mp3 from the first frame's bitrate.
func MediaDurationSeconds(data []byte) float64 {
	if secs := mp4Duration(data); secs > 0 {
		return secs
	}
	if secs := wavDuration(data); secs > 0 {
		return secs
	}
	return mp3Duration(data)
}

// mp4Duration walks the ISOBMFF box tree to moov/mvhd and reads timescale+duration.
func mp4Duration(data []byte) float64 {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return 0
	}
	moov := findBox(data, "moov")
	if moov == nil {
		return 0
	}
	mvhd := findBox(moov, "mvhd")
	if len(mvhd) < 20 {
		return 0
	}
	version := mvhd[0]
	if version == 1 {
		if len(mvhd) < 32 {
			return 0
		}
		timescale := binary.BigEndian.Uint32(mvhd[20:24])
		duration := binary.BigEndian.Uint64(mvhd[24:32])
		if timescale == 0 {
			return 0
		}
		return float64(duration) / float64(timescale)
	}
	timescale := binary.BigEndian.Uint32(mvhd[12:16])
	duration := binary.BigEndian.Uint32(mvhd[16:20])
	if timescale == 0 {
		return 0
	}
	return float64(duration) / float64(timescale)
}

// findBox returns the payload of the first box named typ among data's boxes
// (callers descend one level at a time by passing a parent's payload back in).
func findBox(data []byte, typ string) []byte {
	for off := 0; off+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		name := string(data[off+4 : off+8])
		header := 8
		if size == 1 { // 64-bit size
			if off+16 > len(data) {
				return nil
			}
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			header = 16
		} else if size == 0 {
			size = len(data) - off
		}
		if size < header || off+size > len(data) {
			return nil
		}
		if name == typ {
			return data[off+header : off+size]
		}
		off += size
	}
	return nil
}

// wavDuration reads the RIFF fmt chunk's byte rate and the data chunk size.
func wavDuration(data []byte) float64 {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0
	}
	byteRate := 0
	dataSize := 0
	for off := 12; off+8 <= len(data); {
		name := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if size < 0 || body > len(data) {
			break
		}
		switch name {
		case "fmt ":
			if body+16 <= len(data) {
				byteRate = int(binary.LittleEndian.Uint32(data[body+8 : body+12]))
			}
		case "data":
			dataSize = size
			if body+size > len(data) {
				dataSize = len(data) - body
			}
		}
		off = body + size + size%2
	}
	if byteRate <= 0 || dataSize <= 0 {
		return 0
	}
	return float64(dataSize) / float64(byteRate)
}

// mp3Bitrates are the Layer III bitrate tables (kbps) indexed by the frame
// header's bitrate index: 1 = MPEG1, 2 = MPEG2/2.5.
var mp3Bitrates = map[int][]int{
	1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
}

// mp3Duration estimates the duration from the first frame header's bitrate
// (constant-bitrate assumption — good enough for the guidance duration field).
func mp3Duration(data []byte) float64 {
	off := 0
	if len(data) >= 10 && string(data[0:3]) == "ID3" {
		// syncsafe int: 7 bits per byte
		tagSize := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
		off = 10 + tagSize
	}
	for ; off+4 <= len(data); off++ {
		if data[off] != 0xFF || data[off+1]&0xE0 != 0xE0 {
			continue
		}
		versionBits := (data[off+1] >> 3) & 0x03
		layerBits := (data[off+1] >> 1) & 0x03
		if layerBits != 0x01 { // Layer III only
			continue
		}
		table := 1
		if versionBits != 0x03 { // MPEG2 / 2.5
			table = 2
		}
		idx := int((data[off+2] >> 4) & 0x0F)
		rates := mp3Bitrates[table]
		if idx <= 0 || idx >= len(rates) || rates[idx] == 0 {
			continue
		}
		kbps := rates[idx]
		return float64(len(data)-off) * 8 / float64(kbps*1000)
	}
	return 0
}
