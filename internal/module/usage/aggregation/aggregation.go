// Package aggregation exposes the hourly usage store to application wiring and
// legacy event producers while keeping its implementation private to usage.
package aggregation

import implementation "github.com/QuantumNous/new-api/internal/module/usage/internal/aggregation"

type Store = implementation.Store
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Store { return implementation.New(deps) }
