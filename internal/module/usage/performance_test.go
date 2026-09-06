package usage_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/usage"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	"github.com/QuantumNous/new-api/internal/module/usage/performance"
	usagehttp "github.com/QuantumNous/new-api/internal/module/usage/transport/http"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func performanceDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	return db
}

func TestPerformanceQueriesMergePersistedAndPendingSamples(t *testing.T) {
	db := performanceDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.Unix())
	settings := contract.PerformanceSettings{Enabled: true, BucketTime: "hour", FlushInterval: 1}
	store := performance.New(performance.Dependencies{DB: db, Settings: func() contract.PerformanceSettings { return settings }, Now: func() time.Time { return time.Unix(clock.Load(), 0) }, ActiveGroups: func() []string { return []string{"default", "auto"} }})
	// The hot sample must use all of its own counters, and weighted totals must
	// include the older SQL bucket without averaging per-bucket averages.
	require.NoError(t, db.Create(&[]entity.PerfMetric{
		{ModelName: "gpt-a", Group: "default", BucketTs: now.Add(-time.Hour).Unix(), RequestCount: 2, SuccessCount: 1, TotalLatencyMs: 400, TtftSumMs: 100, TtftCount: 1, OutputTokens: 20, GenerationMs: 200},
		{ModelName: "gpt-a", Group: "retired", BucketTs: now.Add(-time.Hour).Unix(), RequestCount: 99, SuccessCount: 99, TotalLatencyMs: 99},
		{ModelName: "too-old", Group: "default", BucketTs: now.Add(-31 * 24 * time.Hour).Unix(), RequestCount: 1},
	}).Error)
	store.Record(contract.Sample{Model: "gpt-a", Success: true, LatencyMs: 100, TtftMs: 20, HasTtft: true, OutputTokens: 10, GenerationMs: 100})
	store.Record(contract.Sample{Model: "gpt-a", Group: "retired", Success: true, LatencyMs: 500})
	store.Record(contract.Sample{Model: "gpt-b", Group: "auto", LatencyMs: -20, TtftMs: -1, HasTtft: true, OutputTokens: -1, GenerationMs: -1})
	first, err := store.Query(t.Context(), contract.QueryParams{Model: "gpt-a"})
	require.NoError(t, err)
	require.Len(t, first.Groups, 1)
	assert.Equal(t, "dbcd0a3c01b55203", first.SeriesSchema)
	assert.Equal(t, "default", first.Groups[0].Group)
	assert.Equal(t, int64(166), first.Groups[0].AvgLatencyMs)
	assert.Equal(t, int64(60), first.Groups[0].AvgTtftMs)
	assert.InDelta(t, 66.6666666667, first.Groups[0].SuccessRate, 0.000001)
	assert.Equal(t, float64(100), first.Groups[0].AvgTps)
	require.Len(t, first.Groups[0].Series, 2)
	summary, err := store.QuerySummary(t.Context(), math.MaxInt)
	require.NoError(t, err)
	require.Len(t, summary.Models, 2)
	assert.Equal(t, contract.ModelSummary{ModelName: "gpt-a", AvgLatencyMs: 166, SuccessRate: 66.67, AvgTps: 100, RecentSuccessRates: []float64{50, 100}, RequestCount: 3}, summary.Models[0])
	assert.Equal(t, contract.ModelSummary{ModelName: "gpt-b", RecentSuccessRates: []float64{0}, RequestCount: 1}, summary.Models[1])
	require.NoError(t, store.Flush(t.Context(), false))
	var row entity.PerfMetric
	err = db.Where("model_name = ? AND bucket_ts = ?", "gpt-a", now.Unix()).First(&row).Error
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	// A completed bucket commits identically, and repeated flushing is a no-op.
	clock.Add(3600)
	require.NoError(t, store.Flush(t.Context(), false))
	require.NoError(t, store.Flush(t.Context(), false))
	after, err := store.Query(t.Context(), contract.QueryParams{Model: "gpt-a"})
	require.NoError(t, err)
	assert.Equal(t, first, after)
	handler := usagehttp.New(&usage.Service{Performance: store})
	router := gin.New()
	router.GET("/metrics", handler.GetPerfMetrics)
	router.GET("/summary", handler.GetPerfMetricsSummary)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics?model=gpt-a&group=retired", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Success bool                 `json:"success"`
		Data    contract.QueryResult `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success)
	assert.Empty(t, body.Data.Groups)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusBadRequest, response.Code)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/summary?hours=bad", nil))
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "request_count")
	assert.NotContains(t, response.Body.String(), "retired")

	for _, groups := range [][]string{nil, {}} {
		restricted := performance.New(performance.Dependencies{DB: db, Settings: func() contract.PerformanceSettings { return settings }, Now: func() time.Time { return now }, ActiveGroups: func() []string { return groups }})
		hidden, err := restricted.Query(t.Context(), contract.QueryParams{Model: "gpt-a"})
		require.NoError(t, err)
		assert.Empty(t, hidden.Groups)
		hiddenSummary, err := restricted.QuerySummary(t.Context(), 24)
		require.NoError(t, err)
		assert.Empty(t, hiddenSummary.Models)
	}
}

func TestPerformanceFlushRollbackKeepsConcurrentSamplesAndReaders(t *testing.T) {
	db := performanceDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	settings := contract.PerformanceSettings{Enabled: true}
	store := performance.New(performance.Dependencies{DB: db, Settings: func() contract.PerformanceSettings { return settings }, Now: func() time.Time { return now }})
	store.Record(contract.Sample{Model: "model", Success: true, LatencyMs: 100, HasTtft: true, TtftMs: 20, OutputTokens: 10, GenerationMs: 80})
	require.NoError(t, db.Exec("ALTER TABLE perf_metrics ADD CONSTRAINT reject_performance_fixture CHECK (model_name <> 'model')").Error)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("performance_flush_fixture", func(tx *gorm.DB) {
		if tx.Statement.Table == "perf_metrics" {
			once.Do(func() { close(entered); <-release })
		}
	}))
	flushResult := make(chan error, 1)
	go func() { flushResult <- store.Flush(t.Context(), true) }()
	<-entered
	// Recording stays available while SQL is blocked. A query crossing the
	// flush must observe the complete restored sample, not drained counters.
	store.Record(contract.Sample{Model: "model", LatencyMs: 300})
	queryResult := make(chan contract.QueryResult, 1)
	queryError := make(chan error, 1)
	go func() {
		result, err := store.Query(t.Context(), contract.QueryParams{Model: "model"})
		queryResult <- result
		queryError <- err
	}()
	close(release)
	require.Error(t, <-flushResult)
	result := <-queryResult
	require.NoError(t, <-queryError)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, int64(200), result.Groups[0].AvgLatencyMs)
	assert.Equal(t, float64(50), result.Groups[0].SuccessRate)
	assert.Equal(t, int64(20), result.Groups[0].AvgTtftMs)
	assert.Equal(t, float64(125), result.Groups[0].AvgTps)
	require.NoError(t, db.Callback().Create().Remove("performance_flush_fixture"))
	var count int64
	require.NoError(t, db.Model(&entity.PerfMetric{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Exec("ALTER TABLE perf_metrics DROP CONSTRAINT reject_performance_fixture").Error)
	require.NoError(t, store.Flush(t.Context(), true))
	require.NoError(t, store.Flush(t.Context(), true))
	var row entity.PerfMetric
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, int64(2), row.RequestCount)
	assert.Equal(t, int64(1), row.SuccessCount)
	assert.Equal(t, int64(400), row.TotalLatencyMs)
	assert.Equal(t, int64(1), row.TtftCount)
	assert.Equal(t, int64(10), row.OutputTokens)
	assert.Equal(t, int64(80), row.GenerationMs)
}

func TestPerformanceInstancesBucketsRetentionAndShutdown(t *testing.T) {
	db := performanceDatabase(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 9, 6, 12, 1, 12, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	settings := contract.PerformanceSettings{Enabled: true, BucketTime: "minute", FlushInterval: math.MaxInt}
	deps := performance.Dependencies{DB: db, Settings: func() contract.PerformanceSettings { return settings }, Now: now}
	first, second := performance.New(deps), performance.New(deps)
	sample := contract.Sample{Model: "model", Success: true, LatencyMs: 100}
	first.Record(sample)
	second.Record(sample)
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, store := range []*performance.Store{first, second} {
		go func() { <-start; results <- store.Flush(t.Context(), true) }()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	var row entity.PerfMetric
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, clock.Load()-clock.Load()%60, row.BucketTs)
	assert.Equal(t, int64(2), row.RequestCount)
	assert.Equal(t, int64(200), row.TotalLatencyMs)
	settings.BucketTime = "5min"
	first.Record(sample)
	settings.Enabled = false
	first.Record(sample)
	ctx, cancel := context.WithCancel(t.Context())
	done := first.Start(ctx)
	cancel()
	<-done
	// Already accepted data survives disabling collection and an active-bucket shutdown.
	require.NoError(t, first.Flush(t.Context(), true))
	var rows []entity.PerfMetric
	require.NoError(t, db.Order("bucket_ts").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, clock.Load()-clock.Load()%300, rows[0].BucketTs)
	assert.Equal(t, int64(1), rows[0].RequestCount)
	settings.RetentionDays = 1
	clock.Add(86400)
	cutoff := now().Unix() - 86400
	require.NoError(t, db.Create(&entity.PerfMetric{ModelName: "boundary", Group: "default", BucketTs: cutoff, RequestCount: 1}).Error)
	require.NoError(t, first.Cleanup(t.Context()))
	rows = nil
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "boundary", rows[0].ModelName)
	settings.RetentionDays = math.MaxInt
	require.NoError(t, first.Cleanup(t.Context()))
	settings.Enabled = true
	first.Record(sample)
	canceled, cancelFlush := context.WithCancel(t.Context())
	cancelFlush()
	require.ErrorIs(t, first.Flush(canceled, true), context.Canceled)
	require.NoError(t, first.Flush(t.Context(), true))
	result, err := first.QuerySummary(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, result.Models, 2)
}
