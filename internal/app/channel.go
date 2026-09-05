package app

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/model"
)

// channelPricing exposes only the catalog projection while pricing ownership
// is being moved out of the legacy model package.
type channelPricing struct{}

func (channelPricing) ModelEndpointTypes(name string) []constant.EndpointType {
	return model.GetModelSupportEndpointTypes(name)
}

func (channelPricing) ModelGroups(name string) []string {
	return model.GetModelEnableGroups(name)
}

func (channelPricing) ModelQuotaTypes(name string) []int {
	return model.GetModelQuotaTypes(name)
}

func (channelPricing) Models() []contract.ModelPricing {
	prices := model.GetPricing()
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
	model.RefreshPricing()
}
