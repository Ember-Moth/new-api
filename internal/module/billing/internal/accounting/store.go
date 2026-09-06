package accounting

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Dependencies struct {
	DB           *gorm.DB
	Redis        *redis.Client
	CacheEnabled func() bool
	BatchEnabled func() bool
}
type Store struct {
	db              *gorm.DB
	deps            Dependencies
	deferCache      bool
	historicalToken bool
	batchFlushMutex sync.Mutex
	workerMu        sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
}

func New(deps Dependencies) *Store {
	if deps.CacheEnabled == nil {
		deps.CacheEnabled = func() bool { return false }
	}
	if deps.BatchEnabled == nil {
		deps.BatchEnabled = func() bool { return false }
	}
	return &Store{db: deps.DB, deps: deps}
}

// Transaction executes fn in one PostgreSQL transaction. Callers that need
// more than one billing ledger to commit together should bind each store to
// the *gorm.DB passed to fn instead of starting nested transactions.
func (s *Store) Transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	if s == nil || s.db == nil {
		return errors.New("accounting database is unavailable")
	}
	if fn == nil {
		return errors.New("accounting transaction callback is nil")
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

// WithTx returns a store whose balance operations use tx. Cache publication
// is deferred because a transaction may still roll back after its SQL writes.
// The caller must publish/invalidate projections after Transaction returns.
func (s *Store) WithTx(tx *gorm.DB) *Store {
	if tx == nil {
		return nil
	}
	return &Store{db: tx, deps: s.deps, deferCache: true}
}

// WithHistoricalTx binds an already-authorized lifecycle operation to tx while
// allowing token ledger writes for a token that was subsequently soft-deleted
// or rotated. This is for the current deployment's durable lifecycle, not a
// compatibility path for old schemas or old deployments. Authorization is
// established when the session/operation starts; terminal settlement and
// refund use the persisted token id and never re-authorize it.
func (s *Store) WithHistoricalTx(tx *gorm.DB) *Store {
	if tx == nil {
		return nil
	}
	return &Store{db: tx, deps: s.deps, deferCache: true, historicalToken: true}
}

// UserQuotaTx locks and reads a user's authoritative wallet balance. It is
// used by a billing transaction when deciding trust bypass and when filling
// the immutable session snapshot.
func (s *Store) UserQuotaTx(ctx context.Context, id int) (int, error) {
	if id <= 0 {
		return 0, errors.New("invalid user id")
	}
	var user entity.User
	err := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "quota").Where("id = ? AND deleted_at IS NULL", id).First(&user).Error
	if err != nil {
		return 0, err
	}
	return user.Quota, nil
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
				_ = s.Flush(runCtx)
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
