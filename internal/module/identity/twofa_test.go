package identity_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTwoFAManagementRotatesSessionsAndRecoveryCodes(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "twofa-managed", common.RoleCommonUser)
	browser, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "first-browser")
	require.NoError(t, err)
	other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-browser")
	require.NoError(t, err)
	response := selfRequest(t, f, browser.AccessToken, http.MethodPost, "/self/2fa/setup", nil)
	require.True(t, response.Success, response.Message)
	var setup contract.TwoFASetup
	require.NoError(t, common.Unmarshal(response.Data, &setup))
	assert.NotEmpty(t, setup.Secret)
	assert.NotEmpty(t, setup.QRCodeData)
	require.NotEmpty(t, setup.BackupCodes)
	var factor entity.TwoFA
	require.NoError(t, f.db.Where("user_id = ?", user.Id).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
	var codes []entity.TwoFABackupCode
	require.NoError(t, f.db.Where("user_id = ?", user.Id).Find(&codes).Error)
	require.Len(t, codes, len(setup.BackupCodes))
	for i, code := range codes {
		assert.NotEqual(t, setup.BackupCodes[i], code.CodeHash)
		assert.True(t, common.ValidatePasswordAndHash(setup.BackupCodes[i], code.CodeHash))
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	_, err = f.service.EnableTwoFA(t.Context(), user.Id, code, nil)
	require.ErrorIs(t, err, identity.ErrSessionRequired)
	enabled := selfRequest(t, f, browser.AccessToken, http.MethodPost, "/self/2fa/enable", contract.TwoFACodeRequest{Code: code})
	require.True(t, enabled.Success, enabled.Message)
	var active contract.AuthBundle
	require.NoError(t, common.Unmarshal(enabled.Data, &active))
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 2, user.AuthVersion)
	firstIdentity, err := service.ParseAccessToken(browser.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(firstIdentity)
	require.Error(t, err)
	revoked, err := model.GetUserSessionBySID(other.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, revoked.Status)
	status := selfRequest(t, f, active.AccessToken, http.MethodGet, "/self/2fa", nil)
	require.True(t, status.Success, status.Message)
	assert.NotContains(t, string(status.Data), setup.Secret)
	var statusData contract.TwoFAStatus
	require.NoError(t, common.Unmarshal(status.Data, &statusData))
	assert.True(t, statusData.Enabled)
	require.NotNil(t, statusData.BackupCodesRemaining)
	assert.Equal(t, len(setup.BackupCodes), *statusData.BackupCodesRemaining)
	repeated := selfRequest(t, f, active.AccessToken, http.MethodPost, "/self/2fa/setup", nil)
	assert.False(t, repeated.Success)
	code, err = totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	regenerated := selfRequest(t, f, active.AccessToken, http.MethodPost, "/self/2fa/backup", contract.TwoFACodeRequest{Code: code})
	require.True(t, regenerated.Success, regenerated.Message)
	var recovery contract.TwoFARecoveryRotation
	require.NoError(t, common.Unmarshal(regenerated.Data, &recovery))
	require.NotNil(t, recovery.AuthBundle)
	require.NotEmpty(t, recovery.BackupCodes)
	valid, err := model.ValidateBackupCode(user.Id, setup.BackupCodes[0])
	require.NoError(t, err)
	assert.False(t, valid)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 3, user.AuthVersion)
	disabled := selfRequest(t, f, recovery.AccessToken, http.MethodPost, "/self/2fa/disable", contract.TwoFACodeRequest{Code: recovery.BackupCodes[0]})
	require.True(t, disabled.Success, disabled.Message)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 4, user.AuthVersion)
	var remaining int64
	require.NoError(t, f.db.Model(&entity.TwoFA{}).Where("user_id = ?", user.Id).Count(&remaining).Error)
	assert.Zero(t, remaining)
	require.NoError(t, f.db.Model(&entity.TwoFABackupCode{}).Where("user_id = ?", user.Id).Count(&remaining).Error)
	assert.Zero(t, remaining)
}

func TestTwoFAEnrollmentFailureKeepsPreviousPendingSetup(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "twofa-rollback", common.RoleCommonUser)
	setup, err := f.service.SetupTwoFA(t.Context(), user.Id)
	require.NoError(t, err)
	var previous entity.TwoFA
	require.NoError(t, f.db.Where("user_id = ?", user.Id).First(&previous).Error)
	require.NoError(t, f.db.Exec(`CREATE FUNCTION reject_recovery_code() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected recovery code failure'; END; $$;
 CREATE TRIGGER reject_recovery_code BEFORE INSERT ON two_fa_backup_codes FOR EACH ROW EXECUTE FUNCTION reject_recovery_code();`).Error)
	_, err = f.service.SetupTwoFA(t.Context(), user.Id)
	require.Error(t, err)
	var current entity.TwoFA
	require.NoError(t, f.db.Where("user_id = ?", user.Id).First(&current).Error)
	assert.Equal(t, previous.Id, current.Id)
	assert.Equal(t, previous.Secret, current.Secret)
	valid, err := model.ValidateBackupCode(user.Id, setup.BackupCodes[0])
	require.NoError(t, err)
	assert.True(t, valid)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 1, user.AuthVersion)
}

func TestTwoFAAdministratorResetEnforcesCurrentRoleAndRevokesSessions(t *testing.T) {
	f := newUserFixture(t)
	target := seedManagedUser(t, f, "twofa-admin", common.RoleAdminUser)
	setup, err := f.service.SetupTwoFA(t.Context(), target.Id)
	require.NoError(t, err)
	browser, err := service.CreateLoginSession(target.Id, "password", "127.0.0.1", "admin-browser")
	require.NoError(t, err)
	session, err := service.ParseAccessToken(browser.AccessToken)
	require.NoError(t, err)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	_, err = f.service.EnableTwoFA(t.Context(), target.Id, code, &session)
	require.NoError(t, err)
	require.Error(t, f.service.AdminDisableTwoFA(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleAdminUser}, target.Id))
	require.NoError(t, f.db.First(target, target.Id).Error)
	assert.EqualValues(t, 2, target.AuthVersion)
	require.NoError(t, f.service.AdminDisableTwoFA(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, target.Id))
	require.NoError(t, f.db.First(target, target.Id).Error)
	assert.EqualValues(t, 3, target.AuthVersion)
	assert.Equal(t, []string{"admin_twofa_disabled"}, f.revocations)
	stored, err := model.GetUserSessionBySID(browser.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
}
