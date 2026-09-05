package rankings

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/module/usage/aggregation"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
)

const (
	rankingCacheTTL         = 5 * time.Minute
	rankingLeaderboardLimit = 20
	rankingHistoryLimit     = 10
	rankingVendorLimit      = 5
	rankingMoverLimit       = 6
	rankingOthersLabel      = "Others"
	rankingUnknownVendor    = "Unknown"
)

type rankingPeriodConfig struct {
	id          string
	duration    time.Duration
	bucketSize  int64
	labelLayout string
	hasPrevious bool
}

type rankingCacheItem struct {
	expiresAt time.Time
	data      *contract.RankingsResponse
}

type vendorAggregate struct {
	name           string
	icon           string
	totalTokens    int64
	previousTokens int64
	models         map[string]struct{}
	topModel       string
	topModelTokens int64
}

type Reader struct {
	source   *aggregation.Store
	metadata func(context.Context) map[string]contract.RankingModelMetadata
	now      func() time.Time
	cacheMu  sync.Mutex
	cache    map[string]rankingCacheItem
}

func New(source *aggregation.Store, metadata func(context.Context) map[string]contract.RankingModelMetadata, now func() time.Time) *Reader {
	return &Reader{source: source, metadata: metadata, now: now, cache: make(map[string]rankingCacheItem)}
}

func (r *Reader) GetRankingsSnapshot(ctx context.Context, period string) (*contract.RankingsResponse, error) {
	config, err := rankingConfig(period)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := r.now()
	r.cacheMu.Lock()
	if item, ok := r.cache[config.id]; ok && now.Before(item.expiresAt) {
		r.cacheMu.Unlock()
		return item.data, nil
	}
	r.cacheMu.Unlock()
	data, err := r.buildRankingsSnapshot(ctx, config, now)
	if err != nil {
		return nil, err
	}
	r.cacheMu.Lock()
	r.cache[config.id] = rankingCacheItem{expiresAt: now.Add(rankingCacheTTL), data: data}
	r.cacheMu.Unlock()
	return data, nil
}

func rankingConfig(period string) (rankingPeriodConfig, error) {
	switch period {
	case "", "week":
		return rankingPeriodConfig{id: "week", duration: 7 * 24 * time.Hour, bucketSize: 24 * 3600, labelLayout: "Jan 2", hasPrevious: true}, nil
	case "today":
		return rankingPeriodConfig{id: "today", duration: 24 * time.Hour, bucketSize: 3600, labelLayout: "15:04", hasPrevious: true}, nil
	case "month":
		return rankingPeriodConfig{id: "month", duration: 30 * 24 * time.Hour, bucketSize: 24 * 3600, labelLayout: "Jan 2", hasPrevious: true}, nil
	case "year":
		return rankingPeriodConfig{id: "year", duration: 365 * 24 * time.Hour, bucketSize: 7 * 24 * 3600, labelLayout: "Jan 2", hasPrevious: true}, nil
	default:
		return rankingPeriodConfig{}, fmt.Errorf("invalid ranking period: %s", period)
	}
}

func (r *Reader) buildRankingsSnapshot(ctx context.Context, config rankingPeriodConfig, now time.Time) (*contract.RankingsResponse, error) {
	startTime, endTime := now.Add(-config.duration).Unix(), now.Unix()
	currentTotals, err := r.source.GetRankingQuotaTotals(ctx, startTime, endTime)
	if err != nil {
		return nil, err
	}
	currentBuckets, err := r.source.GetRankingQuotaBuckets(ctx, startTime, endTime, config.bucketSize)
	if err != nil {
		return nil, err
	}

	var previousTotals []contract.RankingQuotaTotal
	if config.hasPrevious {
		previousStart, previousEnd := now.Add(-2*config.duration).Unix(), startTime-1
		previousTotals, err = r.source.GetRankingQuotaTotals(ctx, previousStart, previousEnd)
		if err != nil {
			return nil, err
		}
	}

	var meta map[string]contract.RankingModelMetadata
	if r.metadata != nil {
		meta = r.metadata(ctx)
	}
	var totalTokens int64
	for _, item := range currentTotals {
		totalTokens += item.TotalTokens
	}
	previousRankByModel := make(map[string]int, len(previousTotals))
	previousTokensByModel := make(map[string]int64, len(previousTotals))
	for idx, item := range previousTotals {
		previousRankByModel[item.ModelName] = idx + 1
		previousTokensByModel[item.ModelName] = item.TotalTokens
	}

	rankedModels := buildRankedModels(currentTotals, totalTokens, previousRankByModel, previousTokensByModel, meta, config.hasPrevious)
	vendors := buildRankedVendors(currentTotals, previousTotals, totalTokens, meta, config.hasPrevious)
	modelHistory := buildModelHistory(currentBuckets, currentTotals, meta, config)
	vendorHistory := buildVendorShareHistory(currentBuckets, vendors, totalTokens, meta, config)
	movers, droppers := buildRankingMovers(rankedModels)

	return &contract.RankingsResponse{
		Models:             rankedModels[:min(len(rankedModels), rankingLeaderboardLimit)],
		Vendors:            vendors,
		TopMovers:          movers,
		TopDroppers:        droppers,
		ModelsHistory:      modelHistory,
		VendorShareHistory: vendorHistory,
	}, nil
}
