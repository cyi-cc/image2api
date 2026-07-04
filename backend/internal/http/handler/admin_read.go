package handler

import (
	"net/http"
	"strconv"
	"time"

	"backend/internal/model"
	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

type AdminReadHandler struct {
	admin *service.AdminReadService
}

func NewAdminReadHandler(admin *service.AdminReadService) *AdminReadHandler {
	return &AdminReadHandler{admin: admin}
}

func (h *AdminReadHandler) Users(c *gin.Context) {
	users, stats, err := h.admin.Users(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load users"})
		return
	}

	out := make([]gin.H, 0, len(users))
	for _, user := range users {
		row := userPublic(user)
		row["generation_count"] = user.GenerationCount
		row["banned_word_hits"] = user.BannedWordHits
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "stats": stats})
}

func (h *AdminReadHandler) Models(c *gin.Context) {
	items, err := h.admin.ModelsView(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load models"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (h *AdminReadHandler) Logs(c *gin.Context) {
	limit := parseInt(c.Query("limit"), 50)
	offset := parseInt(c.Query("offset"), 0)
	kind := c.Query("kind")
	status := c.Query("status")
	var since *time.Time
	if raw := c.Query("since"); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			t := time.Unix(int64(f), 0)
			since = &t
		}
	}

	items, total, stats, err := h.admin.Logs(c.Request.Context(), limit, offset, kind, status, nil, since, "", "", c.Query("source"), false, false, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}
	// Resolve user_id -> display name once for the page (mirrors admin.py).
	nameByID, err := h.admin.UserNameMap(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load logs"})
		return
	}
	// Resolve account_id -> account label (email) so the log table can show which
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
		var accountName any
		if item.AccountID != "" {
			if label, ok := accountByID[item.AccountID]; ok {
				accountName = label
			} else {
				accountName = item.AccountID
			}
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
			"source":     item.Source,
			"user_id":    emptyStringNil(item.UserID),
			"user_name":  userName,
			"account_id": emptyStringNil(item.AccountID),
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

func (h *AdminReadHandler) Stats(c *gin.Context) {
	stats, err := h.admin.Stats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load stats"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (h *AdminReadHandler) Dashboard(c *gin.Context) {
	data, err := h.admin.Dashboard(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load dashboard"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *AdminReadHandler) Invites(c *gin.Context) {
	items, stats, err := h.admin.Invites(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load invites"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "stats": stats})
}

// DeleteImage removes one generated file (plus derived stills) and blanks the
// log rows referencing it. Admin 图片管理 delete; ?name= is the storage key.
func (h *AdminReadHandler) DeleteImage(c *gin.Context) {
	if err := h.admin.DeleteFile(c.Request.Context(), c.Query("name")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AdminReadHandler) Providers(c *gin.Context) {
	items, err := h.admin.Providers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load providers"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (h *AdminReadHandler) Images(c *gin.Context) {
	limit := parseInt(c.Query("limit"), 30)
	offset := parseInt(c.Query("offset"), 0)
	kind := c.Query("kind")
	items, total, stats, err := h.admin.Images(c.Request.Context(), limit, offset, kind)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load images"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":   items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"stats":  stats,
	})
}

func userPublic(user model.User) gin.H {
	keys := make([]gin.H, 0, len(user.APIKeys))
	for _, key := range user.APIKeys {
		keys = append(keys, gin.H{
			"id":           key.ID,
			"name":         key.Name,
			"key_preview":  key.KeyPreview,
			"created_at":   unixSec(key.CreatedAt),
			"last_used_at": unixSecPtr(key.LastUsedAt),
		})
	}
	return gin.H{
		"id":                   user.ID,
		"email":                user.Email,
		"name":                 user.Name,
		"role":                 user.Role,
		"status":               user.Status,
		"credits":              user.Credits,
		"notes":                user.Notes,
		"recharge_total":       user.RechargeTotal,
		"concurrency_group_id": user.ConcurrencyGroupID,
		"created_at":           unixSec(user.CreatedAt),
		"last_login_at":        unixSecPtr(user.LastLoginAt),
		"last_login_ip":        user.LastLoginIP,
		"invite_code":          user.InviteCode,
		"invited_by":           user.InvitedBy,
		"checkin_last":         user.CheckinLast,
		"checkin_streak":       user.CheckinStreak,
		"api_keys":             keys,
		"has_password":         user.PasswordHash != "",
	}
}

// unixSec / unixSecPtr render timestamps as unix SECONDS — the frontend's
// fmtTs/fmtRelative expect seconds (matching the Python reference's time.time()),
// not the RFC3339 string Go marshals a time.Time into (which parses to NaN → "—").
func unixSec(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func unixSecPtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

func parseInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return fallback
}

func emptyStringNil(v string) any {
	if v == "" {
		return nil
	}
	return v
}
