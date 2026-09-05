// Package performance exposes relay performance collection and dashboard queries.
package performance

import implementation "github.com/QuantumNous/new-api/internal/module/usage/internal/performance"

type Store = implementation.Store
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Store { return implementation.New(deps) }
