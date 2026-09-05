package authn

import (
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/authentication"
)

type Runtime = implementation.Runtime
type Dependencies = implementation.Dependencies
type AuthIdentity = contract.AuthIdentity
type AuthBundle = contract.AuthBundle
type LoginSessionView = contract.LoginSessionView

func New(deps Dependencies) *Runtime { return implementation.New(deps) }

const AccessTokenTTL = implementation.AccessTokenTTL
const SecurityProofTTL = implementation.SecurityProofTTL
const LoginSessionTTL = implementation.LoginSessionTTL
const RefreshReplayWindow = implementation.RefreshReplayWindow

var ErrAuthTokenInvalid = implementation.ErrAuthTokenInvalid
var ErrAuthTokenExpired = implementation.ErrAuthTokenExpired
var ErrProofScope = implementation.ErrProofScope
var ErrProofMethod = implementation.ErrProofMethod

const RefreshCookieName = implementation.RefreshCookieName
const SessionHintCookieName = implementation.SessionHintCookieName
const SessionHintCookieValue = implementation.SessionHintCookieValue

var ErrLoginSessionInvalid = implementation.ErrLoginSessionInvalid
var ErrLoginSessionRevoked = implementation.ErrLoginSessionRevoked
var ErrLoginSessionMismatch = implementation.ErrLoginSessionMismatch
var ErrRefreshTokenInvalid = implementation.ErrRefreshTokenInvalid
var ErrRefreshRace = implementation.ErrRefreshRace

func IssueAccessToken(identity AuthIdentity) (string, int64, error) {
	return implementation.IssueAccessToken(identity)
}

func ParseAccessToken(raw string) (AuthIdentity, error) { return implementation.ParseAccessToken(raw) }

func ParseDashboardAccessToken(raw string) (identity AuthIdentity, internal bool, err error) {
	return implementation.ParseDashboardAccessToken(raw)
}

func IssueSecurityProof(identity AuthIdentity, method string, scopes []string) (string, int64, error) {
	return implementation.IssueSecurityProof(identity, method, scopes)
}

func VerifySecurityProof(raw string, identity AuthIdentity, requiredScope string, allowedMethods []string) (string, error) {
	return implementation.VerifySecurityProof(raw, identity, requiredScope, allowedMethods)
}

func RefreshTokenSID(rawRefreshToken string) (string, bool) {
	return implementation.RefreshTokenSID(rawRefreshToken)
}

func AuthSessionErrorCode(err error) (int, string) { return implementation.AuthSessionErrorCode(err) }

func FormatAuthError(err error) string { return implementation.FormatAuthError(err) }
