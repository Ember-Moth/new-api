package usercache

import (
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/usercache"
	"gorm.io/gorm"
)

type Store = implementation.Store

const SchemaVersion = entity.UserCacheSchemaVersion

var ErrUserAuthCachePending = implementation.ErrUserAuthCachePending
var ErrUserAuthVersionConflict = implementation.ErrUserAuthVersionConflict

func New(db *gorm.DB) *Store   { return implementation.New(db) }
func CacheKey(id int) string   { return implementation.CacheKey(id) }
func FenceKey(id int) string   { return implementation.FenceKey(id) }
func VersionKey(id int) string { return implementation.VersionKey(id) }
func CacheTTLSeconds() int     { return implementation.CacheTTLSeconds() }
