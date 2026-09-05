package routing

import (
	"context"

	"github.com/QuantumNous/new-api/common"
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
	return r.GetPaginatedChannelTags(r.listQuery(ctx, filter), offset, limit)
}

func (r *Runtime) CountTags(ctx context.Context, filter ListFilter) (int64, error) {
	return r.CountChannelTags(r.listQuery(ctx, filter))
}

func (r *Runtime) ListChannelsForTag(ctx context.Context, filter ListFilter, tag string, sort ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	err := sort.Apply(r.listQuery(ctx, filter).Where("tag = ?", tag)).Omit("key").Find(&channels).Error
	return channels, err
}

func (r *Runtime) CountChannels(ctx context.Context, filter ListFilter) (int64, error) {
	var total int64
	err := r.listQuery(ctx, filter).Count(&total).Error
	return total, err
}

func (r *Runtime) ListChannels(ctx context.Context, filter ListFilter, offset, limit int, sort ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	err := sort.Apply(r.listQuery(ctx, filter)).Limit(limit).Offset(offset).Omit("key").Find(&channels).Error
	return channels, err
}

func (r *Runtime) ChannelTypeCounts(ctx context.Context, filter ListFilter) (map[int64]int64, error) {
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
