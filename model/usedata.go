package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QuotaData 柱状图数据
type QuotaData struct {
	Id        int    `json:"id"`
	UserID    int    `json:"user_id" gorm:"index"`
	Username  string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:''"`
	ModelName string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index:idx_qdt_created_at,priority:2"`
	TokenUsed int    `json:"token_used" gorm:"default:0"`
	Count     int    `json:"count" gorm:"default:0"`
	Quota     int    `json:"quota" gorm:"default:0"`
}

type QuotaDataDaily QuotaData

func (QuotaDataDaily) TableName() string {
	return "quota_data_daily"
}

type QuotaDataMonthly QuotaData

func (QuotaDataMonthly) TableName() string {
	return "quota_data_monthly"
}

func LogQuotaData(userId int, username string, modelName string, quota int, createdAt int64, tokenUsed int) {
	// 只精确到小时
	createdAt = createdAt - (createdAt % 3600)

	quotaData := &QuotaData{
		UserID:    userId,
		Username:  username,
		ModelName: modelName,
		CreatedAt: createdAt,
		Count:     1,
		Quota:     quota,
		TokenUsed: tokenUsed,
	}
	if err := upsertQuotaDataRows([]*QuotaData{quotaData}); err != nil {
		common.SysLog("保存数据看板数据失败: " + err.Error())
	}
}

const quotaDataUpsertBatchSize = 500

func upsertQuotaDataRows(quotaDatas []*QuotaData) error {
	return upsertQuotaDataRowsToTable("quota_data", quotaDatas)
}

func upsertQuotaDataRowsToTable(tableName string, quotaDatas []*QuotaData) error {
	if len(quotaDatas) == 0 {
		return nil
	}

	quotedTable := quotePostgresIdentifier(tableName)
	return DB.Table(tableName).Session(&gorm.Session{SkipDefaultTransaction: true}).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "username"},
			{Name: "model_name"},
			{Name: "created_at"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"count":      gorm.Expr(fmt.Sprintf(`%s."count" + EXCLUDED."count"`, quotedTable)),
			"quota":      gorm.Expr(fmt.Sprintf(`%s.quota + EXCLUDED.quota`, quotedTable)),
			"token_used": gorm.Expr(fmt.Sprintf(`%s.token_used + EXCLUDED.token_used`, quotedTable)),
		}),
	}).CreateInBatches(quotaDatas, quotaDataUpsertBatchSize).Error
}

func GetQuotaDataByUsername(username string, startTime int64, endTime int64, defaultTime string) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	target := getQuotaDataReadTarget(defaultTime, startTime, endTime)
	err = queryQuotaDataByUsername(target.Table, username, target.StartTime, target.EndTime, &quotaDatas)
	if err == nil && len(quotaDatas) == 0 && target.Table != "quota_data" {
		err = queryQuotaDataByUsername("quota_data", username, startTime, endTime, &quotaDatas)
	}
	return quotaDatas, err
}

func queryQuotaDataByUsername(tableName string, username string, startTime int64, endTime int64, quotaDatas *[]*QuotaData) error {
	return applyQuotaDataTimeRange(DB.Table(tableName).Where("username = ?", username), startTime, endTime).Find(quotaDatas).Error
}

func GetQuotaDataByUserId(userId int, startTime int64, endTime int64, defaultTime string) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	target := getQuotaDataReadTarget(defaultTime, startTime, endTime)
	err = queryQuotaDataByUserId(target.Table, userId, target.StartTime, target.EndTime, &quotaDatas)
	if err == nil && len(quotaDatas) == 0 && target.Table != "quota_data" {
		err = queryQuotaDataByUserId("quota_data", userId, startTime, endTime, &quotaDatas)
	}
	return quotaDatas, err
}

func queryQuotaDataByUserId(tableName string, userId int, startTime int64, endTime int64, quotaDatas *[]*QuotaData) error {
	return applyQuotaDataTimeRange(DB.Table(tableName).Where("user_id = ?", userId), startTime, endTime).Find(quotaDatas).Error
}

func GetQuotaDataGroupByUser(startTime int64, endTime int64, defaultTime string) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	target := getQuotaDataReadTarget(defaultTime, startTime, endTime)
	err = queryQuotaDataGroupByUser(target.Table, target.StartTime, target.EndTime, &quotaDatas)
	if err == nil && len(quotaDatas) == 0 && target.Table != "quota_data" {
		err = queryQuotaDataGroupByUser("quota_data", startTime, endTime, &quotaDatas)
	}
	return quotaDatas, err
}

func queryQuotaDataGroupByUser(tableName string, startTime int64, endTime int64, quotaDatas *[]*QuotaData) error {
	return applyQuotaDataTimeRange(DB.Table(tableName), startTime, endTime).
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Group("username, created_at").
		Find(quotaDatas).Error
}

func GetAllQuotaDates(startTime int64, endTime int64, username string, defaultTime string) (quotaData []*QuotaData, err error) {
	if username != "" {
		return GetQuotaDataByUsername(username, startTime, endTime, defaultTime)
	}
	var quotaDatas []*QuotaData
	target := getQuotaDataReadTarget(defaultTime, startTime, endTime)
	err = queryQuotaDataGroupByModel(target.Table, target.StartTime, target.EndTime, &quotaDatas)
	if err == nil && len(quotaDatas) == 0 && target.Table != "quota_data" {
		err = queryQuotaDataGroupByModel("quota_data", startTime, endTime, &quotaDatas)
	}
	return quotaDatas, err
}

func queryQuotaDataGroupByModel(tableName string, startTime int64, endTime int64, quotaDatas *[]*QuotaData) error {
	return applyQuotaDataTimeRange(DB.Table(tableName), startTime, endTime).
		Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, created_at").
		Group("model_name, created_at").
		Find(quotaDatas).Error
}

type quotaDataReadTarget struct {
	Table     string
	StartTime int64
	EndTime   int64
}

func getQuotaDataReadTarget(defaultTime string, startTime int64, endTime int64) quotaDataReadTarget {
	granularity := normalizeQuotaDataGranularity(defaultTime)
	target := quotaDataReadTarget{
		Table:     "quota_data",
		StartTime: startTime,
		EndTime:   endTime,
	}
	switch granularity {
	case "day", "week":
		target.Table = "quota_data_daily"
		target.StartTime = quotaDataDayBucket(startTime)
		target.EndTime = quotaDataDayBucket(endTime)
	case "month":
		target.Table = "quota_data_monthly"
		target.StartTime = quotaDataMonthBucket(startTime)
		target.EndTime = quotaDataMonthBucket(endTime)
	}
	return target
}

func normalizeQuotaDataGranularity(defaultTime string) string {
	value := strings.ToLower(strings.TrimSpace(defaultTime))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(common.DataExportDefaultTime))
	}
	switch value {
	case "day", "week", "month":
		return value
	default:
		return "hour"
	}
}

func applyQuotaDataTimeRange(query *gorm.DB, startTime int64, endTime int64) *gorm.DB {
	if startTime > 0 {
		query = query.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		query = query.Where("created_at <= ?", endTime)
	}
	return query
}

func quotaDataDayBucket(timestamp int64) int64 {
	if timestamp <= 0 {
		return timestamp
	}
	return timestamp - timestamp%86400
}

func quotaDataMonthBucket(timestamp int64) int64 {
	if timestamp <= 0 {
		return timestamp
	}
	value := time.Unix(timestamp, 0).UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}
