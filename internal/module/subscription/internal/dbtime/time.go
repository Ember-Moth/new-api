package dbtime

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

func Timestamp(db *gorm.DB) int64 {
	var now int64
	if err := db.Raw("SELECT EXTRACT(EPOCH FROM NOW())::bigint").Scan(&now).Error; err != nil || now <= 0 {
		return common.GetTimestamp()
	}
	return now
}
