package accounting

import (
	"context"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type Dependencies struct {
	DB           *gorm.DB
	Redis        *redis.Client
	CacheEnabled func() bool
	BatchEnabled func() bool
}
type Store struct {
	db                *gorm.DB
	deps              Dependencies
	batchUpdateStores []map[int]int
	batchUpdateLocks  []sync.Mutex
	batchFlushMutex   sync.Mutex
	pendingQuotaBatch *quotaBatch
	workerMu          sync.Mutex
	cancel            context.CancelFunc
	done              chan struct{}
}

func New(deps Dependencies) *Store {
	if deps.CacheEnabled == nil {
		deps.CacheEnabled = func() bool { return false }
	}
	if deps.BatchEnabled == nil {
		deps.BatchEnabled = func() bool { return false }
	}
	s := &Store{db: deps.DB, deps: deps}
	for range BatchUpdateTypeCount {
		s.batchUpdateStores = append(s.batchUpdateStores, make(map[int]int))
		s.batchUpdateLocks = append(s.batchUpdateLocks, sync.Mutex{})
	}
	return s
}

func (s *Store) Start(ctx context.Context, interval time.Duration) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.done != nil {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
				_ = s.batchUpdate(runCtx)
			}
		}
	}()
}

// Stop joins the periodic writer before draining requests' remaining deltas.
func (s *Store) Stop(ctx context.Context) error {
	s.workerMu.Lock()
	cancel, done := s.cancel, s.done
	s.workerMu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Flush(ctx)
}
