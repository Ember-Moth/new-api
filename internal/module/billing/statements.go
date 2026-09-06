package billing

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/shopspring/decimal"
)

func (s *Service) DashboardSubscription(ctx context.Context, userID, tokenID int) (contract.OpenAISubscriptionResponse, error) {
	cfg := s.statementConfig()
	var remaining, used int
	var expiry int64
	var unlimited bool
	if cfg.TokenStats {
		token, err := s.statements.Token(ctx, userID, tokenID)
		if err != nil {
			return contract.OpenAISubscriptionResponse{}, err
		}
		remaining, used, expiry, unlimited = token.RemainQuota, token.UsedQuota, token.ExpiredTime, token.UnlimitedQuota
	} else {
		var err error
		remaining, used, err = s.statements.UserBalances(ctx, userID)
		if err != nil {
			return contract.OpenAISubscriptionResponse{}, err
		}
	}
	// Add in decimal before rendering: two valid bigint counters can overflow int.
	amount, err := statementAmount(decimal.NewFromInt(int64(remaining)).Add(decimal.NewFromInt(int64(used))), cfg)
	if err != nil {
		return contract.OpenAISubscriptionResponse{}, err
	}
	if unlimited {
		amount = 100000000
	}
	return contract.OpenAISubscriptionResponse{Object: "billing_subscription", HasPaymentMethod: true, SoftLimitUSD: amount, HardLimitUSD: amount, SystemHardLimitUSD: amount, AccessUntil: max(0, expiry)}, nil
}
func (s *Service) DashboardUsage(ctx context.Context, userID, tokenID int) (contract.OpenAIUsageResponse, error) {
	cfg := s.statementConfig()
	var used int
	if cfg.TokenStats {
		token, err := s.statements.Token(ctx, userID, tokenID)
		if err != nil {
			return contract.OpenAIUsageResponse{}, err
		}
		used = token.UsedQuota
	} else {
		var err error
		used, err = s.statements.UserUsedQuota(ctx, userID)
		if err != nil {
			return contract.OpenAIUsageResponse{}, err
		}
	}
	amount, err := statementAmount(decimal.NewFromInt(int64(used)).Mul(decimal.NewFromInt(100)), cfg)
	if err != nil {
		return contract.OpenAIUsageResponse{}, err
	}
	return contract.OpenAIUsageResponse{Object: "list", TotalUsage: amount}, nil
}
func statementAmount(quota decimal.Decimal, cfg contract.StatementConfig) (float64, error) {
	if cfg.DisplayType != "TOKENS" {
		if cfg.QuotaPerUnit <= 0 || math.IsNaN(cfg.QuotaPerUnit) || math.IsInf(cfg.QuotaPerUnit, 0) {
			return 0, errors.New("额度单位配置错误")
		}
		quota = quota.Div(decimal.NewFromFloat(cfg.QuotaPerUnit))
		if cfg.DisplayType == "CNY" {
			if cfg.ExchangeRate <= 0 || math.IsNaN(cfg.ExchangeRate) || math.IsInf(cfg.ExchangeRate, 0) {
				return 0, errors.New("汇率配置错误")
			}
			quota = quota.Mul(decimal.NewFromFloat(cfg.ExchangeRate))
		}
	}
	value := quota.InexactFloat64()
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, errors.New("账单金额超出可表示范围")
	}
	return value, nil
}
func (s *Service) TokenUsage(ctx context.Context, key string) (contract.TokenUsage, error) {
	token, err := s.statements.TokenByKey(ctx, key)
	if err != nil {
		return contract.TokenUsage{}, err
	}
	expiry := token.ExpiredTime
	if expiry == -1 {
		expiry = 0
	}
	total := decimal.NewFromInt(int64(token.RemainQuota)).Add(decimal.NewFromInt(int64(token.UsedQuota)))
	return contract.TokenUsage{Object: "token_usage", Name: token.Name, TotalGranted: json.Number(total.StringFixed(0)), TotalUsed: token.UsedQuota, TotalAvailable: token.RemainQuota, UnlimitedQuota: token.UnlimitedQuota, ModelLimits: token.GetModelLimitsMap(), ModelLimitsEnabled: token.ModelLimitsEnabled, ExpiresAt: expiry}, nil
}
