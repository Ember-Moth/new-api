package billing

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
)

var (
	ErrRedemptionNameLength    = errors.New("redemption name must contain 1 to 20 characters")
	ErrRedemptionCountPositive = errors.New("redemption count must be positive")
	ErrRedemptionCountMax      = errors.New("redemption count cannot exceed 100")
	ErrRedemptionExpired       = errors.New("redemption expiry cannot be in the past")
	ErrRedemptionCreateFailed  = errors.New("failed to create redemption")
)

func (s *Service) ListRedemptions(ctx context.Context, keyword, status string, offset, limit int) ([]*contract.Redemption, int64, error) {
	rows, total, err := s.redemptions.List(ctx, keyword, status, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	result := make([]*contract.Redemption, 0, len(rows))
	for _, row := range rows {
		result = append(result, redemptionResponse(row))
	}
	return result, total, nil
}

func (s *Service) GetRedemption(ctx context.Context, id int) (*contract.Redemption, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	row, err := s.redemptions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return redemptionResponse(row), nil
}

func (s *Service) CreateRedemptions(ctx context.Context, actorID int, request contract.CreateRedemptionsRequest) ([]string, error) {
	if err := s.RequirePaymentCompliance(); err != nil {
		return nil, err
	}
	if length := utf8.RuneCountInString(request.Name); length == 0 || length > 20 {
		return nil, ErrRedemptionNameLength
	}
	if request.Count <= 0 {
		return nil, ErrRedemptionCountPositive
	}
	if request.Count > 100 {
		return nil, ErrRedemptionCountMax
	}
	if request.Quota <= 0 {
		return nil, errors.New("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(request.Quota); err != nil {
		return nil, err
	}
	if request.ExpiredTime != 0 && request.ExpiredTime < common.GetTimestamp() {
		return nil, ErrRedemptionExpired
	}
	var keys []string
	for i := 0; i < request.Count; i++ {
		key := common.GetUUID()
		row := entity.Redemption{UserId: actorID, Name: request.Name, Key: key, CreatedTime: common.GetTimestamp(), Quota: request.Quota, ExpiredTime: request.ExpiredTime}
		if err := s.redemptions.Create(ctx, &row); err != nil {
			common.SysError("failed to insert redemption: " + err.Error())
			return keys, ErrRedemptionCreateFailed
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (s *Service) UpdateRedemption(ctx context.Context, request contract.UpdateRedemptionRequest, statusOnly bool) (*contract.Redemption, error) {
	if request.Id == 0 {
		return nil, errors.New("id 为空！")
	}
	row, err := s.redemptions.Get(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if statusOnly {
		row.Status = request.Status
	} else {
		row.Name, row.Quota, row.ExpiredTime = request.Name, request.Quota, request.ExpiredTime
	}
	if row.Quota <= 0 {
		return nil, errors.New("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(row.Quota); err != nil {
		return nil, err
	}
	if !statusOnly && request.ExpiredTime != 0 && request.ExpiredTime < common.GetTimestamp() {
		return nil, ErrRedemptionExpired
	}
	if err := s.redemptions.Update(ctx, row, statusOnly); err != nil {
		return nil, err
	}
	return redemptionResponse(row), nil
}

func (s *Service) DeleteRedemption(ctx context.Context, id int) error {
	if id == 0 {
		return errors.New("id 为空！")
	}
	return s.redemptions.Delete(ctx, id)
}

func (s *Service) DeleteInvalidRedemptions(ctx context.Context) (int64, error) {
	return s.redemptions.DeleteInvalid(ctx)
}

func redemptionResponse(row *entity.Redemption) *contract.Redemption {
	result := &contract.Redemption{Id: row.Id, UserId: row.UserId, Key: row.Key, Status: row.Status, Name: row.Name, Quota: row.Quota, CreatedTime: row.CreatedTime, RedeemedTime: row.RedeemedTime, Count: row.Count, UsedUserId: row.UsedUserId, ExpiredTime: row.ExpiredTime}
	if row.DeletedAt.Valid {
		result.DeletedAt = &row.DeletedAt.Time
	}
	return result
}
