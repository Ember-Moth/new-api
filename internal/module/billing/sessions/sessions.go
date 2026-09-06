package sessions

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/sessions"

type Engine = implementation.Engine
type Session = implementation.Session
type Dependencies = implementation.Dependencies

func New(deps Dependencies) *Engine { return implementation.New(deps) }
