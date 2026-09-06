package usage_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/channel"
	channelentity "github.com/QuantumNous/new-api/internal/module/channel/entity"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/usage"
	"github.com/QuantumNous/new-api/internal/module/usage/aggregation"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newUsageAggregates(t *testing.T) (*gorm.DB, *aggregation.Store) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })
	store := aggregation.New(aggregation.Dependencies{DB: db, ChannelNames: channel.New(channel.Dependencies{DB: db}).ChannelNames, TokenNames: identity.New(identity.Dependencies{DB: db}).TokenNames})
	return db, store
}

func TestGetFlowQuotaDataUsesQuotaDataRoleSpecificDimensions(t *testing.T) {
	db, store := newUsageAggregates(t)
	require.NoError(t, db.Create(&channelentity.Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, db.Create(&channelentity.Channel{Id: 2, Name: "west"}).Error)
	require.NoError(t, db.Create(&identityentity.Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, db.Create(&identityentity.Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)
	require.NoError(t, db.Delete(&identityentity.Token{Id: 11}).Error)

	require.NoError(t, db.Create(&entity.QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1000,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	}).Error)
	require.NoError(t, db.Create(&entity.QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1100,
		Count:     1,
		Quota:     50,
		TokenUsed: 20,
	}).Error)
	require.NoError(t, db.Create(&entity.QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 2,
		CreatedAt: 1200,
		Count:     1,
		Quota:     25,
		TokenUsed: 10,
	}).Error)
	require.NoError(t, db.Create(&entity.QuotaData{
		UserID:    2,
		Username:  "bob",
		NodeName:  "node-b",
		TokenID:   22,
		UseGroup:  "default",
		ModelName: "gpt-b",
		ChannelID: 1,
		CreatedAt: 1300,
		Count:     3,
		Quota:     70,
		TokenUsed: 30,
	}).Error)
	require.NoError(t, db.Create(&entity.QuotaData{
		UserID:    1,
		Username:  "alice",
		ModelName: "legacy",
		CreatedAt: 1400,
		Count:     99,
		Quota:     999,
		TokenUsed: 999,
	}).Error)

	rootRows, err := store.GetFlowQuotaData(t.Context(), 900, 2000, "", 0, common.RoleRootUser)
	require.NoError(t, err)
	require.Len(t, rootRows, 3)
	// Token 11 was soft-deleted, so its name is intentionally left empty for the
	// frontend to render a localized "deleted (id)" label instead.
	assert.Equal(t, contract.FlowQuotaData{
		UserID:      1,
		Username:    "alice",
		NodeName:    "node-a",
		TokenID:     11,
		TokenName:   "",
		UseGroup:    "vip",
		ChannelID:   1,
		ChannelName: "east",
		ModelName:   "gpt-a",
		TokenUsed:   60,
		Count:       3,
		Quota:       150,
	}, *rootRows[0])
	// A token that still exists resolves to its current name.
	assert.Equal(t, 22, rootRows[1].TokenID)
	assert.Equal(t, "backup", rootRows[1].TokenName)

	adminRows, err := store.GetFlowQuotaData(t.Context(), 900, 2000, "alice", 0, common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, adminRows, 2)
	assert.Equal(t, 0, adminRows[0].TokenID)
	assert.Empty(t, adminRows[0].TokenName)
	assert.Empty(t, adminRows[0].NodeName)
	assert.Equal(t, "alice", adminRows[0].Username)
	assert.Equal(t, "vip", adminRows[0].UseGroup)
	assert.Equal(t, "east", adminRows[0].ChannelName)
	assert.Equal(t, 150, adminRows[0].Quota)

	selfRows, err := store.GetFlowQuotaData(t.Context(), 900, 2000, "", 1, common.RoleCommonUser)
	require.NoError(t, err)
	require.Len(t, selfRows, 1)
	assert.Empty(t, selfRows[0].Username)
	assert.Equal(t, 0, selfRows[0].ChannelID)
	assert.Empty(t, selfRows[0].ChannelName)
	assert.Empty(t, selfRows[0].TokenName)
	assert.Equal(t, "vip", selfRows[0].UseGroup)
	assert.Equal(t, 175, selfRows[0].Quota)

	handler := usagehttp.New(&usage.Service{Aggregates: store})
	for _, test := range []struct {
		name                           string
		role, actor                    int
		self                           bool
		username, token, node, channel string
	}{
		{name: "admin", role: common.RoleAdminUser, username: "bob", channel: "east"},
		{name: "root", role: common.RoleRootUser, username: "bob", token: "backup", node: "node-b", channel: "east"},
		{name: "self", role: common.RoleRootUser, actor: 2, self: true, token: "backup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Set("role", test.role)
			ctx.Set("id", test.actor)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/flow?start_timestamp=900&end_timestamp=2000&username=bob", nil)
			if test.self {
				handler.GetUserFlowQuotaDates(ctx)
			} else {
				handler.GetAllFlowQuotaDates(ctx)
			}
			var body struct {
				Success bool                     `json:"success"`
				Data    []contract.FlowQuotaData `json:"data"`
			}
			require.Equal(t, http.StatusOK, response.Code)
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			require.True(t, body.Success, response.Body.String())
			require.Len(t, body.Data, 1)
			assert.Equal(t, 70, body.Data[0].Quota)
			assert.Equal(t, test.username, body.Data[0].Username)
			assert.Equal(t, test.token, body.Data[0].TokenName)
			assert.Equal(t, test.node, body.Data[0].NodeName)
			assert.Equal(t, test.channel, body.Data[0].ChannelName)
		})
	}
}

func TestUsageAggregateConcurrentInstancesAndDimensionIsolation(t *testing.T) {
	db, first := newUsageAggregates(t)
	second := aggregation.New(aggregation.Dependencies{DB: db})
	base := contract.QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: "gpt-a", CreatedAt: 3661, UseGroup: "vip", TokenID: 11, ChannelID: 1, NodeName: "node-a", Quota: 100, TokenUsed: 40}
	first.Record(base)
	later := base
	later.CreatedAt = 3700
	later.Quota = 50
	later.TokenUsed = 20
	second.Record(later)
	// Each coordinate independently separates a row, including changes to a
	// user's display name and requests made in the next hour.
	variants := []contract.QuotaDataLogParams{base, base, base, base, base, base, base, base}
	variants[0].UserID = 2
	variants[1].Username = "alice-renamed"
	variants[2].ModelName = "gpt-b"
	variants[3].CreatedAt = 7261
	variants[4].UseGroup = "default"
	variants[5].TokenID = 22
	variants[6].ChannelID = 2
	variants[7].NodeName = "node-b"
	for _, event := range variants {
		first.Record(event)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*aggregation.Store{first, second} {
		go func() { <-start; results <- store.Flush(t.Context()) }()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.NoError(t, first.Flush(t.Context()))
	require.NoError(t, second.Flush(t.Context()))
	var rows []entity.QuotaData
	require.NoError(t, db.Order("quota DESC").Find(&rows).Error)
	require.Len(t, rows, 9)
	assert.Equal(t, 2, rows[0].Count)
	assert.Equal(t, 150, rows[0].Quota)
	assert.Equal(t, 60, rows[0].TokenUsed)
	assert.Equal(t, int64(3600), rows[0].CreatedAt)
	duplicate := rows[0]
	duplicate.Id = 0
	assert.Error(t, db.Create(&duplicate).Error)
	assert.Error(t, db.Exec("UPDATE quota_data SET username = NULL WHERE id = ?", rows[0].Id).Error)
	ctx, cancel := context.WithCancel(t.Context())
	done := first.Start(ctx, func() time.Duration { return time.Hour })
	cancel()
	<-done
	first.Record(later)
	require.NoError(t, first.Flush(t.Context()))
	row := entity.QuotaData{}
	require.NoError(t, db.First(&row, rows[0].Id).Error)
	assert.Equal(t, 200, row.Quota)
	assert.Equal(t, 3, row.Count)
}

func TestUsageAggregateRollbackRetainsSnapshotAndNewArrivals(t *testing.T) {
	db, store := newUsageAggregates(t)
	// 501 distinct dimensions put the invalid row in the second SQL batch,
	// proving that a successful first batch rolls back with the failed second.
	for i := 0; i < 501; i++ {
		store.Record(contract.QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: fmt.Sprintf("model-%03d", i), CreatedAt: 3600, Quota: 1, TokenUsed: 2})
	}
	require.NoError(t, db.Exec("ALTER TABLE quota_data ADD CONSTRAINT reject_usage_fixture CHECK (model_name <> 'model-500')").Error)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("usage_flush_fixture", func(tx *gorm.DB) {
		if tx.Statement.Table == "quota_data" {
			once.Do(func() { close(entered); <-release })
		}
	}))
	result := make(chan error, 1)
	go func() { result <- store.Flush(t.Context()) }()
	<-entered
	// A new request must not wait for the database operation or disappear with
	// its snapshot. Use the same dimensions to verify retry increments too.
	store.Record(contract.QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: "model-000", CreatedAt: 3661, Quota: 5, TokenUsed: 3})
	close(release)
	require.Error(t, <-result)
	require.NoError(t, db.Callback().Create().Remove("usage_flush_fixture"))
	var count int64
	require.NoError(t, db.Model(&entity.QuotaData{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Exec("ALTER TABLE quota_data DROP CONSTRAINT reject_usage_fixture").Error)
	require.NoError(t, store.Flush(t.Context()))
	require.NoError(t, store.Flush(t.Context()))
	var rows []entity.QuotaData
	require.NoError(t, db.Order("model_name").Find(&rows).Error)
	require.Len(t, rows, 501)
	assert.Equal(t, 6, rows[0].Quota)
	assert.Equal(t, 5, rows[0].TokenUsed)
	assert.Equal(t, 2, rows[0].Count)
	total := 0
	for _, row := range rows {
		total += row.Quota
	}
	assert.Equal(t, 506, total)
	// Cancellation is also a failed flush and must leave the accepted event.
	store.Record(contract.QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: "model-000", CreatedAt: 3661, Quota: 7})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, store.Flush(ctx), context.Canceled)
	require.NoError(t, store.Flush(t.Context()))
	require.NoError(t, db.Where("model_name = ?", "model-000").First(&rows[0]).Error)
	assert.Equal(t, 13, rows[0].Quota)
	assert.Equal(t, 3, rows[0].Count)
}

func TestUsageDashboardQueriesAndHTTPViewerIsolation(t *testing.T) {
	_, store := newUsageAggregates(t)
	for _, event := range []contract.QuotaDataLogParams{
		{UserID: 1, Username: "shared", ModelName: "gpt-a", CreatedAt: 3600, UseGroup: "vip", Quota: 10, TokenUsed: 20},
		{UserID: 2, Username: "shared", ModelName: "gpt-a", CreatedAt: 3600, UseGroup: "vip", Quota: 100, TokenUsed: 200},
		{UserID: 1, Username: "shared", ModelName: "gpt-b", CreatedAt: 7200, UseGroup: "vip", Quota: 5, TokenUsed: 30},
	} {
		store.Record(event)
	}
	require.NoError(t, store.Flush(t.Context()))
	totals, err := store.GetRankingQuotaTotals(t.Context(), 3600, 7200)
	require.NoError(t, err)
	assert.Equal(t, []contract.RankingQuotaTotal{{ModelName: "gpt-a", TotalTokens: 220}, {ModelName: "gpt-b", TotalTokens: 30}}, totals)
	buckets, err := store.GetRankingQuotaBuckets(t.Context(), 3600, 7200, 0)
	require.NoError(t, err)
	assert.Equal(t, []contract.RankingQuotaBucket{{ModelName: "gpt-a", Bucket: 3600, Tokens: 220}, {ModelName: "gpt-b", Bucket: 7200, Tokens: 30}}, buckets)
	grouped, err := store.GetQuotaDataGroupByUser(t.Context(), 3600, 3600)
	require.NoError(t, err)
	require.Len(t, grouped, 1)
	assert.Equal(t, 110, grouped[0].Quota)
	filtered, err := store.GetAllQuotaDates(t.Context(), 3600, 3600, "shared")
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	handler := usagehttp.New(&usage.Service{Aggregates: store})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 1); c.Set("role", common.RoleRootUser) })
	router.GET("/self", handler.GetUserQuotaDates)
	router.GET("/flow/self", handler.GetUserFlowQuotaDates)
	router.GET("/all", handler.GetAllQuotaDates)
	for _, test := range []struct {
		url     string
		quota   int
		success bool
	}{
		{"/self?start_timestamp=3600&end_timestamp=3600&user_id=2&username=shared", 10, true},
		{"/flow/self?start_timestamp=3600&end_timestamp=3600&user_id=2&role=100", 10, true},
		{"/all?start_timestamp=3600&end_timestamp=3600", 110, true},
		{"/flow/self?start_timestamp=bad&end_timestamp=3600", 0, false},
		{"/flow/self?start_timestamp=7200&end_timestamp=3600", 0, false},
		{"/flow/self?start_timestamp=3600&end_timestamp=3000000", 0, false},
		{"/self?start_timestamp=3600&end_timestamp=3000000", 0, false},
	} {
		t.Run(test.url, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.url, nil))
			require.Equal(t, http.StatusOK, response.Code)
			var body struct {
				Success bool               `json:"success"`
				Data    []entity.QuotaData `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, test.success, body.Success, response.Body.String())
			if test.success {
				require.Len(t, body.Data, 1)
				assert.Equal(t, test.quota, body.Data[0].Quota)
			}
		})
	}
}
