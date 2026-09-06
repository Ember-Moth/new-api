package routing

import (
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	"github.com/QuantumNous/new-api/internal/shared/common"
)

func (r *Runtime) IsChannelEnabledForGroupModel(group string, modelName string, channelID int) bool {
	if group == "" || modelName == "" || channelID <= 0 {
		return false
	}
	if r.snapshot.readOnly || common.MemoryCacheEnabled {
		channel, err := r.CacheGetChannel(channelID)
		if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled {
			return false
		}
		if channel.ChannelInfo.IsMultiKey && !hasEnabledMultiKey(channel.GetKeys(), channel.ChannelInfo.MultiKeyStatusList) {
			return false
		}
		r.channelSyncLock.RLock()
		defer r.channelSyncLock.RUnlock()
		if isChannelIDInList(r.group2model2channels[group][modelName], channelID) {
			return true
		}
		normalized := ratio_setting.RoutingMatchModelName(modelName)
		return normalized != "" && normalized != modelName && isChannelIDInList(r.group2model2channels[group][normalized], channelID)
	}
	if !common.MemoryCacheEnabled {
		return r.isChannelEnabledForGroupModelDB(group, modelName, channelID)
	}

	r.channelSyncLock.RLock()
	defer r.channelSyncLock.RUnlock()

	if r.group2model2channels == nil {
		return false
	}

	if isChannelIDInList(r.group2model2channels[group][modelName], channelID) {
		return true
	}
	normalized := ratio_setting.RoutingMatchModelName(modelName)
	if normalized != "" && normalized != modelName {
		return isChannelIDInList(r.group2model2channels[group][normalized], channelID)
	}
	return false
}

func (r *Runtime) IsChannelEnabledForAnyGroupModel(groups []string, modelName string, channelID int) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if r.IsChannelEnabledForGroupModel(g, modelName, channelID) {
			return true
		}
	}
	return false
}

func (r *Runtime) isChannelEnabledForGroupModelDB(group string, modelName string, channelID int) bool {
	var count int64
	err := r.db.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, modelName, channelID, true).
		Count(&count).Error
	if err == nil && count > 0 {
		return true
	}
	normalized := ratio_setting.RoutingMatchModelName(modelName)
	if normalized == "" || normalized == modelName {
		return false
	}
	count = 0
	err = r.db.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, normalized, channelID, true).
		Count(&count).Error
	return err == nil && count > 0
}

func isChannelIDInList(list []int, channelID int) bool {
	for _, id := range list {
		if id == channelID {
			return true
		}
	}
	return false
}
