package topups

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/topups"

type Store = implementation.Store
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Store { return implementation.New(deps) }
