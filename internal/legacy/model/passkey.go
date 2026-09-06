package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/passkeys"
	"github.com/go-webauthn/webauthn/webauthn"
)

type PasskeyCredential = entity.PasskeyCredential

var ErrPasskeyNotFound = entity.ErrPasskeyNotFound
var ErrFriendlyPasskeyNotFound = entity.ErrFriendlyPasskeyNotFound

func passkeyStore() *passkeys.Store {
	return passkeys.NewStore(DB, IncrementUserAuthVersionWithTx, PublishUserAuthCache)
}
func NewPasskeyCredentialFromWebAuthn(userID int, credential *webauthn.Credential) *PasskeyCredential {
	return entity.NewPasskeyCredentialFromWebAuthn(userID, credential)
}
func GetPasskeyByUserID(userID int) (*PasskeyCredential, error) {
	return passkeyStore().GetPasskeyByUserID(context.Background(), userID)
}
func GetPasskeyByCredentialID(credentialID []byte) (*PasskeyCredential, error) {
	return passkeyStore().GetPasskeyByCredentialID(context.Background(), credentialID)
}
func UpdatePasskeyAssertionState(userID int, credential *webauthn.Credential, lastUsedAt time.Time) error {
	return passkeyStore().UpdatePasskeyAssertionState(context.Background(), userID, credential, lastUsedAt)
}
func UpsertPasskeyCredentialWithAuthVersion(credential *PasskeyCredential) error {
	return passkeyStore().UpsertPasskeyCredentialWithAuthVersion(context.Background(), credential)
}
func DeletePasskeyByUserIDWithAuthVersion(userID int) error {
	return passkeyStore().DeletePasskeyByUserIDWithAuthVersion(context.Background(), userID)
}
