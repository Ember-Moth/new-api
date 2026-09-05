package rankings

import (
	"math"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
)

func modelMeta(modelName string, meta map[string]contract.RankingModelMetadata) contract.RankingModelMetadata {
	if item, ok := meta[modelName]; ok && item.Vendor != "" {
		return item
	}
	return contract.RankingModelMetadata{Vendor: rankingUnknownVendor}
}

func buildRankedModels(totals []contract.RankingQuotaTotal, totalTokens int64, previousRanks map[string]int, previousTokens map[string]int64, meta map[string]contract.RankingModelMetadata, showGrowth bool) []contract.RankedModel {
	rows := make([]contract.RankedModel, 0, len(totals))
	for idx, item := range totals {
		modelMeta := modelMeta(item.ModelName, meta)
		var previousRank *int
		if rank, ok := previousRanks[item.ModelName]; ok {
			rankCopy := rank
			previousRank = &rankCopy
		}
		growth := 0.0
		if showGrowth {
			growth = rankingGrowthPct(item.TotalTokens, previousTokens[item.ModelName])
		}
		rows = append(rows, contract.RankedModel{
			Rank:         idx + 1,
			PreviousRank: previousRank,
			ModelName:    item.ModelName,
			Vendor:       modelMeta.Vendor,
			VendorIcon:   modelMeta.VendorIcon,
			Category:     "all",
			TotalTokens:  item.TotalTokens,
			Share:        rankingShare(item.TotalTokens, totalTokens),
			GrowthPct:    growth,
		})
	}
	return rows
}

func buildRankedVendors(currentTotals []contract.RankingQuotaTotal, previousTotals []contract.RankingQuotaTotal, totalTokens int64, meta map[string]contract.RankingModelMetadata, showGrowth bool) []contract.RankedVendor {
	aggregates := make(map[string]*vendorAggregate)
	for _, item := range currentTotals {
		modelMeta := modelMeta(item.ModelName, meta)
		agg := ensureVendorAggregate(aggregates, modelMeta)
		agg.totalTokens += item.TotalTokens
		agg.models[item.ModelName] = struct{}{}
		if item.TotalTokens > agg.topModelTokens {
			agg.topModel = item.ModelName
			agg.topModelTokens = item.TotalTokens
		}
	}
	for _, item := range previousTotals {
		modelMeta := modelMeta(item.ModelName, meta)
		agg := ensureVendorAggregate(aggregates, modelMeta)
		agg.previousTokens += item.TotalTokens
	}

	rows := make([]contract.RankedVendor, 0, len(aggregates))
	for _, agg := range aggregates {
		if agg.totalTokens <= 0 {
			continue
		}
		growth := 0.0
		if showGrowth {
			growth = rankingGrowthPct(agg.totalTokens, agg.previousTokens)
		}
		rows = append(rows, contract.RankedVendor{
			Vendor:      agg.name,
			VendorIcon:  agg.icon,
			TotalTokens: agg.totalTokens,
			Share:       rankingShare(agg.totalTokens, totalTokens),
			GrowthPct:   growth,
			ModelsCount: len(agg.models),
			TopModel:    agg.topModel,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalTokens == rows[j].TotalTokens {
			return rows[i].Vendor < rows[j].Vendor
		}
		return rows[i].TotalTokens > rows[j].TotalTokens
	})
	for idx := range rows {
		rows[idx].Rank = idx + 1
	}
	return rows
}

func ensureVendorAggregate(aggregates map[string]*vendorAggregate, meta contract.RankingModelMetadata) *vendorAggregate {
	name := meta.Vendor
	if name == "" {
		name = rankingUnknownVendor
	}
	agg, ok := aggregates[name]
	if !ok {
		agg = &vendorAggregate{
			name:   name,
			icon:   meta.VendorIcon,
			models: make(map[string]struct{}),
		}
		aggregates[name] = agg
	}
	if agg.icon == "" && meta.VendorIcon != "" {
		agg.icon = meta.VendorIcon
	}
	return agg
}

func buildModelHistory(buckets []contract.RankingQuotaBucket, totals []contract.RankingQuotaTotal, meta map[string]contract.RankingModelMetadata, config rankingPeriodConfig) contract.ModelHistorySeries {
	topModels := make(map[string]struct{})
	models := make([]contract.ModelHistoryModel, 0, min(len(totals), rankingHistoryLimit)+1)
	otherTotal := int64(0)
	for idx, item := range totals {
		if idx < rankingHistoryLimit {
			topModels[item.ModelName] = struct{}{}
			modelMeta := modelMeta(item.ModelName, meta)
			models = append(models, contract.ModelHistoryModel{Name: item.ModelName, Vendor: modelMeta.Vendor, Total: item.TotalTokens})
			continue
		}
		otherTotal += item.TotalTokens
	}
	if otherTotal > 0 {
		models = append(models, contract.ModelHistoryModel{Name: rankingOthersLabel, Vendor: "Various", Total: otherTotal})
	}

	bucketSet := make(map[int64]struct{})
	tokensByBucketAndModel := make(map[int64]map[string]int64)
	for _, item := range buckets {
		modelName := item.ModelName
		if _, ok := topModels[modelName]; !ok {
			modelName = rankingOthersLabel
		}
		bucketSet[item.Bucket] = struct{}{}
		if _, ok := tokensByBucketAndModel[item.Bucket]; !ok {
			tokensByBucketAndModel[item.Bucket] = make(map[string]int64)
		}
		tokensByBucketAndModel[item.Bucket][modelName] += item.Tokens
	}

	sortedBuckets := sortedRankingBuckets(bucketSet)
	points := make([]contract.ModelHistoryPoint, 0, len(sortedBuckets)*len(models))
	for _, bucket := range sortedBuckets {
		for _, historyModel := range models {
			tokens := tokensByBucketAndModel[bucket][historyModel.Name]
			if tokens <= 0 {
				continue
			}
			points = append(points, contract.ModelHistoryPoint{
				Ts:     rankingBucketTs(bucket),
				Label:  rankingBucketLabel(bucket, config),
				Model:  historyModel.Name,
				Vendor: historyModel.Vendor,
				Tokens: tokens,
			})
		}
	}

	return contract.ModelHistorySeries{
		Points:  points,
		Models:  models,
		Buckets: len(sortedBuckets),
	}
}

func buildVendorShareHistory(buckets []contract.RankingQuotaBucket, vendors []contract.RankedVendor, totalTokens int64, meta map[string]contract.RankingModelMetadata, config rankingPeriodConfig) contract.VendorShareSeries {
	topVendors := make(map[string]struct{})
	vendorRows := make([]contract.VendorShareVendor, 0, min(len(vendors), rankingVendorLimit)+1)
	otherTotal := int64(0)
	for idx, vendor := range vendors {
		if idx < rankingVendorLimit {
			topVendors[vendor.Vendor] = struct{}{}
			vendorRows = append(vendorRows, contract.VendorShareVendor{Name: vendor.Vendor, Total: vendor.TotalTokens, Share: vendor.Share})
			continue
		}
		otherTotal += vendor.TotalTokens
	}
	if otherTotal > 0 {
		vendorRows = append(vendorRows, contract.VendorShareVendor{Name: rankingOthersLabel, Total: otherTotal, Share: rankingShare(otherTotal, totalTokens)})
	}

	bucketSet := make(map[int64]struct{})
	tokensByBucketAndVendor := make(map[int64]map[string]int64)
	totalsByBucket := make(map[int64]int64)
	for _, item := range buckets {
		modelMeta := modelMeta(item.ModelName, meta)
		vendorName := modelMeta.Vendor
		if _, ok := topVendors[vendorName]; !ok {
			vendorName = rankingOthersLabel
		}
		bucketSet[item.Bucket] = struct{}{}
		if _, ok := tokensByBucketAndVendor[item.Bucket]; !ok {
			tokensByBucketAndVendor[item.Bucket] = make(map[string]int64)
		}
		tokensByBucketAndVendor[item.Bucket][vendorName] += item.Tokens
		totalsByBucket[item.Bucket] += item.Tokens
	}

	sortedBuckets := sortedRankingBuckets(bucketSet)
	points := make([]contract.VendorSharePoint, 0, len(sortedBuckets)*len(vendorRows))
	for _, bucket := range sortedBuckets {
		for _, vendor := range vendorRows {
			tokens := tokensByBucketAndVendor[bucket][vendor.Name]
			if tokens <= 0 {
				continue
			}
			points = append(points, contract.VendorSharePoint{
				Ts:     rankingBucketTs(bucket),
				Label:  rankingBucketLabel(bucket, config),
				Vendor: vendor.Name,
				Share:  rankingShare(tokens, totalsByBucket[bucket]),
				Tokens: tokens,
			})
		}
	}

	return contract.VendorShareSeries{
		Points:  points,
		Vendors: vendorRows,
		Buckets: len(sortedBuckets),
	}
}

func buildRankingMovers(models []contract.RankedModel) ([]contract.RankingMover, []contract.RankingMover) {
	movers := make([]contract.RankingMover, 0)
	droppers := make([]contract.RankingMover, 0)
	for _, item := range models {
		if item.PreviousRank == nil {
			continue
		}
		delta := *item.PreviousRank - item.Rank
		if delta == 0 {
			continue
		}
		row := contract.RankingMover{
			ModelName:   item.ModelName,
			Vendor:      item.Vendor,
			VendorIcon:  item.VendorIcon,
			RankDelta:   delta,
			CurrentRank: item.Rank,
			GrowthPct:   item.GrowthPct,
		}
		if delta > 0 {
			movers = append(movers, row)
		} else {
			droppers = append(droppers, row)
		}
	}
	sort.Slice(movers, func(i, j int) bool {
		if movers[i].RankDelta == movers[j].RankDelta {
			return movers[i].GrowthPct > movers[j].GrowthPct
		}
		return movers[i].RankDelta > movers[j].RankDelta
	})
	sort.Slice(droppers, func(i, j int) bool {
		if droppers[i].RankDelta == droppers[j].RankDelta {
			return droppers[i].GrowthPct < droppers[j].GrowthPct
		}
		return droppers[i].RankDelta < droppers[j].RankDelta
	})
	return limitRankingMovers(movers, rankingMoverLimit), limitRankingMovers(droppers, rankingMoverLimit)
}

func sortedRankingBuckets(bucketSet map[int64]struct{}) []int64 {
	buckets := make([]int64, 0, len(bucketSet))
	for bucket := range bucketSet {
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i] < buckets[j]
	})
	return buckets
}

func rankingBucketTs(bucket int64) string {
	return time.Unix(bucket, 0).UTC().Format(time.RFC3339)
}

func rankingBucketLabel(bucket int64, config rankingPeriodConfig) string {
	return time.Unix(bucket, 0).Format(config.labelLayout)
}

func rankingShare(value int64, total int64) float64 {
	if total <= 0 || value <= 0 {
		return 0
	}
	return roundRankingFloat(float64(value) / float64(total))
}

func rankingGrowthPct(current int64, previous int64) float64 {
	if previous <= 0 {
		if current > 0 {
			return 100
		}
		return 0
	}
	return roundRankingFloat((float64(current-previous) / float64(previous)) * 100)
}

func roundRankingFloat(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func limitRankingMovers(rows []contract.RankingMover, limit int) []contract.RankingMover {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}
