package options

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/config/setting/billing_setting"
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useOptionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousType := common.MainDatabaseType()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	t.Cleanup(func() {
		common.SetMainDatabaseType(previousType)
	})
	return db
}

func requireOptionValue(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var option entity.Option
	require.NoError(t, db.Where(&entity.Option{Key: key}).First(&option).Error)
	return option.Value
}

func TestRetiredThemeOptionIsPersistedButNotPublished(t *testing.T) {
	db := useOptionTestDB(t)
	previousMap := common.OptionMap
	t.Cleanup(func() { common.OptionMap = previousMap })
	common.OptionMap = map[string]string{}

	require.NoError(t, New(Dependencies{DB: db}).UpdateOption(t.Context(), retiredThemeOptionKey, "default"))
	assert.Equal(t, "default", requireOptionValue(t, db, retiredThemeOptionKey))
	_, published := common.OptionMap[retiredThemeOptionKey]
	assert.False(t, published)
}

func TestOptionPersistenceFailureDoesNotPublishConfiguration(t *testing.T) {
	db := useOptionTestDB(t)
	previousMap := common.OptionMap
	previousRatios := ratio_setting.ImageRatio2JSONString()
	common.OptionMap = map[string]string{"Notice": "before", "ImageRatio": `{"old-model":2}`}
	require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"old-model":2}`))
	t.Cleanup(func() {
		common.OptionMap = previousMap
		require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(previousRatios))
	})
	manager := New(Dependencies{DB: db.Session(&gorm.Session{CreateBatchSize: 1, SkipDefaultTransaction: true})})
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_option_write() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.key IN ('ImageRatio','zz-fail') THEN RAISE EXCEPTION 'injected option failure'; END IF; RETURN NEW; END;
 $$;
 CREATE TRIGGER reject_option_write BEFORE INSERT OR UPDATE ON options FOR EACH ROW EXECUTE FUNCTION reject_option_write();`).Error)
	err := manager.UpdateManagedOption(t.Context(), contract.OptionUpdateRequest{Key: "ImageRatio", Value: `{"new-model":3}`})
	require.Error(t, err)
	ratio, ok := ratio_setting.GetImageRatio("old-model")
	assert.True(t, ok)
	assert.Equal(t, 2.0, ratio)
	_, ok = ratio_setting.GetImageRatio("new-model")
	assert.False(t, ok)
	assert.Equal(t, `{"old-model":2}`, common.OptionMap["ImageRatio"])
	require.Error(t, manager.UpdateOptionsBulk(t.Context(), map[string]string{"Notice": "not-published", "zz-fail": "failure"}))
	var rows int64
	require.NoError(t, db.Model(&entity.Option{}).Where(`"key" IN ?`, []string{"Notice", "ImageRatio", "zz-fail"}).Count(&rows).Error)
	assert.Zero(t, rows)
	assert.Equal(t, "before", common.OptionMap["Notice"])
	require.NoError(t, db.Exec("DROP TRIGGER reject_option_write ON options").Error)
	require.NoError(t, manager.UpdateOptionsBulk(t.Context(), map[string]string{"Notice": "saved", "zz-fail": "saved"}))
	assert.Equal(t, "saved", requireOptionValue(t, db, "Notice"))
	assert.Equal(t, "saved", common.OptionMap["Notice"])
	require.NoError(t, manager.UpdateOption(t.Context(), "Notice", ""))
	assert.Empty(t, requireOptionValue(t, db, "Notice"))
	assert.Empty(t, common.OptionMap["Notice"])
	// Invalid ratios must be rejected before either storage or the live map changes.
	require.Error(t, manager.UpdateOptionsBulk(t.Context(), map[string]string{"Notice": "not-saved", "ImageRatio": `{"broken":`}))
	assert.Empty(t, requireOptionValue(t, db, "Notice"))
	ratio, ok = ratio_setting.GetImageRatio("old-model")
	assert.True(t, ok)
	assert.Equal(t, 2.0, ratio)
}

func TestOptionSnapshotHidesSecretsAndIncludesEffectiveBilling(t *testing.T) {
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{
		"Notice": "public", "SMTPToken": "secret-token", "WaffoApiKey": "secret-key", "oidc.client_secret": "secret-value", "theme.frontend": "default",
		"billing_setting.billing_expr": "{}", "billing_setting.billing_mode": "{}", "ModelRatio": `{"example-model":1}`,
	}
	t.Cleanup(func() { common.OptionMap = previousMap })
	options, err := New(Dependencies{}).GetOptions()
	require.NoError(t, err)
	values := make(map[string]string)
	for _, option := range options {
		values[option.Key] = option.Value
	}
	assert.Equal(t, "public", values["Notice"])
	for _, key := range []string{"SMTPToken", "WaffoApiKey", "oidc.client_secret", "theme.frontend"} {
		assert.NotContains(t, values, key)
	}
	var expressions map[string]string
	require.NoError(t, common.UnmarshalJsonStr(values["billing_setting.billing_expr"], &expressions))
	assert.Equal(t, billing_setting.GetBillingExprCopy(), expressions)
	var modes map[string]string
	require.NoError(t, common.UnmarshalJsonStr(values["billing_setting.billing_mode"], &modes))
	assert.Equal(t, billing_setting.GetBillingModeCopy(), modes)
	var metadata map[string]ratio_setting.CompletionRatioInfo
	require.NoError(t, common.UnmarshalJsonStr(values["CompletionRatioMeta"], &metadata))
	assert.Contains(t, metadata, "example-model")
}

func TestOptionReloadPublishesCommittedValuesAndPreservesFailedSnapshots(t *testing.T) {
	db := useOptionTestDB(t)
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{"Notice": "initial"}
	t.Cleanup(func() { common.OptionMap = previousMap })
	writer := New(Dependencies{DB: db})
	reader := New(Dependencies{DB: db})
	require.NoError(t, writer.UpdateOption(t.Context(), "Notice", "committed"))
	common.OptionMap["Notice"] = "before-reload"
	require.NoError(t, reader.Reload(t.Context()))
	assert.Equal(t, "committed", common.OptionMap["Notice"])
	require.NoError(t, db.Create(&entity.Option{Key: "MaxTokenAutoGroups", Value: "invalid"}).Error)
	require.NoError(t, db.Model(&entity.Option{}).Where(`"key" = ?`, "Notice").Update("value", "not-published").Error)
	require.Error(t, reader.Reload(t.Context()))
	assert.Equal(t, "committed", common.OptionMap["Notice"])
}
