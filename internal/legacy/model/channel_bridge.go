package model

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/internal/shared/common"

	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/shared/dto"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"gorm.io/gorm"
)

// This adapter is temporary while callers move to injected channel services.
// Channel persistence, selection, caches and key state belong to the module.
type Channel = channelmodule.Channel
type ChannelInfo = channelmodule.ChannelInfo
type Ability = channelmodule.Ability
type AbilityWithChannel = channelmodule.AbilityWithChannel
type ChannelSortOptions = channelmodule.ChannelSortOptions
type TaskAliasTarget = channelmodule.TaskAliasTarget

var channelServicesMu sync.Mutex
var channelServices = make(map[*gorm.DB]*channelmodule.Service)

func ChannelService() *channelmodule.Service {
	channelServicesMu.Lock()
	defer channelServicesMu.Unlock()
	if service := channelServices[DB]; service != nil {
		return service
	}
	service := channelmodule.New(channelmodule.Dependencies{DB: DB, RoutingChanged: InvalidatePricingCache, QueueUsedQuota: queueChannelUsedQuota})
	channelServices[DB] = service
	return service
}

// ConfigureChannelService binds legacy callers to the application-owned instance.
func ConfigureChannelService(service *channelmodule.Service) {
	channelServicesMu.Lock()
	defer channelServicesMu.Unlock()
	channelServices[DB] = service
}

func queueChannelUsedQuota(ctx context.Context, id, quota int) error {
	return AccountingStore().RecordChannelUsage(ctx, id, quota)
}

func ChannelDependencies() channelmodule.Dependencies {
	return channelmodule.Dependencies{DB: DB, RoutingChanged: InvalidatePricingCache, QueueUsedQuota: queueChannelUsedQuota}
}

func NewChannelSortOptions(sortBy string, sortOrder string, idSort bool) ChannelSortOptions {
	return channelmodule.NewChannelSortOptions(sortBy, sortOrder, idSort)
}

func NormalizeChannelGroupFilter(group string) string {
	return channelmodule.NormalizeChannelGroupFilter(group)
}

func ApplyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	return channelmodule.ApplyChannelGroupFilter(query, group)
}

func GetAllChannels(startIdx int, num int, selectAll bool, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	return ChannelService().GetAllChannels(startIdx, num, selectAll, idSort, sortOptions...)
}

func GetChannelsByTag(tag string, idSort bool, selectAll bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	return ChannelService().GetChannelsByTag(tag, idSort, selectAll, sortOptions...)
}

func SearchChannels(keyword string, group string, model string, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	return ChannelService().SearchChannels(keyword, group, model, idSort, sortOptions...)
}

func GetChannelById(id int, selectAll bool) (*Channel, error) {
	return ChannelService().GetChannelById(id, selectAll)
}

func BatchInsertChannels(channels []Channel) error {
	return ChannelService().BatchInsertChannels(channels)
}

func BatchDeleteChannels(ids []int) (int64, error) { return ChannelService().BatchDeleteChannels(ids) }

func GetChannelKeyLock(channelId int) *sync.Mutex {
	return ChannelService().GetChannelKeyLock(channelId)
}

func CleanupChannelKeyLocks() { ChannelService().CleanupChannelKeyLocks() }

func UpdateChannelStatus(channelId int, usingKey string, status int, reason string) bool {
	return ChannelService().UpdateChannelStatus(channelId, usingKey, status, reason)
}

func EnableChannelByTag(tag string) error { return ChannelService().EnableChannelByTag(tag) }

func DisableChannelByTag(tag string) error { return ChannelService().DisableChannelByTag(tag) }

func EditChannelByTag(tag string, newTag *string, modelMapping *string, models *StringList, group *StringList, priority *int64, weight *uint, paramOverride *string, headerOverride *string) error {
	return ChannelService().EditChannelByTag(tag, newTag, modelMapping, models, group, priority, weight, paramOverride, headerOverride)
}

func UpdateChannelUsedQuota(id int, quota int) {
	if err := ChannelService().UpdateChannelUsedQuota(context.Background(), id, quota); err != nil {
		common.SysError("failed to record channel usage: " + err.Error())
	}
}

func DeleteChannelByStatus(status int64) (int64, error) {
	return ChannelService().DeleteChannelByStatus(status)
}

func DeleteDisabledChannel() (int64, error) { return ChannelService().DeleteDisabledChannel() }

func GetPaginatedTags(offset int, limit int) ([]*string, error) {
	return ChannelService().GetPaginatedTags(offset, limit)
}

func GetPaginatedChannelTags(query *gorm.DB, offset int, limit int) ([]*string, error) {
	return ChannelService().GetPaginatedChannelTags(query, offset, limit)
}

func SearchTags(keyword string, group string, model string, idSort bool) ([]*string, error) {
	return ChannelService().SearchTags(keyword, group, model, idSort)
}

func GetChannelsByIds(ids []int) ([]*Channel, error) { return ChannelService().GetChannelsByIds(ids) }

func BatchSetChannelTag(ids []int, tag *string) error {
	return ChannelService().BatchSetChannelTag(ids, tag)
}

func CountAllChannels() (int64, error) { return ChannelService().CountAllChannels() }

func CountAllTags() (int64, error) { return ChannelService().CountAllTags() }

func CountChannelTags(query *gorm.DB) (int64, error) { return ChannelService().CountChannelTags(query) }

func GetChannelsByType(startIdx int, num int, idSort bool, channelType int) ([]*Channel, error) {
	return ChannelService().GetChannelsByType(startIdx, num, idSort, channelType)
}

func CountChannelsByType(channelType int) (int64, error) {
	return ChannelService().CountChannelsByType(channelType)
}

func CountChannelsGroupByType() (map[int64]int64, error) {
	return ChannelService().CountChannelsGroupByType()
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	return ChannelService().GetAllEnableAbilityWithChannels()
}

func GetGroupEnabledModels(group string) []string {
	return ChannelService().GetGroupEnabledModels(group)
}

func GetEnabledModels() []string { return ChannelService().GetEnabledModels() }

func GetAllEnableAbilities() []Ability { return ChannelService().GetAllEnableAbilities() }

func GetChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	return ChannelService().GetChannel(group, model, retry, filters)
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return ChannelService().UpdateAbilityStatus(channelId, status)
}

func UpdateAbilityStatusByTag(tag string, status bool) error {
	return ChannelService().UpdateAbilityStatusByTag(tag, status)
}

func UpdateAbilityByTag(tag string, newTag *string, priority *int64, weight *uint, tx *gorm.DB) error {
	return ChannelService().UpdateAbilityByTag(tag, newTag, priority, weight, tx)
}

func FixAbility() (int, int, error) { return ChannelService().FixAbility() }

func InitChannelCache() { ChannelService().InitChannelCache() }

func GetRandomSatisfiedChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	return ChannelService().GetRandomSatisfiedChannel(group, model, retry, filters)
}

func CacheGetChannel(id int) (*Channel, error) { return ChannelService().CacheGetChannel(id) }

func CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	return ChannelService().CacheGetChannelInfo(id)
}

func CacheUpdateChannelStatus(id int, status int) {
	ChannelService().CacheUpdateChannelStatus(id, status)
}

func CacheUpdateChannel(channel *Channel) { ChannelService().CacheUpdateChannel(channel) }

func ChannelSatisfiesFilters(ch *Channel, modelName string, filters []dto.ChannelFilter) (bool, dto.ChannelFilterKind) {
	return channelmodule.ChannelSatisfiesFilters(ch, modelName, filters)
}

func IsChannelEnabledForGroupModel(group string, modelName string, channelID int) bool {
	return ChannelService().IsChannelEnabledForGroupModel(group, modelName, channelID)
}

func IsChannelEnabledForAnyGroupModel(groups []string, modelName string, channelID int) bool {
	return ChannelService().IsChannelEnabledForAnyGroupModel(groups, modelName, channelID)
}

func ResolveTaskModelAlias(g *jsplugin.RoutingGeneration, name string) (TaskAliasTarget, bool) {
	return ChannelService().ResolveTaskModelAlias(g, name)
}
