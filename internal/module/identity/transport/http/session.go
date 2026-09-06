package identityhttp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	identitymodule "github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/authn"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *Handler) RefreshAuth(c *gin.Context) {
	setAuthNoStore(c)
	rawRefreshToken, err := c.Cookie(authn.RefreshCookieName)
	if err != nil || rawRefreshToken == "" {
		h.clearRefreshCookie(c)
		writeAuthSessionError(c, authn.ErrRefreshTokenInvalid)
		return
	}
	bundle, user, err := h.identity.Authentication.RefreshLoginSession(rawRefreshToken, c.GetHeader("X-Auth-Session"), c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, authn.ErrRefreshTokenInvalid) || errors.Is(err, authn.ErrLoginSessionRevoked) {
			h.clearRefreshCookie(c)
		}
		writeAuthSessionError(c, err)
		return
	}
	h.writeRefreshCookie(c, bundle.RefreshToken)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"user":              identitymodule.SelfUserData(user, user.Role, h.identity.UserCapabilities(user.Id, user.Role)),
			"session":           bundle.Session,
		},
	})
}

func (h *Handler) AuthLogout(c *gin.Context) {
	setAuthNoStore(c)
	expectedSID := strings.TrimSpace(c.GetHeader("X-Auth-Session"))
	rawRefreshToken, cookieErr := c.Cookie(authn.RefreshCookieName)
	cookieSID, hasCookieSID := authn.RefreshTokenSID(rawRefreshToken)
	if expectedSID != "" && cookieErr == nil && hasCookieSID && cookieSID != expectedSID {
		writeAuthSessionError(c, authn.ErrLoginSessionMismatch)
		return
	}

	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok {
		if identity, err := authn.ParseAccessToken(rawAccessToken); err == nil {
			if expectedSID != "" && expectedSID != identity.SessionID {
				writeAuthSessionError(c, authn.ErrLoginSessionMismatch)
				return
			}
			if _, err := h.identity.Authentication.RevokeSession(identity.UserID, identity.SessionID, "logout"); err != nil {
				writeAuthSessionError(c, err)
				return
			}
			cookieCleared := false
			if cookieErr == nil && hasCookieSID && cookieSID == identity.SessionID {
				if err := h.identity.Authentication.RevokeByRefreshToken(rawRefreshToken, identity.SessionID, "logout"); err != nil {
					writeAuthSessionError(c, err)
					return
				}
				h.clearRefreshCookie(c)
				cookieCleared = true
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "",
				"data":    gin.H{"revoked_sid": identity.SessionID, "cookie_cleared": cookieCleared},
			})
			return
		}
	}
	if cookieErr != nil || rawRefreshToken == "" {
		h.clearRefreshCookie(c)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	}
	if err := h.identity.Authentication.RevokeByRefreshToken(rawRefreshToken, expectedSID, "logout"); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	h.clearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func (h *Handler) GetLoginSessions(c *gin.Context) {
	identity, ok := h.requireBrowserSession(c)
	if !ok {
		return
	}
	sessions, err := h.identity.Authentication.ListLoginSessions(identity.UserID, identity.SessionID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sessions})
}

func (h *Handler) DeleteLoginSession(c *gin.Context) {
	identity, ok := h.requireBrowserSession(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "AUTH_SESSION_ID_REQUIRED", "message": "session id is required"})
		return
	}
	revoked, err := h.identity.Authentication.RevokeSession(identity.UserID, sid, "user_revoked")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "AUTH_SESSION_NOT_FOUND", "message": "session not found"})
		return
	}
	if rawRefreshToken, cookieErr := c.Cookie(authn.RefreshCookieName); cookieErr == nil {
		cookieSID, ok := authn.RefreshTokenSID(rawRefreshToken)
		if ok && cookieSID == sid {
			h.clearRefreshCookie(c)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_sid": sid, "current": sid == identity.SessionID}})
}

func (h *Handler) RevokeOtherLoginSessions(c *gin.Context) {
	identity, ok := h.requireBrowserSession(c)
	if !ok {
		return
	}
	count, err := h.identity.Authentication.RevokeOtherSessions(identity.UserID, identity.SessionID, "user_revoked_others")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_count": count}})
}

func (h *Handler) requireBrowserSession(c *gin.Context) (authn.AuthIdentity, bool) {
	identity, ok := h.sessionIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_REQUIRED",
			"message": "a dashboard login session is required",
		})
		return authn.AuthIdentity{}, false
	}
	return identity, true
}

func writeAuthSessionError(c *gin.Context, err error) {
	status, code := authn.AuthSessionErrorCode(err)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code = http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	}
	if status == http.StatusInternalServerError {
		// The response body only carries the generic AUTH_INTERNAL_ERROR
		// code; without this log the underlying Redis/database/session
		// failure is indistinguishable from the client side.
		logger.LogError(c.Request.Context(), fmt.Sprintf("auth session internal error (%s %s): %v", c.Request.Method, c.Request.URL.Path, err))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": http.StatusText(status)})
}

func setAuthNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

func dashboardBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (h *Handler) sessionIdentity(c *gin.Context) (authn.AuthIdentity, bool) {
	if h.hooks.SessionIdentity == nil {
		return authn.AuthIdentity{}, false
	}
	return h.hooks.SessionIdentity(c)
}
func (h *Handler) clearRefreshCookie(c *gin.Context) {
	if h.hooks.ClearRefreshCookie != nil {
		h.hooks.ClearRefreshCookie(c)
	}
}
func (h *Handler) writeRefreshCookie(c *gin.Context, token string) {
	if h.hooks.WriteRefreshCookie != nil {
		h.hooks.WriteRefreshCookie(c, token)
	}
}
