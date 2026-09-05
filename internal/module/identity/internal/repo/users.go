package repo

import (
	"context"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Users struct{ db *gorm.DB }

func NewUsers(db *gorm.DB) *Users { return &Users{db: db} }

func (r *Users) Transaction(ctx context.Context, action func(*Users, *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return action(NewUsers(tx), tx) })
}

func (r *Users) Get(ctx context.Context, id int, includeDeleted bool) (*entity.User, error) {
	query := r.db.WithContext(ctx)
	if includeDeleted {
		query = query.Unscoped()
	}
	user := &entity.User{}
	err := query.Omit("password", "access_token").First(user, "id = ?", id).Error
	return user, err
}

func (r *Users) Lock(id int, includeDeleted bool) (*entity.User, error) {
	query := r.db.Clauses(clause.Locking{Strength: "UPDATE"})
	if includeDeleted {
		query = query.Unscoped()
	}
	user := &entity.User{}
	err := query.First(user, "id = ?", id).Error
	return user, err
}

func (r *Users) List(ctx context.Context, filter contract.UserFilter) ([]*entity.User, int64, error) {
	var users []*entity.User
	var total int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Unscoped().Model(&entity.User{})
		if filter.Search {
			condition := "username LIKE ? OR email LIKE ? OR display_name LIKE ?"
			pattern := "%" + filter.Keyword + "%"
			args := []any{pattern, pattern, pattern}
			if id, err := strconv.Atoi(filter.Keyword); err == nil {
				condition = "id = ? OR " + condition
				args = append([]any{id}, args...)
			}
			query = query.Where("("+condition+")", args...)
			if filter.Group != "" {
				query = query.Where(`"group" = ?`, filter.Group)
			}
			if filter.Role != nil {
				query = query.Where("role = ?", *filter.Role)
			}
			if filter.Status != nil {
				if *filter.Status == -1 {
					query = query.Where("deleted_at IS NOT NULL")
				} else {
					query = query.Where("deleted_at IS NULL AND status = ?", *filter.Status)
				}
			}
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		column := strings.ToLower(strings.TrimSpace(filter.SortBy))
		order := strings.ToLower(strings.TrimSpace(filter.SortOrder))
		switch column {
		case "id", "username", "quota", "group", "created_at", "last_login_at":
		default:
			column = "id"
			order = "desc"
		}
		query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: column}, Desc: order != "asc"})
		if column != "id" {
			query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true})
		}
		return query.Omit("password", "access_token").Limit(filter.Limit).Offset(filter.Offset).Find(&users).Error
	})
	return users, total, err
}

func (r *Users) Create(user *entity.User) error { return r.db.Create(user).Error }

func (r *Users) Update(user *entity.User, fields map[string]any) error {
	result := r.db.Model(user).Clauses(clause.Returning{}).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Users) Delete(user *entity.User, hard bool) error {
	query := r.db
	if hard {
		query = query.Unscoped()
	}
	result := query.Delete(user)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Users) TokenKeys(userID int) ([]string, error) {
	var keys []string
	err := r.db.Unscoped().Model(&entity.Token{}).Where("user_id = ?", userID).Pluck(`"key"`, &keys).Error
	return keys, err
}
