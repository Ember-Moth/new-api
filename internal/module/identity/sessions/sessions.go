package sessions

import (
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/sessions"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type Store = implementation.Store

func New(db *gorm.DB, cache *redis.Client) *Store { return implementation.New(db, cache) }
