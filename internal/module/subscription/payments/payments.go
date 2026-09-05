package payments

import implementation "github.com/QuantumNous/new-api/internal/module/subscription/internal/payments"

type Store = implementation.Store
type Dependencies = implementation.Dependencies
type Billing = implementation.Billing

var ErrOrderNotFound = implementation.ErrOrderNotFound
var ErrOrderStatusInvalid = implementation.ErrOrderStatusInvalid

func New(deps Dependencies) *Store { return implementation.New(deps) }
