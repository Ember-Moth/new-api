package accounting

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/accounting"

type Store = implementation.Store
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Store { return implementation.New(deps) }
