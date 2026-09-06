package entity

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
)

type SystemInstance struct {
	NodeName   string `json:"node_name" gorm:"type:varchar(128);primaryKey"`
	Info       string `json:"info" gorm:"type:text"`
	StartedAt  int64  `json:"started_at" gorm:"bigint;index"`
	LastSeenAt int64  `json:"last_seen_at" gorm:"bigint;index"`
	CreatedAt  int64  `json:"created_at" gorm:"bigint;index"`
	UpdatedAt  int64  `json:"updated_at" gorm:"bigint;index"`
}

func (instance *SystemInstance) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if instance.CreatedAt == 0 {
		instance.CreatedAt = now
	}
	if instance.UpdatedAt == 0 {
		instance.UpdatedAt = now
	}
	return nil
}
