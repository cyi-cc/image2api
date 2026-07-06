package handler

import (
	"errors"
	"net/http"
	"strings"

	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

type UserGenerationHandler struct {
	userGen *service.UserGenerationService
	admin   *service.AdminReadService
}

func NewUserGenerationHandler(userGen *service.UserGenerationService, admin *service.AdminReadService) *UserGenerationHandler {
	return &UserGenerationHandler{
		userGen: userGen,
		admin:   admin,
	}
}

// MyImages returns the current user's own recently generated images (scoped to
// their owner directory) — used by the showcase "选择已生成" picker so an admin
// only sees their own images, not everyone's.
func (h *UserGenerationHandler) MyImages(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
		return
	}
	items, err := h.admin.RecentImagesOwned(c.Request.Context(), service.OwnerDir(user), 60)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load images"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// DeleteMyFile removes ONE of the caller's own generated files (plus its
// thumbnail) and blanks the log rows referencing it, so the 画图台 grid and
// 创作记录 gallery stop showing it. ?file= is the storage key (owner/name).
func (h *UserGenerationHandler) DeleteMyFile(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
		return
	}
	if err := h.admin.DeleteOwnedFile(c.Request.Context(), service.OwnerDir(user), c.Query("file")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *UserGenerationHandler) Generate(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
		return
	}

	var body struct {
		Model           string   `json:"model"`
		Prompt          string   `json:"prompt"`
		Ratio           string   `json:"ratio"`
		Resolution      string   `json:"resolution"`
		Duration        string   `json:"duration"`
		ReferenceImages []string `json:"reference_images"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}

	resp, err := h.userGen.Generate(c.Request.Context(), user, service.UserGenerateRequest{
		Model:           body.Model,
		Prompt:          body.Prompt,
		Ratio:           body.Ratio,
		Resolution:      body.Resolution,
		Duration:        body.Duration,
		ReferenceImages: body.ReferenceImages,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUnknownModel):
			c.JSON(http.StatusNotFound, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrUnsupportedParams), errors.Is(err, service.ErrBannedPrompt):
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrInsufficientFunds):
			c.JSON(http.StatusPaymentRequired, gin.H{"detail": "积分不足"})
		case errors.Is(err, service.ErrNoProviderAccount):
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderAuth), errors.Is(err, service.ErrProviderTemporary):
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderQuota):
			c.JSON(http.StatusTooManyRequests, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrConcurrencyFull), errors.Is(err, service.ErrUserConcurrencyFull):
			c.JSON(http.StatusTooManyRequests, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderExecution):
			c.JSON(http.StatusBadGateway, gin.H{"detail": err.Error()})
		default:
			if err.Error() == "已有正在生成的任务,请稍候" {
				c.JSON(http.StatusConflict, gin.H{"detail": err.Error()})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *UserGenerationHandler) Test(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
		return
	}
	if user.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"detail": "需要管理员权限"})
		return
	}

	var body struct {
		Model           string   `json:"model"`
		Prompt          string   `json:"prompt"`
		Ratio           string   `json:"ratio"`
		Resolution      string   `json:"resolution"`
		Duration        string   `json:"duration"`
		ReferenceImages []string `json:"reference_images"`
		AccountID       string   `json:"account_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}

	resp, err := h.userGen.AdminTest(c.Request.Context(), user, service.UserGenerateRequest{
		Model:           body.Model,
		Prompt:          body.Prompt,
		Ratio:           body.Ratio,
		Resolution:      body.Resolution,
		Duration:        body.Duration,
		ReferenceImages: body.ReferenceImages,
		AccountID:       body.AccountID,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUnknownModel):
			c.JSON(http.StatusNotFound, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrUnsupportedParams), errors.Is(err, service.ErrBannedPrompt):
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderQuota):
			c.JSON(http.StatusTooManyRequests, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrConcurrencyFull), errors.Is(err, service.ErrUserConcurrencyFull):
			c.JSON(http.StatusTooManyRequests, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrNoProviderAccount):
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderAuth), errors.Is(err, service.ErrProviderTemporary):
			c.JSON(http.StatusServiceUnavailable, gin.H{"detail": err.Error()})
		case errors.Is(err, service.ErrProviderExecution):
			c.JSON(http.StatusBadGateway, gin.H{"detail": err.Error()})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *UserGenerationHandler) MyJobs(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusOK, gin.H{"pending": nil, "latest": nil})
		return
	}
	data, err := h.userGen.MyJobs(c.Request.Context(), user, c.Query("source"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load jobs"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *UserGenerationHandler) Logs(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
		return
	}

	limit := parseInt(c.Query("limit"), 50)
	offset := parseInt(c.Query("offset"), 0)
	kind := c.Query("kind")
	status := c.Query("status")
	// statuses=pending,success → status IN (...). Used by the 画图台 grid so it can
	// fetch exactly the rows it shows (进行中 + 成功) in one query, server-side.
	var statuses []string
	if s := strings.TrimSpace(c.Query("statuses")); s != "" {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				statuses = append(statuses, p)
			}
		}
	}
	// Secure-by-default: always scope to the caller's OWN records. This endpoint
	// serves the front-end 日志 / 创作记录 pages, so an admin viewing their personal
	// records must NOT see other users' work. Only an admin who explicitly opts
	// into the full view (?scope=all — the admin 日志 page) sees everyone's logs.
	// API-key ("v1") usage IS included for the caller's own records so the user
	// can audit their key's calls on /mylogs; the image-only 创作记录 gallery still
	// hides them client-side (they have no stored file).
	userID := user.ID
	excludeSource := ""
	if user.Role == "admin" && c.Query("scope") == "all" {
		userID = ""
	}
	// 来源筛选: "v1" = API key, "user" = 前台画图, "admin" = 测试模型. 始终生效 ——
	// 普通用户已被 userID 限定为本人记录,按来源服务端筛选 + 分页(/mylogs 翻全部历史)。
	source := c.Query("source")
	// 创作记录 gallery passes has_file=1 so server-side pagination counts only
	// rows with real media (success + stored file), not failed/pending events.
	hasFile := c.Query("has_file") == "1" || c.Query("has_file") == "true"

	// Media views hide homepage showcase files — those belong to the public
	// landing page, not to the caller's personal works. Galleries imply it via
	// has_file; the 画图台 grid opts in with exclude_showcase=1.
	excludeShowcase := hasFile || c.Query("exclude_showcase") == "1"
	// media=1 (画图台 grid): only pending rows or rows with a stored file, so a
	// deleted work's blanked row doesn't consume one of the grid's slots.
	mediaOnly := c.Query("media") == "1"
	// ?user= — admin-only 用户搜索 (the 日志管理 page with scope=all). Ignored for
	// normal users, whose rows are already pinned to their own userID.
	var userIDs []string
	if term := strings.TrimSpace(c.Query("user")); term != "" && userID == "" {
		ids, uerr := h.admin.MatchUserIDs(c.Request.Context(), term)
		if uerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
			return
		}
		if len(ids) == 0 {
			ids = []string{"__no_match__"}
		}
		userIDs = ids
	}
	items, total, stats, err := h.admin.Logs(c.Request.Context(), limit, offset, kind, status, statuses, nil, userID, userIDs, strings.TrimSpace(c.Query("q")), excludeSource, source, hasFile, excludeShowcase, mediaOnly)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}
	// Resolve user_id -> display name (mirrors admin.py / AdminReadHandler.Logs).
	// Without this the log table showed every row as "匿名".
	nameByID, err := h.admin.UserNameMap(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}
	// Resolve account_id -> account label so the log table can show which
	// provider account fulfilled each generation under the user.
	accountByID, err := h.admin.AccountNameMap(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}
	modelByID, err := h.admin.ModelNameMap(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}

	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		var userName any
		if item.UserID == "" {
			userName = "匿名"
		} else if name, ok := nameByID[item.UserID]; ok {
			userName = name
		} else {
			userName = item.UserID
		}
		// Provider account identity is admin-only: normal users must not see
		// which upstream account (email) fulfilled their generation.
		var accountName, accountID any
		if userID == "" {
			if item.AccountEmail != "" {
				// Email stamped on the row itself survives account deletion/re-import.
				accountName = item.AccountEmail
			} else if item.AccountID != "" {
				if label, ok := accountByID[item.AccountID]; ok {
					accountName = label
				} else {
					accountName = item.AccountID
				}
			}
			accountID = emptyStringNil(item.AccountID)
		}
		out = append(out, gin.H{
			"id":         item.ID,
			"ts":         item.TS.Unix(),
			"kind":       item.Kind,
			"status":     item.Status,
			"model":      displayModelName(modelByID, item.Model),
			"provider":   item.Provider,
			"prompt":     item.Prompt,
			"ratio":      item.Ratio,
			"resolution": item.Resolution,
			"duration":   item.Duration,
			"refs":       item.Refs,
			"source":     emptyStringNil(item.Source),
			"user_id":    emptyStringNil(item.UserID),
			"user_name":  userName,
			"account_id": accountID,
			"account":    accountName,
			"cost":       item.Cost,
			"elapsed_ms": item.ElapsedMS,
			"file":       emptyStringNil(item.File),
			"error":      emptyStringNil(item.Error),
			"created_at": unixSec(item.CreatedAt),
			"updated_at": unixSec(item.UpdatedAt),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":   out,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"stats":  stats,
	})
}

func (h *UserGenerationHandler) VideoPresets(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": []gin.H{
			{
				"key":                  "gemini-veo31",
				"label":                "Veo31",
				"type":                 "video",
				"provider":             "adobe",
				"durations":            []string{"4s", "6s", "8s"},
				"ratios":               []string{"16x9", "9x16"},
				"resolutions":          []string{"720p", "1080p"},
				"max_reference_images": 2,
				"reference_mode":       "frame",
			},
			{
				"key":                  "firefly-ray",
				"label":                "Luma Ray",
				"type":                 "video",
				"provider":             "adobe",
				"durations":            []string{"5s", "10s"},
				"ratios":               []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16", "9:21"},
				"resolutions":          []string{"720p"},
				"max_reference_images": 2,
				"reference_mode":       "frame",
			},
			{
				"key":                  "firefly-video",
				"label":                "Firefly Video",
				"type":                 "video",
				"provider":             "adobe",
				"durations":            []string{"5s"},
				"ratios":               []string{"16:9", "1:1", "9:16"},
				"resolutions":          []string{"540p", "720p", "1080p"},
				"max_reference_images": 2,
				"reference_mode":       "frame",
			},
			{
				"key":                  "runway-gen4-turbo",
				"label":                "Runway Gen-4 Turbo",
				"type":                 "video",
				"provider":             "runway",
				"durations":            []string{"5s", "10s"},
				"ratios":               []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"},
				"resolutions":          []string{"2K"},
				"max_reference_images": 1,
				"reference_mode":       "frame",
				// Runway is strictly image-to-video — a first-frame image is required
				// (no text2video), so the UI must block submit without one.
				"requires_reference": true,
			},
		},
	})
}

func (h *UserGenerationHandler) Catalog(c *gin.Context) {
	items, err := h.catalogEntries(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load catalog"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data": items,
	})
}

func (h *UserGenerationHandler) Models(c *gin.Context) {
	items, err := h.publicModels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load models"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (h *UserGenerationHandler) catalogEntries(c *gin.Context) ([]gin.H, error) {
	items := []gin.H{
		{
			"id":       "gpt-image-2",
			"provider": "chatgpt",
			"type":     "image",
			// ChatGPT web backend only reliably produces 1K and honors a limited
			// ratio set; size params are advisory prompt hints. Mirrors the Python
			// reference (providers/chatgpt/provider.py) — do not offer 2K/4K.
			"ratios":               []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions":          []string{"1K"},
			"image_to_image":       true,
			"max_reference_images": 6,
			"description":          "ChatGPT image generation",
		},
		{
			"id":                   "firefly-gpt-image-2",
			"provider":             "adobe",
			"type":                 "image",
			"ratios":               []string{"1:1", "5:4", "9:16", "21:9", "16:9", "4:3", "3:2", "4:5", "3:4", "2:3"},
			"resolutions":          []string{"1K", "2K", "4K"},
			"image_to_image":       true,
			"max_reference_images": 6,
			"description":          "Adobe Firefly GPT Image",
		},
		{
			"id":             "firefly-image-5",
			"provider":       "adobe",
			"type":           "image",
			"ratios":         []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions":    []string{"1K", "2K"},
			"image_to_image": true,
			"description":    "Adobe Firefly Image 5",
		},
		{
			"id":                   "flux-kontext-max",
			"provider":             "adobe",
			"type":                 "image",
			"ratios":               []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions":          []string{"1K"},
			"image_to_image":       true,
			"max_reference_images": 4,
			"description":          "Adobe Flux Kontext Max",
		},
		{
			"id":                   "nano-banana-2",
			"provider":             "runway",
			"type":                 "image",
			"ratios":               []string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"},
			"resolutions":          []string{"1K", "2K", "4K"},
			"image_to_image":       true,
			"max_reference_images": 6,
			"reference_mode":       "asset",
			"description":          "Runway Nano Banana 2 (图/参考图)",
		},
		{
			"id":                   "gemini-veo31",
			"provider":             "adobe",
			"type":                 "video",
			"ratios":               []string{"16x9", "9x16"},
			"resolutions":          []string{"720p", "1080p"},
			"durations":            []string{"4s", "6s", "8s"},
			"max_reference_images": 2,
			"reference_mode":       "frame",
			"description":          "Veo31 video",
		},
		{
			"id":                   "firefly-ray",
			"provider":             "adobe",
			"type":                 "video",
			"ratios":               []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16", "9:21"},
			"resolutions":          []string{"720p"},
			"durations":            []string{"5s", "10s"},
			"max_reference_images": 2,
			"reference_mode":       "frame",
			"description":          "Luma Ray video",
		},
		{
			"id":                   "firefly-video",
			"provider":             "adobe",
			"type":                 "video",
			"ratios":               []string{"16:9", "1:1", "9:16"},
			"resolutions":          []string{"720p", "1080p"},
			"durations":            []string{"5s"},
			"max_reference_images": 2,
			"reference_mode":       "frame",
			"description":          "Adobe Firefly Video",
		},
		{
			"id":                   "runway-gen4-turbo",
			"provider":             "runway",
			"type":                 "video",
			"ratios":               []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"},
			"resolutions":          []string{"720p"},
			"durations":            []string{"5s", "10s"},
			"max_reference_images": 1,
			"reference_mode":       "frame",
			"description":          "Runway Gen-4 Turbo video (图生视频)",
		},
		{
			"id":                   "grok-video",
			"provider":             "grok",
			"type":                 "video",
			"ratios":               []string{"2:3", "3:2", "1:1", "9:16", "16:9"},
			"resolutions":          []string{"720p"},
			"durations":            []string{"6s", "10s"},
			"max_reference_images": 6,
			"reference_mode":       "asset",
			"description":          "Grok Imagine video (文/图生视频)",
		},
		{
			"id":                   "seedream-4.5",
			"provider":             "leonardo",
			"type":                 "image",
			"ratios":               []string{"2:3", "1:1", "16:9", "4:3", "4:5", "9:16", "2:1"},
			"resolutions":          []string{"2K", "4K"},
			"image_to_image":       true,
			"max_reference_images": 6,
			"description":          "Leonardo Seedream 4.5 (生图 / 图生图)",
		},
		{
			"id":                   "flux-klein-2",
			"provider":             "krea",
			"type":                 "image",
			"ratios":               []string{"1:1", "4:3", "3:4", "16:9", "9:16"},
			"resolutions":          []string{"1K", "2K"},
			"image_to_image":       true,
			"max_reference_images": 4,
			"description":          "Krea Flux Klein (生图 / 图生图)",
		},
		{
			"id":                   "imagine-1.5",
			"provider":             "imagine",
			"type":                 "image",
			"ratios":               []string{"1:3", "9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "3:1"},
			"resolutions":          []string{"2K"},
			"max_reference_images": 0,
			"description":          "Imagine 1.5 (文生图)",
		},
		{
			"id":                   "imagine-1.5pro",
			"provider":             "imagine",
			"type":                 "image",
			"ratios":               []string{"1:3", "9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "3:1"},
			"resolutions":          []string{"4K"},
			"max_reference_images": 0,
			"description":          "Imagine 1.5 Pro (文生图)",
		},
	}
	existing := map[string]bool{}
	if h.admin != nil {
		models, err := h.admin.Models(c.Request.Context())
		if err != nil {
			return nil, err
		}
		for _, item := range models {
			existing[item.ID] = true
		}
	}
	for i := range items {
		items[i]["added"] = existing[items[i]["id"].(string)]
	}
	return items, nil
}

func (h *UserGenerationHandler) publicModels() ([]gin.H, error) {
	items := []gin.H{
		{
			"id":       "gpt-image-2",
			"provider": "chatgpt",
			"kind":     "image",
			// See catalogEntries — ChatGPT only reliably does 1K and a limited
			// ratio set; matches the Python reference. Keep both lists in sync.
			"ratios":      []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions": []string{"1K"},
			"description": "ChatGPT image generation",
			"stub":        false,
		},
		{
			"id":          "firefly-gpt-image-2",
			"provider":    "adobe",
			"kind":        "image",
			"ratios":      []string{"1:1", "5:4", "9:16", "21:9", "16:9", "4:3", "3:2", "4:5", "3:4", "2:3"},
			"resolutions": []string{"1K", "2K", "4K"},
			"description": "Adobe Firefly GPT Image",
			"stub":        false,
		},
		{
			"id":          "firefly-image-5",
			"provider":    "adobe",
			"kind":        "image",
			"ratios":      []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions": []string{"1K", "2K"},
			"description": "Adobe Firefly Image 5",
			"stub":        false,
		},
		{
			"id":          "flux-kontext-max",
			"provider":    "adobe",
			"kind":        "image",
			"ratios":      []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
			"resolutions": []string{"1K"},
			"description": "Adobe Flux Kontext Max",
			"stub":        false,
		},
		{
			"id":          "nano-banana-2",
			"provider":    "runway",
			"kind":        "image",
			"ratios":      []string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"},
			"resolutions": []string{"1K", "2K", "4K"},
			"description": "Runway Nano Banana 2",
			"stub":        false,
		},
		{
			"id":          "gemini-veo31",
			"provider":    "adobe",
			"kind":        "video",
			"ratios":      []string{"16x9", "9x16"},
			"resolutions": []string{"720p", "1080p"},
			"description": "Veo31 video",
			"stub":        false,
		},
		{
			"id":          "firefly-ray",
			"provider":    "adobe",
			"kind":        "video",
			"ratios":      []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16", "9:21"},
			"resolutions": []string{"720p"},
			"description": "Luma Ray video",
			"stub":        false,
		},
		{
			"id":          "firefly-video",
			"provider":    "adobe",
			"kind":        "video",
			"ratios":      []string{"16:9", "1:1", "9:16"},
			"resolutions": []string{"720p", "1080p"},
			"description": "Adobe Firefly Video",
			"stub":        false,
		},
		{
			"id":          "runway-gen4-turbo",
			"provider":    "runway",
			"kind":        "video",
			"ratios":      []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"},
			"resolutions": []string{"720p"},
			"description": "Runway Gen-4 Turbo video",
			"stub":        false,
		},
		{
			"id":          "grok-video",
			"provider":    "grok",
			"kind":        "video",
			"ratios":      []string{"2:3", "3:2", "1:1", "9:16", "16:9"},
			"resolutions": []string{"720p"},
			"description": "Grok Imagine video",
			"stub":        false,
		},
		{
			"id":          "seedream-4.5",
			"provider":    "leonardo",
			"kind":        "image",
			"ratios":      []string{"2:3", "1:1", "16:9", "4:3", "4:5", "9:16", "2:1"},
			"resolutions": []string{"2K", "4K"},
			"description": "Leonardo Seedream 4.5",
			"stub":        false,
		},
		{
			"id":          "flux-klein-2",
			"provider":    "krea",
			"kind":        "image",
			"ratios":      []string{"1:1", "4:3", "3:4", "16:9", "9:16"},
			"resolutions": []string{"1K", "2K"},
			"description": "Krea Flux Klein",
			"stub":        false,
		},
		{
			"id":          "imagine-1.5",
			"provider":    "imagine",
			"kind":        "image",
			"ratios":      []string{"1:3", "9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "3:1"},
			"resolutions": []string{"2K"},
			"description": "Imagine 1.5",
			"stub":        false,
		},
		{
			"id":          "imagine-1.5pro",
			"provider":    "imagine",
			"kind":        "image",
			"ratios":      []string{"1:3", "9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "3:1"},
			"resolutions": []string{"4K"},
			"description": "Imagine 1.5 Pro",
			"stub":        false,
		},
	}
	return items, nil
}
