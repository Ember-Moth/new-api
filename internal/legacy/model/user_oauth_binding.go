package model

import (
	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
)

type UserOAuthBinding = entity.UserOAuthBinding

func GetUserByOAuthBinding(providerID int, subject string) (*User, error) {
	user, err := identity.New(identity.Dependencies{DB: DB}).UserByOAuthBinding(context.Background(), providerID, subject)
	return (*User)(user), err
}
func IsProviderUserIdTaken(providerID int, subject string) bool {
	taken, err := identity.New(identity.Dependencies{DB: DB}).IsProviderUserIDTaken(context.Background(), providerID, subject)
	if err != nil {
		common.SysError("failed to inspect OAuth binding: " + err.Error())
	}
	return taken
}
func UpdateUserOAuthBinding(userID, providerID int, subject string) error {
	return identity.New(identity.Dependencies{DB: DB}).SetOAuthBinding(context.Background(), userID, providerID, subject)
}
func CreateUserOAuthBindingWithTx(tx *gorm.DB, binding *UserOAuthBinding) error {
	return identity.CreateUserOAuthBindingWithTx(tx, binding)
}
func deleteUserOAuthBindingsByUserId(tx *gorm.DB, userID int) error {
	return identity.DeleteUserOAuthBindingsWithTx(tx, userID)
}
