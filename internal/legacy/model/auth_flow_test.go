package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuthFlowIsBoundAndConsumedOnce(t *testing.T) {
	truncateTables(t)
	testdb.UseCache(t)

	token, created, err := CreateAuthFlow(AuthFlowCreate{
		Purpose:   AuthFlowPurposeOAuth,
		Provider:  "github",
		Intent:    AuthFlowIntentBind,
		UserId:    42,
		SessionId: "session-a",
		Payload:   `{"affiliate_code":"invite"}`,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NotEmpty(t, token)
	assert.NotEqual(t, token, created.TokenHash)

	_, err = ConsumeAuthFlow(token, AuthFlowMatch{
		Purpose:   AuthFlowPurposeOAuth,
		Provider:  "github",
		Intent:    AuthFlowIntentBind,
		UserId:    99,
		SessionId: "session-a",
	})
	assert.ErrorIs(t, err, ErrAuthFlowInvalid)

	peeked, err := GetAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github"})
	require.NoError(t, err)
	assert.Nil(t, peeked.ConsumedAt)

	consumed, err := ConsumeAuthFlow(token, AuthFlowMatch{
		Purpose:   AuthFlowPurposeOAuth,
		Provider:  "github",
		Intent:    AuthFlowIntentBind,
		UserId:    42,
		SessionId: "session-a",
	})
	require.NoError(t, err)
	require.NotNil(t, consumed.ConsumedAt)

	_, err = ConsumeAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, ErrAuthFlowConsumed)
}

func TestAuthFlowExpiryIsEnforced(t *testing.T) {
	truncateTables(t)
	testdb.UseCache(t)

	token, flow, err := CreateAuthFlow(AuthFlowCreate{
		Purpose:   AuthFlowPurposeTwoFALogin,
		UserId:    7,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, common.RDB.PExpireAt(t.Context(), "auth:flow:"+flow.TokenHash, time.Unix(1, 0)).Err())

	_, err = GetAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeTwoFALogin})
	assert.True(t, errors.Is(err, ErrAuthFlowInvalid))
	_, err = ConsumeAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeTwoFALogin})
	assert.True(t, errors.Is(err, ErrAuthFlowInvalid))
}

func TestExternalAuthAssertionCanOnlyBeClaimedOnce(t *testing.T) {
	truncateTables(t)
	testdb.UseCache(t)
	expiresAt := time.Now().Add(time.Minute)

	require.NoError(t, ClaimExternalAuthAssertion(AuthFlowPurposeTelegramAssertion, "signed-assertion", expiresAt))
	err := ClaimExternalAuthAssertion(AuthFlowPurposeTelegramAssertion, "signed-assertion", expiresAt)
	assert.ErrorIs(t, err, ErrAuthFlowConsumed)

	require.NoError(t, ClaimExternalAuthAssertion(AuthFlowPurposeTelegramAssertion, "different-assertion", expiresAt))
}

func TestConsumeAuthFlowWithActionDoesNotRearmSecretsAfterRollback(t *testing.T) {
	truncateTables(t)
	testdb.UseCache(t)
	token, _, err := CreateAuthFlow(AuthFlowCreate{
		Purpose:   AuthFlowPurposeTelegramBind,
		UserId:    42,
		SessionId: "session-a",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	actionErr := errors.New("binding failed")

	_, err = ConsumeAuthFlowWithAction(token, AuthFlowMatch{
		Purpose: AuthFlowPurposeTelegramBind, UserId: 42, SessionId: "session-a",
	}, func(tx *gorm.DB, _ *AuthFlow) error {
		if err := ClaimExternalAuthAssertionWithTx(tx, AuthFlowPurposeTelegramAssertion, "assertion-a", time.Now().Add(time.Minute)); err != nil {
			return err
		}
		return actionErr
	})
	assert.ErrorIs(t, err, actionErr)

	_, err = GetAuthFlow(token, AuthFlowMatch{Purpose: AuthFlowPurposeTelegramBind})
	assert.ErrorIs(t, err, ErrAuthFlowConsumed)
	require.NoError(t, ClaimExternalAuthAssertion(AuthFlowPurposeTelegramAssertion, "assertion-a", time.Now().Add(time.Minute)))
}
