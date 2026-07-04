package handler

import (
	"net/http"

	"backend/internal/repo"
	"github.com/gin-gonic/gin"
)

// BannedWordsHandler — admin 违禁词管理: list / add / delete prompt blocklist
// entries. The generation path (V1Service.checkBannedPrompt) enforces them.
type BannedWordsHandler struct {
	words *repo.BannedWordRepository
}

func NewBannedWordsHandler(words *repo.BannedWordRepository) *BannedWordsHandler {
	return &BannedWordsHandler{words: words}
}

func (h *BannedWordsHandler) List(c *gin.Context) {
	items, err := h.words.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to load banned words"})
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, w := range items {
		out = append(out, gin.H{
			"id":         w.ID,
			"word":       w.Word,
			"hits":       w.Hits,
			"created_at": w.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *BannedWordsHandler) Create(c *gin.Context) {
	var body struct {
		Word string `json:"word"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid request body"})
		return
	}
	item, err := h.words.Create(c.Request.Context(), body.Word)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": item.ID, "word": item.Word, "hits": item.Hits, "created_at": item.CreatedAt}})
}

func (h *BannedWordsHandler) Delete(c *gin.Context) {
	n, err := h.words.Delete(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "delete failed"})
		return
	}
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
