package checkout

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/checkout"

type Client = implementation.Client
type Options = implementation.Options

func New(options Options) *Client { return implementation.New(options) }
