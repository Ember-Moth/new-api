package memberships

import implementation "github.com/QuantumNous/new-api/internal/module/subscription/internal/memberships"

type Store = implementation.Store
type Dependencies = implementation.Dependencies
type UserGroups = implementation.UserGroups

func New(deps Dependencies) *Store { return implementation.New(deps) }
