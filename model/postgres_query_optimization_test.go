package model

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountLimitedRowsCapsSubqueryWithoutMutatingBaseQuery(t *testing.T) {
	truncateTables(t)

	for i := 1; i <= 3; i++ {
		topUp := &TopUp{
			UserId:     901,
			Amount:     int64(i),
			Money:      float64(i),
			TradeNo:    "count-limited-topup-" + strconv.Itoa(i),
			CreateTime: int64(1000 + i),
			Status:     "pending",
		}
		require.NoError(t, DB.Create(topUp).Error)
	}

	query := DB.Model(&TopUp{}).Where("user_id = ?", 901)
	var total int64
	require.NoError(t, countLimitedRows(DB, query, &TopUp{}, "id", 2, &total))
	assert.EqualValues(t, 2, total)

	var topUps []TopUp
	require.NoError(t, query.Order("id DESC").Find(&topUps).Error)
	assert.Len(t, topUps, 3)
}

func TestDeleteOldLogDeletesInPostgresBatches(t *testing.T) {
	truncateTables(t)

	for _, createdAt := range []int64{10, 20, 30, 100} {
		require.NoError(t, LOG_DB.Create(&Log{
			UserId:    902,
			CreatedAt: createdAt,
			Type:      LogTypeSystem,
			Content:   "cleanup test",
		}).Error)
	}

	deleted, err := DeleteOldLog(context.Background(), 50, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, deleted)

	var remaining []Log
	require.NoError(t, LOG_DB.Order("created_at ASC").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.EqualValues(t, 100, remaining[0].CreatedAt)
}
