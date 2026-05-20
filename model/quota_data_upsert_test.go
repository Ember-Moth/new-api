package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogQuotaDataUsesPostgresUpsert(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&QuotaData{
		UserID:    1001,
		Username:  "quota-user",
		ModelName: "quota-model",
		CreatedAt: 3600,
		Count:     2,
		Quota:     200,
		TokenUsed: 20,
	}).Error)

	LogQuotaData(1001, "quota-user", "quota-model", 50, 3610, 5)
	LogQuotaData(1001, "quota-user", "quota-model", 70, 3620, 7)

	var row QuotaData
	require.NoError(t, DB.Where("user_id = ? AND username = ? AND model_name = ? AND created_at = ?",
		1001, "quota-user", "quota-model", int64(3600)).
		First(&row).Error)
	assert.Equal(t, 4, row.Count)
	assert.Equal(t, 320, row.Quota)
	assert.Equal(t, 32, row.TokenUsed)

	var count int64
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 1001).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
