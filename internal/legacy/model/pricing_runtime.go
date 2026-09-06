package model

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/pricing"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
)

type Pricing = contract.Pricing
type PricingVendor = contract.PricingVendor

var pricingServices sync.Map

func PricingService() *pricing.Service {
	if value, ok := pricingServices.Load(DB); ok {
		return value.(*pricing.Service)
	}
	service := pricing.New(pricing.Dependencies{Channels: ChannelService(), Users: usercache.New(DB), SaveOption: OptionManager().UpdateOption})
	actual, _ := pricingServices.LoadOrStore(DB, service)
	return actual.(*pricing.Service)
}
func GetPricing() []Pricing {
	snapshot, err := PricingService().Snapshot(context.Background())
	if err != nil {
		common.SysError("read pricing: " + err.Error())
	}
	return snapshot.Prices
}
func GetVendors() []PricingVendor {
	snapshot, err := PricingService().Snapshot(context.Background())
	if err != nil {
		common.SysError("read pricing vendors: " + err.Error())
	}
	return snapshot.Vendors
}
func GetSupportedEndpointMap() map[string]common.EndpointInfo {
	snapshot, err := PricingService().Snapshot(context.Background())
	if err != nil {
		common.SysError("read pricing endpoints: " + err.Error())
	}
	return snapshot.Endpoints
}
func GetModelSupportEndpointTypes(name string) []constant.EndpointType {
	value, err := PricingService().EndpointTypes(context.Background(), name)
	if err != nil {
		common.SysError("read model endpoints: " + err.Error())
	}
	return value
}
func GetModelEnableGroups(name string) []string {
	value, err := PricingService().ModelGroups(context.Background(), name)
	if err != nil {
		common.SysError("read model groups: " + err.Error())
	}
	return value
}
func GetModelQuotaTypes(name string) []int {
	value, err := PricingService().QuotaTypes(context.Background(), name)
	if err != nil {
		common.SysError("read model quota types: " + err.Error())
	}
	return value
}
func InvalidatePricingCache() {
	if value, ok := pricingServices.Load(DB); ok {
		value.(*pricing.Service).Invalidate()
	}
}
func RefreshPricing() {
	if err := PricingService().Refresh(context.Background()); err != nil {
		common.SysError("refresh pricing: " + err.Error())
	}
}
