package model

import "gorm.io/gorm"

func countLimitedRows(db *gorm.DB, query *gorm.DB, tableModel any, idColumn string, limit int, total *int64) error {
	if limit <= 0 {
		return query.Model(tableModel).Count(total).Error
	}

	limited := query.Session(&gorm.Session{}).Model(tableModel).Select(idColumn).Limit(limit)
	return db.Table("(?) AS limited_rows", limited).Count(total).Error
}
