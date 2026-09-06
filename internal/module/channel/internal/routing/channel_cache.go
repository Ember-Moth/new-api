package routing

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"

	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/shared/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
)

func (r *Runtime) InitChannelCache() {
	if !common.MemoryCacheEnabled && !r.snapshot.configured && !r.snapshot.readOnly {
		r.changed()
		r.rebuildTaskAliasView()
		return
	}
	if err := r.ReloadChannelCache(context.Background()); err != nil {
		common.SysError("failed to reload channel snapshot: " + err.Error())
	}
}

func (r *Runtime) applyChannelSnapshot(snapshot channelSnapshot) {
	newChannelId2channel := make(map[int]*Channel)
	newChannel2advancedCustomConfig := make(map[int]*kitdto.AdvancedCustomConfig)
	channels := snapshot.Channels
	for _, channel := range channels {
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
				newChannel2advancedCustomConfig[channel.Id] = config
			}
		}
	}
	groups := make(map[string]bool)
	for _, channel := range channels {
		for _, group := range channel.GetGroups() {
			groups[group] = true
		}
	}
	newGroup2model2channels := make(map[string]map[string][]int)
	for group := range groups {
		newGroup2model2channels[group] = make(map[string][]int)
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue // skip disabled channels
		}
		groups := channel.GetGroups()
		for _, group := range groups {
			models := channel.GetModels()
			for _, model := range models {
				if _, ok := newGroup2model2channels[group][model]; !ok {
					newGroup2model2channels[group][model] = make([]int, 0)
				}
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], channel.Id)
			}
		}
	}

	// sort by priority
	for group, model2channels := range newGroup2model2channels {
		for model, channels := range model2channels {
			sort.Slice(channels, func(i, j int) bool {
				return newChannelId2channel[channels[i]].GetPriority() > newChannelId2channel[channels[j]].GetPriority()
			})
			newGroup2model2channels[group][model] = channels
		}
	}

	r.channelSyncLock.Lock()
	r.group2model2channels = newGroup2model2channels
	//r.channelsIDM = newChannelId2channel
	for _, channel := range newChannelId2channel {
		if channel.ChannelInfo.IsMultiKey {
			channel.Keys = channel.GetKeys()

		}
	}
	r.snapshotAbilities = snapshot.Abilities
	r.channelsIDM = newChannelId2channel
	r.channel2advancedCustomConfig = newChannel2advancedCustomConfig
	r.snapshot.dirty.Store(false)
	r.channelSyncLock.Unlock()
	// Release the routing lock before notifying projections: a pricing rebuild
	// may read the parsed channel snapshot through AdvancedConfigs.
	r.changed()
	r.rebuildTaskAliasView()

}

func (r *Runtime) GetRandomSatisfiedChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled && !r.snapshot.readOnly {
		return r.GetChannel(group, model, retry, filters)
	}

	r.channelSyncLock.RLock()
	defer r.channelSyncLock.RUnlock()

	// First, try to find channels with the exact model name.
	channels, _ := r.filterCandidateIDs(r.group2model2channels[group][model], model, filters)

	// If no channels found, try to find channels with the normalized model name.
	if len(channels) == 0 {
		normalizedModel := ratio_setting.RoutingMatchModelName(model)
		channels, _ = r.filterCandidateIDs(r.group2model2channels[group][normalizedModel], model, filters)
	}

	if len(channels) == 0 {
		return nil, nil
	}

	if len(channels) == 1 {
		if channel, ok := r.channelsIDM[channels[0]]; ok {
			return channel, nil
		}
		return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channels[0])
	}

	uniquePriorities := make(map[int]bool)
	for _, channelId := range channels {
		if channel, ok := r.channelsIDM[channelId]; ok {
			uniquePriorities[int(channel.GetPriority())] = true
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}
	var sortedUniquePriorities []int
	for priority := range uniquePriorities {
		sortedUniquePriorities = append(sortedUniquePriorities, priority)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedUniquePriorities)))

	if retry >= len(uniquePriorities) {
		retry = len(uniquePriorities) - 1
	}
	targetPriority := int64(sortedUniquePriorities[retry])

	// get the priority for the given retry number
	var sumWeight = 0
	var targetChannels []*Channel
	for _, channelId := range channels {
		if channel, ok := r.channelsIDM[channelId]; ok {
			if channel.GetPriority() == targetPriority {
				sumWeight += channel.GetWeight()
				targetChannels = append(targetChannels, channel)
			}
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}

	if len(targetChannels) == 0 {
		return nil, errors.New(fmt.Sprintf("no channel found, group: %s, model: %s, priority: %d", group, model, targetPriority))
	}

	// smoothing factor and adjustment
	smoothingFactor := 1
	smoothingAdjustment := 0

	if sumWeight == 0 {
		// when all channels have weight 0, set sumWeight to the number of channels and set smoothing adjustment to 100
		// each channel's effective weight = 100
		sumWeight = len(targetChannels) * 100
		smoothingAdjustment = 100
	} else if sumWeight/len(targetChannels) < 10 {
		// when the average weight is less than 10, set smoothing factor to 100
		smoothingFactor = 100
	}

	// Calculate the total weight of all channels up to endIdx
	totalWeight := sumWeight * smoothingFactor

	// Generate a random value in the range [0, totalWeight)
	randomWeight := rand.Intn(totalWeight)

	// Find a channel based on its weight
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return channel, nil
		}
	}
	// return null if no channel is not found
	return nil, errors.New("channel not found")
}

func (r *Runtime) CacheGetChannel(id int) (*Channel, error) {
	if !common.MemoryCacheEnabled && !r.snapshot.readOnly {
		return r.GetChannelById(id, true)
	}
	r.channelSyncLock.RLock()
	defer r.channelSyncLock.RUnlock()

	c, ok := r.channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return c, nil
}

func (r *Runtime) CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	if !common.MemoryCacheEnabled && !r.snapshot.readOnly {
		channel, err := r.GetChannelById(id, true)
		if err != nil {
			return nil, err
		}
		return &channel.ChannelInfo, nil
	}
	r.channelSyncLock.RLock()
	defer r.channelSyncLock.RUnlock()

	c, ok := r.channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return &c.ChannelInfo, nil
}

func (r *Runtime) CacheUpdateChannelStatus(id int, status int) {
	defer r.snapshot.dirty.Store(true)
	if !common.MemoryCacheEnabled && !r.snapshot.readOnly {
		return
	}
	r.channelSyncLock.Lock()
	defer r.channelSyncLock.Unlock()
	if channel, ok := r.channelsIDM[id]; ok {
		channel.Status = status
	}
	if status != common.ChannelStatusEnabled {
		// delete the channel from r.group2model2channels
		for group, model2channels := range r.group2model2channels {
			for model, channels := range model2channels {
				for i, channelId := range channels {
					if channelId == id {
						// remove the channel from the slice
						r.group2model2channels[group][model] = append(channels[:i], channels[i+1:]...)
						break
					}
				}
			}
		}
	}
}

func (r *Runtime) CacheUpdateChannel(channel *Channel) {
	defer r.snapshot.dirty.Store(true)
	if !common.MemoryCacheEnabled && !r.snapshot.readOnly {
		return
	}
	r.channelSyncLock.Lock()
	if channel == nil {
		r.channelSyncLock.Unlock()
		return
	}

	if r.channelsIDM == nil {
		r.channelsIDM = make(map[int]*Channel)
	}
	if _, ok := r.channelsIDM[channel.Id]; ok {
		logger.LogDebug(nil, "CacheUpdateChannel before: id=%d, name=%s, status=%d", channel.Id, channel.Name, channel.Status)
	}
	r.channelsIDM[channel.Id] = channel
	if r.channel2advancedCustomConfig == nil {
		r.channel2advancedCustomConfig = make(map[int]*kitdto.AdvancedCustomConfig)
	}
	delete(r.channel2advancedCustomConfig, channel.Id)
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			r.channel2advancedCustomConfig[channel.Id] = config
		}
	}
	logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d", channel.Id, channel.Name, channel.Status)
	// Notify pricing after releasing the routing lock; a projection refresh may
	// read AdvancedConfigs and must not re-enter the same lock.
	r.channelSyncLock.Unlock()
	r.changed()
}
