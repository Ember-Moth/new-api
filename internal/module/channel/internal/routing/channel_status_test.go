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

func TestAutomaticStatusStaysInDragonflyAndManualStopWins(t *testing.T) {
	db, runtime := setupChannelStatusTest(t)
	channel := Channel{Name: "runtime-status", Key: "key-a", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)

	require.True(t, runtime.UpdateChannelStatus(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "provider rejected key"))
	effective, err := runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, effective.Status)
	staleAutoProjection := *effective
	require.NoError(t, runtime.SaveChannel(&staleAutoProjection))
	var storedAfterAutoProjection Channel
	require.NoError(t, db.First(&storedAfterAutoProjection, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, storedAfterAutoProjection.Status)
	assert.NotContains(t, storedAfterAutoProjection.GetOtherInfo(), "status_reason")
	assert.NotContains(t, storedAfterAutoProjection.GetOtherInfo(), "status_time")

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)

	assert.True(t, runtime.UpdateManualChannelStatus(channel.Id, common.ChannelStatusManuallyDisabled, "manual operation"))
	effective, err = runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, effective.Status)
	assert.False(t, runtime.UpdateChannelStatus(channel.Id, channel.Key, common.ChannelStatusEnabled, "provider recovered"))
	effective, err = runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, effective.Status)
	staleAutomaticProjection := *effective
	staleAutomaticProjection.Status = common.ChannelStatusAutoDisabled
	require.NoError(t, runtime.SaveChannel(&staleAutomaticProjection))
	var storedAfterStaleSave Channel
	require.NoError(t, db.First(&storedAfterStaleSave, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, storedAfterStaleSave.Status)

	assert.True(t, runtime.UpdateManualChannelStatus(channel.Id, common.ChannelStatusEnabled, "manual operation"))
	effective, err = runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, effective.Status)
}

func TestAutomaticStatusRejectsAStaleKeyPool(t *testing.T) {
	db, runtime := setupChannelStatusTest(t)
	channel := Channel{
		Name:   "rotating-runtime-status",
		Key:    "old-a\nold-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModePolling,
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	oldFingerprint := ChannelKeyPoolFingerprint(&channel)
	require.True(t, runtime.UpdateChannelStatus(channel.Id, "old-a", common.ChannelStatusAutoDisabled, "old request"))

	channel.Key = "new-a\nnew-b"
	channel.ChannelInfo.MultiKeySize = 2
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{"key": channel.Key, "channel_info": channel.ChannelInfo}).Error)

	assert.False(t, runtime.UpdateChannelStatus(channel.Id, "old-a", common.ChannelStatusAutoDisabled, "stale request"))
	assert.False(t, runtime.UpdateChannelStatusForKeyPool(channel.Id, "new-a", common.ChannelStatusAutoDisabled, "stale overlapping request", oldFingerprint))
	effective, err := runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, effective.Status)
	assert.Empty(t, effective.ChannelInfo.MultiKeyStatusList)
}

func TestReadOnlyRoutingUsesSharedAutomaticStatus(t *testing.T) {
	cache := testdb.UseCache(t)
	runtime := New(nil, nil, nil, SnapshotConfig{Cache: cache, ReadOnly: true})
	priority := int64(0)
	weight := uint(1)
	runtime.applyChannelSnapshot(channelSnapshot{Channels: []*Channel{{
		Id:       900100,
		Name:     "data-plane-runtime-status",
		Key:      "key-a\nkey-b",
		Status:   common.ChannelStatusEnabled,
		Models:   StringList{"shared"},
		Group:    StringList{"default"},
		Priority: &priority,
		Weight:   &weight,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}}})

	require.True(t, runtime.UpdateChannelStatus(900100, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key"))
	effective, err := runtime.GetChannelById(900100, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, effective.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, effective.ChannelInfo.MultiKeyStatusList[0])
	selected, err := runtime.GetRandomSatisfiedChannel("default", "shared", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	key, index, apiErr := runtime.GetNextEnabledKey(selected)
	require.Nil(t, apiErr)
	assert.Equal(t, "key-b", key)
	assert.Equal(t, 1, index)

	require.True(t, runtime.UpdateChannelStatus(900100, "key-b", common.ChannelStatusAutoDisabled, "provider rejected key"))
	assert.True(t, runtime.UpdateChannelStatus(900100, "key-a", common.ChannelStatusEnabled, "provider recovered"))
	effective, err = runtime.GetChannelById(900100, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, effective.Status)
	selected, err = runtime.GetRandomSatisfiedChannel("default", "shared", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "key-a", effective.GetKeys()[0])
}

func setupChannelStatusTest(t *testing.T) (*gorm.DB, *Runtime) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	cache := testdb.UseCache(t)
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
	return db, New(db, nil, nil, SnapshotConfig{Cache: cache})
}

func TestUpdateChannelStatusStoresMultiKeyStateInRuntime(t *testing.T) {
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
	assert.Empty(t, stored.ChannelInfo.MultiKeyStatusList)
	assert.Empty(t, stored.ChannelInfo.MultiKeyDisabledReason)
	assert.Empty(t, stored.ChannelInfo.MultiKeyDisabledTime)
	effective, err := runtime.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, effective.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, effective.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", effective.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, effective.ChannelInfo.MultiKeyDisabledTime[0])
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
