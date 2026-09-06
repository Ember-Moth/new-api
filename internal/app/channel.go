package app

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/model"
)

// channelPricing adapts the billing catalog to channel management.
type channelPricing struct{}

func (channelPricing) ModelEndpointTypes(name string) []constant.EndpointType {
	value, err := model.PricingService().EndpointTypes(context.Background(), name)
	if err != nil {
		common.SysError("channel pricing endpoints: " + err.Error())
	}
	return value
}

func (channelPricing) ModelGroups(name string) []string {
	value, err := model.PricingService().ModelGroups(context.Background(), name)
	if err != nil {
		common.SysError("channel pricing groups: " + err.Error())
	}
	return value
}

func (channelPricing) ModelQuotaTypes(name string) []int {
	value, err := model.PricingService().QuotaTypes(context.Background(), name)
	if err != nil {
		common.SysError("channel pricing quota types: " + err.Error())
	}
	return value
}

func (channelPricing) Models() []contract.ModelPricing {
	snapshot, err := model.PricingService().Snapshot(context.Background())
	if err != nil {
		common.SysError("channel pricing catalog: " + err.Error())
	}
	prices := snapshot.Prices
	result := make([]contract.ModelPricing, 0, len(prices))
	for _, price := range prices {
		result = append(result, contract.ModelPricing{
			ModelName: price.ModelName, SupportedEndpointTypes: price.SupportedEndpointTypes,
			EnableGroup: price.EnableGroup, QuotaType: price.QuotaType,
		})
	}
	return result
}

func (channelPricing) Refresh() {
	if err := model.PricingService().Refresh(context.Background()); err != nil {
		common.SysError("refresh channel pricing: " + err.Error())
	}
}
