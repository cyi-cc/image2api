package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

type V1Handler struct {
	v1 *service.V1Service
}

func NewV1Handler(v1 *service.V1Service) *V1Handler {
	return &V1Handler{v1: v1}
}

func (h *V1Handler) Models(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	_ = principal

	items, err := h.v1.ListModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load models"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   items,
	})
}

// ImageGenerations — OpenAI POST /v1/images/generations (text-to-image only).
// Accepts exactly OpenAI's fields; size→aspect ratio and quality→resolution tier
// are mapped server-side. Returns {created, data:[{b64_json}]}.
func (h *V1Handler) ImageGenerations(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}

	var body struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              int    `json:"n"`
		Size           string `json:"size"`
		Quality        string `json:"quality"`
		ResponseFormat string `json:"response_format"`
		Background     string `json:"background"`
		OutputFormat   string `json:"output_format"`
		User           string `json:"user"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}

	resp, err := h.v1.PrepareImageRequest(c.Request.Context(), principal, service.V1ImageRequest{
		Model:   body.Model,
		Prompt:  body.Prompt,
		N:       body.N,
		Size:    body.Size,
		BaseURL: requestBaseURL(c),
	})
	if err != nil {
		h.writeV1Error(c, err, resp)
		return
	}
	c.JSON(http.StatusOK, openaiImageResponse(resp))
}

// ImageEdits — OpenAI POST /v1/images/edits (image-to-image). multipart/form-data
// only: image / image[] file uploads (+ optional mask), prompt, model, n, size,
// quality. Files become reference images. Returns {created, data:[{b64_json}]}.
func (h *V1Handler) ImageEdits(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "images/edits requires multipart/form-data"})
		return
	}
	if err := c.Request.ParseMultipartForm(64 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid multipart form"})
		return
	}
	refs := readMultipartImages(c, "image", "image[]")
	if len(refs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "images/edits requires at least one image file"})
		return
	}
	n, _ := strconv.Atoi(strings.TrimSpace(c.PostForm("n")))
	resp, err := h.v1.PrepareImageRequest(c.Request.Context(), principal, service.V1ImageRequest{
		Model:           c.PostForm("model"),
		Prompt:          c.PostForm("prompt"),
		N:               n,
		Size:            c.PostForm("size"),
		ReferenceImages: refs,
		BaseURL:         requestBaseURL(c),
	})
	if err != nil {
		h.writeV1Error(c, err, resp)
		return
	}
	c.JSON(http.StatusOK, openaiImageResponse(resp))
}

// CreateVideo — OpenAI POST /v1/videos. Creates an async job and returns the
// video object immediately ({id, status:"queued"}). Accepts JSON {model, prompt,
// seconds, size} or multipart (with an input_reference file). size→ratio+
// resolution, seconds→duration.
func (h *V1Handler) CreateVideo(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	var modelID, prompt, seconds, size string
	var refs []string
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(64 << 20); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid multipart form"})
			return
		}
		modelID = c.PostForm("model")
		prompt = c.PostForm("prompt")
		seconds = c.PostForm("seconds")
		size = c.PostForm("size")
		refs = readMultipartImages(c, "input_reference", "input_reference[]")
	} else {
		var body struct {
			Model   string          `json:"model"`
			Prompt  string          `json:"prompt"`
			Seconds json.RawMessage `json:"seconds"`
			Size    string          `json:"size"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
			return
		}
		modelID, prompt, size = body.Model, body.Prompt, body.Size
		seconds = rawToString(body.Seconds)
	}
	duration := strings.TrimSpace(seconds)
	if duration != "" && !strings.HasSuffix(duration, "s") {
		duration += "s"
	}
	aspect, resolution := videoSizeToInternal(size)
	resp, err := h.v1.StartVideoJob(c.Request.Context(), principal, service.V1VideoRequest{
		Model:           modelID,
		Prompt:          prompt,
		Duration:        duration,
		AspectRatio:     aspect,
		Resolution:      resolution,
		ReferenceImages: refs,
		BaseURL:         requestBaseURL(c),
	})
	if err != nil {
		h.writeV1Error(c, err, nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetVideo — OpenAI GET /v1/videos/{id}. Returns the job status object.
func (h *V1Handler) GetVideo(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	resp, err := h.v1.VideoJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeV1Error(c, err, nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetVideoContent — OpenAI GET /v1/videos/{id}/content. Streams the rendered mp4
// by proxying the stored upstream URL (downloaded on demand, never persisted).
func (h *V1Handler) GetVideoContent(c *gin.Context) {
	principal, err := h.v1.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	body, contentType, err := h.v1.OpenVideoContent(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeV1Error(c, err, nil)
		return
	}
	defer body.Close()
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, body)
}

// readMultipartImages reads the given file fields and returns each as base64.
func readMultipartImages(c *gin.Context, keys ...string) []string {
	var out []string
	form := c.Request.MultipartForm
	if form == nil {
		return out
	}
	for _, key := range keys {
		for _, fh := range form.File[key] {
			f, e := fh.Open()
			if e != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(f, 8<<20+1))
			f.Close()
			if len(b) > 0 {
				out = append(out, base64.StdEncoding.EncodeToString(b))
			}
		}
	}
	return out
}

// rawToString accepts OpenAI's `seconds` whether sent as a JSON string or number.
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return strings.Trim(string(raw), `"`)
}

// videoSizeToInternal maps OpenAI's "WxH" size to our aspect ratio + resolution
// tier (height ≥1080 → 1080p, else 720p).
func videoSizeToInternal(size string) (ratio, resolution string) {
	var w, h int
	if s := strings.TrimSpace(strings.ToLower(size)); s != "" {
		_, _ = fmt.Sscanf(s, "%dx%d", &w, &h)
	}
	if w == 0 || h == 0 {
		return "16:9", "720p"
	}
	// The "p" resolution is the SHORT edge (720p = 1280×720, 1080p = 1920×1080),
	// so a standard 1280×720 must read as 720p — not 1080p off the long edge.
	resolution = "720p"
	if min(w, h) >= 1080 {
		resolution = "1080p"
	}
	return guessRatioWH(w, h), resolution
}

func guessRatioWH(w, h int) string {
	if w == h {
		return "1:1"
	}
	r := float64(w) / float64(h)
	cands := []struct {
		name string
		v    float64
	}{{"16:9", 16.0 / 9}, {"9:16", 9.0 / 16}, {"4:3", 4.0 / 3}, {"3:4", 3.0 / 4}, {"1:1", 1}}
	best, bestD := "16:9", 1e9
	for _, cd := range cands {
		d := r - cd.v
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = cd.name, d
		}
	}
	return best
}

// openaiImageResponse strips our rich internal map down to OpenAI's image shape.
func openaiImageResponse(m map[string]any) gin.H {
	out := gin.H{"created": m["created"]}
	if d, ok := m["data"]; ok && d != nil {
		out["data"] = d
	} else {
		out["data"] = []any{}
	}
	return out
}

func (h *V1Handler) writeAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrMissingAPIKey):
		c.JSON(http.StatusUnauthorized, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrInvalidAPIKey):
		c.JSON(http.StatusUnauthorized, gin.H{"detail": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to validate api key"})
	}
}

func (h *V1Handler) writeV1Error(c *gin.Context, err error, payload map[string]any) {
	switch {
	case errors.Is(err, service.ErrUnknownModel):
		c.JSON(http.StatusNotFound, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrUnsupportedParams), errors.Is(err, service.ErrBannedPrompt):
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrInsufficientFunds):
		c.JSON(http.StatusPaymentRequired, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrReferenceTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrNoProviderAccount):
		c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrProviderAuth):
		c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrProviderQuota):
		// Match the Python contract: provider quota exhaustion maps to 401
		// (QuotaExhaustedError is handled alongside AuthError in routes.py).
		c.JSON(http.StatusUnauthorized, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrProviderTemporary):
		c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrConcurrencyFull), errors.Is(err, service.ErrUserConcurrencyFull):
		c.JSON(http.StatusTooManyRequests, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrVideoJobNotFound):
		c.JSON(http.StatusNotFound, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrVideoNotReady):
		c.JSON(http.StatusConflict, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrProviderUnsupported):
		c.JSON(http.StatusNotImplemented, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrProviderExecution):
		c.JSON(http.StatusBadGateway, gin.H{"detail": err.Error()})
	case errors.Is(err, service.ErrGenerationPending):
		c.JSON(http.StatusNotImplemented, payload)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
	}
}

// requestBaseURL derives the scheme+host of the inbound request so the service
// layer can build absolute, directly-downloadable output URLs. Honors
// X-Forwarded-Proto (reverse-proxy / TLS termination) before falling back to
// the connection's TLS state. Returns "" when the host is unknown, which makes
// the service fall back to a relative path.
func requestBaseURL(c *gin.Context) string {
	host := c.Request.Host
	if host == "" {
		return ""
	}
	scheme := "http"
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		scheme = strings.ToLower(strings.Split(proto, ",")[0])
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}
