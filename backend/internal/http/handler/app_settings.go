package handler

import (
	"encoding/base64"
	"net/http"
	"strings"

	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

type AppSettingsHandler struct {
	settings *service.AppSettingsService
}

func NewAppSettingsHandler(settings *service.AppSettingsService) *AppSettingsHandler {
	return &AppSettingsHandler{settings: settings}
}

// LogoUpload stores a base64 image as the site logo in R2 (deleting the old
// one) and persists site.logo. Called on 保存 — not on file pick.
func (h *AppSettingsHandler) LogoUpload(c *gin.Context) {
	var body struct {
		Data        string `json:"data"` // base64, optionally a "data:...;base64," URL
		ContentType string `json:"content_type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	raw := strings.TrimSpace(body.Data)
	if strings.HasPrefix(raw, "data:") {
		if i := strings.Index(raw, ","); i >= 0 {
			if body.ContentType == "" {
				meta := raw[5:i] // e.g. image/png;base64
				if j := strings.Index(meta, ";"); j >= 0 {
					body.ContentType = meta[:j]
				}
			}
			raw = raw[i+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "图片解码失败"})
		return
	}
	url, err := h.settings.UploadLogo(c.Request.Context(), data, body.ContentType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "logo": url})
}

// AssetUpload stores a public image (homepage 底图 etc.) in R2 and returns
// its storage path for the caller to save (e.g. as a showcase card's image).
func (h *AppSettingsHandler) AssetUpload(c *gin.Context) {
	var body struct {
		Data        string `json:"data"`
		ContentType string `json:"content_type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	raw := strings.TrimSpace(body.Data)
	if strings.HasPrefix(raw, "data:") {
		if i := strings.Index(raw, ","); i >= 0 {
			if body.ContentType == "" {
				meta := raw[5:i]
				if j := strings.Index(meta, ";"); j >= 0 {
					body.ContentType = meta[:j]
				}
			}
			raw = raw[i+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "图片解码失败"})
		return
	}
	path, err := h.settings.UploadAsset(c.Request.Context(), data, body.ContentType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "path": path})
}

// LogoDelete removes the uploaded logo and falls back to the built-in default.
func (h *AppSettingsHandler) LogoDelete(c *gin.Context) {
	if err := h.settings.RemoveLogo(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to remove logo"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "logo": ""})
}

func (h *AppSettingsHandler) RegistrationGet(c *gin.Context) {
	data, err := h.settings.Registration(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load registration settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) RegistrationPut(c *gin.Context) {
	var body service.RegistrationSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveRegistration(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) SMTPGet(c *gin.Context) {
	data, err := h.settings.SMTP(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load smtp settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) SMTPPut(c *gin.Context) {
	var body service.SMTPSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveSMTP(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) SMTPTest(c *gin.Context) {
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	if err := h.settings.TestSMTP(c.Request.Context(), body.Email); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "detail": "测试邮件已发送"})
}

func (h *AppSettingsHandler) ProxyGet(c *gin.Context) {
	data, err := h.settings.Proxy(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load proxy settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) ProxyPut(c *gin.Context) {
	var body struct {
		Proxy string `json:"proxy"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveProxy(c.Request.Context(), body.Proxy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) ProxyTest(c *gin.Context) {
	var body struct {
		Proxy string `json:"proxy"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.TestProxy(c.Request.Context(), body.Proxy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) R2Get(c *gin.Context) {
	data, err := h.settings.R2(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load R2 settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) R2Put(c *gin.Context) {
	var body service.R2Settings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveR2(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) R2Test(c *gin.Context) {
	if err := h.settings.TestR2(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "detail": "R2 写入和删除测试成功"})
}

func (h *AppSettingsHandler) CreditsGet(c *gin.Context) {
	data, err := h.settings.Credits(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load credit settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) CreditsPut(c *gin.Context) {
	var body service.CreditSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveCredits(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

// DeAIGet returns the 去AI特征 per-tier surcharge. Also mounted publicly (the
// 画图台 needs it to show the price next to the toggle) — prices aren't secret.
func (h *AppSettingsHandler) DeAIGet(c *gin.Context) {
	data, err := h.settings.DeAI(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load deai settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) DeAIPut(c *gin.Context) {
	var body service.DeAISettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveDeAI(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) AdobeGet(c *gin.Context) {
	data, err := h.settings.Adobe(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load Adobe settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) AdobePut(c *gin.Context) {
	var body service.AdobeSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveAdobe(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) LogsGet(c *gin.Context) {
	data, err := h.settings.Logs(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load log settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) LogsPut(c *gin.Context) {
	var body struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveLogs(c.Request.Context(), body.RetentionDays)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func (h *AppSettingsHandler) MediaGet(c *gin.Context) {
	data, err := h.settings.Media(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load media settings"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AppSettingsHandler) MediaPut(c *gin.Context) {
	var body struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	data, err := h.settings.SaveMedia(c.Request.Context(), body.RetentionDays)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data.Settings, "removed": data.Removed, "freed_bytes": data.FreedBytes})
}
