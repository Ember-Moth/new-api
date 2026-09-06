package repo

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"gorm.io/gorm"
)

type Statements struct{ db *gorm.DB }

func NewStatements(db *gorm.DB) *Statements { return &Statements{db: db} }
func (r *Statements) Token(ctx context.Context, userID, tokenID int) (*entity.Token, error) {
	var token entity.Token
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}
func (r *Statements) TokenByKey(ctx context.Context, key string) (*entity.Token, error) {
	return tokencache.New(r.db.WithContext(ctx)).GetByKey(key, false)
}
func (r *Statements) UserBalances(ctx context.Context, id int) (int, int, error) {
	user, err := usercache.New(r.db.WithContext(ctx)).GetUserCache(id)
	if err != nil {
		return 0, 0, err
	}
	used, err := r.UserUsedQuota(ctx, id)
	return user.Quota, used, err
}
func (r *Statements) UserUsedQuota(ctx context.Context, id int) (int, error) {
	var user struct{ UsedQuota int }
	err := r.db.WithContext(ctx).Model(&entity.User{}).Select("used_quota").Where("id = ?", id).Take(&user).Error
	return user.UsedQuota, err
}
