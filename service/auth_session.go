package service

import (
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/authn"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AuthBundle = authn.AuthBundle
type LoginSessionView = authn.LoginSessionView

const RefreshCookieName = authn.RefreshCookieName
const SessionHintCookieName = authn.SessionHintCookieName
const SessionHintCookieValue = authn.SessionHintCookieValue

var ErrLoginSessionInvalid = authn.ErrLoginSessionInvalid
var ErrLoginSessionRevoked = authn.ErrLoginSessionRevoked
var ErrLoginSessionMismatch = authn.ErrLoginSessionMismatch
var ErrRefreshTokenInvalid = authn.ErrRefreshTokenInvalid
var ErrRefreshRace = authn.ErrRefreshRace

// AuthenticationRuntime adapts storage until user/session caches move into identity.
func AuthenticationRuntime() *authn.Runtime {
	return authn.New(authn.Dependencies{
		GetUserCache: func(id int) (*entity.UserBase, error) {
			user, err := model.GetUserCache(id)
			return (*entity.UserBase)(user), err
		},
		GetUserById: func(id int, all bool) (*entity.User, error) {
			user, err := model.GetUserById(id, all)
			return (*entity.User)(user), err
		},
		BumpUserAuthVersion:           model.BumpUserAuthVersion,
		CountActiveUserSessions:       model.CountActiveUserSessions,
		CountUserSessionsCreatedSince: model.CountUserSessionsCreatedSince,
		CreateUserSession:             func(session *entity.UserSession) error { return model.CreateUserSession((*model.UserSession)(session)) },
		GetUserSessionCached: func(sid string) (*entity.UserSession, error) {
			session, err := model.GetUserSessionCached(sid)
			return (*entity.UserSession)(session), err
		},
		RevokeUserSession: model.RevokeUserSession,
		AdvanceUserSessionAuthVersion: func(id int, sid string, sv, uv, next int64) (*entity.UserSession, error) {
			session, err := model.AdvanceUserSessionAuthVersion(id, sid, sv, uv, next)
			return (*entity.UserSession)(session), err
		},
		RevokeOtherUserSessions: model.RevokeOtherUserSessions,
		RotateUserSessionRefresh: func(id int, sid, current, next string, now int64, grace time.Duration) (*entity.UserSession, error) {
			session, err := model.RotateUserSessionRefresh(id, sid, current, next, now, grace)
			return (*entity.UserSession)(session), err
		},
		RevokeUserSessionByRefreshHash: model.RevokeUserSessionByRefreshHash,
		ListActiveUserSessions: func(id int, sid string, now int64) ([]entity.UserSession, error) {
			rows, err := model.ListActiveUserSessions(id, sid, now)
			result := make([]entity.UserSession, len(rows))
			for i := range rows {
				result[i] = entity.UserSession(rows[i])
			}
			return result, err
		},
	})
}
func CreateLoginSession(userID int, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	return AuthenticationRuntime().CreateLoginSession(userID, loginMethod, ip, userAgent)
}
func CreateLoginSessionAtAuthVersion(userID int, expectedAuthVersion int64, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	return AuthenticationRuntime().CreateLoginSessionAtAuthVersion(userID, expectedAuthVersion, loginMethod, ip, userAgent)
}
func ValidateLoginSession(identity AuthIdentity) (*model.UserSession, *model.UserBase, error) {
	session, user, err := AuthenticationRuntime().ValidateLoginSession(identity)
	return (*model.UserSession)(session), (*model.UserBase)(user), err
}
func ValidateSessionReference(userID int, sid string) (AuthIdentity, error) {
	return AuthenticationRuntime().ValidateSessionReference(userID, sid)
}
func AdvanceCurrentSessionSecurity(identity AuthIdentity, reason string) (*AuthBundle, error) {
	return AuthenticationRuntime().AdvanceCurrentSessionSecurity(identity, reason)
}
func AdvanceCurrentSessionToUserVersion(identity AuthIdentity, reason string) (*AuthBundle, error) {
	return AuthenticationRuntime().AdvanceCurrentSessionToUserVersion(identity, reason)
}
func RefreshLoginSession(rawRefreshToken, expectedSID, ip, userAgent string) (*AuthBundle, *model.User, error) {
	bundle, user, err := AuthenticationRuntime().RefreshLoginSession(rawRefreshToken, expectedSID, ip, userAgent)
	return bundle, (*model.User)(user), err
}
func RevokeByRefreshToken(rawRefreshToken, expectedSID, reason string) error {
	return AuthenticationRuntime().RevokeByRefreshToken(rawRefreshToken, expectedSID, reason)
}
func ListLoginSessions(userID int, currentSID string) ([]LoginSessionView, error) {
	return AuthenticationRuntime().ListLoginSessions(userID, currentSID)
}
func RefreshTokenSID(rawRefreshToken string) (string, bool) {
	return authn.RefreshTokenSID(rawRefreshToken)
}
func AuthSessionErrorCode(err error) (int, string) { return authn.AuthSessionErrorCode(err) }
func FormatAuthError(err error) string             { return authn.FormatAuthError(err) }
func splitRefreshToken(raw string) (string, string, bool) {
	sid, secret, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok || sid == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", "", false
	}
	return sid, secret, true
}
func WriteRefreshCookie(c *gin.Context, rawToken string) {
	expiresAt := time.Now().Add(LoginSessionTTL)
	if sid, _, ok := splitRefreshToken(rawToken); ok {
		if session, err := model.GetUserSessionCached(sid); err == nil && session.ExpiresAt > time.Now().Unix() {
			expiresAt = time.Unix(session.ExpiresAt, 0)
		}
	}
	maxAge := int(time.Until(expiresAt) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    rawToken,
		Path:     "/api/user/auth",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	writeSessionHintCookie(c, maxAge, expiresAt)
}

func ClearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     "/api/user/auth",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	clearSessionHintCookie(c)
}

// writeSessionHintCookie mirrors the Refresh Cookie's lifetime with a
// script-readable marker. The Refresh Cookie itself is HttpOnly and scoped to
// /api/user/auth, so a page at / cannot tell whether a login session exists;
// without this hint the frontend has to POST /api/user/auth/refresh on every
// cold boot just to learn that an anonymous visitor is anonymous. That request
// is guaranteed to 401 and still consumes a slot of the IP-keyed
// CriticalRateLimit budget shared by everyone behind the same address.
//
// The value is the constant "1" and carries no credential: it states that a
// Refresh Cookie was issued, never who for. Authorization still derives solely
// from the Refresh Cookie and the Access Token, so forging this hint only costs
// the forger the round trip it was meant to avoid.
//
// It must be written and cleared in lockstep with the Refresh Cookie, which is
// why it lives inside these two helpers rather than at their call sites: both
// cookies then ride the same response with the same expiry, and no login path
// can set one without the other.
func writeSessionHintCookie(c *gin.Context, maxAge int, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionHintCookieName,
		Value:    SessionHintCookieValue,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: false,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionHintCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionHintCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: false,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}
