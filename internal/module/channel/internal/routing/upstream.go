package routing

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

var upstreamModelUpdateFields = []string{
	"id", "name", "type", "key", "status", "base_url", "models", "model_mapping", "settings", "setting",
	"other", "group", "priority", "weight", "tag", "channel_info", "header_override",
}

func (r *Runtime) EnabledChannelBatch(lastID, batchSize int) ([]*Channel, error) {
	var channels []*Channel
	query := r.db.Select(upstreamModelUpdateFields).Where("status = ?", common.ChannelStatusEnabled).Order("id asc").Limit(batchSize)
	if lastID > 0 {
		query = query.Where("id > ?", lastID)
	}
	return channels, query.Find(&channels).Error
}

func (r *Runtime) SaveUpstreamModelSettings(channel *Channel, settings dto.ChannelOtherSettings, updateModels bool) error {
	channel.SetOtherSettings(settings)
	updates := map[string]any{"settings": channel.OtherSettings}
	if updateModels {
		updates["models"] = channel.Models
	}
	return r.db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates).Error
}
