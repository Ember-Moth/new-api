package channel

import (
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/routing"
	"github.com/QuantumNous/new-api/internal/shared/dto"
	"gorm.io/gorm"
)

type Channel = entity.Channel
type ChannelInfo = entity.ChannelInfo
type Ability = entity.Ability
type AbilityWithChannel = entity.AbilityWithChannel
type ChannelSortOptions = routing.ChannelSortOptions
type TaskAliasTarget = routing.TaskAliasTarget
type ListFilter = routing.ListFilter

func NewChannelSortOptions(sortBy string, sortOrder string, idSort bool) ChannelSortOptions {
	return routing.NewChannelSortOptions(sortBy, sortOrder, idSort)
}
func NormalizeChannelGroupFilter(group string) string {
	return routing.NormalizeChannelGroupFilter(group)
}
func ApplyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	return routing.ApplyChannelGroupFilter(query, group)
}
func ChannelSatisfiesFilters(ch *Channel, modelName string, filters []dto.ChannelFilter) (bool, dto.ChannelFilterKind) {
	return routing.ChannelSatisfiesFilters(ch, modelName, filters)
}

func ChannelKeyPoolFingerprint(ch *Channel) string {
	return routing.ChannelKeyPoolFingerprint(ch)
}
