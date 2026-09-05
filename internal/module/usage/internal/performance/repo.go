package performance

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/usage/entity"
)

const commonGroupCol = `"group"`

func (s *Store) metrics(ctx context.Context, modelName string, group string, startTs int64, endTs int64) ([]entity.PerfMetric, error) {
	var metrics []entity.PerfMetric
	query := s.db.WithContext(ctx).Model(&entity.PerfMetric{}).
		Where("model_name = ? AND bucket_ts >= ? AND bucket_ts <= ?", modelName, startTs, endTs)
	if group != "" {
		query = query.Where(commonGroupCol+" = ?", group)
	}
	err := query.Order("bucket_ts ASC").Find(&metrics).Error
	return metrics, err
}

type summaryBucket struct {
	ModelName      string `json:"model_name"`
	BucketTs       int64  `json:"bucket_ts"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	TotalLatencyMs int64  `json:"total_latency_ms"`
	OutputTokens   int64  `json:"output_tokens"`
	GenerationMs   int64  `json:"generation_ms"`
}

func (s *Store) summaryBuckets(ctx context.Context, startTs int64, endTs int64, groups []string) ([]summaryBucket, error) {
	var summaries []summaryBucket
	query := s.db.WithContext(ctx).Model(&entity.PerfMetric{}).
		Select("model_name, bucket_ts, SUM(request_count) as request_count, SUM(success_count) as success_count, SUM(total_latency_ms) as total_latency_ms, SUM(output_tokens) as output_tokens, SUM(generation_ms) as generation_ms").
		Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if groups != nil {
		if len(groups) == 0 {
			return summaries, nil
		}
		query = query.Where(commonGroupCol+" IN ?", groups)
	}
	err := query.
		Group("model_name, bucket_ts").
		Having("SUM(request_count) > 0").
		Order("bucket_ts ASC").
		Find(&summaries).Error
	return summaries, err
}

func (s *Store) deleteBefore(ctx context.Context, cutoffTs int64) error {
	if cutoffTs <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("bucket_ts < ?", cutoffTs).Delete(&entity.PerfMetric{}).Error
}
