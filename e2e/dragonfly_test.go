package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing"
	billingentity "github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/module/system"
	systemcontract "github.com/QuantumNous/new-api/internal/module/system/contract"
	systemhttp "github.com/QuantumNous/new-api/internal/module/system/transport/http"

	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"

	"crypto/sha256"
	"encoding/hex"

	"github.com/QuantumNous/new-api/internal/infra/configsync"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityflows "github.com/QuantumNous/new-api/internal/module/identity/flows"
	usersessions "github.com/QuantumNous/new-api/internal/module/identity/sessions"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/internal/transport/http/controller"
	"github.com/QuantumNous/new-api/internal/transport/http/middleware"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDragonflyCacheContracts(t *testing.T) {
	dsn := os.Getenv("TEST_DRAGONFLY_DSN")
	if dsn == "" {
		t.Skip("TEST_DRAGONFLY_DSN is required for the real DragonflyDB integration test")
	}
	previousClient, previousEnabled := common.RDB, common.RedisEnabled
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousBatch, previousSync := common.BatchUpdateEnabled, common.SyncFrequency
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousClient, previousEnabled
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.BatchUpdateEnabled, common.SyncFrequency = previousBatch, previousSync
	})
	t.Setenv("REDIS_CONN_STRING", dsn)
	t.Setenv("SYNC_FREQUENCY", "60")
	common.RedisEnabled = false
	require.NoError(t, common.InitRedisClient())
	client := common.RDB
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	assert.True(t, common.RedisEnabled)
	// This test owns logical database 15 exclusively, never the application's DB 0.
	require.Equal(t, 15, client.Options().DB, "use a disposable DragonflyDB database 15")
	info, err := client.Info(t.Context(), "server").Result()
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(info), "dragonfly_version")
	count, err := client.DBSize(t.Context()).Result()
	require.NoError(t, err)
	require.Zero(t, count, "refusing to clear a non-empty cache database")
	t.Cleanup(func() { require.NoError(t, client.FlushDB(context.Background()).Err()) })

	database, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	model.DB = database
	model.LOG_DB = testdb.Logs(t, database)
	common.BatchUpdateEnabled, common.SyncFrequency = false, 60
	pool, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	require.NoError(t, schema.UpPostgres(pool))

	t.Run("multi-key polling shares a bounded cursor without PostgreSQL writes", func(t *testing.T) {
		node := redis.NewClient(client.Options())
		t.Cleanup(func() { _ = node.Close() })
		one := channelmodule.New(channelmodule.Dependencies{Cache: client, ReadOnly: true})
		two := channelmodule.New(channelmodule.Dependencies{Cache: node, ReadOnly: true})
		channel := model.Channel{Name: "shared-key-pool", Type: 1, Key: "key-a\nkey-b\nkey-c", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: map[int]int{1: common.ChannelStatusAutoDisabled}}}
		require.NoError(t, database.Create(&channel).Error)
		t.Cleanup(func() { require.NoError(t, database.Delete(&model.Channel{}, channel.Id).Error) })
		var before string
		require.NoError(t, database.Raw("SELECT channel_info::text FROM channels WHERE id = ?", channel.Id).Scan(&before).Error)
		for i, expected := range []string{"key-a", "key-c", "key-a", "key-c"} {
			worker := one
			if i%2 == 1 {
				worker = two
			}
			key, _, apiErr := worker.GetNextEnabledKey(&channel)
			require.Nil(t, apiErr)
			assert.Equal(t, expected, key)
		}
		start := make(chan struct{})
		results := make(chan string, 2)
		failures := make(chan bool, 2)
		for _, worker := range []*channelmodule.Service{one, two} {
			go func() {
				<-start
				key, _, apiErr := worker.GetNextEnabledKey(&channel)
				results <- key
				failures <- apiErr != nil
			}()
		}
		close(start)
		assert.ElementsMatch(t, []string{"key-a", "key-c"}, []string{<-results, <-results})
		assert.False(t, <-failures)
		assert.False(t, <-failures)
		rotated := channel
		rotated.Key = "rotated-a\nrotated-b"
		rotated.Keys = nil
		rotated.ChannelInfo.MultiKeyStatusList = nil
		key, _, apiErr := one.GetNextEnabledKey(&rotated)
		require.Nil(t, apiErr)
		assert.Equal(t, "rotated-a", key)
		_, _, apiErr = two.GetNextEnabledKey(&channel)
		require.Nil(t, apiErr)
		key, _, apiErr = two.GetNextEnabledKey(&rotated)
		require.Nil(t, apiErr)
		assert.Equal(t, "rotated-b", key)
		keys, err := client.Keys(t.Context(), fmt.Sprintf("channel:poll:%d:*", channel.Id)).Result()
		require.NoError(t, err)
		require.Len(t, keys, 2)
		for _, key := range keys {
			ttl, err := client.TTL(t.Context(), key).Result()
			require.NoError(t, err)
			assert.InDelta(t, 86400, ttl.Seconds(), 2)
			assert.NotContains(t, key, "key-a")
		}
		t.Cleanup(func() { require.NoError(t, client.Del(context.Background(), keys...).Err()) })
		var after string
		require.NoError(t, database.Raw("SELECT channel_info::text FROM channels WHERE id = ?", channel.Id).Scan(&after).Error)
		assert.Equal(t, before, after)
		assert.NotContains(t, after, "multi_key_polling_index")
		require.NoError(t, node.Close())
		key, _, apiErr = two.GetNextEnabledKey(&channel)
		require.NotNil(t, apiErr)
		assert.Empty(t, key)
	})

	t.Run("plugin snapshots compile without a database and retain the last good generation", func(t *testing.T) {
		oldRegistry := jsplugin.DefaultRegistry
		jsplugin.DefaultRegistry = jsplugin.NewRegistry()
		writer := model.NewTaskPluginSnapshots(database, client, false)
		reader := model.NewTaskPluginSnapshots(nil, client, true)
		previousSource := controller.ConfigureTaskPluginSnapshots(reader)
		t.Cleanup(func() {
			controller.ConfigureTaskPluginSnapshots(previousSource)
			jsplugin.DefaultRegistry = oldRegistry
			require.NoError(t, client.Del(context.Background(), "config:snapshot:task-plugins").Err())
			require.NoError(t, database.Where("key = ?", "snapshot-plugin").Delete(&model.TaskPlugin{}).Error)
		})
		require.Error(t, controller.InitializeTaskPlugins(t.Context()))
		source := `export const meta = {apiVersion:1,key:"snapshot-plugin",name:"Snapshot",version:"1.0.0",author:{name:"Test"},models:["snapshot-model"],fetchMode:"per_task"};
export function buildSubmitRequest(){return {};}
export function parseSubmitResponse(){return {};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}`
		digest := sha256.Sum256([]byte(source))
		plugin := model.TaskPlugin{Key: "snapshot-plugin", APIVersion: 1, Version: "1.0.0", Source: source, SourceHash: hex.EncodeToString(digest[:]), Enabled: true}
		require.NoError(t, model.SaveTaskPlugin(&plugin))
		published, err := writer.Load(t.Context())
		require.NoError(t, err)
		loaded, err := reader.Load(t.Context())
		require.NoError(t, err)
		assert.Equal(t, published.PublishedVersion, loaded.PublishedVersion)
		require.Len(t, loaded.Plugins, 1)
		assert.Equal(t, source, loaded.Plugins[0].Source)
		require.NoError(t, controller.InitializeTaskPlugins(t.Context()))
		generation := jsplugin.DefaultRegistry.Generation()
		original := jsplugin.DefaultRegistry.OverridePlugins()["snapshot-plugin"]
		require.NotNil(t, original)
		assert.Equal(t, published.PublishedVersion, controller.TaskPluginSnapshotVersion())
		require.NoError(t, controller.InitializeTaskPlugins(t.Context()))
		assert.Same(t, generation, jsplugin.DefaultRegistry.Generation())
		require.NoError(t, database.Model(&model.TaskPlugin{}).Where("id = ?", plugin.Id).Updates(map[string]any{"source": "invalid javascript {", "source_hash": "invalid-source"}).Error)
		_, err = writer.Load(t.Context())
		require.NoError(t, err)
		require.NoError(t, controller.InitializeTaskPlugins(t.Context()))
		assert.Same(t, original, jsplugin.DefaultRegistry.OverridePlugins()["snapshot-plugin"])
		assert.Empty(t, controller.TaskPluginSnapshotVersion(), "a partial generation must not advertise complete application")
		require.NoError(t, client.Del(t.Context(), "config:snapshot:task-plugins").Err())
		require.Error(t, controller.InitializeTaskPlugins(t.Context()))
		assert.Same(t, original, jsplugin.DefaultRegistry.OverridePlugins()["snapshot-plugin"])
		require.NoError(t, database.Model(&model.TaskPlugin{}).Where("id = ?", plugin.Id).Update("enabled", false).Error)
		published, err = writer.Load(t.Context())
		require.NoError(t, err)
		require.NoError(t, controller.InitializeTaskPlugins(t.Context()))
		assert.Nil(t, jsplugin.DefaultRegistry.OverridePlugins()["snapshot-plugin"])
		assert.Equal(t, published.PublishedVersion, controller.TaskPluginSnapshotVersion())
	})

	t.Run("channel routing and pricing load only from published snapshots", func(t *testing.T) {
		previousMemory := common.MemoryCacheEnabled
		common.MemoryCacheEnabled = false
		t.Cleanup(func() {
			common.MemoryCacheEnabled = previousMemory
			require.NoError(t, client.Del(context.Background(), "config:snapshot:channels").Err())
		})
		writer := channelmodule.New(channelmodule.Dependencies{DB: database, Cache: client})
		reader := channelmodule.New(channelmodule.Dependencies{Cache: client, ReadOnly: true})
		require.Error(t, reader.ReloadChannelCache(t.Context()))
		lowPriority, highPriority := int64(1), int64(9)
		channel := model.Channel{Type: 1, Name: "snapshot-low", Key: "snapshot-secret", Status: common.ChannelStatusEnabled, Models: model.StringList{"snapshot-model"}, Group: model.StringList{"default"}, Priority: &lowPriority}
		require.NoError(t, writer.InsertChannel(&channel))
		higher := model.Channel{Type: 1, Name: "snapshot-high", Key: "higher-secret", Status: common.ChannelStatusEnabled, Models: model.StringList{"snapshot-model"}, Group: model.StringList{"default"}, Priority: &highPriority}
		require.NoError(t, writer.InsertChannel(&higher))
		t.Cleanup(func() {
			require.NoError(t, database.Where("channel_id IN ?", []int{channel.Id, higher.Id}).Delete(&model.Ability{}).Error)
			require.NoError(t, database.Delete(&model.Channel{}, []int{channel.Id, higher.Id}).Error)
		})
		require.NoError(t, writer.ReloadChannelCache(t.Context()))
		require.NoError(t, reader.ReloadChannelCache(t.Context()))
		assert.Equal(t, writer.ChannelSnapshotVersion(), reader.ChannelSnapshotVersion())
		selected, err := reader.GetRandomSatisfiedChannel("default", "snapshot-model", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, selected)
		assert.Equal(t, higher.Id, selected.Id)
		assert.Equal(t, "higher-secret", selected.Key)
		fallback, err := reader.GetRandomSatisfiedChannel("default", "snapshot-model", 1, nil)
		require.NoError(t, err)
		require.NotNil(t, fallback)
		assert.Equal(t, channel.Id, fallback.Id)
		assert.Equal(t, []string{"snapshot-model"}, reader.GetGroupEnabledModels("default"))
		abilities, err := reader.EnabledPricingAbilities(t.Context())
		require.NoError(t, err)
		require.Len(t, abilities, 2)
		assert.Equal(t, 1, abilities[0].ChannelType)
		redacted, err := reader.GetChannelById(higher.Id, false)
		require.NoError(t, err)
		assert.Empty(t, redacted.Key)
		full, err := reader.GetChannelById(higher.Id, true)
		require.NoError(t, err)
		assert.Equal(t, "higher-secret", full.Key)
		// Failed optimistic local changes can be reconciled even if the source version is unchanged.
		reader.CacheUpdateChannelStatus(higher.Id, common.ChannelStatusAutoDisabled)
		require.NoError(t, reader.ReloadChannelCache(t.Context()))
		selected, err = reader.GetRandomSatisfiedChannel("default", "snapshot-model", 0, nil)
		require.NoError(t, err)
		assert.Equal(t, higher.Id, selected.Id)
		require.True(t, writer.UpdateChannelStatus(higher.Id, higher.Key, common.ChannelStatusManuallyDisabled, "test"))
		require.NoError(t, writer.ReloadChannelCache(t.Context()))
		require.NoError(t, reader.ReloadChannelCache(t.Context()))
		selected, err = reader.GetRandomSatisfiedChannel("default", "snapshot-model", 0, nil)
		require.NoError(t, err)
		assert.Equal(t, channel.Id, selected.Id)
		version := reader.ChannelSnapshotVersion()
		require.NoError(t, database.Exec("ALTER TABLE abilities RENAME TO test_unavailable_abilities").Error)
		err = writer.ReloadChannelCache(t.Context())
		require.NoError(t, database.Exec("ALTER TABLE test_unavailable_abilities RENAME TO abilities").Error)
		require.Error(t, err)
		require.NoError(t, reader.ReloadChannelCache(t.Context()))
		assert.Equal(t, version, reader.ChannelSnapshotVersion())
	})

	t.Run("versioned options publish and data readers never access PostgreSQL", func(t *testing.T) {
		previous := common.OptionMap
		common.OptionMap = map[string]string{}
		t.Cleanup(func() {
			common.OptionMap = previous
			require.NoError(t, client.Del(context.Background(), "config:snapshot:options").Err())
		})
		writer := system.NewOptions(system.OptionDependencies{DB: database, Cache: client})
		reader := system.NewOptions(system.OptionDependencies{Cache: client, ReadOnly: true})
		require.Error(t, reader.Reload(t.Context()))
		require.NoError(t, writer.UpdateOption(t.Context(), "Notice", "first"))
		t.Cleanup(func() {
			require.NoError(t, database.Exec(`DELETE FROM options WHERE "key" IN ('Notice','About')`).Error)
		})
		first := writer.AppliedVersion()
		require.NotEmpty(t, first)
		common.OptionMap["Notice"] = "stale replica"
		require.NoError(t, reader.Reload(t.Context()))
		assert.Equal(t, "first", common.OptionMap["Notice"])
		assert.Equal(t, first, reader.AppliedVersion())
		require.Error(t, reader.UpdateOption(t.Context(), "Notice", "forbidden"))
		require.NoError(t, writer.UpdateOption(t.Context(), "Notice", "second"))
		require.NotEqual(t, first, writer.AppliedVersion())
		require.NoError(t, reader.Reload(t.Context()))
		assert.Equal(t, "second", common.OptionMap["Notice"])
		require.NoError(t, client.HSet(t.Context(), "config:snapshot:options", "data", "corrupted").Err())
		require.Error(t, reader.Reload(t.Context()))
		assert.Equal(t, "second", common.OptionMap["Notice"])
		require.NoError(t, writer.Reload(t.Context()))
		require.NoError(t, reader.Reload(t.Context()))
		require.NoError(t, client.Del(t.Context(), "config:snapshot:options").Err())
		require.Error(t, reader.Reload(t.Context()))
		require.NoError(t, writer.Reload(t.Context()))
		require.NoError(t, reader.Reload(t.Context()))
		assert.Equal(t, writer.AppliedVersion(), reader.AppliedVersion())
		// Separate control-plane writers must publish the union of committed changes.
		other := system.NewOptions(system.OptionDependencies{DB: database, Cache: client})
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; done <- writer.UpdateOption(t.Context(), "Notice", "left") }()
		go func() { <-start; done <- other.UpdateOption(t.Context(), "About", "right") }()
		close(start)
		for range 2 {
			require.NoError(t, <-done)
		}
		require.NoError(t, reader.Reload(t.Context()))
		assert.Equal(t, "left", common.OptionMap["Notice"])
		assert.Equal(t, "right", common.OptionMap["About"])
		published, err := configsync.New(client, "options").Read(t.Context())
		require.NoError(t, err)
		var rows []struct{ Key, Value string }
		require.NoError(t, common.Unmarshal(published.Data, &rows))
		values := make(map[string]string)
		for _, row := range rows {
			values[row.Key] = row.Value
		}
		assert.Equal(t, "left", values["Notice"])
		assert.Equal(t, "right", values["About"])
		version := reader.AppliedVersion()
		require.NoError(t, database.Exec(`CREATE FUNCTION reject_config_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected configuration write failure'; END; $$; CREATE TRIGGER reject_config_publish BEFORE INSERT OR UPDATE ON options FOR EACH ROW EXECUTE FUNCTION reject_config_publish();`).Error)
		require.Error(t, writer.UpdateOption(t.Context(), "Notice", "must-not-publish"))
		require.NoError(t, database.Exec("DROP FUNCTION reject_config_publish() CASCADE").Error)
		require.NoError(t, reader.Reload(t.Context()))
		assert.Equal(t, version, reader.AppliedVersion())
		assert.Equal(t, "left", common.OptionMap["Notice"])

	})

	t.Run("configuration notifications and missed-message reconciliation", func(t *testing.T) {
		store := configsync.New(client, "watch-test")
		t.Cleanup(func() { require.NoError(t, client.Del(context.Background(), "config:snapshot:watch-test").Err()) })
		_, err := store.Publish(t.Context(), []byte("first"))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		observed := make(chan string, 64)
		done := make(chan struct{})
		failures := make(chan error, 4)
		go func() {
			defer close(done)
			store.Watch(ctx, 20*time.Millisecond, func(ctx context.Context) error {
				snapshot, err := store.Read(ctx)
				if err != nil {
					return err
				}
				select {
				case observed <- string(snapshot.Data):
				case <-ctx.Done():
				}
				return nil
			}, func(err error) {
				select {
				case failures <- err:
				default:
				}
			})
		}()
		for _, want := range []string{"first", "notified", "missed"} {
			if want == "notified" {
				_, err = store.Publish(t.Context(), []byte(want))
				require.NoError(t, err)
			}
			if want == "missed" {
				// Inject a valid snapshot without PUBLISH, as if a notification was lost.
				digest := sha256.Sum256([]byte(want))
				require.NoError(t, client.HSet(t.Context(), "config:snapshot:watch-test", "version", hex.EncodeToString(digest[:]), "data", want).Err())
			}
			deadline := time.NewTimer(5 * time.Second)
			found := false
			for !found {
				select {
				case value := <-observed:
					found = value == want
				case err := <-failures:
					require.NoError(t, err)
				case <-deadline.C:
					t.Fatal("snapshot did not converge")
				}
			}
			deadline.Stop()
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("configuration watcher did not stop")
		}
	})

	t.Run("session authority shares atomic issuance rotation and revocation", func(t *testing.T) {
		oldActive, oldIssued := common.UserSessionActiveLimit, common.UserSessionIssuanceLimit
		common.UserSessionActiveLimit, common.UserSessionIssuanceLimit = 1, 2
		t.Cleanup(func() { common.UserSessionActiveLimit, common.UserSessionIssuanceLimit = oldActive, oldIssued })
		node := redis.NewClient(client.Options())
		t.Cleanup(func() { require.NoError(t, node.Close()) })
		one, two := usersessions.New(database, client), usersessions.New(database, node)
		assert.False(t, database.Migrator().HasTable("user_sessions"))
		now := time.Now().Unix()
		start := make(chan struct{})
		results := make(chan error, 2)
		for i, store := range []*usersessions.Store{one, two} {
			go func() {
				<-start
				results <- store.CreateUserSession(&model.UserSession{SID: fmt.Sprintf("atomic-session-%d", i), UserID: 987654, UserAuthVersion: 1, RefreshHash: "initial-hash", CreatedAt: now, ExpiresAt: now + 3600})
			}()
		}
		close(start)
		winners := 0
		for range 2 {
			err := <-results
			if err == nil {
				winners++
			} else {
				assert.ErrorIs(t, err, model.ErrUserSessionLimit)
			}
		}
		require.Equal(t, 1, winners)
		sid := "atomic-session-0"
		current, err := one.GetUserSessionCached(sid)
		if errors.Is(err, model.ErrUserSessionInactive) {
			sid = "atomic-session-1"
			current, err = one.GetUserSessionCached(sid)
		}
		require.NoError(t, err)
		_, err = two.RotateUserSessionRefresh(current.UserID, sid, "unknown", "next", now, 5*time.Second)
		assert.ErrorIs(t, err, model.ErrUserSessionRefreshInvalid)
		_, err = two.RotateUserSessionRefresh(current.UserID, sid, "initial-hash", "next", now, 5*time.Second)
		require.NoError(t, err)
		_, err = one.RotateUserSessionRefresh(current.UserID, sid, "initial-hash", "loser", now+1, 5*time.Second)
		assert.ErrorIs(t, err, model.ErrUserSessionRefreshRace)
		_, err = one.RotateUserSessionRefresh(current.UserID, sid, "initial-hash", "replay", now+6, 5*time.Second)
		assert.ErrorIs(t, err, model.ErrUserSessionRefreshReuse)
		_, err = two.GetUserSessionCached(sid)
		assert.ErrorIs(t, err, model.ErrUserSessionInactive)
		count, err := one.CountUserSessionsCreatedSince(current.UserID, now-1)
		require.NoError(t, err)
		assert.EqualValues(t, 1, count)
		session := &model.UserSession{SID: "second-session", UserID: current.UserID, UserAuthVersion: 1, RefreshHash: "second-hash", CreatedAt: now, ExpiresAt: now + 3600}
		require.NoError(t, two.CreateUserSession(session))
		revoked, err := one.RevokeUserSession(current.UserID, session.SID, "logout")
		require.NoError(t, err)
		assert.True(t, revoked)
		assert.ErrorIs(t, two.CreateUserSession(&model.UserSession{SID: "third-session", UserID: current.UserID, UserAuthVersion: 1, RefreshHash: "third-hash", CreatedAt: now, ExpiresAt: now + 3600}), model.ErrUserSessionIssuanceLimit)
		require.NoError(t, one.DeleteUserSessions(current.UserID))
		_, err = two.GetUserSessionCached(session.SID)
		assert.ErrorIs(t, err, model.ErrUserSessionInactive)
		unavailable := redis.NewClient(client.Options())
		require.NoError(t, unavailable.Close())
		_, err = usersessions.New(database, unavailable).GetUserSessionCached(sid)
		require.Error(t, err)
	})

	t.Run("authentication ceremonies use cache while spent provider signatures survive cache loss", func(t *testing.T) {
		node := redis.NewClient(client.Options())
		t.Cleanup(func() { require.NoError(t, node.Close()) })
		issuer := identityflows.New(database, client)
		consumer := identityflows.New(database, node)
		assert.False(t, database.Migrator().HasTable("auth_flows"))
		match := identityflows.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth, Provider: "github", Intent: model.AuthFlowIntentBind, UserId: 42, SessionId: "session-a"}
		input := identityflows.AuthFlowCreate{Purpose: match.Purpose, Provider: match.Provider, Intent: match.Intent, UserId: match.UserId, SessionId: match.SessionId, Payload: `{"large_number":9007199254740993,"challenge":"private"}`, ExpiresAt: time.Now().Add(time.Minute)}
		token, flow, err := issuer.CreateAuthFlow(t.Context(), input)
		require.NoError(t, err)
		assert.NotEqual(t, token, flow.TokenHash)
		ttl, err := client.PTTL(t.Context(), "auth:flow:"+flow.TokenHash).Result()
		require.NoError(t, err)
		assert.InDelta(t, 60, ttl.Seconds(), 2)
		for _, wrong := range []identityflows.AuthFlowMatch{
			{Purpose: model.AuthFlowPurposeTwoFALogin},
			{Purpose: match.Purpose, Provider: "discord"},
			{Purpose: match.Purpose, Intent: model.AuthFlowIntentLogin},
			{Purpose: match.Purpose, UserId: 99},
			{Purpose: match.Purpose, SessionId: "session-b"},
		} {
			_, err := consumer.ConsumeAuthFlow(t.Context(), token, wrong)
			assert.ErrorIs(t, err, model.ErrAuthFlowInvalid)
		}
		peek, err := consumer.GetAuthFlow(t.Context(), token, match)
		require.NoError(t, err)
		assert.Equal(t, input.Payload, peek.Payload)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, store := range []*identityflows.Store{issuer, consumer} {
			go func() { <-start; _, err := store.ConsumeAuthFlow(t.Context(), token, match); results <- err }()
		}
		close(start)
		accepted := 0
		for range 2 {
			err := <-results
			if err == nil {
				accepted++
			} else {
				assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
			}
		}
		assert.Equal(t, 1, accepted)
		_, err = issuer.GetAuthFlow(t.Context(), token, match)
		assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
		token, flow, err = issuer.CreateAuthFlow(t.Context(), input)
		require.NoError(t, err)
		require.NoError(t, client.PExpireAt(t.Context(), "auth:flow:"+flow.TokenHash, time.Unix(1, 0)).Err())
		_, err = consumer.ConsumeAuthFlow(t.Context(), token, match)
		assert.ErrorIs(t, err, model.ErrAuthFlowInvalid)

		user := model.User{Username: "flow-action", AffCode: "flow-action", DisplayName: "before", AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		token, _, err = issuer.CreateAuthFlow(t.Context(), input)
		require.NoError(t, err)
		forced := errors.New("forced binding failure")
		_, err = consumer.ConsumeAuthFlowWithAction(t.Context(), token, match, func(tx *gorm.DB, flow *identityflows.AuthFlow) error {
			require.NoError(t, tx.Model(&model.User{}).Where("id = ?", user.Id).Update("display_name", "after").Error)
			return forced
		})
		assert.ErrorIs(t, err, forced)
		_, err = issuer.GetAuthFlow(t.Context(), token, match)
		assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
		var unchanged model.User
		require.NoError(t, database.First(&unchanged, user.Id).Error)
		assert.Equal(t, "before", unchanged.DisplayName)

		token, _, err = issuer.CreateAuthFlow(t.Context(), input)
		require.NoError(t, err)
		require.NoError(t, consumer.DeleteUserAuthFlows(t.Context(), match.UserId))
		_, err = issuer.GetAuthFlow(t.Context(), token, match)
		assert.ErrorIs(t, err, model.ErrAuthFlowInvalid)
		expires := time.Now().Add(time.Minute)
		require.NoError(t, issuer.ClaimExternalAuthAssertion(t.Context(), model.AuthFlowPurposeTelegramAssertion, "signed-provider-assertion", expires))
		// This suite exclusively owns disposable cache DB 15. Losing every
		// cache key must not make an already used signed assertion valid again.
		require.NoError(t, client.FlushDB(t.Context()).Err())
		assert.ErrorIs(t, consumer.ClaimExternalAuthAssertion(t.Context(), model.AuthFlowPurposeTelegramAssertion, "signed-provider-assertion", expires), model.ErrAuthFlowConsumed)
		unavailable := redis.NewClient(client.Options())
		require.NoError(t, unavailable.Close())
		offline := identityflows.New(database, unavailable)
		_, _, err = offline.CreateAuthFlow(t.Context(), input)
		require.Error(t, err)
		_, err = offline.ConsumeAuthFlow(t.Context(), token, match)
		require.Error(t, err)
	})

	t.Run("verification codes are shared single-use secrets with expiry", func(t *testing.T) {
		const email = "verification@example.test"
		const code = "123abc"
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose))
		// A separate application client sees the challenge issued by the first.
		node := redis.NewClient(client.Options())
		common.RDB = node
		t.Cleanup(func() {
			common.RDB = client
			_ = node.Close()
		})
		keys, err := node.Keys(t.Context(), "auth:verification:*").Result()
		require.NoError(t, err)
		require.Len(t, keys, 1)
		assert.NotContains(t, keys[0], email)
		stored, err := node.Get(t.Context(), keys[0]).Result()
		require.NoError(t, err)
		assert.NotEqual(t, code, stored)
		ttl, err := node.TTL(t.Context(), keys[0]).Result()
		require.NoError(t, err)
		assert.InDelta(t, float64(common.VerificationValidMinutes*60), ttl.Seconds(), 2)
		assert.False(t, common.VerifyCodeWithKey(email, code, common.PasswordResetPurpose))
		assert.False(t, common.VerifyCodeWithKey("other@example.test", code, common.EmailVerificationPurpose))
		assert.False(t, common.VerifyCodeWithKey(email, "wrong", common.EmailVerificationPurpose))
		start := make(chan struct{})
		results := make(chan bool, 2)
		for range 2 {
			go func() { <-start; results <- common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose) }()
		}
		close(start)
		accepted := 0
		for range 2 {
			if <-results {
				accepted++
			}
		}
		assert.Equal(t, 1, accepted)
		assert.False(t, common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose))
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, "old-code", common.EmailVerificationPurpose))
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, "new-code", common.EmailVerificationPurpose))
		assert.False(t, common.VerifyCodeWithKey(email, "old-code", common.EmailVerificationPurpose))
		assert.True(t, common.VerifyCodeWithKey(email, "new-code", common.EmailVerificationPurpose))
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose))
		require.NoError(t, node.PExpireAt(t.Context(), keys[0], time.Unix(1, 0)).Err())
		assert.False(t, common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose))
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, code, common.PasswordResetPurpose))
		assert.True(t, common.VerifyCodeWithKey(email, code, common.PasswordResetPurpose))
		assert.False(t, common.VerifyCodeWithKey(email, code, common.PasswordResetPurpose))
		passwordHash, err := common.Password2Hash("previous-password")
		require.NoError(t, err)
		user := model.User{Username: "verification-reset", AffCode: "verification-reset", Email: email, Password: passwordHash, Status: common.UserStatusEnabled, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "reset-test")
		require.NoError(t, err)
		require.NoError(t, common.RegisterVerificationCodeWithKey(email, "reset-once", common.PasswordResetPurpose))
		router := gin.New()
		router.POST("/reset", controller.ResetPassword)
		body, err := common.Marshal(controller.PasswordResetRequest{Email: email, Token: "reset-once"})
		require.NoError(t, err)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/reset", strings.NewReader(string(body))))
		var reset struct {
			Success bool   `json:"success"`
			Data    string `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &reset))
		require.True(t, reset.Success, response.Body.String())
		require.NotEmpty(t, reset.Data)
		var updated model.User
		require.NoError(t, database.First(&updated, user.Id).Error)
		assert.True(t, common.ValidatePasswordAndHash(reset.Data, updated.Password))
		assert.Greater(t, updated.AuthVersion, user.AuthVersion)
		_, err = model.GetUserSessionCached(login.Session.SID)
		assert.ErrorIs(t, err, model.ErrUserSessionInactive)
		issued, err := model.CountUserSessionsCreatedSince(user.Id, time.Now().Add(-time.Hour).Unix())
		require.NoError(t, err)
		assert.EqualValues(t, 1, issued)
		replay := httptest.NewRecorder()
		router.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/reset", strings.NewReader(string(body))))
		var replayBody struct {
			Success bool `json:"success"`
		}
		require.NoError(t, common.Unmarshal(replay.Body.Bytes(), &replayBody))
		assert.False(t, replayBody.Success)
		var unchanged model.User
		require.NoError(t, database.First(&unchanged, user.Id).Error)
		assert.Equal(t, updated.Password, unchanged.Password)
		assert.Equal(t, updated.AuthVersion, unchanged.AuthVersion)

		require.NoError(t, node.Close())
		require.Error(t, common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose))
		assert.False(t, common.VerifyCodeWithKey(email, code, common.EmailVerificationPurpose))
	})

	t.Run("system task leases fence competing workers and recover expired executions", func(t *testing.T) {
		first := system.New(system.Dependencies{DB: database, Cache: client})
		second := system.New(system.Dependencies{DB: database, Cache: client})
		const taskType = "dragonfly_task_contract"
		const leaseKey = "system:task-lease:" + taskType
		t.Cleanup(func() { require.NoError(t, client.Del(context.Background(), leaseKey).Err()) })
		assert.False(t, database.Migrator().HasTable("system_task_locks"))
		task, err := first.CreateSystemTask(t.Context(), taskType, map[string]int{"target": 42}, nil)
		require.NoError(t, err)
		type outcome struct {
			owner   string
			claimed bool
			err     error
		}
		results := make(chan outcome, 2)
		start := make(chan struct{})
		for owner, worker := range map[string]*system.Service{"node-a": first, "node-b": second} {
			go func() {
				<-start
				_, claimed, err := worker.ClaimSystemTask(t.Context(), task.ID, task.Type, owner, common.GetTimestamp()+60)
				results <- outcome{owner, claimed, err}
			}()
		}
		close(start)
		owner := ""
		winners := 0
		for range 2 {
			result := <-results
			require.NoError(t, result.err)
			if result.claimed {
				owner = result.owner
				winners++
			}
		}
		require.Equal(t, 1, winners)
		require.NoError(t, first.RenewSystemTaskLock(t.Context(), task.TaskID, owner, common.GetTimestamp()+120))
		ttl, err := client.PTTL(t.Context(), leaseKey).Result()
		require.NoError(t, err)
		assert.InDelta(t, 120, ttl.Seconds(), 2)
		assert.ErrorIs(t, second.RenewSystemTaskLock(t.Context(), task.TaskID, "foreign", common.GetTimestamp()+600), system.ErrSystemTaskLockLost)
		require.NoError(t, second.ReleaseSystemTaskLock(t.Context(), task.TaskID, "foreign"))
		require.NoError(t, first.UpdateSystemTaskState(t.Context(), task.TaskID, owner, map[string]int{"processed": 1}))
		require.NoError(t, first.ExpireStaleSystemTaskLocks(t.Context(), common.GetTimestamp()))
		running, err := first.GetSystemTaskByTaskID(t.Context(), task.TaskID)
		require.NoError(t, err)
		assert.Equal(t, system.SystemTaskStatusRunning, running.Status)
		unavailable := redis.NewClient(client.Options())
		require.NoError(t, unavailable.Close())
		offline := system.New(system.Dependencies{DB: database, Cache: unavailable})
		require.Error(t, offline.ExpireStaleSystemTaskLocks(t.Context(), common.GetTimestamp()))
		require.Error(t, offline.FinishSystemTask(t.Context(), task.TaskID, owner, system.SystemTaskStatusSucceeded, nil, ""))
		require.Error(t, offline.RenewSystemTaskLock(t.Context(), task.TaskID, owner, common.GetTimestamp()+60))
		running, err = first.GetSystemTaskByTaskID(t.Context(), task.TaskID)
		require.NoError(t, err)
		assert.Equal(t, system.SystemTaskStatusRunning, running.Status)
		require.NoError(t, client.PExpireAt(t.Context(), leaseKey, time.Unix(1, 0)).Err())
		assert.ErrorIs(t, first.RenewSystemTaskLock(t.Context(), task.TaskID, owner, common.GetTimestamp()+60), system.ErrSystemTaskLockLost)
		assert.ErrorIs(t, first.UpdateSystemTaskState(t.Context(), task.TaskID, owner, map[string]int{"processed": 999}), system.ErrSystemTaskLockLost)
		assert.ErrorIs(t, first.FinishSystemTask(t.Context(), task.TaskID, owner, system.SystemTaskStatusSucceeded, nil, ""), system.ErrSystemTaskLockLost)
		require.NoError(t, second.ExpireStaleSystemTaskLocks(t.Context(), common.GetTimestamp()))
		expired, err := first.GetSystemTaskByTaskID(t.Context(), task.TaskID)
		require.NoError(t, err)
		assert.Equal(t, system.SystemTaskStatusFailed, expired.Status)
		assert.Nil(t, expired.ActiveKey)
		var state map[string]int
		require.NoError(t, expired.DecodeState(&state))
		assert.Equal(t, 1, state["processed"])
		next, err := second.CreateSystemTask(t.Context(), taskType, nil, nil)
		require.NoError(t, err)
		_, claimed, err := second.ClaimSystemTask(t.Context(), next.ID, next.Type, "replacement", common.GetTimestamp()+60)
		require.NoError(t, err)
		require.True(t, claimed)
		require.NoError(t, first.ReleaseSystemTaskLock(t.Context(), task.TaskID, owner))
		require.NoError(t, second.RenewSystemTaskLock(t.Context(), next.TaskID, "replacement", common.GetTimestamp()+60))
		require.NoError(t, second.FinishSystemTask(t.Context(), next.TaskID, "replacement", system.SystemTaskStatusSucceeded, map[string]bool{"completed": true}, ""))
		assert.ErrorIs(t, first.FinishSystemTask(t.Context(), task.TaskID, owner, system.SystemTaskStatusSucceeded, nil, ""), system.ErrSystemTaskLockLost)
	})

	t.Run("instance heartbeats and guarded stale deletion", func(t *testing.T) {
		cpuUsage := 12.5
		config := system.InstanceReportConfig{Node: common.NodeIdentity{Name: "node-a"}, Version: "test-version", StartedAt: 1000, Resources: func() systemcontract.SystemInstanceResources {
			return systemcontract.SystemInstanceResources{CPU: systemcontract.SystemInstanceResourceUsage{UsagePercent: cpuUsage}, Storage: systemcontract.SystemInstanceStorageMetrics{TotalBytes: 100, UsedBytes: 20, FreeBytes: 80, UsedPercent: 20}}
		}}
		service := system.New(system.Dependencies{Cache: common.RDB, Master: true, InstanceReport: config})
		require.NoError(t, service.ReportCurrentSystemInstance(t.Context()))
		cpuUsage = 25
		require.NoError(t, service.ReportCurrentSystemInstance(t.Context()))
		rows, err := service.ListSystemInstances(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		refreshed := rows[0]
		t.Cleanup(func() {
			require.NoError(t, common.RDB.Del(context.Background(), "system:instance:node-a", "system:instance:revived-node", "system:instance:stale-node", "system:instance:boundary-node").Err())
		})
		ttl, err := common.RDB.TTL(t.Context(), "system:instance:node-a").Result()
		require.NoError(t, err)
		assert.InDelta(t, (24 * time.Hour).Seconds(), ttl.Seconds(), 2)
		var info systemcontract.SystemInstanceInfo
		require.NoError(t, common.UnmarshalJsonStr(refreshed.Info, &info))
		assert.Equal(t, "node-a", info.Node.Name)
		assert.Equal(t, "test-version", info.Runtime.Version)
		assert.Equal(t, int64(1000), info.Runtime.StartedAt)
		assert.True(t, info.Role.IsMaster)
		assert.Equal(t, 25.0, info.Resources.CPU.UsagePercent)
		assert.Equal(t, uint64(100), info.Resources.Storage.TotalBytes)
		now := common.GetTimestamp()
		require.NoError(t, service.UpsertSystemInstance(t.Context(), "stale-node", map[string]string{"version": "old"}, 1, now-1000))
		require.NoError(t, service.UpsertSystemInstance(t.Context(), "boundary-node", nil, 1, now-90))
		deleted, err := service.DeleteStaleSystemInstance(t.Context(), "boundary-node", now)
		require.NoError(t, err)
		assert.False(t, deleted)
		deleted, err = service.DeleteStaleSystemInstance(t.Context(), "boundary-node", now+1)
		require.NoError(t, err)
		assert.True(t, deleted)
		// A heartbeat that refreshed an old row must protect it from the delete predicate.
		require.NoError(t, service.UpsertSystemInstance(t.Context(), "revived-node", nil, 1, now-1000))
		require.NoError(t, service.UpsertSystemInstance(t.Context(), "revived-node", nil, 2, now))
		deleted, err = service.DeleteStaleSystemInstance(t.Context(), "revived-node", now)
		require.NoError(t, err)
		assert.False(t, deleted)
		handler := systemhttp.New(service)
		router := gin.New()
		router.GET("/instances", handler.ListSystemInstances)
		router.DELETE("/instances/stale", handler.DeleteStaleSystemInstances)
		router.DELETE("/instances/:node_name", handler.DeleteStaleSystemInstance)
		list := httptest.NewRecorder()
		router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/instances", nil))
		require.Equal(t, http.StatusOK, list.Code)
		var response struct {
			Success bool                                    `json:"success"`
			Data    []systemcontract.SystemInstanceResponse `json:"data"`
		}
		require.NoError(t, common.Unmarshal(list.Body.Bytes(), &response))
		require.True(t, response.Success)
		require.Len(t, response.Data, 3)
		assert.Equal(t, "stale-node", response.Data[2].NodeName)
		assert.Equal(t, systemcontract.SystemInstanceStatusStale, response.Data[2].Status)
		alive := httptest.NewRecorder()
		router.ServeHTTP(alive, httptest.NewRequest(http.MethodDelete, "/instances/node-a", nil))
		assert.Contains(t, alive.Body.String(), `"success":false`)
		cleanup := httptest.NewRecorder()
		router.ServeHTTP(cleanup, httptest.NewRequest(http.MethodDelete, "/instances/stale", nil))
		assert.Contains(t, cleanup.Body.String(), `"deleted_count":1`)
		rows, err = service.ListSystemInstances(t.Context())
		require.NoError(t, err)
		assert.Len(t, rows, 2)
		// Expiration removes a node without PostgreSQL cleanup or a running worker.
		require.NoError(t, common.RDB.PExpireAt(t.Context(), "system:instance:node-a", time.Unix(1, 0)).Err())
		rows, err = service.ListSystemInstances(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "revived-node", rows[0].NodeName)
	})

	t.Run("instance hostname fallback and shutdown", func(t *testing.T) {
		service := system.New(system.Dependencies{Cache: common.RDB, InstanceReport: system.InstanceReportConfig{Version: "fallback", StartedAt: 1}})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := service.StartSystemInstanceReporter(ctx)
		require.Eventually(t, func() bool {
			rows, err := service.Instances(t.Context())
			return err == nil && len(rows) == 1
		}, 2*time.Second, 10*time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("instance reporter did not stop")
		}
		hostname, err := os.Hostname()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, common.RDB.Del(context.Background(), "system:instance:"+hostname).Err()) })
		rows, err := service.ListSystemInstances(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		row := rows[0]
		assert.Equal(t, hostname, row.NodeName)
		var info systemcontract.SystemInstanceInfo
		require.NoError(t, common.UnmarshalJsonStr(row.Info, &info))
		assert.Equal(t, common.NodeNameSourceHostname, info.Node.Source)
		assert.True(t, info.Node.ShouldConfigureManually)
		assert.False(t, info.Role.IsMaster)
	})

	t.Run("concurrent reservations cannot spend the same cached balance twice", func(t *testing.T) {
		user := model.User{Username: "dragonfly-reserve", AffCode: "dragonfly-reserve", Quota: 10, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		type outcome struct {
			reserved bool
			err      error
		}
		start := make(chan struct{})
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				reserved, err := model.TryReserveUserQuota(user.Id, 8)
				results <- outcome{reserved: reserved, err: err}
			}()
		}
		close(start)
		winners := 0
		for range 2 {
			result := <-results
			require.NoError(t, result.err)
			if result.reserved {
				winners++
			}
		}
		assert.Equal(t, 1, winners)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 2, cached.Quota)
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 2, user.Quota)
	})

	t.Run("wallet maximum and token usage retain exact integer values", func(t *testing.T) {
		user := model.User{Username: "dragonfly-large-wallet", AffCode: "dragonfly-large-wallet", Quota: common.MaxWalletQuota, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 2)
		require.NoError(t, err)
		assert.True(t, reserved)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, common.MaxWalletQuota-2, cached.Quota)
		token := model.Token{UserId: user.Id, Key: strings.Repeat("a", 32), Status: common.TokenStatusEnabled, RemainQuota: 20, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		reserved, err = model.TryReserveTokenQuota(token.Id, token.Key, 7, false)
		require.NoError(t, err)
		assert.True(t, reserved)
		reserved, err = model.TryReserveTokenQuota(token.Id, token.Key, 14, false)
		require.NoError(t, err)
		assert.False(t, reserved)
		stored, err := model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 13, stored.RemainQuota)
		assert.Equal(t, 7, stored.UsedQuota)
		require.NoError(t, database.First(&token, token.Id).Error)
		assert.Equal(t, stored.RemainQuota, token.RemainQuota)
		assert.Equal(t, stored.UsedQuota, token.UsedQuota)
	})

	t.Run("ledger persistence failures leave caches unchanged and reservations compensated", func(t *testing.T) {
		user := model.User{Username: "df-ledger-failure", AffCode: "df-ledger-failure", Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		token := model.Token{UserId: user.Id, Key: "df-ledger-failure-token", Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		_, err = model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		ledger := model.AccountingStore()
		require.NoError(t, database.Exec(`CREATE FUNCTION fail_ledger_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected ledger write failure'; END; $$; CREATE TRIGGER fail_ledger_write BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION fail_ledger_write(); CREATE TRIGGER fail_ledger_write BEFORE UPDATE ON tokens FOR EACH ROW EXECUTE FUNCTION fail_ledger_write();`).Error)
		t.Cleanup(func() { require.NoError(t, database.Exec("DROP FUNCTION IF EXISTS fail_ledger_write() CASCADE").Error) })
		require.Error(t, ledger.DecreaseUserQuota(t.Context(), user.Id, 10, true))
		require.Error(t, ledger.IncreaseUserQuota(t.Context(), user.Id, 10, true))
		require.Error(t, ledger.DecreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		require.Error(t, ledger.IncreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		reserved, err := ledger.TryReserveUserQuota(t.Context(), user.Id, 10)
		require.Error(t, err)
		assert.False(t, reserved)
		reserved, err = ledger.TryReserveTokenQuota(t.Context(), token.Id, token.Key, 10, false)
		require.Error(t, err)
		assert.False(t, reserved)
		cachedUser, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		cachedToken, err := model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 100, cachedUser.Quota)
		assert.Equal(t, 100, cachedToken.RemainQuota)
		assert.Zero(t, cachedToken.UsedQuota)
		require.NoError(t, database.Exec("DROP FUNCTION fail_ledger_write() CASCADE").Error)
		require.NoError(t, ledger.DecreaseUserQuota(t.Context(), user.Id, 10, true))
		require.NoError(t, ledger.DecreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
		cachedUser, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		cachedToken, err = model.GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, 90, cachedUser.Quota)
		assert.Equal(t, 90, cachedToken.RemainQuota)
		assert.Equal(t, 10, cachedToken.UsedQuota)
		require.NoError(t, database.First(&user, user.Id).Error)
		require.NoError(t, database.First(&token, token.Id).Error)
		assert.Equal(t, user.Quota, cachedUser.Quota)
		assert.Equal(t, token.RemainQuota, cachedToken.RemainQuota)
	})

	t.Run("token management invalidates hydrated authorization snapshots", func(t *testing.T) {
		management := identity.New(identity.Dependencies{
			DB: database, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
			TokenPolicy: identity.TokenPolicy{
				MaxAutoGroups:     func() int { return 5 },
				IsSelectableGroup: func(userGroup, group string) bool { return group == "vip" },
			},
		})
		tokens := []model.Token{
			{UserId: 71, Name: "dragonfly-edit-token", Key: "dragonfly-edit-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100, Group: "auto", CrossGroupRetry: true, AutoGroups: model.StringList{"default", "vip"}},
			{UserId: 71, Name: "dragonfly-batch-token", Key: "dragonfly-batch-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
			{UserId: 72, Name: "dragonfly-foreign-token", Key: "dragonfly-foreign-token-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
		}
		require.NoError(t, database.Create(&tokens).Error)
		for _, token := range tokens {
			_, err := model.GetTokenByKey(token.Key, false)
			require.NoError(t, err)
		}
		updated, err := management.UpdateToken(t.Context(), contract.TokenActor{ID: 71, Group: "default"}, contract.TokenRequest{
			TokenSettings: contract.TokenSettings{Id: tokens[0].Id, Name: tokens[0].Name, ExpiredTime: -1, RemainQuota: 100, Group: "auto", CrossGroupRetry: true},
			AutoGroups:    contract.TokenAutoGroupsInput{Set: true, Groups: []string{"vip"}},
		}, false)
		require.NoError(t, err)
		assert.Equal(t, []string{"vip"}, updated.AutoGroups)
		reloaded, err := model.GetTokenByKey(tokens[0].Key, false)
		require.NoError(t, err)
		assert.Equal(t, model.StringList{"vip"}, reloaded.AutoGroups)
		// A status-only update must preserve a quota change already committed by accounting.
		require.NoError(t, database.Model(&tokens[0]).Updates(map[string]any{"remain_quota": 73, "used_quota": 27}).Error)
		_, err = management.UpdateToken(t.Context(), contract.TokenActor{ID: 71}, contract.TokenRequest{
			TokenSettings: contract.TokenSettings{Id: tokens[0].Id, Status: common.TokenStatusDisabled},
		}, true)
		require.NoError(t, err)
		reloaded, err = model.GetTokenByKey(tokens[0].Key, false)
		require.NoError(t, err)
		assert.Equal(t, 73, reloaded.RemainQuota)
		assert.Equal(t, 27, reloaded.UsedQuota)
		_, err = model.ValidateUserToken(tokens[0].Key)
		require.ErrorIs(t, err, model.ErrTokenInvalid)
		require.NoError(t, management.DeleteToken(t.Context(), tokens[0].Id, 71))
		_, err = model.GetTokenByKey(tokens[0].Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		deleted, err := management.DeleteTokens(t.Context(), []int{tokens[1].Id, tokens[2].Id}, 71)
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
		_, err = model.GetTokenByKey(tokens[1].Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		retained, err := model.ValidateUserToken(tokens[2].Key)
		require.NoError(t, err)
		assert.Equal(t, 72, retained.UserId)
	})

	t.Run("user management invalidates cached sessions and API access", func(t *testing.T) {
		management := identity.New(identity.Dependencies{
			DB: database, InvalidateTokenCache: model.InvalidateTokenCacheForMutation,
			UserSecurity: identity.UserSecurity{
				AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
				PublishDeletedVersion: model.PublishCommittedUserAuthVersion,
				RevokeSessions:        func(id int, reason string) error { _, err := model.RevokeAllUserSessions(id, reason); return err },
				InvalidateUser:        model.InvalidateUserCache, InvalidateTokens: model.InvalidateUserTokensCache,
				DeleteCredentials: model.DeleteUserAuthenticationData,
			},
		})
		user := model.User{Username: "dragonfly-managed-user", AffCode: "dragonfly-managed-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		token := model.Token{UserId: user.Id, Key: strings.Repeat("c", 32), Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1}
		require.NoError(t, database.Create(&token).Error)
		now := time.Now().Unix()
		session := model.UserSession{SID: "dragonfly-managed-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: strings.Repeat("d", 64), LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600}
		require.NoError(t, model.CreateUserSession(&session))
		_, err := model.GetUserSessionCached(session.SID)
		require.NoError(t, err)
		router := gin.New()
		router.GET("/managed-access", middleware.TokenAuthReadOnly(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		request := httptest.NewRequest(http.MethodGet, "/managed-access", nil)
		request.Header.Set("Authorization", "Bearer sk-"+token.Key)
		allowed := httptest.NewRecorder()
		router.ServeHTTP(allowed, request)
		require.Equal(t, http.StatusNoContent, allowed.Code)
		_, err = management.ManageUser(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, contract.ManageUserRequest{Id: user.Id, Action: "disable"})
		require.NoError(t, err)
		blocked := httptest.NewRecorder()
		router.ServeHTTP(blocked, request.Clone(t.Context()))
		assert.Equal(t, http.StatusForbidden, blocked.Code)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, common.UserStatusDisabled, cached.Status)
		assert.EqualValues(t, 2, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
		_, err = model.GetUserSessionCached(session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		_, err = management.DeleteUser(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, user.Id)
		require.NoError(t, err)
		_, err = model.GetTokenByKey(token.Key, false)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		_, err = model.GetUserCache(user.Id)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(session.SID)
		require.Error(t, err)
	})

	t.Run("self password rotation preserves the current cached session", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-self-password-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		password, err := common.Password2Hash("CurrentPassword123")
		require.NoError(t, err)
		user := model.User{Username: "dragonfly-self", AffCode: "dragonfly-self", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100, Setting: `{"language":"zh","billing_preference":"subscription_first","sidebar_modules":"saved"}`}
		require.NoError(t, database.Create(&user).Error)
		first, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "first-browser")
		require.NoError(t, err)
		second, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "second-browser")
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(first.Session.SID)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(second.Session.SID)
		require.NoError(t, err)
		identityBefore, err := service.ParseAccessToken(first.AccessToken)
		require.NoError(t, err)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		bundle, err := management.UpdateSelf(t.Context(), user.Id, contract.SelfUpdateRequest{ProfileInput: contract.ProfileInput{Password: "NewPassword123", OriginalPassword: "CurrentPassword123"}}, &identityBefore)
		require.NoError(t, err)
		assert.Equal(t, first.Session.SID, bundle.Session.SID)
		_, _, err = service.ValidateLoginSession(identityBefore)
		require.Error(t, err)
		identityAfter, err := service.ParseAccessToken(bundle.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(identityAfter)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(second.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		require.NoError(t, management.UpdateNotificationSettings(t.Context(), user.Id, contract.NotificationSettingsRequest{QuotaWarningType: "email", QuotaWarningThreshold: 2, NotificationEmail: "alerts@example.test"}))
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 100, cached.Quota)
		assert.EqualValues(t, 2, cached.AuthVersion)
		assert.Contains(t, cached.Setting, `"language":"zh"`)
		assert.Contains(t, cached.Setting, `"billing_preference":"subscription_first"`)
		assert.Contains(t, cached.Setting, `"notification_email":"alerts@example.test"`)
	})

	t.Run("two factor changes rotate cached authentication state", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-twofa-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-twofa", AffCode: "dragonfly-twofa", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		browser, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "twofa-browser")
		require.NoError(t, err)
		other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-twofa-browser")
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(browser.Session.SID)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.NoError(t, err)
		before, err := service.ParseAccessToken(browser.AccessToken)
		require.NoError(t, err)
		setup, err := management.SetupTwoFA(t.Context(), user.Id)
		require.NoError(t, err)
		code, err := totp.GenerateCode(setup.Secret, time.Now())
		require.NoError(t, err)
		active, err := management.EnableTwoFA(t.Context(), user.Id, code, &before)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(before)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		enabledIdentity, err := service.ParseAccessToken(active.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enabledIdentity)
		require.NoError(t, err)
		disabled, err := management.DisableTwoFA(t.Context(), user.Id, setup.BackupCodes[0], &enabledIdentity)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enabledIdentity)
		require.Error(t, err)
		finalIdentity, err := service.ParseAccessToken(disabled.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(finalIdentity)
		require.NoError(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 3, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
		status, err := management.TwoFAStatus(t.Context(), user.Id)
		require.NoError(t, err)
		assert.False(t, status.Enabled)
	})

	t.Run("passkey enrollment and removal refresh cached sessions", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-passkey-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-passkey", AffCode: "dragonfly-passkey", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		browser, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "passkey-browser")
		require.NoError(t, err)
		other, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "other-passkey-browser")
		require.NoError(t, err)
		before, err := service.ParseAccessToken(browser.AccessToken)
		require.NoError(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.NoError(t, err)
		management := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{
			AdvanceVersion: model.IncrementUserAuthVersionWithTx, PublishAuth: model.PublishUserAuthCache,
			AdvanceCurrentSession: service.AdvanceCurrentSessionToUserVersion,
		}})
		saved, err := management.SaveRegisteredPasskey(t.Context(), &entity.PasskeyCredential{UserID: user.Id, CredentialID: "dragonfly-credential", PublicKey: "validated-public-key"}, before)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(before)
		require.Error(t, err)
		_, err = model.GetUserSessionCached(other.Session.SID)
		require.ErrorIs(t, err, model.ErrUserSessionInactive)
		enrolled, err := service.ParseAccessToken(saved.AccessToken)
		require.NoError(t, err)
		removed, err := management.DeletePasskey(t.Context(), user.Id, enrolled)
		require.NoError(t, err)
		final, err := service.ParseAccessToken(removed.AccessToken)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(final)
		require.NoError(t, err)
		_, _, err = service.ValidateLoginSession(enrolled)
		require.Error(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 3, cached.AuthVersion)
		assert.Equal(t, 100, cached.Quota)
	})

	t.Run("authentication runtime refresh recovery and revoke", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-auth-runtime-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-auth-runtime", AffCode: "dragonfly-auth-runtime", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		authentication := service.AuthenticationRuntime()
		login, err := authentication.CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
		require.NoError(t, err)
		identity, err := service.ParseAccessToken(login.AccessToken)
		require.NoError(t, err)
		_, _, err = authentication.ValidateLoginSession(identity)
		require.NoError(t, err)
		refreshed, _, err := authentication.RefreshLoginSession(login.RefreshToken, login.Session.SID, "127.0.0.1", "browser")
		require.NoError(t, err)
		assert.NotEqual(t, login.RefreshToken, refreshed.RefreshToken)
		recovered, _, err := authentication.RefreshLoginSession(login.RefreshToken, login.Session.SID, "127.0.0.1", "browser")
		require.NoError(t, err)
		assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken)
		require.NoError(t, authentication.RevokeByRefreshToken(recovered.RefreshToken, login.Session.SID, "logout"))
		_, _, err = authentication.ValidateLoginSession(identity)
		require.ErrorIs(t, err, service.ErrLoginSessionRevoked)
	})

	t.Run("user metadata publication preserves quota and rejects stale security", func(t *testing.T) {
		user := model.User{Username: "dragonfly-usercache", AffCode: "dragonfly-usercache", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		cache := usercache.New(database)
		_, err := cache.GetUserCache(user.Id)
		require.NoError(t, err)
		stale := entity.User(user)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		assert.True(t, reserved)
		stale.Username = "metadata-updated"
		require.NoError(t, cache.Publish(stale))
		current, err := cache.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 93, current.Quota)
		assert.Equal(t, "metadata-updated", current.Username)
		require.NoError(t, database.Transaction(func(tx *gorm.DB) error {
			if _, err := cache.IncrementUserAuthVersionWithTx(tx, user.Id); err != nil {
				return err
			}
			return tx.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error
		}))
		require.NoError(t, cache.PublishUserAuthCache(user.Id))
		require.ErrorIs(t, cache.Publish(stale), usercache.ErrUserAuthCachePending)
		current, err = cache.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 2, current.AuthVersion)
		assert.Equal(t, common.UserStatusDisabled, current.Status)
		assert.Equal(t, 93, current.Quota)
	})

	t.Run("subscription group transitions preserve cached wallet and login session", func(t *testing.T) {
		previousSecret := common.SessionSecret
		common.SessionSecret = "dragonfly-subscription-secret"
		t.Cleanup(func() { common.SessionSecret = previousSecret })
		user := model.User{Username: "dragonfly-subscription", AffCode: "dragonfly-subscription", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, Quota: 100}
		require.NoError(t, database.Create(&user).Error)
		login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "subscription-browser")
		require.NoError(t, err)
		auth, err := service.ParseAccessToken(login.AccessToken)
		require.NoError(t, err)
		_, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		require.True(t, reserved)
		plan := model.SubscriptionPlan{Title: "Dragonfly subscription", Enabled: true, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, UpgradeGroup: "pro", DowngradeGroup: "default"}
		require.NoError(t, database.Create(&plan).Error)
		_, err = model.SubscriptionMemberships().AdminBindSubscription(t.Context(), user.Id, plan.Id, "")
		require.NoError(t, err)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "pro", cached.Group)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		_, _, err = service.ValidateLoginSession(auth)
		require.NoError(t, err)
		active, err := model.SubscriptionMemberships().GetAllActiveUserSubscriptions(t.Context(), user.Id)
		require.NoError(t, err)
		require.Len(t, active, 1)
		_, err = model.SubscriptionMemberships().AdminInvalidateUserSubscription(t.Context(), active[0].Subscription.Id)
		require.NoError(t, err)
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "default", cached.Group)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		_, _, err = service.ValidateLoginSession(auth)
		require.NoError(t, err)
	})

	t.Run("subscription catalog transactions do not publish uncommitted plans", func(t *testing.T) {
		user := model.User{Username: "dragonfly-catalog", AffCode: "dragonfly-catalog"}
		require.NoError(t, database.Create(&user).Error)
		plan := model.SubscriptionPlan{Title: "cached title", Enabled: true, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1}
		require.NoError(t, database.Create(&plan).Error)
		sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
		require.NoError(t, database.Create(&sub).Error)
		catalog := model.SubscriptionCatalog()
		cached, err := catalog.Plan(t.Context(), nil, plan.Id)
		require.NoError(t, err)
		assert.True(t, cached.Enabled)
		info, err := catalog.PlanInfo(t.Context(), sub.Id)
		require.NoError(t, err)
		assert.Equal(t, "cached title", info.PlanTitle)
		ttl, err := client.TTL(t.Context(), cachex.Namespace("new-api:subscription_plan:v1").FullKey(strconv.Itoa(plan.Id))).Result()
		require.NoError(t, err)
		assert.Greater(t, ttl, time.Duration(0))
		assert.LessOrEqual(t, ttl, 5*time.Minute)
		rollback := errors.New("catalog rollback")
		err = database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{"title": "private transaction", "enabled": false}).Error; err != nil {
				return err
			}
			transactional, err := catalog.Plan(t.Context(), tx, plan.Id)
			require.NoError(t, err)
			assert.False(t, transactional.Enabled)
			assert.Equal(t, "private transaction", transactional.Title)
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		cached, err = catalog.Plan(t.Context(), nil, plan.Id)
		require.NoError(t, err)
		assert.True(t, cached.Enabled)
		assert.Equal(t, "cached title", cached.Title)
		require.NoError(t, database.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Update("title", "committed title").Error)
		require.NoError(t, catalog.Invalidate(plan.Id))
		info, err = catalog.PlanInfo(t.Context(), sub.Id)
		require.NoError(t, err)
		assert.Equal(t, "committed title", info.PlanTitle)
	})

	t.Run("subscription balance purchase preserves other cached reservations", func(t *testing.T) {
		previousUnit := common.QuotaPerUnit
		common.QuotaPerUnit = 10
		t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
		user := model.User{Username: "dragonfly-subpay", AffCode: "dragonfly-subpay", Quota: 50, Group: "default", AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 2)
		require.NoError(t, err)
		require.True(t, reserved)
		plan := model.SubscriptionPlan{Title: "Balance plan", Enabled: true, PriceAmount: 1.25, DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, MaxPurchasePerUser: 1, UpgradeGroup: "pro"}
		require.NoError(t, database.Create(&plan).Error)
		require.NoError(t, model.SubscriptionPayments().PurchaseWithBalance(t.Context(), user.Id, plan.Id))
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 35, user.Quota)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 35, cached.Quota)
		assert.Equal(t, "pro", cached.Group)
		assert.EqualValues(t, 1, cached.AuthVersion)
		require.Error(t, model.SubscriptionPayments().PurchaseWithBalance(t.Context(), user.Id, plan.Id))
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 35, cached.Quota)
		var count int64
		require.NoError(t, database.Model(&model.SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})

	t.Run("billing sessions synchronize cached reservations refunds and settlement", func(t *testing.T) {
		oldBatch := common.BatchUpdateEnabled
		common.BatchUpdateEnabled = true
		t.Cleanup(func() { require.NoError(t, model.FlushQuotaUpdates()); common.BatchUpdateEnabled = oldBatch })
		for _, source := range []string{"wallet", "subscription"} {
			user := model.User{Username: "df-session-" + source, AffCode: "df-session-" + source, Quota: 100, AuthVersion: 1}
			require.NoError(t, database.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: "df-session-token-" + source, Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1}
			require.NoError(t, database.Create(&token).Error)
			var sub model.UserSubscription
			if source == "subscription" {
				plan := model.SubscriptionPlan{Title: "DF Session plan", Enabled: true, QuotaResetPeriod: model.SubscriptionResetNever}
				require.NoError(t, database.Create(&plan).Error)
				sub = model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 100, Status: "active", StartTime: common.GetTimestamp() - 1, EndTime: common.GetTimestamp() + 3600, NextResetTime: common.GetTimestamp() + 1800}
				require.NoError(t, database.Create(&sub).Error)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(t.Context())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil).WithContext(ctx)
			input := &relaycommon.RelayInfo{RequestId: "df-session-refund-" + source, UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, ForcePreConsume: true, UserSetting: kitdto.UserSetting{BillingPreference: source + "_only"}}
			require.Nil(t, service.PreConsumeBilling(c, 30, input))
			require.NoError(t, input.Billing.Reserve(50))
			assert.Equal(t, 50, input.FinalPreConsumedQuota)
			cancel()
			input.Billing.Refund(c)
			input.Billing.Refund(c)
			cached, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			cachedToken, err := model.GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			assert.Equal(t, 100, cached.Quota)
			assert.Equal(t, 100, cachedToken.RemainQuota)
			require.NoError(t, model.FlushQuotaUpdates())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
			input.RequestId = "df-session-settle-" + source
			require.Nil(t, service.PreConsumeBilling(c, 30, input))
			require.NoError(t, input.Billing.Settle(40))
			input.Billing.Refund(c)
			cachedToken, err = model.GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			assert.Equal(t, 60, cachedToken.RemainQuota)
			if source == "subscription" {
				assert.EqualValues(t, 10, input.SubscriptionPostDelta)
			}
			require.NoError(t, model.FlushQuotaUpdates())
			require.NoError(t, database.First(&user, user.Id).Error)
			require.NoError(t, database.First(&token, token.Id).Error)
			assert.Equal(t, 60, token.RemainQuota)
			assert.Equal(t, 40, token.UsedQuota)
			if source == "wallet" {
				assert.Equal(t, 60, user.Quota)
			} else {
				require.NoError(t, database.First(&sub, sub.Id).Error)
				assert.EqualValues(t, 40, sub.AmountUsed)
				assert.Equal(t, 100, user.Quota)
			}
		}
	})

	t.Run("checkin and affiliate credits preserve pending wallet reservations", func(t *testing.T) {
		previous := common.BatchUpdateEnabled
		common.BatchUpdateEnabled = true
		t.Cleanup(func() { require.NoError(t, model.FlushQuotaUpdates()); common.BatchUpdateEnabled = previous })
		user := model.User{Username: "df-reward-wallet", AffCode: "df-reward-wallet", Quota: 100, AffQuota: 20, AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		require.True(t, reserved)
		svc := billing.New(billing.Dependencies{DB: database, Accounting: model.AccountingStore(), PaymentAllowed: func() bool { return true }, RewardConfig: func() billingcontract.RewardConfig {
			return billingcontract.RewardConfig{CheckinEnabled: true, MinQuota: 5, MaxQuota: 5, QuotaPerUnit: 10}
		}, RewardLog: func(ctx context.Context, id int, message string) {
			model.LogService().RecordLog(ctx, id, model.LogTypeSystem, message)
		}})
		award, err := svc.Checkin(t.Context(), user.Id)
		require.NoError(t, err)
		assert.Equal(t, 5, award.QuotaAwarded)
		_, err = svc.Checkin(t.Context(), user.Id)
		require.Error(t, err)
		require.NoError(t, svc.TransferAffiliate(t.Context(), user.Id, 10))
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 108, cached.Quota)
		require.NoError(t, model.FlushQuotaUpdates())
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 108, user.Quota)
		assert.Equal(t, 10, user.AffQuota)
		assert.EqualValues(t, 1, user.AuthVersion)
		var count int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", user.Id, model.LogTypeSystem).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})

	t.Run("billing preference publication preserves pending wallet reservations", func(t *testing.T) {
		previousBatch := common.BatchUpdateEnabled
		common.BatchUpdateEnabled = true
		t.Cleanup(func() { require.NoError(t, model.FlushQuotaUpdates()); common.BatchUpdateEnabled = previousBatch })
		user := model.User{Username: "dragonfly-preference", AffCode: "dragonfly-preference", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100, AuthVersion: 1, Setting: `{"language":"zh","billing_preference":"subscription_first"}`}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		reserved, err := model.TryReserveUserQuota(user.Id, 7)
		require.NoError(t, err)
		require.True(t, reserved)
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 100, user.Quota)
		accounts := identity.New(identity.Dependencies{DB: database, UserSecurity: identity.UserSecurity{PublishAuth: usercache.New(database).PublishUserAuthCache}})
		preference, err := accounts.UpdateBillingPreference(t.Context(), user.Id, "wallet_only")
		require.NoError(t, err)
		assert.Equal(t, "wallet_only", preference)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, 93, cached.Quota)
		assert.EqualValues(t, 1, cached.AuthVersion)
		assert.Equal(t, "wallet_only", cached.GetSetting().BillingPreference)
		assert.Equal(t, "zh", cached.GetSetting().Language)
		require.NoError(t, model.FlushQuotaUpdates())
		require.NoError(t, database.First(&user, user.Id).Error)
		assert.Equal(t, 93, user.Quota)
		assert.Equal(t, "wallet_only", user.GetSetting().BillingPreference)
	})

	t.Run("all topup providers preserve pending reservations and credit once", func(t *testing.T) {
		oldUnit, oldBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
		common.QuotaPerUnit, common.BatchUpdateEnabled = 10, true
		t.Cleanup(func() {
			require.NoError(t, model.FlushQuotaUpdates())
			common.QuotaPerUnit, common.BatchUpdateEnabled = oldUnit, oldBatch
		})
		for _, test := range []struct {
			provider string
			credit   int
		}{{"epay", 20}, {"stripe", 25}, {"creem", 2}, {"waffo", 20}, {"waffo_pancake", 20}} {
			user := model.User{Username: "df-topup-" + test.provider, AffCode: "df-topup-" + test.provider, Quota: 100, AuthVersion: 1}
			require.NoError(t, database.Create(&user).Error)
			_, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			reserved, err := model.TryReserveUserQuota(user.Id, 7)
			require.NoError(t, err)
			require.True(t, reserved)
			row := billingentity.TopUp{UserId: user.Id, Amount: 2, Money: 2.5, TradeNo: "cache-" + test.provider, PaymentProvider: test.provider, PaymentMethod: test.provider, Status: common.TopUpStatusPending}
			require.NoError(t, model.TopUpStore().Create(t.Context(), &row))
			customer := "cus_cache"
			input := billingcontract.TopUpCompletion{TradeNo: row.TradeNo, Provider: test.provider, StripeCustomerID: &customer, CustomerEmail: "payer@example.test"}
			store := model.TopUpStore()
			done, err := store.Complete(t.Context(), input)
			require.NoError(t, err)
			assert.False(t, done)
			done, err = store.Complete(t.Context(), input)
			require.NoError(t, err)
			assert.True(t, done)
			cached, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 93+test.credit, cached.Quota)
			assert.EqualValues(t, 1, cached.AuthVersion)
			require.NoError(t, database.First(&user, user.Id).Error)
			assert.Equal(t, 100+test.credit, user.Quota)
			if test.provider == "creem" {
				assert.Equal(t, "payer@example.test", cached.Email)
			}
			if test.provider == "stripe" {
				assert.Equal(t, "cus_cache", user.StripeCustomer)
			}
			require.NoError(t, model.FlushQuotaUpdates())
			require.NoError(t, database.First(&user, user.Id).Error)
			assert.Equal(t, 93+test.credit, user.Quota)
		}
	})

	t.Run("session revocation and auth version publication invalidate cached access", func(t *testing.T) {
		user := model.User{Username: "dragonfly-session", AffCode: "dragonfly-session", AuthVersion: 1}
		require.NoError(t, database.Create(&user).Error)
		_, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		now := time.Now().Unix()
		session := model.UserSession{SID: "dragonfly-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: strings.Repeat("b", 64), LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600}
		require.NoError(t, model.CreateUserSession(&session))
		_, err = model.GetUserSessionCached(session.SID)
		require.NoError(t, err)
		revoked, err := model.RevokeUserSession(user.Id, session.SID, "integration-test")
		require.NoError(t, err)
		assert.True(t, revoked)
		_, err = model.GetUserSessionCached(session.SID)
		assert.ErrorIs(t, err, model.ErrUserSessionInactive)
		require.NoError(t, model.SetUserAuthVersionFence(user.Id, 2))
		_, err = model.GetUserCache(user.Id)
		assert.ErrorIs(t, err, model.ErrUserAuthCachePending)
		require.NoError(t, database.Model(&user).Update("auth_version", 2).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.EqualValues(t, 2, cached.AuthVersion)
	})

	t.Run("hash transactions and namespaced scan deletion preserve TTL and unrelated keys", func(t *testing.T) {
		value := struct {
			Name  string
			Count int
		}{Name: "cache-value", Count: 7}
		require.NoError(t, common.RedisHSetObj("dragonfly-integration:hash", &value, time.Minute))
		var loaded struct {
			Name  string
			Count int
		}
		require.NoError(t, common.RedisHGetObj("dragonfly-integration:hash", &loaded))
		assert.Equal(t, value, loaded)
		ttl, err := client.TTL(t.Context(), "dragonfly-integration:hash").Result()
		require.NoError(t, err)
		assert.Positive(t, ttl)
		assert.LessOrEqual(t, ttl, time.Minute)
		expiresAt, err := client.Do(t.Context(), "PEXPIRETIME", "dragonfly-integration:hash").Int64()
		require.NoError(t, err)
		require.NoError(t, common.RedisHIncrBy("dragonfly-integration:hash", "Count", 3))
		require.NoError(t, common.RedisHSetField("dragonfly-integration:hash", "Name", "updated"))
		require.NoError(t, common.RedisHGetObj("dragonfly-integration:hash", &loaded))
		assert.Equal(t, 10, loaded.Count)
		assert.Equal(t, "updated", loaded.Name)
		afterMutation, err := client.Do(t.Context(), "PEXPIRETIME", "dragonfly-integration:hash").Int64()
		require.NoError(t, err)
		assert.Equal(t, expiresAt, afterMutation, "atomic field updates must not extend cache life")
		require.NoError(t, common.RedisHIncrBy("dragonfly-integration:missing", "Count", 1))
		exists, err := client.Exists(t.Context(), "dragonfly-integration:missing").Result()
		require.NoError(t, err)
		assert.Zero(t, exists, "mutation must not create a partial cache record")
		cache := cachex.NewHybridCache(cachex.HybridCacheConfig[string]{Namespace: "dragonfly-integration", Redis: client, RedisCodec: cachex.StringCodec{}})
		require.NoError(t, cache.SetWithTTL("target:one", "value", time.Minute))
		require.NoError(t, cache.SetWithTTL("keep", "untouched", time.Minute))
		deleted, err := cache.DeleteByPrefix("target:")
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
		kept, found, err := cache.Get("keep")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "untouched", kept)
	})

	t.Run("fixed window rate limit returns 429 with retry information", func(t *testing.T) {
		previousEnabled, previousNum, previousDuration := common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
		t.Cleanup(func() {
			common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = previousEnabled, previousNum, previousDuration
		})
		common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = true, 1, 60
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.GET("/limited", middleware.GlobalAPIRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		for _, expected := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
			request := httptest.NewRequest(http.MethodGet, "/limited", nil)
			request.RemoteAddr = "192.0.2.18:5000"
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, expected, response.Code)
			if expected == http.StatusTooManyRequests {
				assert.NotEmpty(t, response.Header().Get("Retry-After"))
			}
		}
	})
}
