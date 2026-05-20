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

	var daily QuotaData
	require.NoError(t, DB.Table("quota_data_daily").
		Where("user_id = ? AND username = ? AND model_name = ? AND created_at = ?",
			1001, "quota-user", "quota-model", int64(0)).
		First(&daily).Error)
	assert.Equal(t, row.Count, daily.Count)
	assert.Equal(t, row.Quota, daily.Quota)
	assert.Equal(t, row.TokenUsed, daily.TokenUsed)

	var monthly QuotaData
	require.NoError(t, DB.Table("quota_data_monthly").
		Where("user_id = ? AND username = ? AND model_name = ? AND created_at = ?",
			1001, "quota-user", "quota-model", int64(0)).
		First(&monthly).Error)
	assert.Equal(t, row.Count, monthly.Count)
	assert.Equal(t, row.Quota, monthly.Quota)
	assert.Equal(t, row.TokenUsed, monthly.TokenUsed)

	dailyRows, err := GetQuotaDataByUserId(1001, 3600, 3620, "day")
	require.NoError(t, err)
	require.Len(t, dailyRows, 1)
	assert.Equal(t, row.Quota, dailyRows[0].Quota)

	modelRows, err := GetAllQuotaDates(3600, 3620, "", "month")
	require.NoError(t, err)
	require.Len(t, modelRows, 1)
	assert.Equal(t, row.Quota, modelRows[0].Quota)
}
