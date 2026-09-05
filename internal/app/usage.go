package app

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/model"
)

// rankingModelMetadata adapts the pricing catalog until billing owns its runtime.
func rankingModelMetadata(context.Context) map[string]contract.RankingModelMetadata {
	vendors := make(map[int]model.PricingVendor)
	for _, vendor := range model.GetVendors() {
		vendors[vendor.ID] = vendor
	}
	metadata := make(map[string]contract.RankingModelMetadata)
	for _, pricing := range model.GetPricing() {
		item := contract.RankingModelMetadata{Vendor: pricing.OwnerBy}
		if vendor, ok := vendors[pricing.VendorID]; ok {
			item.Vendor = vendor.Name
			item.VendorIcon = vendor.Icon
		}
		metadata[pricing.ModelName] = item
	}
	return metadata
}
