package rankings_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/usage"
	"github.com/QuantumNous/new-api/internal/module/usage/aggregation"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	"github.com/QuantumNous/new-api/internal/module/usage/internal/rankings"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rankingDatabase(t *testing.T) (*gorm.DB, *aggregation.Store) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	return db, aggregation.New(aggregation.Dependencies{DB: db})
}

func TestRankingsSnapshotPeriodsAndPresentation(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		period   string
		duration time.Duration
		bucket   int64
	}{
		{"today", 24 * time.Hour, 3600},
		{"week", 7 * 24 * time.Hour, 86400},
		{"month", 30 * 24 * time.Hour, 86400},
		{"year", 365 * 24 * time.Hour, 7 * 86400},
	} {
		t.Run(test.period, func(t *testing.T) {
			db, source := rankingDatabase(t)
			start := now.Add(-test.duration).Unix()
			rows := []entity.QuotaData{
				{ModelName: "b", CreatedAt: now.Unix(), TokenUsed: 200},
				{ModelName: "c", CreatedAt: start, TokenUsed: 100},
				{ModelName: "a", CreatedAt: start, TokenUsed: 100},
				{ModelName: "a", CreatedAt: start - 1, TokenUsed: 300},
				{ModelName: "b", CreatedAt: now.Add(-2 * test.duration).Unix(), TokenUsed: 100},
				{ModelName: "too-old", CreatedAt: now.Add(-2*test.duration).Unix() - 1, TokenUsed: 99999},
				{ModelName: "future", CreatedAt: now.Unix() + 1, TokenUsed: 99999},
			}
			require.NoError(t, db.Create(&rows).Error)
			reader := rankings.New(source, func(context.Context) map[string]contract.RankingModelMetadata {
				return map[string]contract.RankingModelMetadata{"a": {Vendor: "Alpha", VendorIcon: "alpha.svg"}, "b": {Vendor: "Alpha", VendorIcon: "alpha.svg"}}
			}, func() time.Time { return now })
			result, err := reader.GetRankingsSnapshot(t.Context(), test.period)
			require.NoError(t, err)
			rank1, rank2 := 1, 2
			assert.Equal(t, []contract.RankedModel{
				{Rank: 1, PreviousRank: &rank2, ModelName: "b", Vendor: "Alpha", VendorIcon: "alpha.svg", Category: "all", TotalTokens: 200, Share: 0.5, GrowthPct: 100},
				{Rank: 2, PreviousRank: &rank1, ModelName: "a", Vendor: "Alpha", VendorIcon: "alpha.svg", Category: "all", TotalTokens: 100, Share: 0.25, GrowthPct: -66.6667},
				{Rank: 3, ModelName: "c", Vendor: "Unknown", Category: "all", TotalTokens: 100, Share: 0.25, GrowthPct: 100},
			}, result.Models)
			assert.Equal(t, []contract.RankedVendor{
				{Rank: 1, Vendor: "Alpha", VendorIcon: "alpha.svg", TotalTokens: 300, Share: 0.75, GrowthPct: -25, ModelsCount: 2, TopModel: "b"},
				{Rank: 2, Vendor: "Unknown", TotalTokens: 100, Share: 0.25, GrowthPct: 100, ModelsCount: 1, TopModel: "c"},
			}, result.Vendors)
			assert.Equal(t, []contract.RankingMover{{ModelName: "b", Vendor: "Alpha", VendorIcon: "alpha.svg", RankDelta: 1, CurrentRank: 1, GrowthPct: 100}}, result.TopMovers)
			assert.Equal(t, []contract.RankingMover{{ModelName: "a", Vendor: "Alpha", VendorIcon: "alpha.svg", RankDelta: -1, CurrentRank: 2, GrowthPct: -66.6667}}, result.TopDroppers)
			require.Len(t, result.ModelsHistory.Points, 3)
			assert.Equal(t, 2, result.ModelsHistory.Buckets)
			assert.Equal(t, time.Unix(start/test.bucket*test.bucket, 0).UTC().Format(time.RFC3339), result.ModelsHistory.Points[0].Ts)
			assert.Equal(t, "a", result.ModelsHistory.Points[0].Model)
			require.Len(t, result.VendorShareHistory.Points, 3)
			assert.Equal(t, 0.5, result.VendorShareHistory.Points[0].Share)
			assert.Equal(t, 0.5, result.VendorShareHistory.Points[1].Share)
			assert.Equal(t, 1.0, result.VendorShareHistory.Points[2].Share)
		})
	}
}

func TestRankingsLimitsPreserveOtherSharesAndMovers(t *testing.T) {
	db, source := rankingDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	meta := make(map[string]contract.RankingModelMetadata)
	var rows []entity.QuotaData
	// 22 models cross the visible leaderboard/history limits; reversing their
	// previous totals also crosses both mover limits without synthetic load.
	for i := 1; i <= 22; i++ {
		name := fmt.Sprintf("model-%02d", i)
		meta[name] = contract.RankingModelMetadata{Vendor: fmt.Sprintf("vendor-%d", (i-1)/3)}
		rows = append(rows, entity.QuotaData{ModelName: name, CreatedAt: now.Add(-time.Hour).Unix(), TokenUsed: i}, entity.QuotaData{ModelName: name, CreatedAt: now.Add(-25 * time.Hour).Unix(), TokenUsed: 23 - i})
	}
	require.NoError(t, db.Create(&rows).Error)
	reader := rankings.New(source, func(context.Context) map[string]contract.RankingModelMetadata { return meta }, func() time.Time { return now })
	result, err := reader.GetRankingsSnapshot(t.Context(), "today")
	require.NoError(t, err)
	require.Len(t, result.Models, 20)
	assert.Equal(t, "model-22", result.Models[0].ModelName)
	assert.Equal(t, "model-03", result.Models[19].ModelName)
	require.Len(t, result.ModelsHistory.Models, 11)
	assert.Equal(t, contract.ModelHistoryModel{Name: "Others", Vendor: "Various", Total: 78}, result.ModelsHistory.Models[10])
	require.Len(t, result.ModelsHistory.Points, 11)
	assert.Equal(t, int64(78), result.ModelsHistory.Points[10].Tokens)
	require.Len(t, result.VendorShareHistory.Vendors, 6)
	other := result.VendorShareHistory.Vendors[5]
	assert.Equal(t, "Others", other.Name)
	assert.Equal(t, int64(43), other.Total) // vendor-7 (22), vendor-1 (15), and vendor-0 (6).
	var vendorTotal int64
	for _, vendor := range result.VendorShareHistory.Vendors {
		vendorTotal += vendor.Total
	}
	assert.Equal(t, int64(253), vendorTotal)
	require.Len(t, result.TopMovers, 6)
	require.Len(t, result.TopDroppers, 6)
	assert.Equal(t, "model-22", result.TopMovers[0].ModelName)
	assert.Equal(t, 21, result.TopMovers[0].RankDelta)
	assert.Equal(t, "model-01", result.TopDroppers[0].ModelName)
	assert.Equal(t, -21, result.TopDroppers[0].RankDelta)
}

func TestRankingsCacheIsolationExpiryErrorsAndHTTP(t *testing.T) {
	db, source := rankingDatabase(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	require.NoError(t, db.Create(&entity.QuotaData{ModelName: "model", CreatedAt: now().Add(-time.Hour).Unix(), TokenUsed: 10}).Error)
	reader := rankings.New(source, nil, now)
	first, err := reader.GetRankingsSnapshot(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, first.Models, 1)
	assert.Equal(t, int64(10), first.Models[0].TotalTokens)
	require.NoError(t, db.Model(&entity.QuotaData{}).Where("model_name = ?", "model").Update("token_used", 20).Error)
	cached, err := reader.GetRankingsSnapshot(t.Context(), "week")
	require.NoError(t, err)
	assert.Equal(t, int64(10), cached.Models[0].TotalTokens)
	separate := rankings.New(source, func(context.Context) map[string]contract.RankingModelMetadata {
		return map[string]contract.RankingModelMetadata{"model": {Vendor: "Independent"}}
	}, now)
	isolated, err := separate.GetRankingsSnapshot(t.Context(), "week")
	require.NoError(t, err)
	require.Len(t, isolated.Models, 1)
	assert.Equal(t, int64(20), isolated.Models[0].TotalTokens)
	assert.Equal(t, "Independent", isolated.Models[0].Vendor)
	clock.Add(5 * 60)
	refreshed, err := reader.GetRankingsSnapshot(t.Context(), "week")
	require.NoError(t, err)
	assert.Equal(t, int64(20), refreshed.Models[0].TotalTokens)
	assert.Equal(t, "Unknown", refreshed.Models[0].Vendor)
	// Independent concurrent callers share immutable cached snapshots.
	var wg sync.WaitGroup
	for _, period := range []string{"week", "today"} {
		wg.Go(func() { _, err := reader.GetRankingsSnapshot(t.Context(), period); assert.NoError(t, err) })
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.GetRankingsSnapshot(ctx, "week")
	assert.ErrorIs(t, err, context.Canceled)
	handler := usagehttp.New(&usage.Service{Reader: reader})
	router := gin.New()
	router.GET("/rankings", handler.GetRankings)
	for _, test := range []struct {
		url     string
		status  int
		success bool
	}{
		{"/rankings", http.StatusOK, true},
		{"/rankings?period=invalid", http.StatusBadRequest, false},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.url, nil))
		assert.Equal(t, test.status, response.Code)
		var body struct {
			Success bool                      `json:"success"`
			Data    contract.RankingsResponse `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
		assert.Equal(t, test.success, body.Success)
		if test.success {
			require.Len(t, body.Data.Models, 1)
			assert.Equal(t, int64(20), body.Data.Models[0].TotalTokens)
		}
	}
	require.NoError(t, db.Exec("ALTER TABLE quota_data RENAME TO unavailable_quota_data").Error)
	_, err = reader.GetRankingsSnapshot(t.Context(), "month")
	require.Error(t, err)
	require.NoError(t, db.Exec("ALTER TABLE unavailable_quota_data RENAME TO quota_data").Error)
	recovered, err := reader.GetRankingsSnapshot(t.Context(), "month")
	require.NoError(t, err)
	require.Len(t, recovered.Models, 1)
	assert.Equal(t, int64(20), recovered.Models[0].TotalTokens)
}
