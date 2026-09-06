package billing

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/internal/shared/common"
)

// AdjustWallet applies control-plane changes through the shared ledger. The
// returned amount is the locked previous balance for an absolute replacement audit.
func (s *Service) AdjustWallet(ctx context.Context, id int, mode string, amount int) (int, error) {
	switch mode {
	case "add":
		if amount <= 0 {
			return 0, errors.New("quota must be positive")
		}
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return 0, s.accounting.IncreaseUserQuota(ctx, id, amount, true)
	case "subtract":
		if amount <= 0 {
			return 0, errors.New("quota must be positive")
		}
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return 0, s.accounting.DecreaseUserQuota(ctx, id, amount, true)
	case "override":
		if err := common.ValidateWalletQuota(amount); err != nil {
			return 0, err
		}
		return s.wallets.Replace(ctx, id, amount)
	default:
		return 0, errors.New("invalid wallet adjustment mode")
	}
}
