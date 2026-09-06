package routing

import (
	"context"
	"sort"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

// ListFilter is shared by row, tag, and type-count queries so pagination does
// not silently change the requested group/status/type scope.
type ListFilter struct {
	Group  string
	Status int
	Type   int
}

func (r *Runtime) listQuery(ctx context.Context, filter ListFilter) *gorm.DB {
	query := ApplyChannelGroupFilter(r.db.WithContext(ctx).Model(&Channel{}), filter.Group)
	if filter.Status == common.ChannelStatusEnabled {
		query = query.Where("status = ?", common.ChannelStatusEnabled)
	} else if filter.Status == 0 {
		query = query.Where("status != ?", common.ChannelStatusEnabled)
	}
	if filter.Type >= 0 {
		query = query.Where("type = ?", filter.Type)
	}
	return query
}

func (r *Runtime) ListTags(ctx context.Context, filter ListFilter, offset, limit int) ([]*string, error) {
	if r.cache != nil && !r.snapshot.readOnly && (filter.Status == common.ChannelStatusEnabled || filter.Status == 0) {
		channels, err := r.listEffectiveChannels(ctx, filter, nil, ChannelSortOptions{})
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{})
		for _, channel := range channels {
			if channel.Tag != nil && *channel.Tag != "" {
				seen[*channel.Tag] = struct{}{}
			}
		}
		tags := make([]string, 0, len(seen))
		for tag := range seen {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		if offset < 0 {
			offset = 0
		}
		if offset >= len(tags) {
			return []*string{}, nil
		}
		end := len(tags)
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		result := make([]*string, 0, end-offset)
		for _, tag := range tags[offset:end] {
			value := tag
			result = append(result, &value)
		}
		return result, nil
	}
	return r.GetPaginatedChannelTags(r.listQuery(ctx, filter), offset, limit)
}

func (r *Runtime) CountTags(ctx context.Context, filter ListFilter) (int64, error) {
	if r.cache != nil && !r.snapshot.readOnly && (filter.Status == common.ChannelStatusEnabled || filter.Status == 0) {
		tags, err := r.ListTags(ctx, filter, 0, 0)
		return int64(len(tags)), err
	}
	return r.CountChannelTags(r.listQuery(ctx, filter))
}

func (r *Runtime) ListChannelsForTag(ctx context.Context, filter ListFilter, tag string, sort ChannelSortOptions) ([]*Channel, error) {
	channels, err := r.listEffectiveChannels(ctx, filter, &tag, sort)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		channel.Key = ""
		channel.Keys = nil
	}
	return channels, nil
}

func (r *Runtime) CountChannels(ctx context.Context, filter ListFilter) (int64, error) {
	channels, err := r.listEffectiveChannels(ctx, filter, nil, ChannelSortOptions{})
	return int64(len(channels)), err
}

func (r *Runtime) ListChannels(ctx context.Context, filter ListFilter, offset, limit int, sort ChannelSortOptions) ([]*Channel, error) {
	channels, err := r.listEffectiveChannels(ctx, filter, nil, sort)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(channels) {
		return []*Channel{}, nil
	}
	end := len(channels)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	result := channels[offset:end]
	for _, channel := range result {
		channel.Key = ""
		channel.Keys = nil
	}
	return result, nil
}

func (r *Runtime) listEffectiveChannels(ctx context.Context, filter ListFilter, tag *string, sort ChannelSortOptions) ([]*Channel, error) {
	query := ApplyChannelGroupFilter(r.db.WithContext(ctx).Model(&Channel{}), filter.Group)
	if filter.Status == common.ChannelStatusEnabled {
		query = query.Where("status = ?", common.ChannelStatusEnabled)
	} else if filter.Status == 0 && r.cache == nil {
		query = query.Where("status != ?", common.ChannelStatusEnabled)
	}
	if filter.Type >= 0 {
		query = query.Where("type = ?", filter.Type)
	}
	if tag != nil {
		query = query.Where("tag = ?", *tag)
	}
	var channels []*Channel
	if err := sort.Apply(query).Find(&channels).Error; err != nil {
		return nil, err
	}
	if err := r.applyRuntimeStateToChannels(channels, true); err != nil {
		return nil, err
	}
	if filter.Status == common.ChannelStatusEnabled || filter.Status == 0 {
		filtered := channels[:0]
		for _, channel := range channels {
			if filter.Status == common.ChannelStatusEnabled && channel.Status != common.ChannelStatusEnabled {
				continue
			}
			if filter.Status == 0 && channel.Status == common.ChannelStatusEnabled {
				continue
			}
			filtered = append(filtered, channel)
		}
		channels = filtered
	}
	return channels, nil
}

func (r *Runtime) ChannelTypeCounts(ctx context.Context, filter ListFilter) (map[int64]int64, error) {
	if r.cache != nil && !r.snapshot.readOnly && (filter.Status == common.ChannelStatusEnabled || filter.Status == 0) {
		channels, err := r.listEffectiveChannels(ctx, filter, nil, ChannelSortOptions{})
		if err != nil {
			return nil, err
		}
		result := make(map[int64]int64)
		for _, channel := range channels {
			result[int64(channel.Type)]++
		}
		return result, nil
	}
	var counts []struct{ Type, Count int64 }
	err := r.listQuery(ctx, filter).Select("type, count(*) as count").Group("type").Find(&counts).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int64]int64, len(counts))
	for _, count := range counts {
		result[count.Type] = count.Count
	}
	return result, nil
}
