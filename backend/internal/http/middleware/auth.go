package middleware

import (
	"net/http"

	"backend/internal/config"
	"backend/internal/service"
	"github.com/gin-gonic/gin"
)

const currentUserKey = "current_user"
const currentSessionKey = "current_session"

func RequireSession(auth *service.AuthService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieToken := readCookie(c, cfg.SessionCookieName)
		user, session, err := auth.CurrentUserFromRequest(
			c.Request.Context(),
			c.GetHeader("Authorization"),
			cookieToken,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to validate session"})
			c.Abort()
			return
		}
		if user == nil || session == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
			c.Abort()
			return
		}
		refreshSessionCookie(c, cfg, cookieToken)
		c.Set(currentUserKey, user)
		c.Set(currentSessionKey, session)
		c.Next()
	}
}

func RequireAdminSession(auth *service.AuthService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieToken := readCookie(c, cfg.SessionCookieName)
		user, session, err := auth.CurrentUserFromRequest(
			c.Request.Context(),
			c.GetHeader("Authorization"),
			cookieToken,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "failed to validate session"})
			c.Abort()
			return
		}
		if user == nil || session == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "未登录或会话已过期"})
			c.Abort()
			return
		}
		if user.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"detail": "需要管理员权限"})
			c.Abort()
			return
		}
		refreshSessionCookie(c, cfg, cookieToken)
		c.Set(currentUserKey, user)
		c.Set(currentSessionKey, session)
		c.Next()
	}
}

// refreshSessionCookie rolls the browser session cookie forward on each
// authenticated request that carried it. The server-side session already slides
// its TTL on use, but the cookie's Max-Age was frozen at login — so it would
// lapse mid-session and break cookie-only auth (e.g. <img> loads of private
// images) even while the SPA still looks logged in via its Bearer token. Only
// refresh when the request actually presented the cookie (a pure Bearer/API-key
// caller has none to roll).
func refreshSessionCookie(c *gin.Context, cfg *config.Config, cookieToken string) {
	if cookieToken == "" {
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cfg.SessionCookieName, cookieToken, int(cfg.SessionTTL.Seconds()), "/", "", cfg.CookieSecure, true)
}

func readCookie(c *gin.Context, name string) string {
	v, err := c.Cookie(name)
	if err != nil {
		return ""
	}
	return v
}
