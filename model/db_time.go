package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// GetDBTimestamp returns a UNIX timestamp from database time.
// Falls back to application time on error.
func GetDBTimestamp() int64 {
	return getDBTimestampTx(nil)
}

func getDBTimestampTx(tx *gorm.DB) int64 {
	db := DB
	if tx != nil {
		db = tx
	}
	if db == nil {
		return common.GetTimestamp()
	}
	var ts int64
	var err error
	err = db.Raw("SELECT EXTRACT(EPOCH FROM NOW())::bigint").Scan(&ts).Error
	if err != nil || ts <= 0 {
		return common.GetTimestamp()
	}
	return ts
}
