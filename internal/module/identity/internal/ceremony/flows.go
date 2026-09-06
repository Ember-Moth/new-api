package ceremony

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
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

var ErrAuthFlowInvalid = entity.ErrAuthFlowInvalid
var ErrAuthFlowExpired = entity.ErrAuthFlowExpired
var ErrAuthFlowConsumed = entity.ErrAuthFlowConsumed

const authFlowPrefix = "auth:flow:"

type Flows struct {
	db    *gorm.DB
	cache *redis.Client
}

func NewFlows(db *gorm.DB, cache *redis.Client) *Flows { return &Flows{db: db, cache: cache} }

func authFlowTokenHash(token string) string {
	return common.GenerateHMACWithKey([]byte("auth-flow-v1:"+common.SessionSecret), token)
}

// Keep private payload fields out of API serialization while persisting them
// in the cache. Lua returns these bytes unchanged, avoiding JSON number loss.
type cachedFlow struct {
	Flow    AuthFlow `json:"flow"`
	Payload string   `json:"payload"`
}

func (r *Flows) CreateAuthFlow(ctx context.Context, input AuthFlowCreate) (string, *AuthFlow, error) {
	if r.cache == nil {
		return "", nil, errors.New("DragonflyDB is required for authentication flows")
	}
	if strings.TrimSpace(input.Purpose) == "" || !input.ExpiresAt.After(time.Now()) {
		return "", nil, ErrAuthFlowInvalid
	}
	random := make([]byte, AuthFlowTokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate auth flow token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	flow := &AuthFlow{TokenHash: authFlowTokenHash(token), Purpose: input.Purpose, Provider: input.Provider, Intent: input.Intent, UserId: input.UserId, SessionId: input.SessionId, Payload: input.Payload, CreatedAt: time.Now(), ExpiresAt: input.ExpiresAt}
	data, err := common.Marshal(cachedFlow{Flow: *flow, Payload: input.Payload})
	if err != nil {
		return "", nil, err
	}
	key := authFlowPrefix + flow.TokenHash
	_, err = r.cache.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "purpose", flow.Purpose, "provider", flow.Provider, "intent", flow.Intent, "user", strconv.Itoa(flow.UserId), "session", flow.SessionId, "data", string(data))
		pipe.PExpire(ctx, key, time.Until(flow.ExpiresAt))
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return token, flow, nil
}

// A consumed provider signature is a durable security receipt, not a cache.
// It must survive cache loss until the provider signature itself expires.
type assertionReceipt struct {
	TokenHash  string `gorm:"primaryKey"`
	Purpose    string
	ExpiresAt  time.Time
	ConsumedAt time.Time
}

func (assertionReceipt) TableName() string { return "auth_assertion_receipts" }

func (r *Flows) ClaimExternalAuthAssertion(ctx context.Context, purpose, assertion string, expiresAt time.Time) error {
	if r.db == nil {
		return errors.New("database is required for assertion receipts")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
	})
}

func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	purpose, assertion = strings.TrimSpace(purpose), strings.TrimSpace(assertion)
	if tx == nil || purpose == "" || assertion == "" || !expiresAt.After(time.Now()) {
		return ErrAuthFlowInvalid
	}
	receipt := assertionReceipt{TokenHash: authFlowTokenHash("external:" + purpose + ":" + assertion), Purpose: purpose, ExpiresAt: expiresAt, ConsumedAt: time.Now()}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuthFlowConsumed
	}
	return nil
}

func (r *Flows) DeleteExpiredAssertionReceipts(ctx context.Context, now time.Time) error {
	return r.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&assertionReceipt{}).Error
}

var readAuthFlow = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {0, ''} end
if redis.call('HGET', KEYS[1], 'purpose') ~= ARGV[1] then return {0, ''} end
local fields = {'provider', 'intent', 'user', 'session'}
for i, field in ipairs(fields) do
    if ARGV[i+1] ~= '' and redis.call('HGET', KEYS[1], field) ~= ARGV[i+1] then return {0, ''} end
end
if redis.call('HEXISTS', KEYS[1], 'consumed') == 1 then return {-1, ''} end
local data = redis.call('HGET', KEYS[1], 'data')
if not data then return {0, ''} end
if ARGV[6] == 'consume' then
    redis.call('HSET', KEYS[1], 'consumed', '1')
    redis.call('HDEL', KEYS[1], 'data')
end
return {1, data}
`)

func (r *Flows) read(ctx context.Context, token string, match AuthFlowMatch, consume bool) (*AuthFlow, error) {
	if r.cache == nil {
		return nil, errors.New("DragonflyDB is required for authentication flows")
	}
	if token == "" || match.Purpose == "" {
		return nil, ErrAuthFlowInvalid
	}
	user := ""
	if match.UserId != 0 {
		user = strconv.Itoa(match.UserId)
	}
	mode := "peek"
	if consume {
		mode = "consume"
	}
	result, err := readAuthFlow.Run(ctx, r.cache, []string{authFlowPrefix + authFlowTokenHash(token)}, match.Purpose, match.Provider, match.Intent, user, match.SessionId, mode).Slice()
	if err != nil {
		return nil, err
	}
	if len(result) != 2 {
		return nil, ErrAuthFlowInvalid
	}
	switch result[0] {
	case int64(0):
		return nil, ErrAuthFlowInvalid
	case int64(-1):
		return nil, ErrAuthFlowConsumed
	case int64(1):
	default:
		return nil, ErrAuthFlowInvalid
	}
	data, ok := result[1].(string)
	if !ok {
		return nil, ErrAuthFlowInvalid
	}
	var cached cachedFlow
	if err := common.UnmarshalJsonStr(data, &cached); err != nil {
		return nil, err
	}
	if !cached.Flow.ExpiresAt.After(time.Now()) {
		return nil, ErrAuthFlowExpired
	}
	cached.Flow.TokenHash, cached.Flow.Payload = authFlowTokenHash(token), cached.Payload
	if consume {
		now := time.Now()
		cached.Flow.ConsumedAt = &now
	}
	return &cached.Flow, nil
}

func (r *Flows) GetAuthFlow(ctx context.Context, token string, match AuthFlowMatch) (*AuthFlow, error) {
	return r.read(ctx, token, match, false)
}

func (r *Flows) ConsumeAuthFlow(ctx context.Context, token string, match AuthFlowMatch) (*AuthFlow, error) {
	return r.read(ctx, token, match, true)
}

// Consumption is final before action starts. The business transaction can roll
// back independently, but an uncertain database outcome never rearms a secret.
func (r *Flows) ConsumeAuthFlowWithAction(ctx context.Context, token string, match AuthFlowMatch, action func(tx *gorm.DB, flow *AuthFlow) error) (*AuthFlow, error) {
	flow, err := r.ConsumeAuthFlow(ctx, token, match)
	if err != nil {
		return nil, err
	}
	if action != nil {
		if r.db == nil {
			return nil, errors.New("database is required for authentication actions")
		}
		if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return action(tx, flow) }); err != nil {
			return nil, err
		}
	}
	return flow, nil
}

var deleteUserFlows = redis.NewScript(`
local deleted = 0
for _, key in ipairs(KEYS) do
    if redis.call('HGET', key, 'user') == ARGV[1] then
        deleted = deleted + redis.call('DEL', key)
    end
end
return deleted
`)

// DeleteUserAuthFlows removes pending ceremonies on account deletion. Signed
// provider assertion tombstones remain until expiry to prevent replay.
func (r *Flows) DeleteUserAuthFlows(ctx context.Context, userID int) error {
	if r.cache == nil {
		return errors.New("DragonflyDB is required for authentication flows")
	}
	var cursor uint64
	for {
		keys, next, err := r.cache.Scan(ctx, cursor, authFlowPrefix+"*", 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := deleteUserFlows.Run(ctx, r.cache, keys, strconv.Itoa(userID)).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
