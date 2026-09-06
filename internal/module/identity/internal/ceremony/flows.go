package ceremony

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

type Flows struct{ db *gorm.DB }

func NewFlows(db *gorm.DB) *Flows { return &Flows{db: db} }

func applyAuthFlowMatch(query *gorm.DB, token string, match AuthFlowMatch) *gorm.DB {
	query = query.Where("token_hash = ? AND purpose = ?", authFlowTokenHash(token), match.Purpose)
	if match.Provider != "" {
		query = query.Where("provider = ?", match.Provider)
	}
	if match.Intent != "" {
		query = query.Where("intent = ?", match.Intent)
	}
	if match.UserId != 0 {
		query = query.Where("user_id = ?", match.UserId)
	}
	if match.SessionId != "" {
		query = query.Where("session_id = ?", match.SessionId)
	}
	return query
}

func authFlowTokenHash(token string) string {
	return common.GenerateHMACWithKey([]byte("auth-flow-v1:"+common.SessionSecret), token)
}

func (r *Flows) CreateAuthFlow(ctx context.Context, input AuthFlowCreate) (string, *AuthFlow, error) {
	if strings.TrimSpace(input.Purpose) == "" || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(time.Now()) {
		return "", nil, ErrAuthFlowInvalid
	}
	random := make([]byte, AuthFlowTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate auth flow token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	flow := &AuthFlow{
		TokenHash: authFlowTokenHash(token),
		Purpose:   input.Purpose,
		Provider:  input.Provider,
		Intent:    input.Intent,
		UserId:    input.UserId,
		SessionId: input.SessionId,
		Payload:   input.Payload,
		ExpiresAt: input.ExpiresAt,
	}
	if err := r.db.WithContext(ctx).Create(flow).Error; err != nil {
		return "", nil, err
	}
	return token, flow, nil
}

// ClaimExternalAuthAssertion records a signed provider assertion as consumed.
// The assertion is HMACed before storage and the unique token_hash index makes
// replay rejection atomic in PostgreSQL.
func (r *Flows) ClaimExternalAuthAssertion(ctx context.Context, purpose, assertion string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
	})
}

// ClaimExternalAuthAssertionWithTx records a provider assertion in the
// caller's transaction so replay protection can commit atomically with the
// authentication flow and its resulting state change.
func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	purpose = strings.TrimSpace(purpose)
	assertion = strings.TrimSpace(assertion)
	now := time.Now()
	if tx == nil || purpose == "" || assertion == "" || !expiresAt.After(now) {
		return ErrAuthFlowInvalid
	}
	flow := AuthFlow{
		TokenHash:  authFlowTokenHash("external:" + purpose + ":" + assertion),
		Purpose:    purpose,
		ExpiresAt:  expiresAt,
		ConsumedAt: &now,
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token_hash"}},
		DoNothing: true,
	}).Create(&flow)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuthFlowConsumed
	}
	return nil
}

// GetAuthFlow validates a flow without consuming it. Callers must still use
// ConsumeAuthFlow with all identity-bound fields before performing the action.
func (r *Flows) GetAuthFlow(ctx context.Context, token string, match AuthFlowMatch) (*AuthFlow, error) {
	if token == "" || match.Purpose == "" {
		return nil, ErrAuthFlowInvalid
	}
	var flow AuthFlow
	if err := applyAuthFlowMatch(r.db.WithContext(ctx), token, match).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAuthFlowInvalid
		}
		return nil, err
	}
	if flow.ConsumedAt != nil {
		return nil, ErrAuthFlowConsumed
	}
	if !flow.ExpiresAt.After(time.Now()) {
		return nil, ErrAuthFlowExpired
	}
	return &flow, nil
}

// ConsumeAuthFlow atomically validates and consumes a flow. Optional match
// fields are enforced when non-zero so tokens cannot cross purposes or users.
func (r *Flows) ConsumeAuthFlow(ctx context.Context, token string, match AuthFlowMatch) (*AuthFlow, error) {
	return r.ConsumeAuthFlowWithAction(ctx, token, match, nil)
}

// ConsumeAuthFlowWithAction consumes a flow and runs action in the same
// database transaction. An action failure rolls the consumption back.
func (r *Flows) ConsumeAuthFlowWithAction(ctx context.Context, token string, match AuthFlowMatch, action func(tx *gorm.DB, flow *AuthFlow) error) (*AuthFlow, error) {
	if token == "" || match.Purpose == "" {
		return nil, ErrAuthFlowInvalid
	}
	var consumed AuthFlow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := applyAuthFlowMatch(tx.Clauses(clause.Locking{Strength: "UPDATE"}), token, match)
		if err := query.First(&consumed).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAuthFlowInvalid
			}
			return err
		}
		if consumed.ConsumedAt != nil {
			return ErrAuthFlowConsumed
		}
		now := time.Now()
		if !consumed.ExpiresAt.After(now) {
			return ErrAuthFlowExpired
		}
		result := tx.Model(&AuthFlow{}).
			Where("id = ? AND consumed_at IS NULL AND expires_at > ?", consumed.Id, now).
			Update("consumed_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAuthFlowConsumed
		}
		consumed.ConsumedAt = &now
		if action != nil {
			if err := action(tx, &consumed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &consumed, nil
}

func (r *Flows) DeleteExpiredAuthFlows(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-AuthFlowDefaultCleanupRetention)
	return r.db.WithContext(ctx).Where("expires_at < ? OR (consumed_at IS NOT NULL AND consumed_at < ?)", cutoff, cutoff).
		Delete(&AuthFlow{}).Error
}
