package identity_test

import (
	"errors"
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
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type userAuthorizationFixture struct{ fail bool }

func (*userAuthorizationFixture) Capabilities(id, role int) map[string]map[string]bool {
	return authz.Capabilities(id, role)
}
func (f *userAuthorizationFixture) SetPermissions(tx *gorm.DB, id int, permissions map[string]map[string]bool) error {
	if err := authz.SetUserPermissionsInTx(tx, id, permissions); err != nil {
		return err
	}
	if f.fail {
		return errors.New("injected permission failure")
	}
	return nil
}
func (f *userAuthorizationFixture) ClearPermissions(tx *gorm.DB, id int) error {
	if err := authz.ClearUserAuthorizationInTx(tx, id); err != nil {
		return err
	}
	if f.fail {
		return errors.New("injected permission failure")
	}
	return nil
}
func (*userAuthorizationFixture) Reload() error { return authz.ReloadPolicy() }

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
	previousRedis, previousMaster, previousBatch := common.RedisEnabled, common.IsMasterNode, common.BatchUpdateEnabled
	previousMain, previousLogs := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled, common.IsMasterNode, common.BatchUpdateEnabled = false, false, false
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLog
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
	require.NoError(t, authz.Init(db))
	f := &userFixture{db: db, router: gin.New(), authorization: &userAuthorizationFixture{}}
	wallet := billing.New(billing.Dependencies{DB: db, WalletRuntime: billing.WalletRuntime{
		Credit: func(id, amount int) error { return model.IncreaseUserQuota(id, amount, true) },
		Debit:  func(id, amount int) error { return model.DecreaseUserQuota(id, amount, true) },
	}})
	f.service = identity.New(identity.Dependencies{
		DB: db, UserAuthorization: f.authorization, UserWallet: wallet, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
		WelcomeQuota: func() int { return 250 }, WelcomeGrant: func(id, quota int) { assert.Equal(t, 250, quota); f.grants = append(f.grants, id) },
		UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			PublishDeletedVersion: model.PublishCommittedUserAuthVersion,
			RevokeSessions: func(id int, reason string) error {
				f.revocations = append(f.revocations, reason)
				_, err := model.RevokeAllUserSessions(id, reason)
				return err
			},
			InvalidateUser: model.InvalidateUserCache, InvalidateTokens: model.InvalidateUserTokensCache,
			DeleteCredentials: model.DeleteUserAuthenticationData, ReleaseExternalBinding: model.ReleaseExternalIdentityWithTx,
		},
	})
	h := identityhttp.New(f.service, identityhttp.ManagementHooks{Audit: func(c *gin.Context, id int, action string, params map[string]any) {
		f.audits = append(f.audits, contract.UserAudit{TargetID: id, Action: action, Parameters: params})
	}})
	f.router.Use(func(c *gin.Context) {
		role, _ := strconv.Atoi(c.GetHeader("X-Test-Role"))
		c.Set("role", role)
		c.Set("id", 9999)
		c.Next()
	})
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
	assert.True(t, authz.Can(user.Id, user.Role, authz.ChannelRead))
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
	assert.True(t, authz.Can(user.Id, user.Role, authz.ChannelRead))
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
	assert.False(t, authz.Can(user.Id, user.Role, authz.ChannelRead))
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
	require.NoError(t, authz.SetUserPermissions(admin.Id, authz.PermissionsMap{"channel": {"read": true}}))
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
	require.NoError(t, f.db.Model(&model.CasbinRule{}).Where("v0 = ?", authz.UserSubject(admin.Id)).Count(&policies).Error)
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
