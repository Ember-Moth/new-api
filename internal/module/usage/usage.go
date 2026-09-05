package usage

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/internal/module/usage/performance"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/module/usage/internal/rankings"

	"github.com/QuantumNous/new-api/internal/module/usage/aggregation"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/usage/internal/logstore"
	"gorm.io/gorm"
)

type Service struct {
	Performance *performance.Store
	*rankings.Reader
	Aggregates *aggregation.Store
	*implementation.Store
	*implementation.Writer
}

type WriterPolicy = implementation.WriterPolicy
type Dependencies struct {
	Performance     *performance.Store
	RankingMetadata func(context.Context) map[string]contract.RankingModelMetadata
	Aggregates      *aggregation.Store
	DB              *gorm.DB
	Kind            common.DatabaseType
	ChannelNames    func(context.Context, []int) (map[int]string, error)
	Writer          WriterPolicy
}

type Log = entity.Log
type LogCursorPage = implementation.LogCursorPage
type Stat = implementation.Stat

var ErrInvalidLogCursor = implementation.ErrInvalidLogCursor

func New(deps Dependencies) *Service {
	store := implementation.New(implementation.Dependencies{DB: deps.DB, Kind: deps.Kind, ChannelNames: deps.ChannelNames})
	return &Service{Performance: deps.Performance, Reader: rankings.New(deps.Aggregates, deps.RankingMetadata, time.Now), Aggregates: deps.Aggregates, Store: store, Writer: implementation.NewWriter(store, deps.Writer)}
}

func FormatAdminLogs(logs []*Log) { implementation.FormatAdminLogs(logs) }

func FormatRootLogs(logs []*Log) { implementation.FormatRootLogs(logs) }

func FormatUserLogs(logs []*Log, offset int) { implementation.FormatUserLogs(logs, offset) }
