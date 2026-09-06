package model

import (
	"errors"

	"gorm.io/gorm"
)

type Midjourney struct {
	Id          int    `json:"id"`
	Code        int    `json:"code"`
	UserId      int    `json:"user_id" gorm:"index"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	MjId        string `json:"mj_id" gorm:"index"`
	Prompt      string `json:"prompt"`
	PromptEn    string `json:"prompt_en"`
	Description string `json:"description"`
	State       string `json:"state"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	ImageUrl    string `json:"image_url"`
	VideoUrl    string `json:"video_url"`
	VideoUrls   string `json:"video_urls"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	Progress    string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason  string `json:"fail_reason"`
	ChannelId   int    `json:"channel_id"`
	Quota       int    `json:"quota"`
	// Billing fields form the durable hand-off between a persisted Midjourney
	// row and its wallet/token accounting receipt. They are intentionally
	// excluded from public JSON responses.
	BillingPending     bool   `json:"-" gorm:"column:billing_pending;not null"`
	BillingAction      string `json:"-" gorm:"column:billing_action;type:varchar(32);not null;default:''"`
	BillingOperationID string `json:"-" gorm:"column:billing_operation_id;type:varchar(128);not null;default:''"`
	BillingTargetQuota int    `json:"-" gorm:"column:billing_target_quota;not null;default:0"`
	BillingDelta       int    `json:"-" gorm:"column:billing_delta;not null;default:0"`
	Buttons            string `json:"buttons"`
	Properties         string `json:"properties"`

	TokenId          int `json:"-" gorm:"default:0"`
	BillingChannelId int `json:"-" gorm:"default:0"`
	// Billing provenance is persisted with the task so delayed settlement and
	// refund never infer authorization from current token metadata.
	BillingRequestID      string `json:"-" gorm:"column:billing_request_id;type:varchar(64);not null;default:''"`
	BillingTokenUnlimited bool   `json:"-" gorm:"column:billing_token_unlimited;not null;default:false"`
	BillingPlayground     bool   `json:"-" gorm:"column:billing_playground;not null;default:false"`
	BillingFreeModel      bool   `json:"-" gorm:"column:billing_free_model;not null;default:false"`
	BillingSource         string `json:"-" gorm:"column:billing_source;type:varchar(32);not null;default:'wallet'"`
	BillingSubscriptionID int    `json:"-" gorm:"column:billing_subscription_id;not null;default:0"`
}

// TaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type TaskQueryParams struct {
	ChannelID      string
	MjID           string
	StartTimestamp string
	EndTimestamp   string
}

func GetAllUserTask(userId int, startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllTasks(startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllUnFinishTasks() []*Midjourney {
	var tasks []*Midjourney
	var err error
	// get all tasks progress is not 100%
	err = DB.Where("progress != ?", "100%").Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

// HasUnfinishedMidjourneyTasks reports whether at least one Midjourney task is
// still in progress. It is a cheap existence check (LIMIT 1) used to decide
// whether the midjourney_poll system task needs to run; when no task is pending
// the scheduler skips creating a row entirely.
func HasUnfinishedMidjourneyTasks() bool {
	var id int
	err := DB.Model(&Midjourney{}).
		Where("progress != ?", "100%").
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func GetByOnlyMJId(mjId string) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("mj_id = ?", mjId).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetByMJId(userId int, mjId string) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id = ?", userId, mjId).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetByMJIds(userId int, mjIds []string) []*Midjourney {
	var mj []*Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id in (?)", userId, mjIds).Find(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetMjByuId(id int) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("id = ?", id).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func UpdateProgress(id int, progress string) error {
	return DB.Model(&Midjourney{}).Where("id = ?", id).Update("progress", progress).Error
}

func (midjourney *Midjourney) Insert() error {
	return midjourney.InsertTx(DB)
}

// InsertTx inserts a Midjourney task in the caller's transaction. Billing
// marker installation can then be committed with this row before the session
// terminal transaction runs.
func (midjourney *Midjourney) InsertTx(tx *gorm.DB) error {
	if tx == nil {
		return errors.New("Midjourney transaction is nil")
	}
	if midjourney == nil {
		return errors.New("Midjourney task is nil")
	}
	return tx.Create(midjourney).Error
}

// LockMidjourneyForBilling loads one row under the shared billing lock. The
// caller must keep the surrounding transaction open through the ledger
// receipt and the marker update.
func LockMidjourneyForBilling(tx *gorm.DB, id int) (*Midjourney, error) {
	if tx == nil {
		return nil, errors.New("Midjourney billing transaction is nil")
	}
	if id <= 0 {
		return nil, errors.New("Midjourney billing id is invalid")
	}
	var task Midjourney
	if err := lockForUpdate(tx).Where("id = ?", id).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// UpdateBillingStateTx updates only billing fields while retaining the
// current task result. Notify/poll updates use separate selected columns, so
// neither path can resurrect a cleared quota from a stale full-row snapshot.
func (midjourney *Midjourney) UpdateBillingStateTx(tx *gorm.DB) error {
	if tx == nil {
		return errors.New("Midjourney billing transaction is nil")
	}
	if midjourney == nil || midjourney.Id <= 0 {
		return errors.New("Midjourney billing id is invalid")
	}
	return tx.Model(&Midjourney{}).Where("id = ?", midjourney.Id).Updates(map[string]any{
		"quota":                   midjourney.Quota,
		"token_id":                midjourney.TokenId,
		"billing_channel_id":      midjourney.BillingChannelId,
		"billing_pending":         midjourney.BillingPending,
		"billing_action":          midjourney.BillingAction,
		"billing_operation_id":    midjourney.BillingOperationID,
		"billing_target_quota":    midjourney.BillingTargetQuota,
		"billing_delta":           midjourney.BillingDelta,
		"billing_request_id":      midjourney.BillingRequestID,
		"billing_token_unlimited": midjourney.BillingTokenUnlimited,
		"billing_playground":      midjourney.BillingPlayground,
		"billing_free_model":      midjourney.BillingFreeModel,
		"billing_source":          midjourney.BillingSource,
		"billing_subscription_id": midjourney.BillingSubscriptionID,
	}).Error
}

// UpdateNotifyState writes only upstream status/result columns. Billing state
// is owned by settlement/refund transactions and must not be part of a notify
// callback's stale full-row write.
func (midjourney *Midjourney) UpdateNotifyState() error {
	if midjourney == nil || midjourney.Id <= 0 {
		return errors.New("Midjourney id is invalid")
	}
	return DB.Model(&Midjourney{}).Where("id = ?", midjourney.Id).Updates(map[string]any{
		"progress":    midjourney.Progress,
		"prompt_en":   midjourney.PromptEn,
		"state":       midjourney.State,
		"submit_time": midjourney.SubmitTime,
		"start_time":  midjourney.StartTime,
		"finish_time": midjourney.FinishTime,
		"image_url":   midjourney.ImageUrl,
		"video_url":   midjourney.VideoUrl,
		"video_urls":  midjourney.VideoUrls,
		"status":      midjourney.Status,
		"fail_reason": midjourney.FailReason,
	}).Error
}

func (midjourney *Midjourney) Update() error {
	var err error
	err = DB.Save(midjourney).Error
	return err
}

func (midjourney *Midjourney) UpdateBillingState() error {
	return DB.Model(midjourney).
		Select("quota", "token_id", "billing_channel_id").
		Updates(midjourney).Error
}

func (midjourney *Midjourney) GetBillingChannelId() int {
	if midjourney.BillingChannelId > 0 {
		return midjourney.BillingChannelId
	}
	return midjourney.ChannelId
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Uses Model().Select("*").Updates() to avoid GORM Save()'s INSERT fallback.
func (midjourney *Midjourney) UpdateWithStatus(fromStatus string) (bool, error) {
	result := DB.Model(midjourney).Where("status = ?", fromStatus).Select("*").Updates(midjourney)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func MjBulkUpdate(mjIds []string, params map[string]any) error {
	return DB.Model(&Midjourney{}).
		Where("mj_id in (?)", mjIds).
		Updates(params).Error
}

func MjBulkUpdateByTaskIds(taskIDs []int, params map[string]any) error {
	return DB.Model(&Midjourney{}).
		Where("id in (?)", taskIDs).
		Updates(params).Error
}

// CountAllTasks returns total midjourney tasks for admin query
func CountAllTasks(queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}

// CountAllUserTask returns total midjourney tasks for user
func CountAllUserTask(userId int, queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{}).Where("user_id = ?", userId)
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}
