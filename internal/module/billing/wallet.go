package billing

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
)

// AdjustWallet preserves the control-plane command contract while credit/debit
// accounting is migrated behind the runtime port. The returned amount is the
// locked previous balance for an absolute replacement's audit record.
func (s *Service) AdjustWallet(ctx context.Context, id int, mode string, amount int) (int, error) {
	switch mode {
	case "add":
		if amount <= 0 {
			return 0, errors.New("quota must be positive")
		}
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return 0, s.walletRuntime.Credit(id, amount)
	case "subtract":
		if amount <= 0 {
			return 0, errors.New("quota must be positive")
		}
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return 0, s.walletRuntime.Debit(id, amount)
	case "override":
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return s.wallets.Replace(ctx, id, amount)
	default:
		return 0, errors.New("invalid wallet adjustment mode")
	}
}
