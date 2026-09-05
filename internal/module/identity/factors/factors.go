package factors

import (
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/twofa"
	"gorm.io/gorm"
)

type Store = implementation.Store

func New(db *gorm.DB, advance func(*gorm.DB, int) (int64, error), publish func(int) error) *Store {
	return implementation.New(db, advance, publish)
}
