package app

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func localPricingSyncData() map[string]any {
	data := billing_setting.GetPricingSyncData(map[string]any(ratio_setting.GetExposedData()))
	data["image_ratio"] = ratio_setting.GetImageRatioCopy()
	data["audio_ratio"] = ratio_setting.GetAudioRatioCopy()
	data["audio_completion_ratio"] = ratio_setting.GetAudioCompletionRatioCopy()
	return data
}

func pricingSyncSources(channels *channel.Service) func(context.Context, []int) ([]contract.SyncableChannel, error) {
	return func(ctx context.Context, ids []int) ([]contract.SyncableChannel, error) {
		rows, err := channels.PricingSyncChannels(ctx, ids)
		if err != nil {
			return nil, err
		}
		sources := make([]contract.SyncableChannel, 0, len(rows))
		for _, row := range rows {
			sources = append(sources, contract.SyncableChannel{ID: row.Id, Name: row.Name, BaseURL: row.GetBaseURL(), Status: row.Status, Type: row.Type})
		}
		return sources, nil
	}
}
