package model

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/factors"
)

type TwoFA entity.TwoFA
type TwoFABackupCode = entity.TwoFABackupCode

func twoFAStore() *factors.Store {
	return factors.New(DB, IncrementUserAuthVersionWithTx, PublishUserAuthCache)
}

func (t *TwoFA) IsLocked() bool { return (*entity.TwoFA)(t).IsLocked() }

func GetTwoFAByUserId(userId int) (*TwoFA, error) {
	factor, err := twoFAStore().GetTwoFAByUserId(context.Background(), userId)
	return (*TwoFA)(factor), err
}

func IsTwoFAEnabled(userId int) (bool, error) {
	return twoFAStore().IsTwoFAEnabled(context.Background(), userId)
}

func (t *TwoFA) CreatePendingTwoFASetup() error {
	return twoFAStore().CreatePendingTwoFASetup(context.Background(), (*entity.TwoFA)(t))
}

func (t *TwoFA) DeletePendingTwoFASetup() error {
	return twoFAStore().DeletePendingTwoFASetup(context.Background(), (*entity.TwoFA)(t))
}

func (t *TwoFA) ResetFailedAttempts() error {
	return twoFAStore().ResetFailedAttempts(context.Background(), (*entity.TwoFA)(t))
}

func (t *TwoFA) IncrementFailedAttempts() error {
	return twoFAStore().IncrementFailedAttempts(context.Background(), (*entity.TwoFA)(t))
}

func CreatePendingTwoFASetupBackupCodes(userId int, codes []string) error {
	return twoFAStore().CreatePendingTwoFASetupBackupCodes(context.Background(), userId, codes)
}

func ReplaceBackupCodesWithAuthVersion(userId int, codes []string) error {
	return twoFAStore().ReplaceBackupCodesWithAuthVersion(context.Background(), userId, codes)
}

func ValidateBackupCode(userId int, code string) (bool, error) {
	return twoFAStore().ValidateBackupCode(context.Background(), userId, code)
}

func GetUnusedBackupCodeCount(userId int) (int, error) {
	return twoFAStore().GetUnusedBackupCodeCount(context.Background(), userId)
}

func DisableTwoFAWithAuthVersion(userId int) error {
	return twoFAStore().DisableTwoFAWithAuthVersion(context.Background(), userId)
}

func (t *TwoFA) EnableWithAuthVersion() error {
	return twoFAStore().EnableWithAuthVersion(context.Background(), (*entity.TwoFA)(t))
}

func (t *TwoFA) ValidateTOTPAndUpdateUsage(code string) (bool, error) {
	return twoFAStore().ValidateTOTPAndUpdateUsage(context.Background(), (*entity.TwoFA)(t), code)
}

func (t *TwoFA) ValidateBackupCodeAndUpdateUsage(code string) (bool, error) {
	return twoFAStore().ValidateBackupCodeAndUpdateUsage(context.Background(), (*entity.TwoFA)(t), code)
}

func GetTwoFAStats() (map[string]interface{}, error) {
	return twoFAStore().GetTwoFAStats(context.Background())
}
