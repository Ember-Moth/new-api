package pricing

import (
	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/groups"
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
)

func (s *Service) View(ctx context.Context, userID int) (contract.PricingView, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return contract.PricingView{}, err
	}
	userGroup := ""
	if userID > 0 {
		user, err := s.deps.Users.GetUserCache(userID)
		if err != nil {
			return contract.PricingView{}, err
		}
		userGroup = user.Group
	}
	usable := groups.GetUserUsableGroups(userGroup)
	ratios := ratio_setting.GetGroupRatioCopy()
	for group := range ratios {
		if _, ok := usable[group]; !ok {
			delete(ratios, group)
			continue
		}
		if value, ok := ratio_setting.GetGroupGroupRatio(userGroup, group); ok {
			ratios[group] = value
		}
	}
	visible := make([]contract.Pricing, 0, len(snapshot.Prices))
	prices := snapshot.Prices
	if len(usable) == 0 {
		prices = nil
	}
	for _, item := range prices {
		if common.StringsContains(item.EnableGroup, "all") {
			visible = append(visible, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usable[group]; ok {
				visible = append(visible, item)
				break
			}
		}
	}
	return contract.PricingView{Success: true, Data: visible, Vendors: snapshot.Vendors, GroupRatio: ratios, UsableGroup: usable, SupportedEndpoint: snapshot.Endpoints, AutoGroups: groups.GetUserAutoGroup(userGroup), PricingVersion: "a42d372ccf0b5dd13ecf71203521f9d2"}, nil
}
func (s *Service) ResetModelRatio(ctx context.Context) error {
	if err := s.deps.SaveOption(ctx, "ModelRatio", ratio_setting.DefaultModelRatio2JSONString()); err != nil {
		return err
	}
	s.Invalidate()
	return nil
}
