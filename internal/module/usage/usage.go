package usage

import (
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/usage/internal/logstore"
)

type Service = implementation.Store
type Dependencies = implementation.Dependencies
type Log = entity.Log
type LogCursorPage = implementation.LogCursorPage
type Stat = implementation.Stat

var ErrInvalidLogCursor = implementation.ErrInvalidLogCursor

func New(deps Dependencies) *Service { return implementation.New(deps) }
func FormatAdminLogs(logs []*Log)    { implementation.FormatAdminLogs(logs) }
func FormatRootLogs(logs []*Log)     { implementation.FormatRootLogs(logs) }

func FormatUserLogs(logs []*Log, offset int) { implementation.FormatUserLogs(logs, offset) }
