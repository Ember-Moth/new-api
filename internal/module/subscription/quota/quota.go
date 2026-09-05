package quota

import (
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	implementation "github.com/QuantumNous/new-api/internal/module/subscription/internal/quota"
	"gorm.io/gorm"
)

type Store = implementation.Store

func New(db *gorm.DB, catalog *catalog.Store) *Store { return implementation.New(db, catalog) }
