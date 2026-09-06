package pricesync

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/pricesync"

type Service = implementation.Service
type Dependencies = implementation.Dependencies
type InputError = implementation.InputError
type QueryError = implementation.QueryError

var ErrNoUpstreams = implementation.ErrNoUpstreams

func New(deps Dependencies) *Service { return implementation.New(deps) }
