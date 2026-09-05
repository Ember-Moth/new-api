package passkeys

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/passkeys"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

type Store = implementation.Store
type WebAuthnUser = implementation.WebAuthnUser

func NewStore(db *gorm.DB, advance func(*gorm.DB, int) (int64, error), publish func(int) error) *Store {
	return implementation.NewStore(db, advance, publish)
}

func BuildWebAuthn(request *http.Request) (*webauthn.WebAuthn, error) {
	return implementation.BuildWebAuthn(request)
}

func NewWebAuthnUser(user *entity.User, credential *entity.PasskeyCredential) *WebAuthnUser {
	return implementation.NewWebAuthnUser(user, credential)
}
