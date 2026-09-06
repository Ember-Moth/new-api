package aggregation

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

type FlowQuotaData = contract.FlowQuotaData

func (s *Store) GetFlowQuotaData(ctx context.Context, startTime int64, endTime int64, username string, userID int, role int) ([]*FlowQuotaData, error) {
	switch {
	case role >= common.RoleRootUser:
		return s.getRootFlowQuotaData(ctx, startTime, endTime, username)
	case role >= common.RoleAdminUser:
		return s.getAdminFlowQuotaData(ctx, startTime, endTime, username)
	default:
		return s.getSelfFlowQuotaData(ctx, startTime, endTime, userID)
	}
}

func (s *Store) flowQuotaBaseQuery(ctx context.Context, startTime int64, endTime int64) *gorm.DB {
	query := s.db.WithContext(ctx).Table("quota_data").
		Where("use_group <> ''").
		Where("created_at >= ? and created_at <= ?", startTime, endTime)
	return query
}

func (s *Store) getSelfFlowQuotaData(ctx context.Context, startTime int64, endTime int64, userID int) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	err := s.flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("token_id, use_group, model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ?", userID).
		Group("token_id, use_group, model_name").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, s.fillFlowTokenNames(ctx, rows)
}

func (s *Store) getAdminFlowQuotaData(ctx context.Context, startTime int64, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := s.flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("user_id, username, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, s.fillFlowChannelNames(ctx, rows)
}

func (s *Store) getRootFlowQuotaData(ctx context.Context, startTime int64, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := s.flowQuotaBaseQuery(ctx, startTime, endTime).
		Select("user_id, username, node_name, token_id, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, node_name, token_id, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if err := s.fillFlowTokenNames(ctx, rows); err != nil {
		return rows, err
	}
	return rows, s.fillFlowChannelNames(ctx, rows)
}

func (s *Store) fillFlowTokenNames(ctx context.Context, rows []*FlowQuotaData) error {
	tokenIDSet := make(map[int]struct{})
	tokenIDs := make([]int, 0)
	for _, row := range rows {
		if row.TokenID == 0 {
			continue
		}
		if _, ok := tokenIDSet[row.TokenID]; ok {
			continue
		}
		tokenIDSet[row.TokenID] = struct{}{}
		tokenIDs = append(tokenIDs, row.TokenID)
	}
	if len(tokenIDs) == 0 {
		return nil
	}

	tokenNameByID, err := s.tokenNames(ctx, tokenIDs)
	if err != nil {
		return err
	}
	// Deleted tokens are intentionally not resolved here: leave TokenName empty
	// so the frontend can render a localized "deleted (id)" label instead.
	for _, row := range rows {
		if name := tokenNameByID[row.TokenID]; name != "" {
			row.TokenName = name
		}
	}
	return nil
}

func (s *Store) fillFlowChannelNames(ctx context.Context, rows []*FlowQuotaData) error {
	channelIDSet := make(map[int]struct{})
	channelIDs := make([]int, 0)
	for _, row := range rows {
		if row.ChannelID == 0 {
			continue
		}
		if _, ok := channelIDSet[row.ChannelID]; ok {
			continue
		}
		channelIDSet[row.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, row.ChannelID)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	channelNameByID, err := s.channelNames(ctx, channelIDs)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if name := channelNameByID[row.ChannelID]; name != "" {
			row.ChannelName = name
			continue
		}
		if row.ChannelID > 0 {
			row.ChannelName = fmt.Sprintf("channel-%d", row.ChannelID)
		}
	}
	return nil
}
