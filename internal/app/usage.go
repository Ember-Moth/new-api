package app

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/model"
)

// rankingModelMetadata projects one billing snapshot for usage rankings.
func rankingModelMetadata(ctx context.Context) map[string]contract.RankingModelMetadata {
	snapshot, err := model.PricingService().Snapshot(ctx)
	if err != nil {
		common.SysError("ranking pricing snapshot: " + err.Error())
	}
	vendors := make(map[int]billingcontract.PricingVendor)
	for _, vendor := range snapshot.Vendors {
		vendors[vendor.ID] = vendor
	}
	metadata := make(map[string]contract.RankingModelMetadata)
	for _, pricing := range snapshot.Prices {
		item := contract.RankingModelMetadata{Vendor: pricing.OwnerBy}
		if vendor, ok := vendors[pricing.VendorID]; ok {
			item.Vendor = vendor.Name
			item.VendorIcon = vendor.Icon
		}
		metadata[pricing.ModelName] = item
	}
	return metadata
}
