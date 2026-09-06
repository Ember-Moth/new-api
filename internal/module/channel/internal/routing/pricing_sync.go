package routing

import (
	"context"
)

func (r *Runtime) PricingSyncChannels(ctx context.Context, ids []int) ([]*Channel, error) {
	var channels []*Channel
	query := r.db.WithContext(ctx).Select("id", "name", "base_url", "status", "type").Order("id")
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	}
	err := query.Find(&channels).Error
	return channels, err
}
func (r *Runtime) PricingSyncCredentialChannel(ctx context.Context, id int) (*Channel, error) {
	var channel Channel
	if err := r.db.WithContext(ctx).First(&channel, id).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}
