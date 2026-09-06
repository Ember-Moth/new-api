package performance

import (
	"cmp"
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Dependencies struct {
	DB           *gorm.DB
	Settings     func() contract.PerformanceSettings
	ActiveGroups func() []string
	Now          func() time.Time
}

type Store struct {
	db           *gorm.DB
	settings     func() contract.PerformanceSettings
	activeGroups func() []string
	now          func() time.Time
	// Readers hold flushMu across SQL and pending snapshots so a committed
	// bucket is counted exactly once when it moves between the two stores.
	flushMu sync.RWMutex
	mu      sync.Mutex
	pending map[bucketKey]counters
}

func New(deps Dependencies) *Store {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Store{db: deps.DB, settings: deps.Settings, activeGroups: deps.ActiveGroups, now: deps.Now, pending: make(map[bucketKey]counters)}
}

func (s *Store) bucketStart(ts int64) int64 {
	seconds := int64(3600)
	switch s.settings().BucketTime {
	case "minute":
		seconds = 60
	case "5min":
		seconds = 300
	}
	return ts - ts%seconds
}

func (s *Store) Record(sample contract.Sample) {
	if !s.settings().Enabled || sample.Model == "" {
		return
	}
	if sample.Group == "" {
		sample.Group = "default"
	}
	value := counters{requestCount: 1}
	if sample.Success {
		value.successCount = 1
	}
	if sample.LatencyMs > 0 {
		value.totalLatencyMs = sample.LatencyMs
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		value.ttftCount = 1
		value.ttftSumMs = sample.TtftMs
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		value.outputTokens = sample.OutputTokens
		value.generationMs = sample.GenerationMs
	}
	key := bucketKey{model: sample.Model, group: sample.Group, bucketTs: s.bucketStart(s.now().Unix())}
	s.mu.Lock()
	current := s.pending[key]
	current.add(value)
	s.pending[key] = current
	s.mu.Unlock()
}

// Flush persists completed buckets; shutdown also includes the active bucket.
// A failed transaction restores complete samples alongside concurrent arrivals.
func (s *Store) Flush(ctx context.Context, includeActive bool) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	currentBucket := s.bucketStart(s.now().Unix())
	snapshot := make(map[bucketKey]counters)
	s.mu.Lock()
	for key, value := range s.pending {
		if !includeActive && key.bucketTs >= currentBucket {
			continue
		}
		snapshot[key] = value
		delete(s.pending, key)
	}
	s.mu.Unlock()
	if len(snapshot) == 0 {
		return nil
	}
	rows := make([]entity.PerfMetric, 0, len(snapshot))
	for key, value := range snapshot {
		rows = append(rows, entity.PerfMetric{ModelName: key.model, Group: key.group, BucketTs: key.bucketTs, RequestCount: value.requestCount, SuccessCount: value.successCount, TotalLatencyMs: value.totalLatencyMs, TtftSumMs: value.ttftSumMs, TtftCount: value.ttftCount, OutputTokens: value.outputTokens, GenerationMs: value.generationMs})
	}
	slices.SortFunc(rows, func(a, b entity.PerfMetric) int {
		return cmp.Or(cmp.Compare(a.ModelName, b.ModelName), cmp.Compare(a.Group, b.Group), cmp.Compare(a.BucketTs, b.BucketTs))
	})
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Omit("id").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "model_name"}, {Name: "group"}, {Name: "bucket_ts"}},
			DoUpdates: clause.Assignments(map[string]any{
				"request_count":    gorm.Expr("perf_metrics.request_count + EXCLUDED.request_count"),
				"success_count":    gorm.Expr("perf_metrics.success_count + EXCLUDED.success_count"),
				"total_latency_ms": gorm.Expr("perf_metrics.total_latency_ms + EXCLUDED.total_latency_ms"),
				"ttft_sum_ms":      gorm.Expr("perf_metrics.ttft_sum_ms + EXCLUDED.ttft_sum_ms"),
				"ttft_count":       gorm.Expr("perf_metrics.ttft_count + EXCLUDED.ttft_count"),
				"output_tokens":    gorm.Expr("perf_metrics.output_tokens + EXCLUDED.output_tokens"),
				"generation_ms":    gorm.Expr("perf_metrics.generation_ms + EXCLUDED.generation_ms"),
			}),
		}).CreateInBatches(&rows, 500).Error
	})
	if err != nil {
		s.mu.Lock()
		for key, value := range snapshot {
			current := s.pending[key]
			current.add(value)
			s.pending[key] = current
		}
		s.mu.Unlock()
	}
	return err
}

func (s *Store) Cleanup(ctx context.Context) error {
	days := s.settings().RetentionDays
	if days <= 0 || int64(days) > math.MaxInt64/86400 {
		return nil
	}
	return s.deleteBefore(ctx, s.now().Unix()-int64(days)*86400)
}

func (s *Store) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			minutes := max(1, s.settings().FlushInterval)
			delay := time.Duration(min(minutes, int(math.MaxInt64/int64(time.Minute)))) * time.Minute
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if err := s.Flush(ctx, false); err != nil && ctx.Err() == nil {
				common.SysError("failed to flush performance metrics: " + err.Error())
			}
			if err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
				common.SysError("failed to cleanup performance metrics: " + err.Error())
			}
		}
	}()
	return done
}
