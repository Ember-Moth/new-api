package purchases

import (
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

func ValidateCredit(quota decimal.Decimal) (int, error) {
	value, err := common.WalletQuotaFromDecimalStrict(quota)
	if err != nil {
		return 0, errors.New("充值额度超出系统可表示范围")
	}
	if value <= 0 {
		return 0, errors.New("充值额度必须大于 0")
	}
	return value, nil
}

// ConvertAmount retains currency-unit truncation used by persisted Epay/Waffo
// orders. All conversions are bounded before taking an integer part.
func ConvertAmount(amount int64, unit float64, tokens bool) (stored int64, credit int, err error) {
	if unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return 0, 0, errors.New("额度单位配置错误")
	}
	if amount <= 0 {
		return 0, 0, errors.New("充值数量无效")
	}
	perUnit := decimal.NewFromFloat(unit)
	maxStored := decimal.NewFromInt(common.MaxWalletQuota).Div(perUnit).Floor()
	maxInput := maxStored
	if tokens {
		maxInput = maxStored.Add(decimal.NewFromInt(1)).Mul(perUnit).Ceil().Sub(decimal.NewFromInt(1))
	}
	if maxInput.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		maxInput = decimal.NewFromInt(math.MaxInt64)
	}
	if maxInput.Sign() > 0 && decimal.NewFromInt(amount).GreaterThan(maxInput) {
		return 0, 0, fmt.Errorf("单笔充值数量不能大于 %d", maxInput.IntPart())
	}
	storedValue := decimal.NewFromInt(amount)
	if tokens {
		storedValue = storedValue.Div(perUnit).Truncate(0)
	}
	if storedValue.Sign() <= 0 || storedValue.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, 0, errors.New("充值数量无效")
	}
	value, err := common.WalletQuotaFromDecimalStrict(storedValue.Mul(perUnit))
	if err != nil || value <= 0 {
		return 0, 0, errors.New("充值数量无效")
	}
	return storedValue.IntPart(), value, nil
}
