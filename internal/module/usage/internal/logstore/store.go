package logs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	dbquery "github.com/QuantumNous/new-api/internal/infra/database/query"
	"github.com/QuantumNous/new-api/internal/module/usage/entity"
	"github.com/QuantumNous/new-api/internal/module/usage/metadata"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

type Log = entity.Log

const (
	LogTypeUnknown = entity.LogTypeUnknown
	LogTypeTopup   = entity.LogTypeTopup
	LogTypeConsume = entity.LogTypeConsume
	LogTypeManage  = entity.LogTypeManage
	LogTypeSystem  = entity.LogTypeSystem
	LogTypeError   = entity.LogTypeError
	LogTypeRefund  = entity.LogTypeRefund
	LogTypeLogin   = entity.LogTypeLogin
)

type Stat struct {
	Quota int `json:"quota"`
	Rpm   int `json:"rpm"`
	Tpm   int `json:"tpm"`
}

const logSearchCountLimit = 10000

type Dependencies struct {
	DB           *gorm.DB
	Kind         common.DatabaseType
	ChannelNames func(context.Context, []int) (map[int]string, error)
}
type Store struct {
	db           *gorm.DB
	kind         common.DatabaseType
	channelNames func(context.Context, []int) (map[int]string, error)
}

func New(deps Dependencies) *Store {
	return &Store{db: deps.DB, kind: deps.Kind, channelNames: deps.ChannelNames}
}
func (r *Store) groupColumn() string {
	if r.kind == common.DatabaseTypeClickHouse {
		return "`group`"
	}
	return `"group"`
}
func (r *Store) Create(ctx context.Context, log *Log) error {
	if log.RequestId == "" {
		log.RequestId = common.NewRequestId()
	}
	return r.db.WithContext(ctx).Create(log).Error
}
func (r *Store) applyExplicitLogTextFilter(tx *gorm.DB, column string, value string) (*gorm.DB, error) {
	if value == "" {
		return tx, nil
	}
	if strings.Contains(value, "%") {
		condition, pattern, err := r.buildLogLikeCondition(column, value)
		if err != nil {
			return nil, err
		}
		return tx.Where(condition, pattern), nil
	}
	return tx.Where(column+" = ?", value), nil
}

func (r *Store) buildLogLikeCondition(column string, value string) (string, string, error) {
	if r.kind == common.DatabaseTypeClickHouse {
		pattern, err := sanitizeClickHouseLikePattern(value)
		if err != nil {
			return "", "", err
		}
		return column + " LIKE ?", pattern, nil
	}

	pattern, err := dbquery.SanitizeLikePattern(value)
	if err != nil {
		return "", "", err
	}
	return column + " LIKE ? ESCAPE '!'", pattern, nil
}

func (r *Store) GetLogByTokenId(ctx context.Context, tokenId int) (logs []*Log, err error) {
	order := "id desc"
	if r.kind == common.DatabaseTypeClickHouse {
		order = clickHouseLogOrder("")
	}
	err = r.db.WithContext(ctx).Model(&Log{}).Where("token_id = ?", tokenId).Order(order).Limit(common.MaxRecentItems).Find(&logs).Error
	formatUserLogs(logs, 0)
	return logs, err
}

func (r *Store) GetAllLogs(ctx context.Context, logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string, startIdx int, num int, channel int, group string, requestId string, upstreamRequestId string, cursorPages ...*LogCursorPage) (logs []*Log, total int64, err error) {
	var tx *gorm.DB
	if logType == LogTypeUnknown {
		tx = r.db.WithContext(ctx)
	} else {
		tx = r.db.WithContext(ctx).Where("logs.type = ?", logType)
	}

	if tx, err = r.applyExplicitLogTextFilter(tx, "logs.model_name", modelName); err != nil {
		return nil, 0, err
	}
	if tx, err = r.applyExplicitLogTextFilter(tx, "logs.username", username); err != nil {
		return nil, 0, err
	}
	if tokenName != "" {
		tx = tx.Where("logs.token_name = ?", tokenName)
	}
	if requestId != "" {
		tx = tx.Where("logs.request_id = ?", requestId)
	}
	if upstreamRequestId != "" {
		tx = tx.Where("logs.upstream_request_id = ?", upstreamRequestId)
	}
	if startTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", endTimestamp)
	}
	if channel != 0 {
		tx = tx.Where("logs.channel_id = ?", channel)
	}
	if group != "" {
		tx = tx.Where("logs."+r.groupColumn()+" = ?", group)
	}
	if len(cursorPages) > 0 && cursorPages[0] != nil {
		logs, err = r.selectLogCursorPage(tx, num, cursorPages[0])
	} else {
		if err = tx.Model(&Log{}).Count(&total).Error; err != nil {
			return nil, 0, err
		}
		order := "logs.created_at desc, logs.id desc"
		if r.kind == common.DatabaseTypeClickHouse {
			order = clickHouseLogOrder("logs.")
		}
		err = tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error
	}
	if err != nil {
		return nil, 0, err
	}
	if r.kind == common.DatabaseTypeClickHouse {
		assignDisplayLogIds(logs, startIdx)
	}

	channelIds := types.NewSet[int]()
	for _, log := range logs {
		if log.ChannelId != 0 {
			channelIds.Add(log.ChannelId)
		}
	}

	if channelIds.Len() > 0 && r.channelNames != nil {
		names, err := r.channelNames(ctx, channelIds.Items())
		if err != nil {
			return logs, total, err
		}
		for _, log := range logs {
			log.ChannelName = names[log.ChannelId]
		}
	}

	return logs, total, err
}

func (r *Store) GetUserLogs(ctx context.Context, userId int, logType int, startTimestamp int64, endTimestamp int64, modelName string, tokenName string, startIdx int, num int, group string, requestId string, upstreamRequestId string, cursorPages ...*LogCursorPage) (logs []*Log, total int64, err error) {
	var tx *gorm.DB
	if logType == LogTypeUnknown {
		tx = r.db.WithContext(ctx).Where("logs.user_id = ?", userId)
	} else {
		tx = r.db.WithContext(ctx).Where("logs.user_id = ? and logs.type = ?", userId, logType)
	}

	if tx, err = r.applyExplicitLogTextFilter(tx, "logs.model_name", modelName); err != nil {
		return nil, 0, err
	}
	if tokenName != "" {
		tx = tx.Where("logs.token_name = ?", tokenName)
	}
	if requestId != "" {
		tx = tx.Where("logs.request_id = ?", requestId)
	}
	if upstreamRequestId != "" {
		tx = tx.Where("logs.upstream_request_id = ?", upstreamRequestId)
	}
	if startTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", endTimestamp)
	}
	if group != "" {
		tx = tx.Where("logs."+r.groupColumn()+" = ?", group)
	}
	if len(cursorPages) > 0 && cursorPages[0] != nil {
		logs, err = r.selectLogCursorPage(tx, num, cursorPages[0])
	} else {
		if err = tx.Model(&Log{}).Limit(logSearchCountLimit).Count(&total).Error; err != nil {
			return nil, 0, err
		}
		order := "logs.id desc"
		if r.kind == common.DatabaseTypeClickHouse {
			order = clickHouseLogOrder("logs.")
		}
		err = tx.Order(order).Limit(num).Offset(startIdx).Find(&logs).Error
	}
	if err != nil {
		return nil, 0, err
	}

	formatUserLogs(logs, startIdx)
	return logs, total, err
}

func (r *Store) SumUsedQuota(ctx context.Context, logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string, channel int, group string, userIDs ...int) (stat Stat, err error) {
	tx := r.db.WithContext(ctx).Table("logs").Select("COALESCE(sum(quota), 0) quota")

	// 为rpm和tpm创建单独的查询
	rpmTpmQuery := r.db.WithContext(ctx).Table("logs").Select("count(*) rpm, COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0) tpm")

	if len(userIDs) > 0 {
		tx = tx.Where("user_id = ?", userIDs[0])
		rpmTpmQuery = rpmTpmQuery.Where("user_id = ?", userIDs[0])
		username = ""
	}

	if tx, err = r.applyExplicitLogTextFilter(tx, "username", username); err != nil {
		return stat, err
	}
	if rpmTpmQuery, err = r.applyExplicitLogTextFilter(rpmTpmQuery, "username", username); err != nil {
		return stat, err
	}
	if tokenName != "" {
		tx = tx.Where("token_name = ?", tokenName)
		rpmTpmQuery = rpmTpmQuery.Where("token_name = ?", tokenName)
	}
	if startTimestamp != 0 {
		tx = tx.Where("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("created_at <= ?", endTimestamp)
	}
	if tx, err = r.applyExplicitLogTextFilter(tx, "model_name", modelName); err != nil {
		return stat, err
	}
	if rpmTpmQuery, err = r.applyExplicitLogTextFilter(rpmTpmQuery, "model_name", modelName); err != nil {
		return stat, err
	}
	if channel != 0 {
		tx = tx.Where("channel_id = ?", channel)
		rpmTpmQuery = rpmTpmQuery.Where("channel_id = ?", channel)
	}
	if group != "" {
		tx = tx.Where(r.groupColumn()+" = ?", group)
		rpmTpmQuery = rpmTpmQuery.Where(r.groupColumn()+" = ?", group)
	}

	tx = tx.Where("type = ?", LogTypeConsume)
	rpmTpmQuery = rpmTpmQuery.Where("type = ?", LogTypeConsume)

	// 只统计最近60秒的rpm和tpm
	rpmTpmQuery = rpmTpmQuery.Where("created_at >= ?", time.Now().Add(-60*time.Second).Unix())

	// 执行查询
	if err := tx.Scan(&stat).Error; err != nil {
		common.SysError("failed to query log stat: " + err.Error())
		return stat, errors.New("查询统计数据失败")
	}
	var rateStat struct {
		Rpm int
		Tpm int
	}
	if err := rpmTpmQuery.Scan(&rateStat).Error; err != nil {
		common.SysError("failed to query rpm/tpm stat: " + err.Error())
		return stat, errors.New("查询统计数据失败")
	}
	stat.Rpm = rateStat.Rpm
	stat.Tpm = rateStat.Tpm

	return stat, nil
}

func (r *Store) SumUsedToken(ctx context.Context, logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string) (token int) {
	tx := r.db.WithContext(ctx).Table("logs").Select("COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0)")
	if username != "" {
		tx = tx.Where("username = ?", username)
	}
	if tokenName != "" {
		tx = tx.Where("token_name = ?", tokenName)
	}
	if startTimestamp != 0 {
		tx = tx.Where("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		tx = tx.Where("created_at <= ?", endTimestamp)
	}
	if modelName != "" {
		tx = tx.Where("model_name = ?", modelName)
	}
	tx.Where("type = ?", LogTypeConsume).Scan(&token)
	return token
}

func (r *Store) CountOldLog(ctx context.Context, targetTimestamp int64) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&Log{}).Where("created_at < ?", targetTimestamp).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Store) DeleteOldLogBatch(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if nil != ctx.Err() {
		return 0, ctx.Err()
	}

	if r.kind == common.DatabaseTypeClickHouse {
		// ClickHouse DELETE is a heavy mutation that rewrites data parts, so
		// per-batch mutations would be pathologically slow. Remove all matching
		// rows in a single synchronous mutation regardless of limit; the reported
		// count lets the caller's progress loop complete in one pass.
		total, err := r.CountOldLog(ctx, targetTimestamp)
		if err != nil {
			return 0, err
		}
		if total == 0 {
			return 0, nil
		}
		if err := r.db.WithContext(ctx).Exec(
			"ALTER TABLE logs DELETE WHERE created_at < ? SETTINGS mutations_sync = 1",
			targetTimestamp,
		).Error; err != nil {
			return 0, err
		}
		return total, nil
	}

	query := r.db.WithContext(ctx)
	ids := query.Model(&Log{}).Select("id").Where("created_at < ?", targetTimestamp).Order("id").Limit(limit)
	result := query.Where("id IN (?)", ids).Delete(&Log{})
	if nil != result.Error {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func sanitizeClickHouseLikePattern(input string) (string, error) {
	input = strings.ReplaceAll(input, `\`, `\\`)
	input = strings.ReplaceAll(input, `_`, `\_`)

	if err := dbquery.ValidateLikePattern(input); err != nil {
		return "", err
	}
	return input, nil
}

func clickHouseLogOrder(prefix string) string {
	return prefix + "created_at desc, " + prefix + "request_id desc"
}

func assignDisplayLogIds(logs []*Log, startIdx int) {
	for i := range logs {
		logs[i].Id = startIdx + i + 1
	}
}

func formatUserLogs(logs []*Log, startIdx int) {
	for i := range logs {
		logs[i].ChannelName = ""
		logs[i].Other = metadata.UserJSON(logs[i].Other)
	}
	assignDisplayLogIds(logs, startIdx)
}

// FormatAdminLogs removes root-only diagnostics while retaining operational
// admin_info. Root callers must not pass their results through this formatter.
func FormatAdminLogs(logs []*Log) {
	for i := range logs {
		logs[i].Other = metadata.AdminJSON(logs[i].Other)
	}
}

// FormatRootLogs normalizes legacy metadata into the current scoped shape
// without removing root-only diagnostics.
func FormatRootLogs(logs []*Log) {
	for i := range logs {
		logs[i].Other = metadata.RootJSON(logs[i].Other)
	}
}
func FormatUserLogs(logs []*Log, offset int) { formatUserLogs(logs, offset) }
