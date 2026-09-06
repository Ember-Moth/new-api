package service

import (
	"github.com/QuantumNous/new-api/internal/module/identity/authn"
)

type AuthIdentity = authn.AuthIdentity

const AccessTokenTTL = authn.AccessTokenTTL
const SecurityProofTTL = authn.SecurityProofTTL
const LoginSessionTTL = authn.LoginSessionTTL
const RefreshReplayWindow = authn.RefreshReplayWindow

var ErrAuthTokenInvalid = authn.ErrAuthTokenInvalid
var ErrAuthTokenExpired = authn.ErrAuthTokenExpired
var ErrProofScope = authn.ErrProofScope
var ErrProofMethod = authn.ErrProofMethod

func IssueAccessToken(identity AuthIdentity) (string, int64, error) {
	return authn.IssueAccessToken(identity)
}
func ParseAccessToken(raw string) (AuthIdentity, error) { return authn.ParseAccessToken(raw) }
func ParseDashboardAccessToken(raw string) (identity AuthIdentity, internal bool, err error) {
	return authn.ParseDashboardAccessToken(raw)
}
func IssueSecurityProof(identity AuthIdentity, method string, scopes []string) (string, int64, error) {
	return authn.IssueSecurityProof(identity, method, scopes)
}
func VerifySecurityProof(raw string, identity AuthIdentity, requiredScope string, allowedMethods []string) (string, error) {
	return authn.VerifySecurityProof(raw, identity, requiredScope, allowedMethods)
}
