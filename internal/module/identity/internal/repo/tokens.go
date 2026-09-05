package repo

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Tokens struct {
	db         *gorm.DB
	invalidate func(string) error
}

func NewTokens(db *gorm.DB, invalidate func(string) error) *Tokens {
	return &Tokens{db: db, invalidate: invalidate}
}

func (r *Tokens) Count(ctx context.Context, userID int) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.Token{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func (r *Tokens) List(ctx context.Context, userID, offset, limit int, namePattern, keyPattern string) ([]*entity.Token, int64, error) {
	query := r.db.WithContext(ctx).Model(&entity.Token{}).Where("user_id = ?", userID)
	if namePattern != "" {
		query = query.Where("name LIKE ? ESCAPE '!'", namePattern)
	}
	if keyPattern != "" {
		query = query.Where(`"key" LIKE ? ESCAPE '!'`, keyPattern)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*entity.Token
	err := query.Order("id desc").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (r *Tokens) Get(ctx context.Context, id, userID int) (*entity.Token, error) {
	row := &entity.Token{}
	err := r.db.WithContext(ctx).First(row, "id = ? AND user_id = ?", id, userID).Error
	return row, err
}

func (r *Tokens) Keys(ctx context.Context, ids []int, userID int) ([]entity.Token, error) {
	var rows []entity.Token
	err := r.db.WithContext(ctx).Select("id", `"key"`).Where("id IN ? AND user_id = ?", ids, userID).Find(&rows).Error
	return rows, err
}

func (r *Tokens) Create(ctx context.Context, token *entity.Token) error {
	return r.db.WithContext(ctx).Create(token).Error
}

func (r *Tokens) Update(ctx context.Context, token *entity.Token, statusOnly, updateAutoGroups bool) error {
	changes := map[string]any{"status": token.Status}
	if !statusOnly {
		if token.ModelLimits == nil {
			token.ModelLimits = []string{}
		}
		changes = map[string]any{
			"name": token.Name, "expired_time": token.ExpiredTime, "remain_quota": token.RemainQuota,
			"unlimited_quota": token.UnlimitedQuota, "model_limits_enabled": token.ModelLimitsEnabled,
			"model_limits": token.ModelLimits, "allow_ips": token.AllowIps, "group": token.Group, "cross_group_retry": token.CrossGroupRetry,
		}
		if updateAutoGroups {
			changes["auto_groups"] = token.AutoGroups
		}
	}
	r.invalidateBeforeMutation(token.Key)
	result := r.db.WithContext(ctx).Model(token).Where("user_id = ?", token.UserId).Clauses(clause.Returning{}).Updates(changes)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Tokens) Delete(ctx context.Context, token *entity.Token) error {
	r.invalidateBeforeMutation(token.Key)
	result := r.db.WithContext(ctx).Where("user_id = ?", token.UserId).Delete(token)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Tokens) DeleteBatch(ctx context.Context, ids []int, userID int) (int, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tokens []entity.Token
		if err := tx.Where("user_id = ? AND id IN ?", userID, ids).Find(&tokens).Error; err != nil {
			return err
		}
		for _, token := range tokens {
			r.invalidateBeforeMutation(token.Key)
		}
		result := tx.Where("user_id = ? AND id IN ?", userID, ids).Delete(&entity.Token{})
		count = result.RowsAffected
		return result.Error
	})
	return int(count), err
}

func (r *Tokens) invalidateBeforeMutation(key string) {
	if r.invalidate == nil {
		return
	}
	if err := r.invalidate(key); err != nil {
		common.SysLog("failed to invalidate token cache before mutation: " + err.Error())
	}
}
