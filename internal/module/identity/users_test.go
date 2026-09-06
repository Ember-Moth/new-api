package identity_test

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/authz"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type userAuthorizationFixture struct {
	fail   bool
	engine *authz.Engine
}

func (f *userAuthorizationFixture) Capabilities(id, role int) map[string]map[string]bool {
	return f.engine.Capabilities(id, role)
}
func (f *userAuthorizationFixture) SetUserPermissionsInTx(tx *gorm.DB, id int, permissions map[string]map[string]bool) error {
	if err := f.engine.SetUserPermissionsInTx(tx, id, permissions); err != nil {
		return err
	}
	if f.fail {
		return errors.New("injected permission failure")
	}
	return nil
}
func (f *userAuthorizationFixture) ClearUserAuthorizationInTx(tx *gorm.DB, id int) error {
	if err := f.engine.ClearUserAuthorizationInTx(tx, id); err != nil {
		return err
	}
	if f.fail {
		return errors.New("injected permission failure")
	}
	return nil
}
func (f *userAuthorizationFixture) ReloadPolicy() error { return f.engine.ReloadPolicy() }

type userFixture struct {
	db            *gorm.DB
	service       *identity.Service
	router        *gin.Engine
	authorization *userAuthorizationFixture
	audits        []contract.UserAudit
	grants        []int
	revocations   []string
}

func newUserFixture(t *testing.T) *userFixture {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousLog := model.DB, model.LOG_DB
	previousSecret := common.SessionSecret
	common.SessionSecret = "identity-self-test-secret"
	previousRedis, previousMaster, previousBatch := common.RedisEnabled, common.IsMasterNode, common.BatchUpdateEnabled
	previousMain, previousLogs := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled, common.IsMasterNode, common.BatchUpdateEnabled = false, false, false
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLog
		common.SessionSecret = previousSecret
		common.RedisEnabled, common.IsMasterNode, common.BatchUpdateEnabled = previousRedis, previousMaster, previousBatch
		common.SetDatabaseTypes(previousMain, previousLogs)
	})
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	authorization, err := authz.New(db, false)
	require.NoError(t, err)
	f := &userFixture{db: db, router: gin.New(), authorization: &userAuthorizationFixture{engine: authorization}}
	wallet := billing.New(billing.Dependencies{DB: db, Accounting: model.AccountingStore()})
	f.service = identity.New(identity.Dependencies{
		Authentication: service.AuthenticationRuntime(),
		VerifyEmail:    func(email, code string) bool { return code == "valid" },
		DB:             db, UserAuthorization: f.authorization, UserWallet: wallet, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
		WelcomeQuota: func() int { return 250 }, WelcomeGrant: func(id, quota int) { assert.Equal(t, 250, quota); f.grants = append(f.grants, id) },
		UserSecurity: identity.UserSecurity{
			IssueProof:            service.IssueSecurityProof,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
			AdvanceVersion:        model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			PublishDeletedVersion: model.PublishCommittedUserAuthVersion,
			RevokeSessions: func(id int, reason string) error {
				f.revocations = append(f.revocations, reason)
				_, err := model.RevokeAllUserSessions(id, reason)
				return err
			},
			InvalidateUser: model.InvalidateUserCache, InvalidateTokens: model.InvalidateUserTokensCache,
			DeleteCredentials: model.DeleteUserAuthenticationData,
		},
	})
	h := identityhttp.New(f.service, identityhttp.ManagementHooks{WriteRefreshCookie: service.WriteRefreshCookie, ClearRefreshCookie: service.ClearRefreshCookie, SessionIdentity: middleware.GetSessionAuthIdentity, RequireSecurityProof: middleware.RequireSecurityProof,
		PasskeyLogin: func(c *gin.Context, user *entity.User) {
			bundle, err := service.CreateLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "passkey", c.ClientIP(), c.Request.UserAgent())
			if err != nil {
				common.ApiError(c, err)
				return
			}
			common.ApiSuccess(c, bundle)
		}, Audit: func(c *gin.Context, id int, action string, params map[string]any) {
			f.audits = append(f.audits, contract.UserAudit{TargetID: id, Action: action, Parameters: params})
		}})
	f.router.Use(middleware.Authorization(authorization))
	f.router.Use(func(c *gin.Context) {
		role, _ := strconv.Atoi(c.GetHeader("X-Test-Role"))
		c.Set("role", role)
		c.Set("id", 9999)
		c.Next()
	})
	selfRoutes := f.router.Group("/self", middleware.UserAuth())
	selfRoutes.GET("/sessions", h.GetLoginSessions)
	selfRoutes.DELETE("/sessions/:sid", h.DeleteLoginSession)
	selfRoutes.POST("/sessions/revoke-others", h.RevokeOtherLoginSessions)
	f.router.POST("/auth/refresh", h.RefreshAuth)
	f.router.POST("/auth/logout", h.AuthLogout)
	selfRoutes.POST("/passkey/register/begin", h.PasskeyRegisterBegin)
	selfRoutes.POST("/passkey/register/finish", h.PasskeyRegisterFinish)
	selfRoutes.POST("/passkey/verify/begin", h.PasskeyVerifyBegin)
	selfRoutes.POST("/passkey/verify/finish", h.PasskeyVerifyFinish)
	selfRoutes.DELETE("/passkey", h.PasskeyDelete)
	selfRoutes.GET("/passkey", h.PasskeyStatus)
	f.router.POST("/passkey/login/begin", h.PasskeyLoginBegin)
	f.router.POST("/passkey/login/finish", h.PasskeyLoginFinish)
	selfRoutes.GET("/2fa", h.TwoFAStatus)
	selfRoutes.POST("/2fa/setup", h.SetupTwoFA)
	selfRoutes.POST("/2fa/enable", h.EnableTwoFA)
	selfRoutes.POST("/2fa/disable", h.DisableTwoFA)
	selfRoutes.POST("/2fa/backup", h.RegenerateTwoFABackupCodes)
	selfRoutes.GET("", h.Self)
	selfRoutes.PUT("", h.UpdateSelf)
	selfRoutes.DELETE("", h.DeleteSelf)
	selfRoutes.PUT("/notifications", h.UpdateNotificationSettings)
	selfRoutes.PUT("/billing-preference", h.UpdateBillingPreference)
	selfRoutes.POST("/email", h.BindEmail)
	f.router.GET("/users", h.ListUsers)
	f.router.GET("/users/search", h.SearchUsers)
	f.router.GET("/users/:id", h.GetUser)
	f.router.POST("/users", h.CreateUser)
	f.router.PUT("/users", h.UpdateUser)
	f.router.POST("/users/manage", h.ManageUser)
	f.router.DELETE("/users/:id", h.DeleteUser)
	f.router.DELETE("/users/:id/bindings/:binding_type", h.ClearUserBinding)
	return f
}

func seedManagedUser(t *testing.T, f *userFixture, name string, role int) *model.User {
	t.Helper()
	user := &model.User{Username: name, Password: "stored-password-hash", DisplayName: name, Role: role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "aff-" + name, Quota: 1000, UsedQuota: 20, RequestCount: 3, AffQuota: 8, AffHistoryQuota: 10}
	require.NoError(t, f.db.Create(user).Error)
	return user
}

func seedManagedSession(t *testing.T, f *userFixture, user *model.User, sid string) {
	t.Helper()
	now := time.Now().Unix()
	require.NoError(t, f.db.Create(&model.UserSession{SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion, Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600}).Error)
}

func TestUserDirectorySortsAndFiltersWithoutLeakingCredentials(t *testing.T) {
	f := newUserFixture(t)
	users := []*model.User{
		seedManagedUser(t, f, "delta", common.RoleCommonUser),
		seedManagedUser(t, f, "alpha", common.RoleCommonUser),
		seedManagedUser(t, f, "charlie", common.RoleAdminUser),
		seedManagedUser(t, f, "bravo", common.RoleCommonUser),
		seedManagedUser(t, f, "echo", common.RoleCommonUser),
	}
	require.NoError(t, f.db.Model(users[0]).Updates(map[string]any{"access_token": "directory-secret-access-token", "email": "delta@example.test", "quota": 500}).Error)
	require.NoError(t, f.db.Model(users[1]).Updates(map[string]any{"status": common.UserStatusDisabled, "group": "vip"}).Error)
	require.NoError(t, f.db.Delete(users[4]).Error)
	page := userRequest(t, f, common.RoleRootUser, http.MethodGet, "/users?sort_by=username&sort_order=asc&p=2&page_size=2", nil)
	require.True(t, page.Success, page.Message)
	var data struct {
		Total int                     `json:"total"`
		Items []contract.UserResponse `json:"items"`
	}
	require.NoError(t, common.Unmarshal(page.Data, &data))
	assert.Equal(t, 5, data.Total)
	require.Len(t, data.Items, 2)
	assert.Equal(t, []string{"charlie", "delta"}, []string{data.Items[0].Username, data.Items[1].Username})
	assert.NotContains(t, string(page.Data), "stored-password-hash")
	assert.NotContains(t, string(page.Data), "directory-secret-access-token")
	searched := userRequest(t, f, common.RoleRootUser, http.MethodGet, "/users/search?keyword=a&sort_by=username&sort_order=asc&p=2&page_size=2", nil)
	require.True(t, searched.Success, searched.Message)
	require.NoError(t, common.Unmarshal(searched.Data, &data))
	assert.Equal(t, 4, data.Total)
	require.Len(t, data.Items, 2)
	assert.Equal(t, []string{"charlie", "delta"}, []string{data.Items[0].Username, data.Items[1].Username})
	for _, tc := range []struct {
		query  string
		wantID int
	}{
		{"group=vip&status=2", users[1].Id},
		{"status=-1", users[4].Id},
		{"role=" + strconv.Itoa(common.RoleAdminUser), users[2].Id},
		{"keyword=" + strconv.Itoa(users[0].Id), users[0].Id},
	} {
		response := userRequest(t, f, common.RoleRootUser, http.MethodGet, "/users/search?"+tc.query, nil)
		require.True(t, response.Success, response.Message)
		require.NoError(t, common.Unmarshal(response.Data, &data))
		assert.Equal(t, 1, data.Total)
		require.Len(t, data.Items, 1)
		assert.Equal(t, tc.wantID, data.Items[0].Id)
	}
	denied := userRequest(t, f, common.RoleAdminUser, http.MethodGet, "/users/"+strconv.Itoa(users[2].Id), nil)
	assert.False(t, denied.Success)
	detail := userRequest(t, f, common.RoleRootUser, http.MethodGet, "/users/"+strconv.Itoa(users[0].Id), nil)
	require.True(t, detail.Success, detail.Message)
	assert.NotContains(t, string(detail.Data), "stored-password-hash")
	assert.NotContains(t, string(detail.Data), "directory-secret-access-token")
}

func TestUserCreationAndEditCommitPermissionsAtomically(t *testing.T) {
	f := newUserFixture(t)
	request := map[string]any{"username": "  managed  ", "password": "Password123", "role": common.RoleAdminUser,
		"quota": 999999, "status": 2, "email": "ignored@example.test", "auth_version": 99, "group": "vip",
		"admin_permissions": map[string]any{"channel": map[string]any{"read": true}},
	}
	created := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users", request)
	require.True(t, created.Success, created.Message)
	var user model.User
	require.NoError(t, f.db.First(&user, "username = ?", "managed").Error)
	assert.Equal(t, "managed", user.DisplayName)
	assert.True(t, common.ValidatePasswordAndHash("Password123", user.Password))
	assert.Equal(t, 250, user.Quota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.Equal(t, "default", user.Group)
	assert.Empty(t, user.Email)
	assert.NotEmpty(t, user.GetSetting().SidebarModules)
	assert.Equal(t, []int{user.Id}, f.grants)
	assert.True(t, f.authorization.engine.Can(user.Id, user.Role, authz.ChannelRead))
	seedManagedSession(t, f, &user, "edit-session")
	beforePassword := user.Password
	edit := map[string]any{"id": user.Id, "username": "managed-edited", "display_name": "", "group": "default", "remark": "", "password": "",
		"quota": 1, "used_quota": 999, "status": 2, "admin_permissions": map[string]any{"channel": map[string]any{"read": false}},
	}
	f.authorization.fail = true
	failed := userRequest(t, f, common.RoleRootUser, http.MethodPut, "/users", edit)
	assert.False(t, failed.Success)
	require.NoError(t, f.db.First(&user, user.Id).Error)
	assert.Equal(t, "managed", user.Username)
	assert.True(t, f.authorization.engine.Can(user.Id, user.Role, authz.ChannelRead))
	assert.Empty(t, f.revocations)
	assert.Len(t, f.audits, 1)
	f.authorization.fail = false
	updated := userRequest(t, f, common.RoleRootUser, http.MethodPut, "/users", edit)
	require.True(t, updated.Success, updated.Message)
	require.NoError(t, f.db.First(&user, user.Id).Error)
	assert.Equal(t, "managed-edited", user.Username)
	assert.Empty(t, user.DisplayName)
	assert.Equal(t, beforePassword, user.Password)
	assert.Equal(t, 250, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.EqualValues(t, 1, user.AuthVersion)
	assert.False(t, f.authorization.engine.Can(user.Id, user.Role, authz.ChannelRead))
	assert.Empty(t, f.revocations)
	edit["password"] = "NewPassword123"
	updated = userRequest(t, f, common.RoleRootUser, http.MethodPut, "/users", edit)
	require.True(t, updated.Success, updated.Message)
	require.NoError(t, f.db.First(&user, user.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("NewPassword123", user.Password))
	assert.EqualValues(t, 2, user.AuthVersion)
	assert.Equal(t, []string{"admin_user_update"}, f.revocations)
	var session model.UserSession
	require.NoError(t, f.db.First(&session, "sid = ?", "edit-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
	// Failed creation must not leave either the user or a welcome grant behind.
	f.authorization.fail = true
	request["username"] = "rolled-back"
	failed = userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users", request)
	assert.False(t, failed.Success)
	var count int64
	require.NoError(t, f.db.Model(&entity.User{}).Where("username = ?", "rolled-back").Count(&count).Error)
	assert.Zero(t, count)
	assert.Equal(t, []int{user.Id}, f.grants)
}

func TestUserManagementProtectsRolesAndRevokesSessionsOnce(t *testing.T) {
	f := newUserFixture(t)
	target := seedManagedUser(t, f, "managed-status", common.RoleCommonUser)
	seedManagedSession(t, f, target, "disable-session")
	disabled := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: target.Id, Action: "disable"})
	require.True(t, disabled.Success, disabled.Message)
	require.NoError(t, f.db.First(target, target.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, target.Status)
	assert.EqualValues(t, 2, target.AuthVersion)
	assert.Equal(t, []string{"user_security_changed"}, f.revocations)
	var session model.UserSession
	require.NoError(t, f.db.First(&session, "sid = ?", "disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
	admin := seedManagedUser(t, f, "managed-demote", common.RoleAdminUser)
	seedManagedSession(t, f, admin, "demote-one")
	seedManagedSession(t, f, admin, "demote-two")
	require.NoError(t, f.authorization.engine.SetUserPermissions(admin.Id, authz.PermissionsMap{"channel": {"read": true}}))
	demoted := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: admin.Id, Action: "demote"})
	require.True(t, demoted.Success, demoted.Message)
	require.NoError(t, f.db.First(admin, admin.Id).Error)
	assert.Equal(t, common.RoleCommonUser, admin.Role)
	assert.EqualValues(t, 2, admin.AuthVersion)
	assert.Equal(t, []string{"user_security_changed", "admin_demote"}, f.revocations)
	var sessions []model.UserSession
	require.NoError(t, f.db.Where("user_id = ?", admin.Id).Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	var policies int64
	require.NoError(t, f.db.Model(&entity.CasbinRule{}).Where("v0 = ?", authz.UserSubject(admin.Id)).Count(&policies).Error)
	assert.Zero(t, policies)
	root := seedManagedUser(t, f, "protected-root", common.RoleRootUser)
	for _, action := range []string{"disable", "delete", "demote", "unknown"} {
		rejected := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: root.Id, Action: action})
		assert.False(t, rejected.Success)
	}
	invalid := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Action: "enable"})
	assert.False(t, invalid.Success)
	createRoot := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users", contract.UserRequest{Username: "new-root", Password: "Password123", Role: common.RoleRootUser})
	assert.False(t, createRoot.Success)
	peer := seedManagedUser(t, f, "peer-admin", common.RoleAdminUser)
	peerUpdate := userRequest(t, f, common.RoleAdminUser, http.MethodPut, "/users", contract.UserRequest{Id: peer.Id, Username: peer.Username, Group: "default"})
	assert.False(t, peerUpdate.Success)
}

func TestUserDeletionClearsCredentialsAndBindingClaims(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "managed-delete", common.RoleCommonUser)
	require.NoError(t, f.db.Model(user).Update("telegram_id", "telegram-subject").Error)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return model.ClaimExternalIdentityWithTx(tx, model.ExternalIdentityProviderTelegram, "telegram-subject", user.Id)
	}))
	cleared := userRequest(t, f, common.RoleRootUser, http.MethodDelete, "/users/"+strconv.Itoa(user.Id)+"/bindings/telegram", nil)
	require.True(t, cleared.Success, cleared.Message)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Empty(t, user.TelegramId)
	var claims int64
	require.NoError(t, f.db.Model(&model.ExternalIdentityClaim{}).Where("user_id = ?", user.Id).Count(&claims).Error)
	assert.Zero(t, claims)
	token := model.Token{UserId: user.Id, Key: "managed-delete-key", ExpiredTime: -1, RemainQuota: 100}
	require.NoError(t, f.db.Create(&token).Error)
	seedManagedSession(t, f, user, "delete-session")
	provider := model.CustomOAuthProvider{Name: "managed provider", Slug: "managed-provider", ClientId: "client", ClientSecret: "secret"}
	require.NoError(t, f.db.Create(&provider).Error)
	require.NoError(t, f.db.Create(&model.UserOAuthBinding{UserId: user.Id, ProviderId: provider.Id, ProviderUserId: "subject"}).Error)
	// Credential deletion failure must roll back both the auth version and the user deletion.
	require.NoError(t, f.db.Exec(`CREATE FUNCTION reject_credential_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected delete failure'; END; $$;
 CREATE TRIGGER reject_credential_delete BEFORE DELETE ON tokens FOR EACH ROW EXECUTE FUNCTION reject_credential_delete();`).Error)
	path := "/users/" + strconv.Itoa(user.Id)
	failed := userRequest(t, f, common.RoleRootUser, http.MethodDelete, path, nil)
	assert.False(t, failed.Success)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 1, user.AuthVersion)
	require.NoError(t, f.db.Exec("DROP TRIGGER reject_credential_delete ON tokens").Error)
	deleted := userRequest(t, f, common.RoleRootUser, http.MethodDelete, path, nil)
	require.True(t, deleted.Success, deleted.Message)
	var count int64
	require.NoError(t, f.db.Unscoped().Model(&entity.User{}).Where("id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
	for _, record := range []any{&model.Token{}, &model.UserSession{}, &model.UserOAuthBinding{}} {
		require.NoError(t, f.db.Unscoped().Model(record).Where("user_id = ?", user.Id).Count(&count).Error)
		assert.Zero(t, count)
	}
	soft := seedManagedUser(t, f, "managed-soft", common.RoleCommonUser)
	removed := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: soft.Id, Action: "delete"})
	require.True(t, removed.Success, removed.Message)
	require.NoError(t, f.db.Unscoped().First(soft, soft.Id).Error)
	assert.True(t, soft.DeletedAt.Valid)
	assert.EqualValues(t, 2, soft.AuthVersion)
	assert.Contains(t, f.revocations, "user_deleted")
}

func TestUserWalletManagementEnforcesCeilingAndAuditsReplacement(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "managed-wallet", common.RoleCommonUser)
	require.NoError(t, f.db.Model(user).Update("quota", common.MaxWalletQuota-1).Error)
	failed := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: user.Id, Action: "add_quota", Mode: "add", Value: 2})
	assert.False(t, failed.Success)
	failed = userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: user.Id, Action: "add_quota", Mode: "override", Value: common.MaxWalletQuota + 1})
	assert.False(t, failed.Success)
	failed = userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: user.Id, Action: "add_quota", Mode: "subtract", Value: common.MaxWalletQuota + 1})
	assert.False(t, failed.Success)
	assert.Empty(t, f.audits)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-1, user.Quota)
	for _, tc := range []struct {
		mode         string
		amount, want int
	}{
		{"override", 500, 500}, {"add", 100, 600}, {"subtract", 50, 550},
	} {
		response := userRequest(t, f, common.RoleRootUser, http.MethodPost, "/users/manage", contract.ManageUserRequest{Id: user.Id, Action: "add_quota", Mode: tc.mode, Value: tc.amount})
		require.True(t, response.Success, response.Message)
		require.NoError(t, f.db.First(user, user.Id).Error)
		assert.Equal(t, tc.want, user.Quota)
		assert.EqualValues(t, 1, user.AuthVersion)
	}
	assert.Equal(t, "user.quota_override", f.audits[0].Action)
	assert.Contains(t, f.audits[0].Parameters, "from")
	assert.Contains(t, f.audits[0].Parameters, "to")
}

func userRequest(t *testing.T, f *userFixture, role int, method, path string, body any) tokenAPIResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Role", strconv.Itoa(role))
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var result tokenAPIResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}

func TestSelfProfileUsesSafeProjectionAndRotatesOnlyCurrentSession(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "self-profile", common.RoleCommonUser)
	password, err := common.Password2Hash("CurrentPassword123")
	require.NoError(t, err)
	require.NoError(t, f.db.Model(user).Updates(map[string]any{"password": password, "access_token": "personal-management-secret", "remark": "private-admin-remark"}).Error)
	first, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "first-browser")
	require.NoError(t, err)
	second, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "second-browser")
	require.NoError(t, err)
	currentIdentity, err := service.ParseAccessToken(first.AccessToken)
	require.NoError(t, err)
	self := selfRequest(t, f, first.AccessToken, http.MethodGet, "/self", nil)
	require.True(t, self.Success, self.Message)
	assert.NotContains(t, string(self.Data), password)
	assert.NotContains(t, string(self.Data), "personal-management-secret")
	assert.NotContains(t, string(self.Data), "private-admin-remark")
	var view contract.SelfUserResponse
	require.NoError(t, common.Unmarshal(self.Data, &view))
	assert.Equal(t, user.Id, view.Id)
	assert.True(t, view.Permissions.SidebarSettings)
	assert.Equal(t, false, view.Permissions.SidebarModules["admin"])
	edited := selfRequest(t, f, first.AccessToken, http.MethodPut, "/self", map[string]any{
		"id": 9999, "username": "self-renamed", "display_name": "Self", "role": common.RoleRootUser, "quota": 0, "used_quota": 999, "group": "vip", "remark": "injected", "password": "",
	})
	require.True(t, edited.Success, edited.Message)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, "self-renamed", user.Username)
	assert.Equal(t, common.RoleCommonUser, user.Role)
	assert.Equal(t, "default", user.Group)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 20, user.UsedQuota)
	assert.Equal(t, "personal-management-secret", user.GetAccessToken())
	assert.Equal(t, "private-admin-remark", user.Remark)
	assert.Equal(t, password, user.Password)
	assert.EqualValues(t, 1, user.AuthVersion)
	input := contract.SelfUpdateRequest{ProfileInput: contract.ProfileInput{Password: "NewPassword123"}}
	_, err = f.service.UpdateSelf(t.Context(), user.Id, input, &currentIdentity)
	require.ErrorIs(t, err, identity.ErrOriginalPassword)
	input.OriginalPassword = "CurrentPassword123"
	_, err = f.service.UpdateSelf(t.Context(), user.Id, input, nil)
	require.ErrorIs(t, err, identity.ErrSessionRequired)
	passwordless := seedManagedUser(t, f, "self-passwordless", common.RoleCommonUser)
	require.NoError(t, f.db.Model(passwordless).Update("password", "").Error)
	_, err = f.service.UpdateSelf(t.Context(), passwordless.Id, input, nil)
	require.ErrorIs(t, err, identity.ErrPasswordUnset)
	changed := selfRequest(t, f, first.AccessToken, http.MethodPut, "/self", map[string]any{"password": "NewPassword123", "original_password": "CurrentPassword123"})
	require.True(t, changed.Success, changed.Message)
	var rotated contract.AuthBundle
	require.NoError(t, common.Unmarshal(changed.Data, &rotated))
	assert.Equal(t, first.Session.SID, rotated.Session.SID)
	assert.True(t, rotated.Session.Current)
	assert.NotEmpty(t, rotated.AccessToken)
	assert.NotContains(t, string(changed.Data), "refresh_token")
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("NewPassword123", user.Password))
	assert.EqualValues(t, 2, user.AuthVersion)
	oldIdentity, err := service.ParseAccessToken(first.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(oldIdentity)
	require.Error(t, err)
	replacement, err := service.ParseAccessToken(rotated.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(replacement)
	require.NoError(t, err)
	var other model.UserSession
	require.NoError(t, f.db.First(&other, "sid = ?", second.Session.SID).Error)
	assert.Equal(t, model.UserSessionStatusRevoked, other.Status)
	assert.Equal(t, "password_changed", other.RevokedReason)
	// A previously validated request cannot change the password after its session version is obsolete.
	input.OriginalPassword = "NewPassword123"
	input.Password = "AnotherPassword123"
	_, err = f.service.UpdateSelf(t.Context(), user.Id, input, &currentIdentity)
	require.ErrorIs(t, err, identity.ErrSessionRevoked)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 2, user.AuthVersion)
	assert.True(t, common.ValidatePasswordAndHash("NewPassword123", user.Password))
}

func TestSelfPreferencesMergeOwnedFieldsAndPreservePrivilegeBoundaries(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "self-settings", common.RoleCommonUser)
	user.SetSetting(dto.UserSetting{NotifyType: dto.NotifyTypeWebhook, QuotaWarningThreshold: 1, WebhookUrl: "https://example.test/old", WebhookSecret: "old-secret", Language: "zh", SidebarModules: `{"chat":true}`, BillingPreference: "subscription_first", UpstreamModelUpdateNotifyEnabled: true})
	require.NoError(t, f.db.Model(user).Update("setting", user.Setting).Error)
	login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "settings-browser")
	require.NoError(t, err)
	ignored := selfRequest(t, f, login.AccessToken, http.MethodPut, "/self", map[string]any{"sidebar_modules": nil, "language": "ignored", "username": "ignored", "password": "ignored"})
	require.True(t, ignored.Success, ignored.Message)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, "zh", user.GetSetting().Language)
	assert.Equal(t, "self-settings", user.Username)
	threshold := contract.NotificationSettingsRequest{QuotaWarningType: dto.NotifyTypeEmail, QuotaWarningThreshold: 2, NotificationEmail: "alerts@example.test", UpstreamModelUpdateNotifyEnabled: common.GetPointer(false), RecordIpLog: true}
	saved := selfRequest(t, f, login.AccessToken, http.MethodPut, "/self/notifications", threshold)
	require.True(t, saved.Success, saved.Message)
	require.NoError(t, f.db.First(user, user.Id).Error)
	settings := user.GetSetting()
	assert.Equal(t, "zh", settings.Language)
	assert.Equal(t, `{"chat":true}`, settings.SidebarModules)
	assert.Equal(t, "subscription_first", settings.BillingPreference)
	assert.True(t, settings.UpstreamModelUpdateNotifyEnabled)
	assert.Equal(t, "alerts@example.test", settings.NotificationEmail)
	assert.Empty(t, settings.WebhookUrl)
	assert.Empty(t, settings.WebhookSecret)
	assert.True(t, settings.RecordIpLog)
	// Two independent settings edits must both survive regardless of transaction order.
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	go func() {
		<-start
		_, err := f.service.UpdateSelf(t.Context(), user.Id, contract.SelfUpdateRequest{Preference: "language", PreferenceValue: common.GetPointer("en")}, nil)
		outcomes <- err
	}()
	go func() {
		<-start
		outcomes <- f.service.UpdateNotificationSettings(t.Context(), user.Id, contract.NotificationSettingsRequest{QuotaWarningType: dto.NotifyTypeGotify, QuotaWarningThreshold: 3, GotifyUrl: "https://example.test", GotifyToken: "token", GotifyPriority: 99})
	}()
	close(start)
	require.NoError(t, <-outcomes)
	require.NoError(t, <-outcomes)
	require.NoError(t, f.db.First(user, user.Id).Error)
	settings = user.GetSetting()
	assert.Equal(t, "en", settings.Language)
	assert.Equal(t, 3.0, settings.QuotaWarningThreshold)
	assert.Equal(t, 5, settings.GotifyPriority)
	assert.Equal(t, "subscription_first", settings.BillingPreference)
	assert.Equal(t, `{"chat":true}`, settings.SidebarModules)
	assert.EqualValues(t, 1, user.AuthVersion)
	for _, request := range []contract.NotificationSettingsRequest{
		{QuotaWarningType: "invalid", QuotaWarningThreshold: 1},
		{QuotaWarningType: dto.NotifyTypeEmail, QuotaWarningThreshold: 0},
		{QuotaWarningType: dto.NotifyTypeEmail, QuotaWarningThreshold: math.NaN()},
		{QuotaWarningType: dto.NotifyTypeEmail, QuotaWarningThreshold: math.Inf(1)},
		{QuotaWarningType: dto.NotifyTypeEmail, QuotaWarningThreshold: 1, NotificationEmail: "invalid"},
		{QuotaWarningType: dto.NotifyTypeWebhook, QuotaWarningThreshold: 1, WebhookUrl: "invalid"},
		{QuotaWarningType: dto.NotifyTypeBark, QuotaWarningThreshold: 1, BarkUrl: "ftp://example.test"},
		{QuotaWarningType: dto.NotifyTypeGotify, QuotaWarningThreshold: 1, GotifyUrl: "https://example.test"},
	} {
		require.Error(t, f.service.UpdateNotificationSettings(t.Context(), user.Id, request))
	}
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, settings, user.GetSetting())
}

func TestSelfPersonalTokenEmailAndDeletionRespectOwnership(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "self-account", common.RoleCommonUser)
	before := *user
	key, err := f.service.RotatePersonalAccessToken(t.Context(), user.Id)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(key), 28)
	assert.LessOrEqual(t, len(key), 32)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, key, user.GetAccessToken())
	assert.Equal(t, before.Quota, user.Quota)
	assert.Equal(t, before.Role, user.Role)
	assert.Equal(t, before.Password, user.Password)
	assert.Equal(t, before.AuthVersion, user.AuthVersion)
	next, err := f.service.RotatePersonalAccessToken(t.Context(), user.Id)
	require.NoError(t, err)
	assert.NotEqual(t, key, next)
	previous, err := model.ValidateAccessToken(key)
	require.NoError(t, err)
	assert.Nil(t, previous)
	authorized, err := model.ValidateAccessToken(next)
	require.NoError(t, err)
	require.NotNil(t, authorized)
	assert.Equal(t, user.Id, authorized.Id)
	require.Error(t, f.service.BindEmail(t.Context(), user.Id, contract.BindEmailRequest{Email: "bound@example.test", Code: "wrong"}))
	require.NoError(t, f.service.BindEmail(t.Context(), user.Id, contract.BindEmailRequest{Email: " BOUND@Example.Test ", Code: "valid"}))
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.Equal(t, "bound@example.test", user.Email)
	assert.Equal(t, before.Role, user.Role)
	assert.Equal(t, before.Quota, user.Quota)
	foreign := seedManagedUser(t, f, "self-foreign", common.RoleCommonUser)
	require.Error(t, f.db.Model(foreign).Update("access_token", next).Error)
	require.Error(t, f.service.BindEmail(t.Context(), foreign.Id, contract.BindEmailRequest{Email: "BOUND@EXAMPLE.TEST", Code: "valid"}))
	require.NoError(t, f.db.First(foreign, foreign.Id).Error)
	assert.Empty(t, foreign.Email)
	// Affiliation code generation is limited to its own column and stays stable across reads.
	require.NoError(t, f.db.Model(user).Update("aff_code", "").Error)
	code, err := f.service.AffiliationCode(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Len(t, code, 4)
	again, err := f.service.AffiliationCode(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Equal(t, code, again)
	seedManagedSession(t, f, user, "self-delete-session")
	require.NoError(t, f.service.DeleteSelf(t.Context(), user.Id))
	var deleted model.User
	require.NoError(t, f.db.Unscoped().First(&deleted, user.Id).Error)
	assert.True(t, deleted.DeletedAt.Valid)
	assert.EqualValues(t, 2, deleted.AuthVersion)
	assert.Equal(t, []string{"user_deleted"}, f.revocations)
	_, err = f.service.RotatePersonalAccessToken(t.Context(), user.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var revoked model.UserSession
	require.NoError(t, f.db.First(&revoked, "sid = ?", "self-delete-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, revoked.Status)
	root := seedManagedUser(t, f, "self-root", common.RoleRootUser)
	require.Error(t, f.service.DeleteSelf(t.Context(), root.Id))
	require.NoError(t, f.db.First(root, root.Id).Error)
	assert.EqualValues(t, 1, root.AuthVersion)
}

func selfRequest(t *testing.T, f *userFixture, accessToken, method, path string, body any) tokenAPIResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var result tokenAPIResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}

func TestBillingPreferencePreservesConcurrentProfileSettingsAndAccounting(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "preference", common.RoleCommonUser)
	user.SetSetting(dto.UserSetting{NotifyType: dto.NotifyTypeEmail, QuotaWarningThreshold: 1, Language: "zh", SidebarModules: `{"saved":true}`, BillingPreference: "subscription_first"})
	require.NoError(t, f.db.Model(user).Update("setting", user.Setting).Error)
	require.NoError(t, f.db.Model(user).Updates(map[string]any{"quota": 750, "used_quota": 270, "request_count": 4}).Error)
	language := "fr"
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := f.service.UpdateBillingPreference(t.Context(), user.Id, " wallet_only ")
		results <- err
	}()
	go func() {
		<-start
		_, err := f.service.UpdateSelf(t.Context(), user.Id, contract.SelfUpdateRequest{Preference: "language", PreferenceValue: &language}, nil)
		results <- err
	}()
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	var updated entity.User
	require.NoError(t, f.db.First(&updated, user.Id).Error)
	settings := updated.GetSetting()
	assert.Equal(t, "wallet_only", settings.BillingPreference)
	assert.Equal(t, "fr", settings.Language)
	assert.Equal(t, `{"saved":true}`, settings.SidebarModules)
	assert.Equal(t, dto.NotifyTypeEmail, settings.NotifyType)
	assert.Equal(t, 750, updated.Quota)
	assert.Equal(t, 270, updated.UsedQuota)
	assert.Equal(t, 4, updated.RequestCount)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	assert.EqualValues(t, 1, updated.AuthVersion)
	preference, err := f.service.BillingPreference(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Equal(t, "wallet_only", preference)
	require.NoError(t, f.db.Exec(`ALTER TABLE users ADD CONSTRAINT reject_preference_fixture CHECK (setting NOT LIKE '%wallet_first%')`).Error)
	_, err = f.service.UpdateBillingPreference(t.Context(), user.Id, "wallet_first")
	require.Error(t, err)
	preference, err = f.service.BillingPreference(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Equal(t, "wallet_only", preference)
	require.NoError(t, f.db.Exec("ALTER TABLE users DROP CONSTRAINT reject_preference_fixture").Error)
	for _, test := range []struct{ input, want string }{{"subscription_only", "subscription_only"}, {" wallet_first ", "wallet_first"}, {"invalid", "subscription_first"}, {"", "subscription_first"}} {
		got, err := f.service.UpdateBillingPreference(t.Context(), user.Id, test.input)
		require.NoError(t, err)
		assert.Equal(t, test.want, got)
	}
	login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "preferences-browser")
	require.NoError(t, err)
	response := selfRequest(t, f, login.AccessToken, http.MethodPut, "/self/billing-preference", map[string]any{"billing_preference": "wallet_only", "user_id": 9999, "quota": 999999, "role": 100})
	require.True(t, response.Success, response.Message)
	require.NoError(t, f.db.First(&updated, user.Id).Error)
	assert.Equal(t, "wallet_only", updated.GetSetting().BillingPreference)
	assert.Equal(t, 750, updated.Quota)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	require.NoError(t, f.db.Delete(user).Error)
	_, err = f.service.UpdateBillingPreference(t.Context(), user.Id, "wallet_first")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.service.BillingPreference(t.Context(), user.Id)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
