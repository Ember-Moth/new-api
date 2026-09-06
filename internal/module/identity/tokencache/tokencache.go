package tokencache

import (
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/tokencache"
	"gorm.io/gorm"
)

type Store = implementation.Store

const FenceSeconds = implementation.FenceSeconds

func New(db *gorm.DB) *Store { return implementation.New(db) }
func Key(key string) string  { return implementation.Key(key) }
