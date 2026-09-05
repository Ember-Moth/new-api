package webhooks

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/webhooks"

type Processor = implementation.Processor
type Config = implementation.Config
type Dependencies = implementation.Dependencies
type SubscriptionOrders = implementation.SubscriptionOrders

var ErrDisabled = implementation.ErrDisabled
var ErrSignature = implementation.ErrSignature
var ErrPayload = implementation.ErrPayload

func New(deps Dependencies) *Processor { return implementation.New(deps) }
