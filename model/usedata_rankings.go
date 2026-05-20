package model

import (
	"fmt"

	"gorm.io/gorm"
)

type RankingQuotaTotal struct {
	ModelName   string `json:"model_name"`
	TotalTokens int64  `json:"total_tokens"`
}

type RankingQuotaBucket struct {
	ModelName string `json:"model_name"`
	Bucket    int64  `json:"bucket"`
	Tokens    int64  `json:"tokens"`
}

func GetRankingQuotaTotals(startTime int64, endTime int64) ([]RankingQuotaTotal, error) {
	var rows []RankingQuotaTotal
	target := getRankingQuotaReadTarget(startTime, endTime, 0)
	query := DB.Table(target.Table).
		Select("model_name, sum(token_used) as total_tokens").
		Where("model_name <> ''").
		Group("model_name").
		Having("sum(token_used) > 0").
		Order("total_tokens DESC")
	query = applyRankingQuotaTimeRange(query, target.StartTime, target.EndTime)
	err := query.Find(&rows).Error
	if err == nil && len(rows) == 0 && target.Table != "quota_data" {
		query = DB.Table("quota_data").
			Select("model_name, sum(token_used) as total_tokens").
			Where("model_name <> ''").
			Group("model_name").
			Having("sum(token_used) > 0").
			Order("total_tokens DESC")
		query = applyRankingQuotaTimeRange(query, startTime, endTime)
		err = query.Find(&rows).Error
	}
	return rows, err
}

func GetRankingQuotaBuckets(startTime int64, endTime int64, bucketSize int64) ([]RankingQuotaBucket, error) {
	if bucketSize <= 0 {
		bucketSize = 3600
	}
	bucketExpr := rankingBucketExpr(bucketSize)
	var rows []RankingQuotaBucket
	target := getRankingQuotaReadTarget(startTime, endTime, bucketSize)
	query := DB.Table(target.Table).
		Select(fmt.Sprintf("model_name, %s as bucket, sum(token_used) as tokens", bucketExpr)).
		Where("model_name <> ''").
		Group(fmt.Sprintf("model_name, %s", bucketExpr)).
		Having("sum(token_used) > 0").
		Order("bucket ASC")
	query = applyRankingQuotaTimeRange(query, target.StartTime, target.EndTime)
	err := query.Find(&rows).Error
	if err == nil && len(rows) == 0 && target.Table != "quota_data" {
		query = DB.Table("quota_data").
			Select(fmt.Sprintf("model_name, %s as bucket, sum(token_used) as tokens", bucketExpr)).
			Where("model_name <> ''").
			Group(fmt.Sprintf("model_name, %s", bucketExpr)).
			Having("sum(token_used) > 0").
			Order("bucket ASC")
		query = applyRankingQuotaTimeRange(query, startTime, endTime)
		err = query.Find(&rows).Error
	}
	return rows, err
}

func getRankingQuotaReadTarget(startTime int64, endTime int64, bucketSize int64) quotaDataReadTarget {
	tableName := "quota_data"
	rangeSeconds := endTime - startTime
	if bucketSize >= 30*86400 || startTime == 0 {
		tableName = "quota_data_monthly"
	} else if bucketSize >= 86400 || rangeSeconds > 2*86400 {
		tableName = "quota_data_daily"
	}

	target := quotaDataReadTarget{
		Table:     tableName,
		StartTime: startTime,
		EndTime:   endTime,
	}
	switch tableName {
	case "quota_data_daily":
		target.StartTime = quotaDataDayBucket(startTime)
		target.EndTime = quotaDataDayBucket(endTime)
	case "quota_data_monthly":
		target.StartTime = quotaDataMonthBucket(startTime)
		target.EndTime = quotaDataMonthBucket(endTime)
	}
	return target
}

func rankingBucketExpr(bucketSize int64) string {
	return fmt.Sprintf("(created_at / %d) * %d", bucketSize, bucketSize)
}

func applyRankingQuotaTimeRange(query *gorm.DB, startTime int64, endTime int64) *gorm.DB {
	if startTime > 0 {
		query = query.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		query = query.Where("created_at <= ?", endTime)
	}
	return query
}
