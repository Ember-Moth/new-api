package usage

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/usage/internal/logstore"
	"gorm.io/gorm"
)

type Service struct {
	*implementation.Store
	*implementation.Writer
}

type WriterPolicy = implementation.WriterPolicy
type Dependencies struct {
	DB           *gorm.DB
	Kind         common.DatabaseType
	ChannelNames func(context.Context, []int) (map[int]string, error)
	Writer       WriterPolicy
}

type Log = entity.Log
type LogCursorPage = implementation.LogCursorPage
type Stat = implementation.Stat

var ErrInvalidLogCursor = implementation.ErrInvalidLogCursor

func New(deps Dependencies) *Service {
	store := implementation.New(implementation.Dependencies{DB: deps.DB, Kind: deps.Kind, ChannelNames: deps.ChannelNames})
	return &Service{Store: store, Writer: implementation.NewWriter(store, deps.Writer)}
}

func FormatAdminLogs(logs []*Log) { implementation.FormatAdminLogs(logs) }

func FormatRootLogs(logs []*Log) { implementation.FormatRootLogs(logs) }

func FormatUserLogs(logs []*Log, offset int) { implementation.FormatUserLogs(logs, offset) }
