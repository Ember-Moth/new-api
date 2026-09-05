package model

import (
	"context"

	"github.com/QuantumNous/new-api/internal/module/usage/contract"
)

type RankingQuotaTotal = contract.RankingQuotaTotal
type RankingQuotaBucket = contract.RankingQuotaBucket

func GetRankingQuotaTotals(startTime, endTime int64) ([]RankingQuotaTotal, error) {
	return QuotaDataStore().GetRankingQuotaTotals(context.Background(), startTime, endTime)
}
func GetRankingQuotaBuckets(startTime, endTime, bucketSize int64) ([]RankingQuotaBucket, error) {
	return QuotaDataStore().GetRankingQuotaBuckets(context.Background(), startTime, endTime, bucketSize)
}
