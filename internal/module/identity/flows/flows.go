package flows

import (
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/ceremony"
	"gorm.io/gorm"
)

type Store = implementation.Flows

func New(db *gorm.DB) *Store { return implementation.NewFlows(db) }

func ClaimExternalAuthAssertionWithTx(tx *gorm.DB, purpose, assertion string, expiresAt time.Time) error {
	return implementation.ClaimExternalAuthAssertionWithTx(tx, purpose, assertion, expiresAt)
}

type AuthFlow = entity.AuthFlow
type AuthFlowCreate = entity.AuthFlowCreate
type AuthFlowMatch = entity.AuthFlowMatch
