package handler

import (
	"net/http"
	"strings"

	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

type SiteSettingsHandler struct {
	site *service.SiteService
}

func NewSiteSettingsHandler(site *service.SiteService) *SiteSettingsHandler {
	return &SiteSettingsHandler{site: site}
}

func (h *SiteSettingsHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	title, err := h.site.Title(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load site settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"title":    title,
		"logo":     h.site.Logo(ctx),
		"subtitle": h.site.Subtitle(ctx),
		"contact":  h.site.Contact(ctx),
	})
}

func (h *SiteSettingsHandler) Put(c *gin.Context) {
	ctx := c.Request.Context()
	var body struct {
		Title    string          `json:"title"`
		Logo     string          `json:"logo"`
		Subtitle string          `json:"subtitle"`
		Contact  service.Contact `json:"contact"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "网页主标题不能为空"})
		return
	}
	updated, err := h.site.SetTitle(ctx, title)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to save site settings"})
		return
	}
	if err := h.site.SetBranding(ctx, body.Logo, body.Subtitle); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to save branding"})
		return
	}
	if err := h.site.SetContact(ctx, body.Contact); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to save contact info"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
		"title": updated, "logo": h.site.Logo(ctx), "subtitle": h.site.Subtitle(ctx),
		"contact": h.site.Contact(ctx),
	}})
}
