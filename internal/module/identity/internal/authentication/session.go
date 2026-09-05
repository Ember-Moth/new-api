package authentication

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	identitycontract "github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/google/uuid"
)

const RefreshCookieName = "new_api_refresh"

// SessionHintCookieName is the script-readable companion to RefreshCookieName.
// See writeSessionHintCookie for why it exists and what it is not.
const SessionHintCookieName = "new_api_has_session"

// SessionHintCookieValue is the only value the hint ever carries.
const SessionHintCookieValue = "1"

var (
	ErrLoginSessionInvalid  = errors.New("login session is invalid")
	ErrLoginSessionRevoked  = errors.New("login session is revoked")
	ErrLoginSessionMismatch = errors.New("login session does not match the expected session")
	ErrRefreshTokenInvalid  = errors.New("refresh token is invalid")
	ErrRefreshRace          = errors.New("refresh token was already rotated")
)

type LoginSessionView = identitycontract.LoginSessionView

type AuthBundle = identitycontract.AuthBundle

func (r *Runtime) CreateLoginSession(userID int, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	return r.createLoginSession(userID, 0, loginMethod, ip, userAgent)
}

func (r *Runtime) CreateLoginSessionAtAuthVersion(userID int, expectedAuthVersion int64, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	if expectedAuthVersion <= 0 {
		return nil, ErrLoginSessionInvalid
	}
	return r.createLoginSession(userID, expectedAuthVersion, loginMethod, ip, userAgent)
}

func (r *Runtime) createLoginSession(userID int, expectedAuthVersion int64, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	user, err := r.deps.GetUserCache(userID)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled || user.AuthVersion <= 0 {
		return nil, ErrLoginSessionInvalid
	}
	if expectedAuthVersion > 0 && user.AuthVersion != expectedAuthVersion {
		return nil, ErrLoginSessionRevoked
	}
	now := time.Now().Unix()
	activeCount, err := r.deps.CountActiveUserSessions(userID, now)
	if err != nil {
		return nil, err
	}
	if activeCount >= int64(common.UserSessionActiveLimit) {
		return nil, identitycontract.ErrUserSessionLimit
	}
	issuanceCount, err := r.deps.CountUserSessionsCreatedSince(userID, now-common.UserSessionIssuanceWindowSeconds)
	if err != nil {
		return nil, err
	}
	if issuanceCount >= int64(common.UserSessionIssuanceLimit) {
		return nil, identitycontract.ErrUserSessionIssuanceLimit
	}
	refreshSecret, err := common.GenerateRandomCharsKey(64)
	if err != nil {
		return nil, err
	}
	session := &entity.UserSession{
		SID:             uuid.NewString(),
		UserID:          userID,
		Version:         1,
		UserAuthVersion: user.AuthVersion,
		Status:          "active",
		RefreshHash:     hashRefreshSecret(refreshSecret),
		LoginMethod:     strings.TrimSpace(loginMethod),
		IP:              truncateAuthMetadata(ip, 64),
		UserAgent:       truncateAuthMetadata(userAgent, 512),
		CreatedAt:       now,
		LastActiveAt:    now,
		ExpiresAt:       time.Unix(now, 0).Add(LoginSessionTTL).Unix(),
	}
	if session.LoginMethod == "" {
		session.LoginMethod = "unknown"
	}
	if err := r.deps.CreateUserSession(session); err != nil {
		return nil, err
	}
	bundle, err := issueAuthBundle(session, session.SID+"."+refreshSecret, true)
	if err != nil {
		_, _ = r.deps.RevokeUserSession(userID, session.SID, "token_issue_failed")
		return nil, err
	}
	return bundle, nil
}

func (r *Runtime) ValidateLoginSession(identity AuthIdentity) (*entity.UserSession, *entity.UserBase, error) {
	session, err := r.deps.GetUserSessionCached(identity.SessionID)
	if err != nil {
		if errors.Is(err, identitycontract.ErrUserSessionInactive) {
			return nil, nil, ErrLoginSessionRevoked
		}
		return nil, nil, err
	}
	now := time.Now().Unix()
	if session.UserID != identity.UserID || session.Status != "active" || session.RevokedAt != 0 || session.ExpiresAt <= now || session.Version != identity.SessionVersion || session.UserAuthVersion != identity.UserAuthVersion {
		return nil, nil, ErrLoginSessionRevoked
	}
	user, err := r.deps.GetUserCache(identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	if user.Status != common.UserStatusEnabled || user.AuthVersion != identity.UserAuthVersion {
		return nil, nil, ErrLoginSessionRevoked
	}
	return session, user, nil
}

// ValidateSessionReference validates a server-side flow bound to an existing
// dashboard session without requiring an access token on the callback request.
func (r *Runtime) ValidateSessionReference(userID int, sid string) (AuthIdentity, error) {
	if userID <= 0 || strings.TrimSpace(sid) == "" {
		return AuthIdentity{}, ErrLoginSessionInvalid
	}
	session, err := r.deps.GetUserSessionCached(sid)
	if err != nil {
		return AuthIdentity{}, err
	}
	identity := AuthIdentity{
		UserID:          userID,
		SessionID:       sid,
		UserAuthVersion: session.UserAuthVersion,
		SessionVersion:  session.Version,
	}
	if _, _, err := r.ValidateLoginSession(identity); err != nil {
		return AuthIdentity{}, err
	}
	return identity, nil
}

// AdvanceCurrentSessionSecurity increments the user's global auth version,
// preserves only the current browser session at a new session version and
// returns a replacement access token. Call after a successful 2FA/passkey
// security-setting mutation that did not already advance AuthVersion.
func (r *Runtime) AdvanceCurrentSessionSecurity(identity AuthIdentity, reason string) (*AuthBundle, error) {
	nextUserAuthVersion, err := r.deps.BumpUserAuthVersion(identity.UserID)
	if err != nil {
		return nil, err
	}
	return r.advanceCurrentSessionToVersion(identity, nextUserAuthVersion, reason)
}

// AdvanceCurrentSessionToUserVersion is used when the security mutation and
// AuthVersion increment were committed in the same transaction (for example,
// a password change).
func (r *Runtime) AdvanceCurrentSessionToUserVersion(identity AuthIdentity, reason string) (*AuthBundle, error) {
	user, err := r.deps.GetUserCache(identity.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled || user.AuthVersion <= identity.UserAuthVersion {
		return nil, ErrLoginSessionRevoked
	}
	return r.advanceCurrentSessionToVersion(identity, user.AuthVersion, reason)
}

func (r *Runtime) advanceCurrentSessionToVersion(identity AuthIdentity, nextUserAuthVersion int64, reason string) (*AuthBundle, error) {
	session, err := r.deps.AdvanceUserSessionAuthVersion(
		identity.UserID,
		identity.SessionID,
		identity.SessionVersion,
		identity.UserAuthVersion,
		nextUserAuthVersion,
	)
	if err != nil {
		return nil, err
	}
	if _, err := r.deps.RevokeOtherUserSessions(identity.UserID, identity.SessionID, reason); err != nil {
		return nil, err
	}
	return issueAuthBundle(session, "", true)
}

func (r *Runtime) RefreshLoginSession(rawRefreshToken, expectedSID, ip, userAgent string) (*AuthBundle, *entity.User, error) {
	sid, secret, ok := splitRefreshToken(rawRefreshToken)
	if !ok {
		return nil, nil, ErrRefreshTokenInvalid
	}
	if expectedSID = strings.TrimSpace(expectedSID); expectedSID != "" && expectedSID != sid {
		return nil, nil, ErrLoginSessionMismatch
	}
	session, err := r.deps.GetUserSessionCached(sid)
	if err != nil {
		if errors.Is(err, identitycontract.ErrUserSessionInactive) {
			return nil, nil, ErrLoginSessionRevoked
		}
		return nil, nil, ErrRefreshTokenInvalid
	}
	if session.Status != "active" || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return nil, nil, ErrLoginSessionRevoked
	}
	userCache, err := r.deps.GetUserCache(session.UserID)
	if err != nil {
		return nil, nil, err
	}
	currentUser, err := r.deps.GetUserById(session.UserID, false)
	if err != nil {
		return nil, nil, err
	}
	if userCache.Status != common.UserStatusEnabled || userCache.AuthVersion != session.UserAuthVersion ||
		currentUser.Status != common.UserStatusEnabled || currentUser.AuthVersion != session.UserAuthVersion {
		_, _ = r.deps.RevokeUserSession(session.UserID, session.SID, "user_security_changed")
		return nil, nil, ErrLoginSessionRevoked
	}
	nextSecret := deriveNextRefreshSecret(sid, secret)
	rotated, err := r.deps.RotateUserSessionRefresh(session.UserID, sid, hashRefreshSecret(secret), hashRefreshSecret(nextSecret), time.Now().Unix(), RefreshReplayWindow)
	if err != nil {
		if errors.Is(err, identitycontract.ErrUserSessionRefreshRace) && rotated != nil &&
			hashRefreshSecret(nextSecret) == rotated.RefreshHash {
			bundle, issueErr := issueAuthBundle(rotated, sid+"."+nextSecret, true)
			if issueErr != nil {
				return nil, nil, issueErr
			}
			return bundle, currentUser, nil
		}
		if errors.Is(err, identitycontract.ErrUserSessionRefreshReuse) {
			return nil, nil, ErrLoginSessionRevoked
		}
		if errors.Is(err, identitycontract.ErrUserSessionRefreshInvalid) {
			return nil, nil, ErrRefreshTokenInvalid
		}
		if errors.Is(err, identitycontract.ErrUserSessionRefreshRace) {
			return nil, nil, ErrRefreshRace
		}
		return nil, nil, err
	}
	rotated.IP = truncateAuthMetadata(ip, 64)
	rotated.UserAgent = truncateAuthMetadata(userAgent, 512)
	bundle, err := issueAuthBundle(rotated, sid+"."+nextSecret, true)
	if err != nil {
		return nil, nil, err
	}
	return bundle, currentUser, nil
}

func (r *Runtime) RevokeByRefreshToken(rawRefreshToken, expectedSID, reason string) error {
	sid, secret, ok := splitRefreshToken(rawRefreshToken)
	if !ok {
		return nil
	}
	if expectedSID = strings.TrimSpace(expectedSID); expectedSID != "" && expectedSID != sid {
		return ErrLoginSessionMismatch
	}
	_, err := r.deps.RevokeUserSessionByRefreshHash(sid, hashRefreshSecret(secret), reason)
	return err
}

func RefreshTokenSID(rawRefreshToken string) (string, bool) {
	sid, _, ok := splitRefreshToken(rawRefreshToken)
	return sid, ok
}

func (r *Runtime) ListLoginSessions(userID int, currentSID string) ([]LoginSessionView, error) {
	sessions, err := r.deps.ListActiveUserSessions(userID, currentSID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	views := make([]LoginSessionView, 0, len(sessions))
	for i := range sessions {
		views = append(views, sessionView(&sessions[i], sessions[i].SID == currentSID))
	}
	return views, nil
}

func issueAuthBundle(session *entity.UserSession, rawRefreshToken string, current bool) (*AuthBundle, error) {
	identity := AuthIdentity{
		UserID:          session.UserID,
		SessionID:       session.SID,
		UserAuthVersion: session.UserAuthVersion,
		SessionVersion:  session.Version,
	}
	accessToken, accessExpiresAt, err := IssueAccessToken(identity)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{
		AccessToken:     accessToken,
		TokenType:       "Bearer",
		AccessExpiresAt: accessExpiresAt,
		Session:         sessionView(session, current),
		RefreshToken:    rawRefreshToken,
	}, nil
}

func sessionView(session *entity.UserSession, current bool) LoginSessionView {
	return LoginSessionView{
		SID:          session.SID,
		Current:      current,
		LoginMethod:  session.LoginMethod,
		IP:           session.IP,
		UserAgent:    session.UserAgent,
		CreatedAt:    session.CreatedAt,
		LastActiveAt: session.LastActiveAt,
		ExpiresAt:    session.ExpiresAt,
	}
}

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

func hashRefreshSecret(secret string) string {
	return common.GenerateHMACWithKey(authSigningKey("refresh"), secret)
}

func deriveNextRefreshSecret(sid, currentSecret string) string {
	return common.GenerateHMACWithKey(authSigningKey("refresh-rotate"), sid+"."+currentSecret)
}

func truncateAuthMetadata(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func authSessionErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, identitycontract.ErrUserSessionLimit):
		return http.StatusConflict, "AUTH_SESSION_LIMIT"
	case errors.Is(err, identitycontract.ErrUserSessionIssuanceLimit):
		return http.StatusTooManyRequests, "AUTH_SESSION_ISSUANCE_LIMIT"
	case errors.Is(err, ErrLoginSessionMismatch):
		return http.StatusConflict, "AUTH_SESSION_MISMATCH"
	case errors.Is(err, ErrRefreshRace):
		return http.StatusConflict, "AUTH_REFRESH_RACE"
	case errors.Is(err, ErrAuthTokenExpired):
		return http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED"
	case errors.Is(err, ErrLoginSessionRevoked):
		return http.StatusUnauthorized, "AUTH_SESSION_REVOKED"
	case errors.Is(err, ErrRefreshTokenInvalid), errors.Is(err, ErrAuthTokenInvalid):
		return http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	default:
		return http.StatusInternalServerError, "AUTH_INTERNAL_ERROR"
	}
}

func AuthSessionErrorCode(err error) (int, string) {
	return authSessionErrorCode(err)
}

func FormatAuthError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("authentication failed: %v", err)
}

func (r *Runtime) RevokeSession(userID int, sid, reason string) (bool, error) {
	return r.deps.RevokeUserSession(userID, sid, reason)
}

func (r *Runtime) RevokeOtherSessions(userID int, sid, reason string) (int64, error) {
	return r.deps.RevokeOtherUserSessions(userID, sid, reason)
}
