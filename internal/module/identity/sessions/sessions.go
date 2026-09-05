package sessions

import (
	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/sessions"
	"gorm.io/gorm"
)

type Store = implementation.Store

func New(db *gorm.DB) *Store { return implementation.New(db) }
