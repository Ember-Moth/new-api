package aggregation

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Dependencies struct {
	DB           *gorm.DB
	TokenNames   func(context.Context, []int) (map[int]string, error)
	ChannelNames func(context.Context, []int) (map[int]string, error)
}

type dimensions struct {
	userID              int
	username, modelName string
	hour                int64
	group               string
	tokenID, channelID  int
	node                string
}

type Store struct {
	db                       *gorm.DB
	tokenNames, channelNames func(context.Context, []int) (map[int]string, error)
	mu                       sync.Mutex
	pending                  map[dimensions]entity.QuotaData
	flushMu                  sync.Mutex
	retry                    []entity.QuotaData
}

func New(deps Dependencies) *Store {
	return &Store{db: deps.DB, tokenNames: deps.TokenNames, channelNames: deps.ChannelNames, pending: make(map[dimensions]entity.QuotaData)}
}

func (s *Store) Record(event contract.QuotaDataLogParams) {
	hour := event.CreatedAt - event.CreatedAt%3600
	key := dimensions{event.UserID, event.Username, event.ModelName, hour, event.UseGroup, event.TokenID, event.ChannelID, event.NodeName}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.pending[key]
	if !ok {
		row = entity.QuotaData{UserID: event.UserID, Username: event.Username, ModelName: event.ModelName, CreatedAt: hour, UseGroup: event.UseGroup, TokenID: event.TokenID, ChannelID: event.ChannelID, NodeName: event.NodeName}
	}
	row.Count++
	row.Quota += event.Quota
	row.TokenUsed += event.TokenUsed
	s.pending[key] = row
}

// Flush retains a rolled-back snapshot separately from new arrivals. Each
// snapshot commits atomically, and retrying does not replay successful snapshots.
func (s *Store) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	if len(s.retry) > 0 {
		if err := s.persist(ctx, s.retry); err != nil {
			return err
		}
		s.retry = nil
	}
	s.mu.Lock()
	snapshot := s.pending
	s.pending = make(map[dimensions]entity.QuotaData)
	s.mu.Unlock()
	if len(snapshot) == 0 {
		return nil
	}
	rows := make([]entity.QuotaData, 0, len(snapshot))
	for _, row := range snapshot {
		rows = append(rows, row)
	}
	// All instances acquire conflicting row locks in the same dimension order.
	slices.SortFunc(rows, func(a, b entity.QuotaData) int {
		return cmp.Or(cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.Username, b.Username), cmp.Compare(a.ModelName, b.ModelName), cmp.Compare(a.CreatedAt, b.CreatedAt), cmp.Compare(a.UseGroup, b.UseGroup), cmp.Compare(a.TokenID, b.TokenID), cmp.Compare(a.ChannelID, b.ChannelID), cmp.Compare(a.NodeName, b.NodeName))
	})
	if err := s.persist(ctx, rows); err != nil {
		s.retry = rows
		return err
	}
	return nil
}

func (s *Store) persist(ctx context.Context, rows []entity.QuotaData) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Omit("id").Clauses(clause.OnConflict{
			OnConstraint: "quota_data_dimensions_key",
			DoUpdates: clause.Assignments(map[string]any{
				"count":      gorm.Expr("quota_data.count + EXCLUDED.count"),
				"quota":      gorm.Expr("quota_data.quota + EXCLUDED.quota"),
				"token_used": gorm.Expr("quota_data.token_used + EXCLUDED.token_used"),
			}),
		}).CreateInBatches(&rows, 500).Error
	})
}

// Start flushes accepted data even if exporting has since been disabled. The
// caller cancels and waits for this worker before a final shutdown flush.
func (s *Store) Start(ctx context.Context, interval func() time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			if err := s.Flush(ctx); err != nil && ctx.Err() == nil {
				common.SysError("failed to flush dashboard usage: " + err.Error())
			}
			delay := interval()
			if delay <= 0 {
				delay = time.Minute
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return done
}
