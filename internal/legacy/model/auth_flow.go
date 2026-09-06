package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/flows"
	"gorm.io/gorm"
)

type AuthFlow = entity.AuthFlow
type AuthFlowCreate = entity.AuthFlowCreate
type AuthFlowMatch = entity.AuthFlowMatch

const AuthFlowPurposeOAuth = entity.AuthFlowPurposeOAuth
const AuthFlowPurposeTwoFALogin = entity.AuthFlowPurposeTwoFALogin
const AuthFlowPurposePasskeyLogin = entity.AuthFlowPurposePasskeyLogin
const AuthFlowPurposePasskeyRegister = entity.AuthFlowPurposePasskeyRegister
const AuthFlowPurposePasskeyStepUp = entity.AuthFlowPurposePasskeyStepUp
const AuthFlowPurposeTelegramBind = entity.AuthFlowPurposeTelegramBind
const AuthFlowPurposeTelegramAssertion = entity.AuthFlowPurposeTelegramAssertion
const AuthFlowIntentLogin = entity.AuthFlowIntentLogin
const AuthFlowIntentBind = entity.AuthFlowIntentBind
const AuthFlowTokenBytes = entity.AuthFlowTokenBytes
const AuthFlowDefaultCleanupRetention = entity.AuthFlowDefaultCleanupRetention

var ErrAuthFlowInvalid = entity.ErrAuthFlowInvalid
var ErrAuthFlowExpired = entity.ErrAuthFlowExpired
var ErrAuthFlowConsumed = entity.ErrAuthFlowConsumed

func CreateAuthFlow(input AuthFlowCreate) (string, *AuthFlow, error) {
	return flows.New(DB).CreateAuthFlow(context.Background(), input)
}
func ClaimExternalAuthAssertion(purpose, assertion string, expiresAt time.Time) error {
	return flows.New(DB).ClaimExternalAuthAssertion(context.Background(), purpose, assertion, expiresAt)
}
func GetAuthFlow(token string, match AuthFlowMatch) (*AuthFlow, error) {
	return flows.New(DB).GetAuthFlow(context.Background(), token, match)
}
func ConsumeAuthFlow(token string, match AuthFlowMatch) (*AuthFlow, error) {
	return flows.New(DB).ConsumeAuthFlow(context.Background(), token, match)
}
func ConsumeAuthFlowWithAction(token string, match AuthFlowMatch, action func(tx *gorm.DB, flow *AuthFlow) error) (*AuthFlow, error) {
	return flows.New(DB).ConsumeAuthFlowWithAction(context.Background(), token, match, action)
}
func DeleteExpiredAuthFlows(now time.Time) error {
	return flows.New(DB).DeleteExpiredAuthFlows(context.Background(), now)
}
func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	return flows.ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
}
func authFlowTokenHash(token string) string {
	return common.GenerateHMACWithKey([]byte("auth-flow-v1:"+common.SessionSecret), token)
}
