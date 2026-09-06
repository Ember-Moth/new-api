package paymentconfig

import implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/paymentconfig"

type ComplianceConfirmation = implementation.ComplianceConfirmation
type Service = implementation.Service
type Config = implementation.Config
type Dependencies = implementation.Dependencies
type PairResult = implementation.PairResult
type Catalog = implementation.Catalog
type CatalogStore = implementation.CatalogStore
type CatalogProduct = implementation.CatalogProduct

func New(deps Dependencies) *Service { return implementation.New(deps) }
