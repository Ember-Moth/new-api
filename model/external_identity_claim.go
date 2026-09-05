package model

import (
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
)

type ExternalIdentityClaim = entity.ExternalIdentityClaim

const ExternalIdentityProviderTelegram = identity.ExternalIdentityProviderTelegram

var ErrExternalIdentityAlreadyClaimed = identity.ErrExternalIdentityAlreadyClaimed

func ClaimExternalIdentityWithTx(tx *gorm.DB, provider, subject string, userID int) error {
	return identity.ClaimExternalIdentityWithTx(tx, provider, subject, userID)
}
func ReleaseExternalIdentityWithTx(tx *gorm.DB, provider string, userID int) error {
	return identity.ReleaseExternalIdentityWithTx(tx, provider, userID)
}
func releaseAllExternalIdentitiesWithTx(tx *gorm.DB, userID int) error {
	return identity.ReleaseAllExternalIdentitiesWithTx(tx, userID)
}
