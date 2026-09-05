package performance

import (
	"context"
	"math"
	"sort"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
)

func (s *Store) Query(ctx context.Context, params contract.QueryParams) (contract.QueryResult, error) {
	if params.Hours <= 0 {
		params.Hours = 24
	}
	if params.Hours > 24*30 {
		params.Hours = 24 * 30
	}
	s.flushMu.RLock()
	defer s.flushMu.RUnlock()
	endTs := s.now().Unix()
	startTs := endTs - int64(params.Hours)*3600

	merged := map[bucketKey]counters{}
	rows, err := s.metrics(ctx, params.Model, params.Group, startTs, endTs)
	if err != nil {
		return contract.QueryResult{}, err
	}
	for _, row := range rows {
		mergeCounters(merged, bucketKey{
			model:    row.ModelName,
			group:    row.Group,
			bucketTs: row.BucketTs,
		}, counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			ttftSumMs:      row.TtftSumMs,
			ttftCount:      row.TtftCount,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		})
	}

	s.mu.Lock()
	for k, value := range s.pending {
		if k.model != params.Model || k.bucketTs < startTs || k.bucketTs > endTs {
			continue
		}
		if params.Group != "" && k.group != params.Group {
			continue
		}
		mergeCounters(merged, k, value)
	}
	s.mu.Unlock()

	result := buildQueryResult(params.Model, merged)
	if s.activeGroups != nil {
		allowed := allowedGroupSet(s.activeGroups())
		visible := result.Groups[:0]
		for _, group := range result.Groups {
			if _, ok := allowed[group.Group]; ok {
				visible = append(visible, group)
			}
		}
		result.Groups = visible
	}
	return result, nil
}

func (s *Store) QuerySummary(ctx context.Context, hours int) (contract.SummaryAllResult, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	s.flushMu.RLock()
	defer s.flushMu.RUnlock()
	endTs := s.now().Unix()
	startTs := endTs - int64(hours)*3600
	var groups []string
	if s.activeGroups != nil {
		groups = s.activeGroups()
		// A configured group provider with no active groups must not turn
		// into the repository's nil (unrestricted) filter.
		if groups == nil {
			groups = []string{}
		}
	}
	allowedGroups := allowedGroupSet(groups)

	rows, err := s.summaryBuckets(ctx, startTs, endTs, groups)
	if err != nil {
		return contract.SummaryAllResult{}, err
	}

	totals := map[string]counters{}
	modelBuckets := map[string]map[int64]counters{}
	for _, row := range rows {
		value := counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		}
		mergeModelTotals(totals, row.ModelName, value)
		mergeModelBucket(modelBuckets, row.ModelName, row.BucketTs, value)
	}

	s.mu.Lock()
	for k, value := range s.pending {
		if k.bucketTs < startTs || k.bucketTs > endTs {
			continue
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[k.group]; !ok {
				continue
			}
		}
		snap := value
		if snap.requestCount == 0 {
			continue
		}
		mergeModelTotals(totals, k.model, snap)
		mergeModelBucket(modelBuckets, k.model, k.bucketTs, snap)
	}
	s.mu.Unlock()

	models := make([]contract.ModelSummary, 0, len(totals))
	for name, total := range totals {
		if total.requestCount == 0 {
			continue
		}
		avgLatency := total.totalLatencyMs / total.requestCount
		successRate := float64(total.successCount) / float64(total.requestCount) * 100
		avgTps := 0.0
		if total.generationMs > 0 {
			avgTps = float64(total.outputTokens) / (float64(total.generationMs) / 1000.0)
		}
		models = append(models, contract.ModelSummary{
			ModelName:          name,
			AvgLatencyMs:       avgLatency,
			SuccessRate:        math.Round(successRate*100) / 100,
			AvgTps:             math.Round(avgTps*100) / 100,
			RecentSuccessRates: recentSuccessRates(modelBuckets[name], 3),
			RequestCount:       total.requestCount,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].RequestCount > models[j].RequestCount
	})

	return contract.SummaryAllResult{Models: models}, nil
}
