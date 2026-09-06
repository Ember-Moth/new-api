package pricing

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/pricing"

type Service = implementation.Service
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Service { return implementation.New(deps) }
