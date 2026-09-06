package routing

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTest(t *testing.T) (*gorm.DB, *Runtime) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
	return db, New(db, nil, nil)
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	db, runtime := setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-status",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModePolling,
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	changed := runtime.UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.True(t, changed)

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	db, runtime := setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      StringList{"original-model"},
		Group:       StringList{"default"},
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, db.Create(&channel).Error)

	stale, err := runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:   true,
		MultiKeySize: 2,
		MultiKeyMode: constant.MultiKeyModePolling,
	}
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       StringList{"concurrent-model"},
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]interface{}{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, runtime.saveChannelStatus(stale))

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, StringList{"concurrent-model"}, stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}

func TestReadingInvalidChannelSettingsDoesNotWriteDatabase(t *testing.T) {
	db, _ := setupChannelStatusTest(t)
	invalid := `[]`
	channel := Channel{Name: "invalid-config", Key: "fixture", Setting: &invalid, OtherSettings: invalid}
	require.NoError(t, db.Create(&channel).Error)
	channel.GetSetting()
	channel.GetOtherSettings()
	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	require.NotNil(t, stored.Setting)
	assert.JSONEq(t, invalid, *stored.Setting)
	assert.JSONEq(t, invalid, stored.OtherSettings)
}
