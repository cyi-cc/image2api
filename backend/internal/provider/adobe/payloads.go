package adobe

import (
	"encoding/json"
	"math/rand"
	"strings"
)

type modelSpec struct {
	UpstreamModelID      string
	UpstreamModelVersion string
}

var lumaSize = map[string]map[string][2]int{
	"720p": {
		"21:9": {1280, 548}, "16:9": {1280, 720}, "4:3": {960, 720},
		"1:1": {720, 720}, "3:4": {720, 960}, "9:16": {720, 1280}, "9:21": {548, 1280},
	},
	"1080p": {
		"21:9": {1920, 822}, "16:9": {1920, 1080}, "4:3": {1440, 1080},
		"1:1": {1080, 1080}, "3:4": {1080, 1440}, "9:16": {1080, 1920}, "9:21": {822, 1920},
	},
	"4k": {
		"21:9": {3840, 1646}, "16:9": {3840, 2160}, "4:3": {2880, 2160},
		"1:1": {2160, 2160}, "3:4": {2160, 2880}, "9:16": {2160, 3840}, "9:21": {1646, 3840},
	},
}

var gptImageSize = map[string]map[string][2]int{
	"1K": {"1:1": {1024, 1024}, "5:4": {1120, 896}, "9:16": {720, 1280}, "21:9": {1456, 624}, "16:9": {1280, 720}, "4:3": {1152, 864}, "3:2": {1248, 832}, "4:5": {896, 1120}, "3:4": {864, 1152}, "2:3": {832, 1248}},
	"2K": {"1:1": {2048, 2048}, "5:4": {2240, 1792}, "9:16": {1440, 2560}, "21:9": {3024, 1296}, "16:9": {2560, 1440}, "4:3": {2304, 1728}, "3:2": {2496, 1664}, "4:5": {1792, 2240}, "3:4": {1728, 2304}, "2:3": {1664, 2496}},
	"4K": {"1:1": {2880, 2880}, "5:4": {3200, 2560}, "9:16": {2160, 3840}, "21:9": {3696, 1584}, "16:9": {3840, 2160}, "4:3": {3264, 2448}, "3:2": {3504, 2336}, "4:5": {2560, 3200}, "3:4": {2448, 3264}, "2:3": {2336, 3504}},
}

var fluxSize = map[string][2]int{
	"1:1":  {1024, 1024},
	"16:9": {1408, 768},
	"9:16": {768, 1408},
	"4:3":  {1280, 896},
	"3:4":  {896, 1280},
}

var defaultSize = map[string]map[string][2]int{
	"1K": {"1:1": {1024, 1024}, "1:8": {384, 3072}, "1:4": {512, 2048}, "16:9": {1360, 768}, "9:16": {768, 1360}, "4:1": {2048, 512}, "4:3": {1152, 864}, "3:4": {864, 1152}, "8:1": {3072, 384}},
	"2K": {"1:1": {2048, 2048}, "1:8": {768, 6144}, "1:4": {1024, 4096}, "16:9": {2752, 1536}, "9:16": {1536, 2752}, "4:1": {4096, 1024}, "4:3": {2048, 1536}, "3:4": {1536, 2048}, "8:1": {6144, 768}},
	"4K": {"1:1": {4096, 4096}, "1:8": {1536, 12288}, "1:4": {2048, 8192}, "16:9": {5504, 3072}, "9:16": {3072, 5504}, "4:1": {8192, 2048}, "4:3": {4096, 3072}, "3:4": {3072, 4096}, "8:1": {12288, 1536}},
}

func ResolveModelSpec(modelID string) modelSpec {
	switch modelID {
	case "firefly-gpt-image", "firefly-gpt-image-2":
		return modelSpec{UpstreamModelID: "gpt-image", UpstreamModelVersion: "2"}
	case "flux-kontext-max":
		return modelSpec{UpstreamModelID: "flux", UpstreamModelVersion: "fluxKontextMax"}
	case "nano-banana-2":
		return modelSpec{UpstreamModelID: "gemini-flash", UpstreamModelVersion: "nano-banana-3"}
	case "nano-banana-pro":
		return modelSpec{UpstreamModelID: "gemini-flash", UpstreamModelVersion: "nano-banana-2"}
	default:
		return modelSpec{UpstreamModelID: "gemini-flash", UpstreamModelVersion: "gemini-3-pro-image-preview"}
	}
}

// buildImage5Payload builds the Adobe Firefly Image 5 request. It uses a distinct
// schema from the firefly-3p models: NO modelId/size, a top-level aspectRatio
// string label and a resolutionLevel (1K→1MP, 2K→4MP). Mirrors a captured
// working image-v5.ff.adobe.io request.
func buildImage5Payload(prompt, aspectRatio, resolution string, blobIDs []string) map[string]any {
	p := map[string]any{
		"n":                    1,
		"seeds":                []int{rand.Intn(999999)},
		"output":               map[string]any{"storeInputs": true},
		"prompt":               prompt,
		"referenceBlobs":       []any{},
		"modelSpecificPayload": map[string]any{"locale": "en-US", "prompt_reasoner": "quality"},
		"modelVersion":         "image5",
		"resolutionLevel":      image5ResolutionLevel(resolution),
		"generationMetadata":   map[string]any{"module": "text2image", "submodule": "ff-image-generate"},
	}
	if len(blobIDs) > 0 {
		// Instruct-edit: aspect ratio is derived from the reference image; sending
		// aspectRatio is rejected with a validation_error.
		p["referenceBlobs"] = blobRefs(blobIDs, "general")
	} else {
		p["aspectRatio"] = defaultString(aspectRatio, "1:1")
	}
	return p
}

// image5ResolutionLevel maps the UI resolution tier to Image 5's megapixel level.
func image5ResolutionLevel(resolution string) string {
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case "1K":
		return "1MP"
	case "2K":
		return "4MP"
	default:
		return "4MP"
	}
}

func BuildImagePayloadCandidates(modelID, prompt, aspectRatio, outputResolution string, blobIDs []string) []map[string]any {
	spec := ResolveModelSpec(modelID)
	ratio := defaultString(aspectRatio, "1:1")
	resolution := defaultString(outputResolution, "2K")

	switch spec.UpstreamModelID {
	case "gpt-image":
		return buildGPTImagePayloads(spec, prompt, ratio, resolution, blobIDs)
	case "flux":
		return buildFluxPayloads(spec, prompt, ratio, blobIDs)
	default:
		return buildDefaultPayloads(spec, prompt, ratio, resolution, blobIDs)
	}
}

func buildGPTImagePayloads(spec modelSpec, prompt, ratio, resolution string, blobIDs []string) []map[string]any {
	size := getSize(gptImageSize, resolution, ratio, "1:1")
	// Mirrors the captured working gpt-image request shape: modelSpecificPayload.size,
	// generationSettings.detailLevel 3, and NO top-level size / outputResolution
	// (sending those got 403). Keeps the chosen size via modelSpecificPayload.size
	// ("WxH") rather than "auto".
	base := map[string]any{
		"modelId":              spec.UpstreamModelID,
		"modelVersion":         spec.UpstreamModelVersion,
		"n":                    1,
		"prompt":               prompt,
		"seeds":                []int{rand.Intn(999999)},
		"output":               map[string]any{"storeInputs": true},
		"referenceBlobs":       []any{},
		"generationMetadata":   map[string]any{"module": "text2image", "submodule": "ff-image-generate"},
		"modelSpecificPayload": map[string]any{"size": sizeString(size)},
		"generationSettings":   map[string]any{"detailLevel": 3},
	}
	if len(blobIDs) == 0 {
		return []map[string]any{base}
	}
	subject := cloneMap(base)
	subject["referenceBlobs"] = blobRefs(blobIDs, "subject")
	return []map[string]any{subject}
}

func buildFluxPayloads(spec modelSpec, prompt, ratio string, blobIDs []string) []map[string]any {
	size := fluxSize[ratio]
	if size == [2]int{} {
		size = fluxSize["1:1"]
	}
	base := map[string]any{
		"modelId":        spec.UpstreamModelID,
		"modelVersion":   spec.UpstreamModelVersion,
		"n":              1,
		"prompt":         prompt,
		"size":           map[string]any{"width": size[0], "height": size[1]},
		"seeds":          []int{rand.Intn(999999)},
		"output":         map[string]any{"storeInputs": true},
		"referenceBlobs": []any{},
		"modelSpecificPayload": map[string]any{
			"prompt_upsampling": true,
			"safety_tolerance":  2,
			"aspect_ratio":      ratio,
		},
		"generationMetadata": map[string]any{"module": "text2image", "submodule": "ff-image-generate"},
	}
	if len(blobIDs) == 0 {
		return []map[string]any{base}
	}
	edited := cloneMap(base)
	edited["generationMetadata"] = map[string]any{"module": "image2image", "submodule": "ff-image-generate"}
	edited["referenceBlobs"] = blobRefs(blobIDs, "general")
	return []map[string]any{edited}
}

func buildDefaultPayloads(spec modelSpec, prompt, ratio, resolution string, blobIDs []string) []map[string]any {
	size := getSize(defaultSize, resolution, ratio, "16:9")
	// Shape mirrors a captured working firefly.adobe.com request exactly: top-level
	// size object, modelSpecificPayload only {parameters:{addWatermark:false}},
	// groundSearch:false, module "text2image" (even with a reference blob). NO
	// skipCai and NO modelSpecificPayload.aspectRatio — sending those got 403.
	base := map[string]any{
		"modelId":      spec.UpstreamModelID,
		"modelVersion": spec.UpstreamModelVersion,
		"n":            1,
		"prompt":       prompt,
		"size":         map[string]any{"width": size[0], "height": size[1]},
		"seeds":        []int{rand.Intn(999999)},
		"groundSearch": false,
		"output":       map[string]any{"storeInputs": true},
		"generationMetadata": map[string]any{
			"module":    "text2image",
			"submodule": "ff-image-generate",
		},
		"modelSpecificPayload": map[string]any{
			"parameters": map[string]any{"addWatermark": false},
		},
	}
	if len(blobIDs) == 0 {
		base["referenceBlobs"] = []any{}
		return []map[string]any{base}
	}
	edited := cloneMap(base)
	edited["referenceBlobs"] = blobRefs(blobIDs, "general")
	return []map[string]any{edited}
}

func getSize(table map[string]map[string][2]int, resolution, ratio, fallbackRatio string) [2]int {
	level := defaultString(resolution, "2K")
	levelTable, ok := table[level]
	if !ok {
		levelTable = table["2K"]
	}
	size, ok := levelTable[ratio]
	if !ok {
		size = levelTable[fallbackRatio]
	}
	return size
}

func sizeString(size [2]int) string {
	return itoa(size[0]) + "x" + itoa(size[1])
}

func blobRefs(ids []string, usage string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "usage": usage})
	}
	return out
}

func referenceImagesByID(ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"id": id})
	}
	return out
}

func referenceImagesByLocal(ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"localBlobRef": id})
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func BuildVideoPayload(engine, prompt, aspectRatio string, durationSeconds int, resolution, referenceMode, upstreamModel string, blobIDs []string) map[string]any {
	seedVal := rand.Intn(999999)
	engine = defaultString(engine, "sora2")
	resolution = defaultString(resolution, "720p")
	aspectRatio = defaultString(aspectRatio, "16:9")
	if durationSeconds <= 0 {
		durationSeconds = 5
	}

	switch engine {
	case "firefly-video":
		// Firefly-native video model — a distinct schema (mirrors a captured
		// working video-v1.ff.adobe.io request): sizes[] carries width/height +
		// numFrames (numFrames encodes duration, ~25.6fps so 5s = 128), and
		// reference frames go under image.conditions with placement.start
		// (0 = first frame / 首帧, 1 = last frame / 末帧). NO modelId / version /
		// engine / duration / referenceBlobs fields.
		w, h, frames := fireflyVideoSize(aspectRatio, resolution, durationSeconds)
		payload := map[string]any{
			"addOnTransparentBackground": false,
			"prompt":                     prompt,
			"seeds":                      []int{seedVal},
			"sizes":                      []any{map[string]any{"width": w, "height": h, "numFrames": frames}},
			"videoSettings":             map[string]any{},
			"locale":                    "en-US",
			"generationMetadata":        map[string]any{"module": "text2video", "submodule": "ff-video-generate"},
			"output":                    map[string]any{"storeInputs": true},
		}
		if len(blobIDs) > 0 {
			conds := make([]any, 0, 2)
			conds = append(conds, map[string]any{
				"source":    map[string]any{"id": blobIDs[0]},
				"placement": map[string]any{"start": 0},
			})
			if len(blobIDs) > 1 {
				conds = append(conds, map[string]any{
					"source":    map[string]any{"id": blobIDs[1]},
					"placement": map[string]any{"start": 1},
				})
			}
			payload["image"] = map[string]any{"conditions": conds}
		}
		return payload
	case "veo31-fast", "veo31-standard":
		modelVersion := "3.1-fast-generate"
		if engine == "veo31-standard" {
			modelVersion = "3.1-generate"
		}
		payload := map[string]any{
			"modelId":        "veo",
			"modelVersion":   modelVersion,
			"size":           videoSize(aspectRatio, resolution),
			"seeds":          []int{seedVal},
			"prompt":         prompt,
			"negativePrompt": "",
			"duration":       durationSeconds,
			"generateAudio":  true,
			"generationMetadata": map[string]any{
				"module":    "text2video",
				"submodule": "ff-video-generate",
			},
			"output":         map[string]any{"storeInputs": true},
			"referenceBlobs": []any{},
		}
		if len(blobIDs) > 0 {
			payload["generationMetadata"] = map[string]any{"module": "image2video", "submodule": "ff-video-generate"}
			refs := make([]any, 0, min(len(blobIDs), 3))
			if referenceMode == "frame" {
				for idx, id := range blobIDs[:min(len(blobIDs), 3)] {
					refs = append(refs, map[string]any{"id": id, "usage": "frame", "order": idx + 1})
				}
			} else {
				for _, id := range blobIDs[:min(len(blobIDs), 3)] {
					refs = append(refs, map[string]any{"id": id, "usage": "asset"})
				}
			}
			payload["referenceBlobs"] = refs
		}
		return payload
	case "seedance-fast", "seedance-2.0":
		modelVersion := "seedance_2.0"
		if engine == "seedance-fast" {
			modelVersion = "seedance_2.0_fast"
		}
		payload := map[string]any{
			"modelId":              "seedance",
			"modelVersion":         modelVersion,
			"size":                 videoSize(aspectRatio, resolution),
			"seeds":                []int{seedVal},
			"prompt":               prompt,
			"negativePrompt":       "",
			"duration":             durationSeconds,
			"generateAudio":        true,
			"generationSettings":   map[string]any{"aspectRatio": aspectRatio},
			"generationMetadata":   map[string]any{"module": "text2video", "submodule": "ff-video-generate"},
			"output":               map[string]any{"storeInputs": true},
			"referenceBlobs":       []any{},
		}
		if len(blobIDs) > 0 {
			payload["generationMetadata"] = map[string]any{"module": "image2video", "submodule": "ff-video-generate"}
			refs := make([]any, 0, min(len(blobIDs), 9))
			if referenceMode == "frame" {
				for idx, id := range blobIDs[:min(len(blobIDs), 9)] {
					refs = append(refs, map[string]any{"id": id, "usage": "frame", "order": idx + 1})
				}
			} else {
				for _, id := range blobIDs[:min(len(blobIDs), 9)] {
					refs = append(refs, map[string]any{"id": id, "usage": "asset"})
				}
			}
			payload["referenceBlobs"] = refs
		}
		return payload
	case "luma":
		payload := map[string]any{
			"modelId":        "luma",
			"modelVersion":   "3.14-ray",
			"size":           lumaVideoSize(aspectRatio, resolution),
			"mode":           "flex_2",
			"prompt":         prompt,
			"negativePrompt": "",
			"duration":       durationSeconds,
			"generationMetadata": map[string]any{
				"module":    "text2video",
				"submodule": "ff-video-generate",
			},
			"modelSpecificPayload": map[string]any{
				"resolution":   strings.ToLower(resolution),
				"aspect_ratio": aspectRatio,
			},
			"output": map[string]any{"storeInputs": true},
		}
		if len(blobIDs) > 0 {
			payload["generationMetadata"] = map[string]any{
				"module":    "image2video",
				"submodule": "ff-video-generate",
			}
			refs := make([]any, 0, min(len(blobIDs), 2))
			for idx, id := range blobIDs[:min(len(blobIDs), 2)] {
				refs = append(refs, map[string]any{"id": id, "usage": "frame", "order": idx + 1})
			}
			payload["referenceBlobs"] = refs
		}
		return payload
	default:
		upstream := defaultString(upstreamModel, "openai:firefly:colligo:sora2")
		payload := map[string]any{
			"n":                          1,
			"seeds":                      []int{seedVal},
			"modelId":                    "sora",
			"modelVersion":               "sora-2",
			"size":                       videoSize(aspectRatio, resolution),
			"duration":                   durationSeconds,
			"fps":                        24,
			"prompt":                     buildVideoPromptJSON(prompt, durationSeconds),
			"generationMetadata":         map[string]any{"module": "text2video"},
			"model":                      upstream,
			"generateAudio":              true,
			"generateLoop":               false,
			"transparentBackground":      false,
			"seed":                       itoa(seedVal),
			"locale":                     "en-US",
			"camera":                     map[string]any{"angle": "none", "shotSize": "none", "motion": nil, "promptStyle": nil},
			"negativePrompt":             "",
			"jobMode":                    "standard",
			"debugGenerationEndpoint":    "",
			"referenceBlobs":             []any{},
			"referenceFrames":            []any{},
			"referenceVideo":             nil,
			"cameraMotionReferenceVideo": nil,
			"characterReference":         nil,
			"editReferenceVideo":         nil,
			"output":                     map[string]any{"storeInputs": true},
		}
		if len(blobIDs) > 0 {
			firstID := blobIDs[0]
			payload["generationMetadata"] = map[string]any{"module": "image2video"}
			payload["referenceBlobs"] = []any{
				map[string]any{"id": firstID, "usage": "general", "promptReference": 1},
			}
			payload["referenceFrames"] = []any{map[string]any{"localBlobRef": firstID}, nil}
		}
		return payload
	}
}

// fireflyVideoSizeTable maps the firefly-video resolution tier + aspect ratio to
// pixel dimensions. Only 1080p 9:16 (1080x1920) is HAR-confirmed; the rest follow
// the standard 540p/720p/1080p grid for each ratio.
var fireflyVideoSizeTable = map[string]map[string][2]int{
	"540p":  {"16:9": {960, 540}, "1:1": {540, 540}, "9:16": {540, 960}},
	"720p":  {"16:9": {1280, 720}, "1:1": {720, 720}, "9:16": {720, 1280}},
	"1080p": {"16:9": {1920, 1080}, "1:1": {1080, 1080}, "9:16": {1080, 1920}},
}

// fireflyVideoSize returns width, height and numFrames. numFrames encodes the
// clip length (~25.6fps; 5s = 128 frames, HAR-confirmed).
func fireflyVideoSize(aspectRatio, resolution string, durationSeconds int) (int, int, int) {
	table, ok := fireflyVideoSizeTable[strings.ToLower(defaultString(resolution, "1080p"))]
	if !ok {
		table = fireflyVideoSizeTable["1080p"]
	}
	wh, ok := table[defaultString(aspectRatio, "9:16")]
	if !ok {
		wh = table["9:16"]
	}
	frames := durationSeconds * 128 / 5
	if frames <= 0 {
		frames = 128
	}
	return wh[0], wh[1], frames
}

func videoSize(aspectRatio, resolution string) map[string]any {
	if strings.EqualFold(resolution, "1080p") {
		if aspectRatio == "16:9" {
			return map[string]any{"width": 1920, "height": 1080}
		}
		return map[string]any{"width": 1080, "height": 1920}
	}
	if aspectRatio == "16:9" {
		return map[string]any{"width": 1280, "height": 720}
	}
	return map[string]any{"width": 720, "height": 1280}
}

func lumaVideoSize(aspectRatio, resolution string) map[string]any {
	table, ok := lumaSize[strings.ToLower(defaultString(resolution, "720p"))]
	if !ok {
		table = lumaSize["720p"]
	}
	size, ok := table[defaultString(aspectRatio, "16:9")]
	if !ok {
		size = table["16:9"]
	}
	return map[string]any{"width": size[0], "height": size[1]}
}

func buildVideoPromptJSON(prompt string, durationSeconds int) string {
	payload := map[string]any{
		"id":           1,
		"duration_sec": durationSeconds,
		"prompt_text":  prompt,
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
