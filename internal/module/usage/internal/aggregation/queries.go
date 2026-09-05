package aggregation

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/usage/entity"
)

type QuotaData = entity.QuotaData

func (s *Store) GetQuotaDataByUsername(ctx context.Context, username string, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	err = s.db.WithContext(ctx).Table("quota_data").
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("username = ? and created_at >= ? and created_at <= ?", username, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func (s *Store) GetQuotaDataByUserId(ctx context.Context, userId int, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	err = s.db.WithContext(ctx).Table("quota_data").
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ? and created_at >= ? and created_at <= ?", userId, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func (s *Store) GetQuotaDataGroupByUser(ctx context.Context, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	err = s.db.WithContext(ctx).Table("quota_data").
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("username, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func (s *Store) GetAllQuotaDates(ctx context.Context, startTime int64, endTime int64, username string) (quotaData []*QuotaData, err error) {
	if username != "" {
		return s.GetQuotaDataByUsername(ctx, username, startTime, endTime)
	}
	var quotaDatas []*QuotaData
	err = s.db.WithContext(ctx).Table("quota_data").Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, created_at").Where("created_at >= ? and created_at <= ?", startTime, endTime).Group("model_name, created_at").Find(&quotaDatas).Error
	return quotaDatas, err
}
