package handler

import (
	"io"
	"net/http"

	"backend/internal/config"
	"backend/internal/service"
	"backend/internal/storage"
	"github.com/gin-gonic/gin"
)

type ImageHandler struct {
	cfg         *config.Config
	imageAccess *service.ImageAccessService
	store       *storage.Client
}

func NewImageHandler(cfg *config.Config, imageAccess *service.ImageAccessService, store *storage.Client) *ImageHandler {
	return &ImageHandler{
		cfg:         cfg,
		imageAccess: imageAccess,
		store:       store,
	}
}

// Serve is the backwards-compatible /images reader for URLs persisted by older
// releases. New releases return the public R2 custom-domain URL directly. It
// gates access (public showcase images, or a logged-in cookie — a regular
// user only their own images, an admin anyone's) and then PROXIES the object
// from R2. Nothing is read from local disk; the R2 endpoint is never
// exposed to the client.
func (h *ImageHandler) Serve(c *gin.Context) {
	user := c.Param("user")
	name := c.Param("name")

	rel, err := h.imageAccess.Resolve(user, name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid path"})
		return
	}

	// A thumbnail shares its original's visibility, and old images without a
	// stored thumb fall back to the original object.
	origRel := rel
	if service.IsThumbKey(rel) {
		origRel = service.OrigKey(rel)
	} else if service.IsLastFrameKey(rel) {
		origRel = service.LastFrameOrigKey(rel)
	}

	public, err := h.imageAccess.IsPublic(c.Request.Context(), origRel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to authorize image"})
		return
	}
	if !public {
		authorized, err := h.imageAccess.IsAuthorized(
			c.Request.Context(),
			readCookie(c, h.cfg.SessionCookieName),
			user,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to authorize image"})
			return
		}
		if !authorized {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "需要登录后访问"})
			return
		}
	}

	// Forward Range so the browser can seek within videos.
	resp, err := h.store.Get(c.Request.Context(), rel, c.GetHeader("Range"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"detail": "failed to fetch object"})
		return
	}
	if resp.StatusCode == http.StatusNotFound && origRel != rel {
		resp.Body.Close()
		resp, err = h.store.Get(c.Request.Context(), origRel, c.GetHeader("Range"))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"detail": "failed to fetch object"})
			return
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	for _, hdr := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "Content-Range", "Last-Modified", "ETag", "Cache-Control"} {
		if v := resp.Header.Get(hdr); v != "" {
			c.Header(hdr, v)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func readCookie(c *gin.Context, name string) string {
	v, err := c.Cookie(name)
	if err != nil {
		return ""
	}
	return v
}
