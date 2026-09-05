package controller

import (
	"github.com/QuantumNous/new-api/internal/module/billing/purchases"
	"github.com/QuantumNous/new-api/service"
)

func isWaffoPancakeTopUpEnabled() bool {
	return purchases.Information(service.WalletConfiguration()).Pancake
}
