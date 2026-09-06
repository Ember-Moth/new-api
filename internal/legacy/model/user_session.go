package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"

	identitycontract "github.com/QuantumNous/new-api/internal/module/identity/contract"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/sessions"
)

type UserSession = identityentity.UserSession

const (
	UserSessionStatusActive  = "active"
	UserSessionStatusRevoked = "revoked"
)

var ErrUserSessionInvalid = identitycontract.ErrUserSessionInvalid
var ErrUserSessionInactive = identitycontract.ErrUserSessionInactive
var ErrUserSessionRefreshInvalid = identitycontract.ErrUserSessionRefreshInvalid
var ErrUserSessionRefreshRace = identitycontract.ErrUserSessionRefreshRace
var ErrUserSessionRefreshReuse = identitycontract.ErrUserSessionRefreshReuse
var ErrUserSessionLimit = identitycontract.ErrUserSessionLimit
var ErrUserSessionIssuanceLimit = identitycontract.ErrUserSessionIssuanceLimit

func CreateUserSession(session *UserSession) error {
	return sessions.New(DB, common.RDB).CreateUserSession(session)
}
func CountActiveUserSessions(userID int, now int64) (int64, error) {
	return sessions.New(DB, common.RDB).CountActiveUserSessions(userID, now)
}
func CountUserSessionsCreatedSince(userID int, createdAfter int64) (int64, error) {
	return sessions.New(DB, common.RDB).CountUserSessionsCreatedSince(userID, createdAfter)
}
func CountUserSessionsCreatedSinceWithContext(ctx context.Context, userID int, createdAfter int64) (int64, error) {
	return sessions.New(DB, common.RDB).CountUserSessionsCreatedSinceWithContext(ctx, userID, createdAfter)
}
func GetUserSessionBySID(sid string) (*UserSession, error) {
	return sessions.New(DB, common.RDB).GetUserSessionBySID(sid)
}
func GetUserSessionCached(sid string) (*UserSession, error) {
	return sessions.New(DB, common.RDB).GetUserSessionCached(sid)
}
func ListActiveUserSessions(userID int, currentSID string, now int64) ([]UserSession, error) {
	return sessions.New(DB, common.RDB).ListActiveUserSessions(userID, currentSID, now)
}
func RotateUserSessionRefresh(userID int, sid, presentedHash, nextHash string, now int64, grace time.Duration) (*UserSession, error) {
	return sessions.New(DB, common.RDB).RotateUserSessionRefresh(userID, sid, presentedHash, nextHash, now, grace)
}
func RevokeUserSession(userID int, sid, reason string) (bool, error) {
	return sessions.New(DB, common.RDB).RevokeUserSession(userID, sid, reason)
}
func RevokeUserSessionByRefreshHash(sid, presentedHash, reason string) (bool, error) {
	return sessions.New(DB, common.RDB).RevokeUserSessionByRefreshHash(sid, presentedHash, reason)
}
func AdvanceUserSessionAuthVersion(userID int, sid string, expectedSessionVersion, expectedUserAuthVersion, nextUserAuthVersion int64) (*UserSession, error) {
	return sessions.New(DB, common.RDB).AdvanceUserSessionAuthVersion(userID, sid, expectedSessionVersion, expectedUserAuthVersion, nextUserAuthVersion)
}
func RevokeOtherUserSessions(userID int, currentSID, reason string) (int64, error) {
	return sessions.New(DB, common.RDB).RevokeOtherUserSessions(userID, currentSID, reason)
}
func RevokeAllUserSessions(userID int, reason string) (int64, error) {
	return sessions.New(DB, common.RDB).RevokeAllUserSessions(userID, reason)
}

func DeleteUserSessions(userID int) error {
	return sessions.New(DB, common.RDB).DeleteUserSessions(userID)
}
