package purchases

import (
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	implementation "github.com/QuantumNous/new-api/internal/module/billing/internal/purchases"
	"github.com/shopspring/decimal"
)

type Service = implementation.Service
type Dependencies = implementation.Dependencies
type Gateway = implementation.Gateway

func New(deps Dependencies) *Service { return implementation.New(deps) }
func ConvertAmount(amount int64, unit float64, tokens bool) (int64, int, error) {
	return implementation.ConvertAmount(amount, unit, tokens)
}
func ValidateCredit(value decimal.Decimal) (int, error) { return implementation.ValidateCredit(value) }

func Information(cfg contract.WalletConfig) contract.TopUpInfo {
	return implementation.Information(cfg)
}
