package flows

import (
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/ceremony"
	"gorm.io/gorm"
)

type Store = implementation.Flows

func New(db *gorm.DB, cache *redis.Client) *Store { return implementation.NewFlows(db, cache) }

type AuthFlow = entity.AuthFlow
type AuthFlowCreate = entity.AuthFlowCreate
type AuthFlowMatch = entity.AuthFlowMatch

func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	return implementation.ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
}
