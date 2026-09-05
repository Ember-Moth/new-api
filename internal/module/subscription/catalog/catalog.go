package catalog

import implementation "github.com/QuantumNous/new-api/internal/module/subscription/internal/catalog"

type Store = implementation.Store
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Store { return implementation.New(deps) }
