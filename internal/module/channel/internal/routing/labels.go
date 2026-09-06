package routing

import (
	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
)

func (r *Runtime) ChannelNames(ctx context.Context, ids []int) (map[int]string, error) {
	result := make(map[int]string, len(ids))
	if common.MemoryCacheEnabled {
		for _, id := range ids {
			if channel, err := r.CacheGetChannel(id); err == nil {
				result[id] = channel.Name
			}
		}
		return result, nil
	}
	var rows []struct {
		Id   int
		Name string
	}
	if err := r.db.WithContext(ctx).Table("channels").Select("id", "name").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.Id] = row.Name
	}
	return result, nil
}
