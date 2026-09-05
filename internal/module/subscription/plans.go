package subscription

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
)

func (s *Service) ListPlans(ctx context.Context, enabledOnly bool) ([]contract.PlanItem, error) {
	if enabledOnly && s.RequirePaymentCompliance() != nil {
		return []contract.PlanItem{}, nil
	}
	plans, err := s.plans.List(ctx, enabledOnly)
	if err != nil {
		return nil, err
	}
	result := make([]contract.PlanItem, 0, len(plans))
	for _, plan := range plans {
		plan.NormalizeDefaults()
		result = append(result, contract.PlanItem{Plan: contract.Plan(plan)})
	}
	return result, nil
}

func (s *Service) CreatePlan(ctx context.Context, input contract.Plan) (*contract.Plan, error) {
	if err := s.RequirePaymentCompliance(); err != nil {
		return nil, err
	}
	plan := entity.SubscriptionPlan(input)
	plan.Id = 0
	if err := s.validatePlan(&plan); err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	if err := s.plans.Create(ctx, &plan); err != nil {
		return nil, err
	}
	if s.invalidatePlan != nil {
		s.invalidatePlan(plan.Id)
	}
	result := contract.Plan(plan)
	return &result, nil
}

func (s *Service) UpdatePlan(ctx context.Context, id int, input contract.Plan) error {
	if err := s.RequirePaymentCompliance(); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("无效的ID")
	}
	plan := entity.SubscriptionPlan(input)
	plan.Id = id
	if err := s.validatePlan(&plan); err != nil {
		return err
	}
	if err := s.plans.Update(ctx, &plan); err != nil {
		return err
	}
	if s.invalidatePlan != nil {
		s.invalidatePlan(id)
	}
	return nil
}

func (s *Service) UpdatePlanStatus(ctx context.Context, id int, enabled bool) error {
	if err := s.RequirePaymentCompliance(); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("无效的ID")
	}
	if err := s.plans.UpdateStatus(ctx, id, enabled); err != nil {
		return err
	}
	if s.invalidatePlan != nil {
		s.invalidatePlan(id)
	}
	return nil
}

func (s *Service) validatePlan(plan *entity.SubscriptionPlan) error {
	if strings.TrimSpace(plan.Title) == "" {
		return errors.New("套餐标题不能为空")
	}
	if plan.PriceAmount < 0 {
		return errors.New("价格不能为负数")
	}
	if plan.PriceAmount > 9999 {
		return errors.New("价格不能超过9999")
	}
	plan.Currency = "USD"
	if plan.DurationUnit == "" {
		plan.DurationUnit = entity.SubscriptionDurationMonth
	}
	if plan.DurationValue <= 0 && plan.DurationUnit != entity.SubscriptionDurationCustom {
		plan.DurationValue = 1
	}
	if plan.MaxPurchasePerUser < 0 {
		return errors.New("购买上限不能为负数")
	}
	if plan.TotalAmount < 0 {
		return errors.New("总额度不能为负数")
	}
	plan.UpgradeGroup = strings.TrimSpace(plan.UpgradeGroup)
	if plan.UpgradeGroup != "" && (s.groupExists == nil || !s.groupExists(plan.UpgradeGroup)) {
		return errors.New("升级分组不存在")
	}
	plan.DowngradeGroup = strings.TrimSpace(plan.DowngradeGroup)
	if plan.DowngradeGroup != "" && (s.groupExists == nil || !s.groupExists(plan.DowngradeGroup)) {
		return errors.New("降级分组不存在")
	}
	plan.QuotaResetPeriod = entity.NormalizeResetPeriod(plan.QuotaResetPeriod)
	if plan.QuotaResetPeriod == entity.SubscriptionResetCustom && plan.QuotaResetCustomSeconds <= 0 {
		return errors.New("自定义重置周期需大于0秒")
	}
	return nil
}
