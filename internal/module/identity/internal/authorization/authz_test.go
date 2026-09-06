package authz

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, schema.UpPostgres(sqlDB, schema.Main))
	require.NoError(t, schema.UpPostgres(sqlDB, schema.Main))
	return db
}

func TestInitSeedsBuiltInRolesAndPoliciesOnce(t *testing.T) {
	db := newAuthzTestDB(t)

	engine, err := New(db, true)
	require.NoError(t, err)
	engine, err = New(db, true)
	require.NoError(t, err)

	// root is a superuser role and is granted everything implicitly, so only the
	// admin baseline is written as explicit policy rows.
	var count int64
	require.NoError(t, db.Model(&identityentity.CasbinRule{}).Count(&count).Error)
	assert.Equal(t, int64(len(PermissionsForRole(BuiltInRoleAdmin))), count)

	var roles []identityentity.AuthzRole
	require.NoError(t, db.Order("sort asc").Find(&roles).Error)
	require.Len(t, roles, 2)
	assert.Equal(t, BuiltInRoleRoot, roles[0].Key)
	assert.Equal(t, BuiltInRoleAdmin, roles[1].Key)

	assert.True(t, engine.Can(1, common.RoleRootUser, ChannelSensitiveWrite))
	assert.True(t, engine.Can(2, common.RoleAdminUser, ChannelRead))
	assert.True(t, engine.Can(2, common.RoleAdminUser, ChannelOperate))
	assert.True(t, engine.Can(2, common.RoleAdminUser, ChannelWrite))
	assert.False(t, engine.Can(2, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, engine.Can(3, common.RoleCommonUser, ChannelRead))
}

func TestInitOnSlaveOnlyLoadsPolicies(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, schema.UpPostgres(sqlDB, schema.Main))
	require.NoError(t, schema.UpPostgres(sqlDB, schema.Main))

	engine, err := New(db, false)
	require.NoError(t, err)

	var roleCount int64
	require.NoError(t, db.Model(&identityentity.AuthzRole{}).Count(&roleCount).Error)
	assert.Equal(t, int64(0), roleCount)
	var policyCount int64
	require.NoError(t, db.Model(&identityentity.CasbinRule{}).Count(&policyCount).Error)
	assert.Equal(t, int64(0), policyCount)
	assert.False(t, engine.Can(2, common.RoleAdminUser, ChannelRead))
}

func TestSetUserPermissionsStoresOnlyOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	require.NoError(t, engine.SetUserPermissions(42, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
			"unknown":            true,
		},
		"unknown": {
			ActionRead: true,
		},
	}))

	assert.True(t, engine.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, engine.Can(42, common.RoleAdminUser, ChannelWrite))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		},
		ResourceTaskPlugin: {
			ActionBind: false,
		},
	}, engine.ExplicitUserPermissions(42))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionSensitiveWrite: true,
			ActionWrite:          false,
		},
	}, engine.ExplicitUserOverrides(42))

	var userPolicyCount int64
	require.NoError(t, db.Model(&identityentity.CasbinRule{}).Where("v0 = ?", UserSubject(42)).Count(&userPolicyCount).Error)
	assert.Equal(t, int64(2), userPolicyCount)

	require.NoError(t, engine.SetUserPermissions(42, PermissionsMap{ResourceChannel: {
		ActionRead:           true,
		ActionOperate:        true,
		ActionWrite:          true,
		ActionSensitiveWrite: false,
		ActionSecretView:     false,
	}}))
	assert.False(t, engine.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: false,
			ActionSecretView:     false,
		},
		ResourceTaskPlugin: {
			ActionBind: false,
		},
	}, engine.ExplicitUserPermissions(42))
	assert.Empty(t, engine.ExplicitUserOverrides(42))
}

func TestClearUserAuthorizationRemovesOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	require.NoError(t, engine.SetUserPermissions(90, PermissionsMap{ResourceChannel: {
		ActionWrite:          false,
		ActionSensitiveWrite: true,
	}}))

	assert.True(t, engine.Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, engine.Can(90, common.RoleAdminUser, ChannelWrite))

	require.NoError(t, engine.ClearUserAuthorization(90))

	assert.Empty(t, engine.ExplicitUserOverrides(90))
	assert.True(t, engine.Can(90, common.RoleAdminUser, ChannelRead))
	assert.True(t, engine.Can(90, common.RoleAdminUser, ChannelWrite))
	assert.False(t, engine.Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, engine.Can(90, common.RoleCommonUser, ChannelRead))
}

func TestSetUserPermissionsInTxDoesNotMutateEnforcerBeforeReload(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return engine.SetUserPermissionsInTx(tx, 42, PermissionsMap{ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		}})
	}))

	assert.False(t, engine.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	require.NoError(t, engine.ReloadPolicy())
	assert.True(t, engine.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
}

func TestSetUserPermissionsInTxRollbackLeavesNoPolicy(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, engine.SetUserPermissionsInTx(tx, 43, PermissionsMap{ResourceChannel: {
		ActionSensitiveWrite: true,
	}}))
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, engine.ReloadPolicy())

	assert.False(t, engine.Can(43, common.RoleAdminUser, ChannelSensitiveWrite))
	var count int64
	require.NoError(t, db.Model(&identityentity.CasbinRule{}).Where("v0 = ?", UserSubject(43)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestAdapterAddPolicyIsIdempotent(t *testing.T) {
	db := newAuthzTestDB(t)
	adapter := newGormAdapter(db)
	rule := []string{UserSubject(55), ResourceChannel, ActionSensitiveWrite, EffectAllow}

	require.NoError(t, adapter.AddPolicy("p", "p", rule))
	require.NoError(t, adapter.AddPolicy("p", "p", rule))

	var count int64
	require.NoError(t, db.Model(&identityentity.CasbinRule{}).Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ?",
		"p",
		UserSubject(55),
		ResourceChannel,
		ActionSensitiveWrite,
		EffectAllow,
	).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCapabilitiesUseCatalogShape(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	capabilities := engine.Capabilities(7, common.RoleAdminUser)

	assert.True(t, capabilities[ResourceChannel][ActionRead])
	assert.True(t, capabilities[ResourceChannel][ActionOperate])
	assert.True(t, capabilities[ResourceChannel][ActionWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSensitiveWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSecretView])
	assert.False(t, capabilities[ResourceTaskPlugin][ActionBind])
}

func TestTaskPluginBindIsRootOnlyUntilGranted(t *testing.T) {
	db := newAuthzTestDB(t)
	engine, err := New(db, true)
	require.NoError(t, err)

	var bindAction *ActionDefinition
	for _, resource := range Catalog() {
		if resource.Resource != ResourceTaskPlugin {
			continue
		}
		assert.Equal(t, "Task Plugin", resource.LabelKey)
		for i := range resource.Actions {
			if resource.Actions[i].Action == ActionBind {
				bindAction = &resource.Actions[i]
			}
		}
	}
	require.NotNil(t, bindAction)
	assert.Equal(t, "Bind task plugins", bindAction.LabelKey)
	assert.Equal(t, "List registered task plugins and bind them when creating or editing task plugin channels.", bindAction.DescriptionKey)
	assert.Empty(t, bindAction.DefaultRoles)

	assert.False(t, engine.Can(2, common.RoleAdminUser, TaskPluginBind))
	assert.True(t, engine.Can(1, common.RoleRootUser, TaskPluginBind))

	enforcer := engine.currentEnforcer()
	require.NotNil(t, enforcer)
	_, err = enforcer.AddPolicy(RoleSubject(BuiltInRoleAdmin), ResourceTaskPlugin, ActionBind, EffectAllow)
	require.NoError(t, err)
	assert.True(t, engine.Can(2, common.RoleAdminUser, TaskPluginBind))

	_, err = enforcer.RemovePolicy(RoleSubject(BuiltInRoleAdmin), ResourceTaskPlugin, ActionBind, EffectAllow)
	require.NoError(t, err)
	assert.False(t, engine.Can(2, common.RoleAdminUser, TaskPluginBind))
}

func TestPolicyInstancesStayIsolatedAndReloadCommittedChanges(t *testing.T) {
	database := newAuthzTestDB(t)
	primary, err := New(database, true)
	require.NoError(t, err)
	replica, err := New(database, false)
	require.NoError(t, err)
	unrelated, err := New(newAuthzTestDB(t), true)
	require.NoError(t, err)
	require.NoError(t, primary.SetUserPermissions(42, PermissionsMap{ResourceChannel: {ActionWrite: false, ActionSensitiveWrite: true}}))
	assert.False(t, primary.Can(42, common.RoleAdminUser, ChannelWrite))
	assert.True(t, primary.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.True(t, replica.Can(42, common.RoleAdminUser, ChannelWrite))
	assert.False(t, replica.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	require.NoError(t, replica.ReloadPolicy())
	assert.False(t, replica.Can(42, common.RoleAdminUser, ChannelWrite))
	assert.True(t, replica.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.True(t, unrelated.Can(42, common.RoleAdminUser, ChannelWrite))
	assert.False(t, unrelated.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	primaryContext := WithEngine(t.Context(), primary)
	otherContext := WithEngine(t.Context(), unrelated)
	assert.True(t, FromContext(primaryContext).Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, FromContext(otherContext).Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, FromContext(t.Context()).Can(42, common.RoleAdminUser, ChannelWrite))
}

func TestFailedBaselineInitializationPreservesExistingPermissions(t *testing.T) {
	database := newAuthzTestDB(t)
	engine, err := New(database, true)
	require.NoError(t, err)
	require.NoError(t, engine.SetUserPermissions(42, PermissionsMap{ResourceChannel: {ActionSensitiveWrite: true}}))
	require.NoError(t, database.Exec(`CREATE FUNCTION reject_baseline_policy() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.v0 LIKE 'role:%' THEN RAISE EXCEPTION 'injected policy seed failure'; END IF; RETURN NEW; END;
 $$;
 CREATE TRIGGER reject_baseline_policy BEFORE INSERT ON casbin_rule FOR EACH ROW EXECUTE FUNCTION reject_baseline_policy();`).Error)
	_, err = New(database, true)
	require.Error(t, err)
	require.NoError(t, engine.ReloadPolicy())
	assert.True(t, engine.Can(42, common.RoleAdminUser, ChannelRead))
	assert.True(t, engine.Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	var roles []identityentity.AuthzRole
	require.NoError(t, database.Order("sort asc").Find(&roles).Error)
	require.Len(t, roles, 2)
}
