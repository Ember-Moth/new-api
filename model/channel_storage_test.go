package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelArraysPreserveMembershipAndSurvivePartialUpdates(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })
	channels := []Channel{
		{Name: "exact", Key: "fixture", Models: StringList{" gpt-one ", "gpt-two", "gpt-one"}, Group: StringList{"vip", "vip%"}},
		{Name: "prefix", Key: "fixture", Models: StringList{"gpt-two"}, Group: StringList{"vip-plus"}},
	}
	require.NoError(t, db.Create(&channels).Error)
	var matches []Channel
	require.NoError(t, ApplyChannelGroupFilter(db, "vip").Find(&matches).Error)
	require.Len(t, matches, 1)
	assert.Equal(t, "exact", matches[0].Name)
	assert.Equal(t, StringList{"gpt-one", "gpt-two"}, matches[0].Models)
	matches = nil
	require.NoError(t, ApplyChannelGroupFilter(db, "vip%").Find(&matches).Error)
	require.Len(t, matches, 1)
	assert.Equal(t, "exact", matches[0].Name)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channels[0].Id).Updates(&Channel{Name: "renamed"}).Error)
	var updated Channel
	require.NoError(t, db.First(&updated, channels[0].Id).Error)
	assert.Equal(t, "renamed", updated.Name)
	assert.Equal(t, StringList{"gpt-one", "gpt-two"}, updated.Models)
	assert.Equal(t, StringList{"vip", "vip%"}, updated.Group)
	encoded, err := common.Marshal(updated)
	require.NoError(t, err)
	var response struct {
		Models []string `json:"models"`
		Group  []string `json:"group"`
	}
	require.NoError(t, common.Unmarshal(encoded, &response))
	assert.Equal(t, []string{"gpt-one", "gpt-two"}, response.Models)
	assert.Equal(t, []string{"vip", "vip%"}, response.Group)
	filtered, err := SearchChannels("", "vip", "gpt", false)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, channels[0].Id, filtered[0].Id)

	settings := `{"proxy":"http://proxy.example:8080"}`
	mapping := `{"alias":"gpt-one"}`
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", updated.Id).
		Updates(&Channel{Setting: &settings, ModelMapping: &mapping}).Error)
	var proxy, mapped string
	require.NoError(t, db.Raw("SELECT setting->>'proxy' FROM channels WHERE id = ?", updated.Id).Scan(&proxy).Error)
	require.NoError(t, db.Raw("SELECT model_mapping->>'alias' FROM channels WHERE id = ?", updated.Id).Scan(&mapped).Error)
	assert.Equal(t, "http://proxy.example:8080", proxy)
	assert.Equal(t, "gpt-one", mapped)
	empty := ""
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", updated.Id).Updates(&Channel{ModelMapping: &empty}).Error)
	require.NoError(t, db.First(&updated, updated.Id).Error)
	assert.JSONEq(t, `{}`, updated.GetModelMapping())
	assert.Equal(t, StringList{"gpt-one", "gpt-two"}, updated.Models)
	require.NoError(t, ChannelService().AddAbilities(&(updated), nil))
	require.NoError(t, db.Exec(`CREATE FUNCTION fail_routing_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected routing relation failure'; END;
$$;
CREATE TRIGGER fail_routing_update BEFORE INSERT ON abilities FOR EACH ROW EXECUTE FUNCTION fail_routing_update();`).Error)
	changed := updated
	changed.Models = StringList{"replacement"}
	require.Error(t, ChannelService().UpdateChannel(&(changed)))
	require.NoError(t, db.First(&updated, updated.Id).Error)
	assert.Equal(t, StringList{"gpt-one", "gpt-two"}, updated.Models)
	var count int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND model = ?", updated.Id, "gpt-one").Count(&count).Error)
	assert.EqualValues(t, 2, count, "routing and channel configuration must roll back together")
}
