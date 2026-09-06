package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ChannelSortOptions struct {
	SortBy    string
	SortOrder string
	IDSort    bool
}

var channelSortColumns = map[string]string{
	"id":            "id",
	"name":          "name",
	"priority":      "priority",
	"balance":       "balance",
	"response_time": "response_time",
	"test_time":     "test_time",
}

func NewChannelSortOptions(sortBy string, sortOrder string, idSort bool) ChannelSortOptions {
	normalizedSortBy := strings.ToLower(strings.TrimSpace(sortBy))
	normalizedSortOrder := strings.ToLower(strings.TrimSpace(sortOrder))
	if _, ok := channelSortColumns[normalizedSortBy]; !ok {
		normalizedSortBy = ""
		normalizedSortOrder = ""
	} else if normalizedSortOrder != "asc" {
		normalizedSortOrder = "desc"
	}

	return ChannelSortOptions{
		SortBy:    normalizedSortBy,
		SortOrder: normalizedSortOrder,
		IDSort:    idSort,
	}
}

func (options ChannelSortOptions) Apply(query *gorm.DB) *gorm.DB {
	if columnName, ok := channelSortColumns[options.SortBy]; ok {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: columnName},
			Desc:   options.SortOrder != "asc",
		})
	}
	if options.IDSort {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: "id"},
			Desc:   true,
		})
	}
	return query.Order(clause.OrderByColumn{
		Column: clause.Column{Name: "priority"},
		Desc:   true,
	})
}

func resolveChannelSortOptions(idSort bool, sortOptions []ChannelSortOptions) ChannelSortOptions {
	if len(sortOptions) == 0 {
		return NewChannelSortOptions("", "", idSort)
	}
	options := sortOptions[0]
	options.IDSort = options.IDSort || idSort
	return options
}

func NormalizeChannelGroupFilter(group string) string {
	group = strings.TrimSpace(group)
	if group == "" || strings.EqualFold(group, "all") || strings.EqualFold(group, "null") {
		return ""
	}
	return group
}

func ApplyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	group = NormalizeChannelGroupFilter(group)
	if group == "" {
		return query
	}
	return query.Where(commonGroupCol+" @> ?::text[]", StringList{group})
}

func (r *Runtime) GetNextEnabledKey(channel *Channel) (string, int, *types.NewAPIError) {
	if channel == nil {
		return "", 0, types.NewError(errors.New("channel is nil"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if r.cache != nil {
		// Callers may hold a channel selected before another data-plane instance
		// changed its key state. Refresh a private copy so key selection observes
		// the same shared runtime state as channel routing.
		refreshed, err := common.DeepCopy(channel)
		if err != nil {
			return "", 0, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if r.snapshot.readOnly && refreshed.Status == common.ChannelStatusAutoDisabled {
			// In a data-plane request this is a projection from the supplied
			// snapshot. Preserve the snapshot's key pool while removing the old
			// top-level projection before refreshing its DragonflyDB state.
			refreshed.Status = common.ChannelStatusEnabled
		}
		normalizeChannelConfiguration(refreshed)
		if err := r.applyChannelRuntimeState(refreshed); err != nil {
			return "", 0, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		channel = refreshed
	}
	// If not in multi-key mode, return the original key string directly.
	if !channel.ChannelInfo.IsMultiKey {
		return channel.Key, 0, nil
	}

	// Obtain all keys (split by \n)
	keys := channel.GetKeys()
	if len(keys) == 0 {
		// No keys available, return error, should disable the channel
		return "", 0, types.NewError(errors.New("no keys available"), types.ErrorCodeChannelNoAvailableKey)
	}

	lock := r.GetChannelKeyLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()

	statusList := channel.ChannelInfo.MultiKeyStatusList
	// helper to get key status, default to enabled when missing
	getStatus := func(idx int) int {
		if statusList == nil {
			return common.ChannelStatusEnabled
		}
		if status, ok := statusList[idx]; ok {
			return status
		}
		return common.ChannelStatusEnabled
	}

	// Collect indexes of enabled keys
	enabledIdx := make([]int, 0, len(keys))
	for i := range keys {
		if getStatus(i) == common.ChannelStatusEnabled {
			enabledIdx = append(enabledIdx, i)
		}
	}
	// If no specific status list or none enabled, return an explicit error so caller can
	// properly handle a channel with no available keys (e.g. mark channel disabled).
	// Returning the first key here caused requests to keep using an already-disabled key.
	if len(enabledIdx) == 0 {
		return "", 0, types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)
	}

	switch channel.ChannelInfo.MultiKeyMode {
	case constant.MultiKeyModeRandom:
		// Randomly pick one enabled key
		selectedIdx := enabledIdx[rand.Intn(len(enabledIdx))]
		return keys[selectedIdx], selectedIdx, nil
	case constant.MultiKeyModePolling:
		index, err := r.nextPollingIndex(channel.Id, keys, enabledIdx)
		if err != nil {
			return "", 0, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		return keys[index], index, nil
	default:
		// Unknown mode, default to first enabled key (or original key string)
		return keys[enabledIdx[0]], enabledIdx[0], nil
	}
}

func (r *Runtime) SaveChannelInfo(channel *Channel) error {
	if err := r.normalizeChannelForWrite(channel); err != nil {
		return err
	}
	return r.db.Model(channel).Update("channel_info", channel.ChannelInfo).Error
}

func (r *Runtime) SaveChannel(channel *Channel) error {
	if err := r.normalizeChannelForWrite(channel); err != nil {
		return err
	}
	return r.db.Save(channel).Error
}

// saveStatusState persists only the fields owned by the channel status flow.
// Keeping this allowlist here prevents a stale channel snapshot from
// overwriting credentials, accounting counters, or channel configuration.
func (r *Runtime) saveChannelStatus(channel *Channel) error {
	if channel.Id == 0 {
		return errors.New("channel ID is 0")
	}
	updates := map[string]any{
		"status":     channel.Status,
		"other_info": channel.OtherInfo,
	}
	if channel.ChannelInfo.IsMultiKey {
		updates["channel_info"] = channel.ChannelInfo
	}
	return r.db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates).Error
}

func (r *Runtime) GetAllChannels(startIdx int, num int, selectAll bool, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	var err error
	order := resolveChannelSortOptions(idSort, sortOptions)
	if selectAll {
		err = order.Apply(r.db).Find(&channels).Error
	} else {
		// Load the key pool before applying shared runtime status; it is needed
		// to select the correct DragonflyDB fingerprint even when the response
		// must hide key material.
		err = order.Apply(r.db).Limit(num).Offset(startIdx).Find(&channels).Error
	}
	if err != nil {
		return nil, err
	}
	if err := r.applyRuntimeStateToChannels(channels, selectAll); err != nil {
		return nil, err
	}
	return channels, err
}

func (r *Runtime) GetChannelsByTag(tag string, idSort bool, selectAll bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	order := resolveChannelSortOptions(idSort, sortOptions)
	query := order.Apply(r.db.Where("tag = ?", tag))
	err := query.Find(&channels).Error
	if err != nil {
		return nil, err
	}
	err = r.applyRuntimeStateToChannels(channels, selectAll)
	return channels, err
}

func (r *Runtime) SearchChannels(keyword string, group string, model string, idSort bool, sortOptions ...ChannelSortOptions) ([]*Channel, error) {
	var channels []*Channel
	modelsCol := `"models"`

	baseURLCol := `"base_url"`

	order := resolveChannelSortOptions(idSort, sortOptions)

	// 构造基础查询
	baseQuery := r.db.Model(&Channel{})

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?)"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)
	if model != "" {
		baseQuery = baseQuery.Where("EXISTS (SELECT 1 FROM unnest("+modelsCol+") AS entry(model) WHERE entry.model LIKE ?)", "%"+model+"%")
	}

	// 执行查询
	err := order.Apply(baseQuery).Find(&channels).Error
	if err != nil {
		return nil, err
	}
	if err := r.applyRuntimeStateToChannels(channels, false); err != nil {
		return nil, err
	}
	return channels, nil
}

// GetChannelById returns the channel configuration with the shared automatic
// runtime status overlaid. Control-plane callers read PostgreSQL; data-plane
// callers read the published DragonflyDB snapshot. Automatic status never
// causes a data-plane database read.
func (r *Runtime) GetChannelById(id int, selectAll bool) (*Channel, error) {
	channel, err := r.getConfiguredChannelByID(id, selectAll)
	if err != nil {
		return nil, err
	}
	if err := r.applyChannelRuntimeState(channel); err != nil {
		return nil, err
	}
	if !selectAll {
		channel.Key = ""
		channel.Keys = nil
	}
	return channel, nil
}

func (r *Runtime) getConfiguredChannelByID(id int, selectAll bool) (*Channel, error) {
	if r.snapshot.readOnly {
		r.channelSyncLock.RLock()
		defer r.channelSyncLock.RUnlock()
		cached, ok := r.channelsIDM[id]
		if !ok {
			return nil, gorm.ErrRecordNotFound
		}
		copied, err := common.DeepCopy(cached)
		if err != nil {
			return nil, err
		}
		normalizeChannelConfiguration(copied)
		return copied, nil
	}
	channel := &Channel{Id: id}
	var err error
	err = r.db.First(channel, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	normalizeChannelConfiguration(channel)
	return channel, nil
}

func (r *Runtime) BatchInsertChannels(channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	for i := range channels {
		if channels[i].Status == common.ChannelStatusAutoDisabled {
			return errors.New("automatic channel status is runtime-only")
		}
		normalizeChannelConfiguration(&channels[i])
	}
	tx := r.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for _, chunk := range lo.Chunk(channels, 50) {
		if err := tx.Create(&chunk).Error; err != nil {
			tx.Rollback()
			return err
		}
		for _, channel_ := range chunk {
			if err := r.AddAbilities(&(channel_), tx); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit().Error
}

func (r *Runtime) BatchDeleteChannels(ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// 使用事务 分批删除channel表和abilities表
	tx := r.db.Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	var deletedCount int64
	for _, chunk := range lo.Chunk(ids, 200) {
		result := tx.Where("id in (?)", chunk).Delete(&Channel{})
		if result.Error != nil {
			tx.Rollback()
			return 0, result.Error
		}
		deletedCount += result.RowsAffected
		if err := tx.Where("channel_id in (?)", chunk).Delete(&Ability{}).Error; err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return 0, err
	}
	return deletedCount, nil
}

func (r *Runtime) InsertChannel(channel *Channel) error {
	if channel == nil {
		return errors.New("channel is nil")
	}
	if channel.Status == common.ChannelStatusAutoDisabled {
		return errors.New("automatic channel status is runtime-only")
	}
	normalizeChannelConfiguration(channel)
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(channel).Error; err != nil {
			return err
		}
		return r.AddAbilities(channel, tx)
	})
}

func (r *Runtime) UpdateChannel(channel *Channel) error {
	// The automatic status and auto key entries are projections kept in
	// DragonflyDB. A channel returned from GetChannelById may include them for
	// administration, so strip those projections before writing configuration.
	if err := r.normalizeChannelForWrite(channel); err != nil {
		return err
	}
	var previousChannel *Channel
	previousFingerprint := ""
	if !r.snapshot.readOnly && r.db != nil && channel != nil {
		if previous, err := r.getConfiguredChannelByID(channel.Id, true); err == nil {
			previousChannel = previous
			previousFingerprint = ChannelKeyPoolFingerprint(previous)
		}
	}
	// If this is a multi-key channel, recalculate MultiKeySize based on the current key list to avoid inconsistency after editing keys
	if channel.ChannelInfo.IsMultiKey {
		var keyStr string
		if channel.Key != "" {
			keyStr = channel.Key
		} else {
			// If key is not provided, read the existing key from the database
			if existing, err := r.GetChannelById(channel.Id, true); err == nil {
				keyStr = existing.Key
			}
		}
		// Parse the key list (supports newline separation or JSON array)
		keys := []string{}
		if keyStr != "" {
			trimmed := strings.TrimSpace(keyStr)
			if strings.HasPrefix(trimmed, "[") {
				var arr []json.RawMessage
				if err := common.Unmarshal([]byte(trimmed), &arr); err == nil {
					keys = make([]string, len(arr))
					for i, v := range arr {
						keys[i] = string(v)
					}
				}
			}
			if len(keys) == 0 { // fallback to newline split
				keys = strings.Split(strings.Trim(keyStr, "\n"), "\n")
			}
		}
		channel.ChannelInfo.MultiKeySize = len(keys)
		// Clean up status data that exceeds the new key count to prevent index out of range
		if channel.ChannelInfo.MultiKeyStatusList != nil {
			for idx := range channel.ChannelInfo.MultiKeyStatusList {
				if idx >= channel.ChannelInfo.MultiKeySize {
					delete(channel.ChannelInfo.MultiKeyStatusList, idx)
				}
			}
		}
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(channel).Updates(channel).Error; err != nil {
			return err
		}
		if err := tx.First(channel, "id = ?", channel.Id).Error; err != nil {
			return err
		}
		return r.UpdateAbilities(channel, tx)
	})
	if err != nil {
		return err
	}
	if previousChannel != nil && previousFingerprint != ChannelKeyPoolFingerprint(channel) {
		// A key-pool replacement starts with a clean runtime state. Clear both
		// fingerprints so a later rollback to an older pool does not resurrect
		// an obsolete failure marker.
		if err := r.clearChannelRuntimeState(previousChannel); err != nil {
			return err
		}
		if err := r.clearChannelRuntimeState(channel); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) UpdateChannelResponseTime(channel *Channel, responseTime int64) {
	err := r.db.Model(channel).Select("response_time", "test_time").Updates(Channel{
		TestTime:     common.GetTimestamp(),
		ResponseTime: int(responseTime),
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update response time: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (r *Runtime) UpdateChannelBalance(channel *Channel, balance float64) {
	err := r.db.Model(channel).Select("balance_updated_time", "balance").Updates(Channel{
		BalanceUpdatedTime: common.GetTimestamp(),
		Balance:            balance,
	}).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update balance: channel_id=%d, error=%v", channel.Id, err))
	}
}

func (r *Runtime) DeleteChannel(channel *Channel) error {
	var err error
	err = r.db.Delete(channel).Error
	if err != nil {
		return err
	}
	err = r.DeleteAbilities(channel)
	if err != nil {
		return err
	}
	return r.clearChannelRuntimeState(channel)
}

// GetChannelKeyLock returns or creates a mutex for the given channel ID
func (r *Runtime) GetChannelKeyLock(channelId int) *sync.Mutex {
	if lock, exists := r.channelKeyLocks.Load(channelId); exists {
		return lock.(*sync.Mutex)
	}
	// Create new lock for this channel
	newLock := &sync.Mutex{}
	actual, _ := r.channelKeyLocks.LoadOrStore(channelId, newLock)
	return actual.(*sync.Mutex)
}

// CleanupChannelKeyLocks removes locks for channels that no longer exist
// This is optional and can be called periodically to prevent memory leaks
func (r *Runtime) CleanupChannelKeyLocks() {
	var activeChannelIds []int
	r.db.Model(&Channel{}).Pluck("id", &activeChannelIds)

	activeChannelSet := make(map[int]bool)
	for _, id := range activeChannelIds {
		activeChannelSet[id] = true
	}

	r.channelKeyLocks.Range(func(key, value interface{}) bool {
		channelId := key.(int)
		if !activeChannelSet[channelId] {
			r.channelKeyLocks.Delete(channelId)
		}
		return true
	})
}

func hasEnabledMultiKey(keys []string, statusList map[int]int) bool {
	for i := range keys {
		if statusList == nil {
			return true
		}
		status, ok := statusList[i]
		if !ok || status == common.ChannelStatusEnabled {
			return true
		}
	}
	return false
}

func (r *Runtime) UpdateChannelStatus(channelId int, usingKey string, status int, reason string) bool {
	return r.UpdateChannelStatusForKeyPool(channelId, usingKey, status, reason, "")
}

// UpdateChannelStatusForKeyPool applies an automatic status transition only
// when the caller observed the same key pool as the current configuration.
// The empty fingerprint keeps the legacy adapter compatible for callers that
// do not carry a channel snapshot; request paths should pass the fingerprint.
func (r *Runtime) UpdateChannelStatusForKeyPool(channelId int, usingKey string, status int, reason, keyPoolFingerprint string) bool {
	switch status {
	case common.ChannelStatusAutoDisabled:
		return r.updateAutomaticChannelStatus(channelId, usingKey, status, reason, keyPoolFingerprint)
	case common.ChannelStatusEnabled:
		// Existing automatic recovery callers use this method. Manual HTTP
		// operations call UpdateManualChannelStatus so an automatic recovery can
		// never turn a manually disabled channel back on.
		return r.updateAutomaticChannelStatus(channelId, usingKey, status, reason, keyPoolFingerprint)
	case common.ChannelStatusManuallyDisabled:
		return r.UpdateManualChannelStatus(channelId, status, reason)
	default:
		return false
	}
}

// UpdateManualChannelStatus changes the persistent channel status requested by
// an administrator. It is deliberately separate from automatic recovery:
// status 2 remains authoritative even when an old automatic state is present.
func (r *Runtime) UpdateManualChannelStatus(channelId int, status int, reason string) bool {
	if r.snapshot.readOnly || r.db == nil {
		return false
	}
	if status != common.ChannelStatusEnabled && status != common.ChannelStatusManuallyDisabled {
		return false
	}

	r.channelStatusLock.Lock()
	defer r.channelStatusLock.Unlock()
	keyLock := r.GetChannelKeyLock(channelId)
	keyLock.Lock()
	defer keyLock.Unlock()

	channel, err := r.getConfiguredChannelByID(channelId, true)
	if err != nil {
		return false
	}
	changed := channel.Status != status
	if status == common.ChannelStatusEnabled {
		state, stateErr := r.readChannelRuntimeState(context.Background(), channel)
		if stateErr != nil {
			common.SysLog(fmt.Sprintf("failed to read channel runtime status: channel_id=%d, error=%v", channelId, stateErr))
			return false
		}
		changed = changed || state.Status == common.ChannelStatusAutoDisabled || len(state.Keys) > 0
	}
	if !changed {
		return false
	}

	info := channel.GetOtherInfo()
	info["status_reason"] = reason
	info["status_time"] = common.GetTimestamp()
	channel.SetOtherInfo(info)
	channel.Status = status
	if err := r.saveChannelStatus(channel); err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel status: channel_id=%d, status=%d, error=%v", channel.Id, status, err))
		return false
	}
	if err := r.clearChannelRuntimeState(channel); err != nil {
		common.SysLog(fmt.Sprintf("failed to clear channel runtime status: channel_id=%d, error=%v", channelId, err))
		return false
	}
	if err := r.UpdateAbilityStatus(channelId, status == common.ChannelStatusEnabled); err != nil {
		common.SysLog(fmt.Sprintf("failed to update ability status: channel_id=%d, error=%v", channelId, err))
	}
	return true
}

func (r *Runtime) updateAutomaticChannelStatus(channelId int, usingKey string, status int, reason, keyPoolFingerprint string) bool {
	keyLock := r.GetChannelKeyLock(channelId)
	keyLock.Lock()
	defer keyLock.Unlock()

	// This read uses the published snapshot on a data-plane instance. In
	// particular, it must not fall through to PostgreSQL when DragonflyDB is
	// unavailable.
	channel, err := r.getConfiguredChannelByID(channelId, true)
	if err != nil {
		return false
	}
	if channel.Status == common.ChannelStatusManuallyDisabled {
		return false
	}
	if keyPoolFingerprint != "" && keyPoolFingerprint != ChannelKeyPoolFingerprint(channel) {
		return false
	}
	if channel.ChannelInfo.IsMultiKey && usingKey != "" {
		found := false
		for _, key := range channel.GetKeys() {
			if key == usingKey {
				found = true
				break
			}
		}
		if !found {
			// The request was generated from an older key pool. Do not apply its
			// key index to the current pool.
			return false
		}
	}
	if !channel.ChannelInfo.IsMultiKey && usingKey != "" && channel.Key != usingKey {
		// A single-key request can also arrive after the channel credential was
		// rotated. Treat it as stale for the same reason as an old multi-key
		// request.
		return false
	}
	changed, err := r.changeChannelRuntimeStatus(channel, usingKey, status, reason)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel runtime status: channel_id=%d, status=%d, error=%v", channelId, status, err))
		return false
	}
	return changed
}

func (r *Runtime) EnableChannelByTag(tag string) error {
	if r.snapshot.readOnly || r.db == nil {
		return errors.New("channel configuration is read-only")
	}
	var channels []Channel
	if err := r.db.Where("tag = ?", tag).Find(&channels).Error; err != nil {
		return err
	}
	err := r.db.Model(&Channel{}).Where("tag = ?", tag).Update("status", common.ChannelStatusEnabled).Error
	if err != nil {
		return err
	}
	for i := range channels {
		if err := r.clearChannelRuntimeState(&channels[i]); err != nil {
			return err
		}
	}
	err = r.UpdateAbilityStatusByTag(tag, true)
	return err
}

func (r *Runtime) DisableChannelByTag(tag string) error {
	if r.snapshot.readOnly || r.db == nil {
		return errors.New("channel configuration is read-only")
	}
	var channels []Channel
	if err := r.db.Where("tag = ?", tag).Find(&channels).Error; err != nil {
		return err
	}
	err := r.db.Model(&Channel{}).Where("tag = ?", tag).Update("status", common.ChannelStatusManuallyDisabled).Error
	if err != nil {
		return err
	}
	for i := range channels {
		if err := r.clearChannelRuntimeState(&channels[i]); err != nil {
			return err
		}
	}
	err = r.UpdateAbilityStatusByTag(tag, false)
	return err
}

func (r *Runtime) EditChannelByTag(tag string, newTag *string, modelMapping *string, models *StringList, group *StringList, priority *int64, weight *uint, paramOverride *string, headerOverride *string) error {
	updateData := Channel{}
	shouldReCreateAbilities := false
	updatedTag := tag
	// 如果 newTag 不为空且不等于 tag，则更新 tag
	if newTag != nil && *newTag != tag {
		updateData.Tag = newTag
		updatedTag = *newTag
	}
	if modelMapping != nil {
		updateData.ModelMapping = modelMapping
	}
	if models != nil && len(*models) > 0 {
		shouldReCreateAbilities = true
		updateData.Models = *models
	}
	if group != nil && len(*group) > 0 {
		shouldReCreateAbilities = true
		updateData.Group = *group
	}
	if priority != nil {
		updateData.Priority = priority
	}
	if weight != nil {
		updateData.Weight = weight
	}
	if paramOverride != nil {
		updateData.ParamOverride = paramOverride
	}
	if headerOverride != nil {
		updateData.HeaderOverride = headerOverride
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Channel{}).Where("tag = ?", tag).Updates(&updateData).Error; err != nil {
			return err
		}
		if !shouldReCreateAbilities {
			return r.UpdateAbilityByTag(tag, newTag, priority, weight, tx)
		}
		var channels []Channel
		if err := tx.Where("tag = ?", updatedTag).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			if err := r.UpdateAbilities(&(channel), tx); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Runtime) UpdateChannelUsedQuota(ctx context.Context, id, quota int) error {
	if r.queueQuota != nil {
		return r.queueQuota(ctx, id, quota)
	}
	return r.updateChannelUsedQuota(ctx, id, quota)
}

func (r *Runtime) updateChannelUsedQuota(ctx context.Context, id int, quota int) error {
	err := r.db.WithContext(ctx).Model(&Channel{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to update channel used quota: channel_id=%d, delta_quota=%d, error=%v", id, quota, err))
	}
	return err
}

func (r *Runtime) DeleteChannelByStatus(status int64) (int64, error) {
	if status == common.ChannelStatusAutoDisabled && r.cache != nil && !r.snapshot.readOnly {
		channels, err := r.listEffectiveChannels(context.Background(), ListFilter{Status: 0, Type: -1}, nil, ChannelSortOptions{})
		if err != nil {
			return 0, err
		}
		ids := make([]int, 0, len(channels))
		for _, channel := range channels {
			if channel.Status == common.ChannelStatusAutoDisabled {
				ids = append(ids, channel.Id)
			}
		}
		return r.deleteChannelsByIDs(channels, ids)
	}
	result := r.db.Where("status = ?", status).Delete(&Channel{})
	return result.RowsAffected, result.Error
}

func (r *Runtime) DeleteDisabledChannel() (int64, error) {
	if r.cache != nil && !r.snapshot.readOnly {
		channels, err := r.listEffectiveChannels(context.Background(), ListFilter{Status: 0, Type: -1}, nil, ChannelSortOptions{})
		if err != nil {
			return 0, err
		}
		ids := make([]int, 0, len(channels))
		for _, channel := range channels {
			if channel.Status != common.ChannelStatusEnabled {
				ids = append(ids, channel.Id)
			}
		}
		return r.deleteChannelsByIDs(channels, ids)
	}
	result := r.db.Where("status = ? or status = ?", common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled).Delete(&Channel{})
	return result.RowsAffected, result.Error
}

func (r *Runtime) deleteChannelsByIDs(channels []*Channel, ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	result := tx.Where("id IN ?", ids).Delete(&Channel{})
	if result.Error != nil {
		tx.Rollback()
		return 0, result.Error
	}
	if err := tx.Where("channel_id IN ?", ids).Delete(&Ability{}).Error; err != nil {
		tx.Rollback()
		return 0, err
	}
	if err := tx.Commit().Error; err != nil {
		return 0, err
	}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		for _, id := range ids {
			if channel.Id != id {
				continue
			}
			if err := r.clearChannelRuntimeState(channel); err != nil {
				return 0, err
			}
			break
		}
	}
	return result.RowsAffected, nil
}

func (r *Runtime) GetPaginatedTags(offset int, limit int) ([]*string, error) {
	return r.GetPaginatedChannelTags(r.db.Model(&Channel{}), offset, limit)
}

func (r *Runtime) GetPaginatedChannelTags(query *gorm.DB, offset int, limit int) ([]*string, error) {
	var tags []*string
	err := query.
		Select("DISTINCT tag").
		Where("tag is not null AND tag != ''").
		Order(clause.OrderByColumn{Column: clause.Column{Name: "tag"}}).
		Offset(offset).
		Limit(limit).
		Find(&tags).Error
	return tags, err
}

func (r *Runtime) SearchTags(keyword string, group string, model string, idSort bool) ([]*string, error) {
	var tags []*string
	modelsCol := `"models"`

	baseURLCol := `"base_url"`

	order := "priority desc"
	if idSort {
		order = "id desc"
	}

	// 构造基础查询
	baseQuery := r.db.Model(&Channel{}).Omit("key")

	// 构造WHERE子句
	whereClause := "(id = ? OR name LIKE ? OR " + commonKeyCol + " = ? OR " + baseURLCol + " LIKE ?)"
	args := []any{common.String2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%"}
	baseQuery = ApplyChannelGroupFilter(baseQuery.Where(whereClause, args...), group)
	if model != "" {
		baseQuery = baseQuery.Where("EXISTS (SELECT 1 FROM unnest("+modelsCol+") AS entry(model) WHERE entry.model LIKE ?)", "%"+model+"%")
	}

	subQuery := baseQuery.
		Select("tag").
		Where("tag != ''").
		Order(order)

	err := r.db.Table("(?) as sub", subQuery).
		Select("DISTINCT tag").
		Find(&tags).Error

	if err != nil {
		return nil, err
	}

	return tags, nil
}

func (r *Runtime) GetChannelsByIds(ids []int) ([]*Channel, error) {
	if r.snapshot.readOnly {
		channels := make([]*Channel, 0, len(ids))
		seen := make(map[int]bool)
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			channel, err := r.GetChannelById(id, true)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			channels = append(channels, channel)
		}
		return channels, nil
	}
	var channels []*Channel
	err := r.db.Where("id in (?)", ids).Find(&channels).Error
	if err != nil {
		return nil, err
	}
	return channels, r.applyRuntimeStateToChannels(channels, true)
}

func (r *Runtime) BatchSetChannelTag(ids []int, tag *string) error {
	// 开启事务
	tx := r.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	// 更新标签
	err := tx.Model(&Channel{}).Where("id in (?)", ids).Update("tag", tag).Error
	if err != nil {
		tx.Rollback()
		return err
	}

	// update ability status
	channels, err := r.GetChannelsByIds(ids)
	if err != nil {
		tx.Rollback()
		return err
	}

	for _, channel := range channels {
		err = r.UpdateAbilities(channel, tx)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	// 提交事务
	return tx.Commit().Error
}

// CountAllChannels returns total channels in r.db
func (r *Runtime) CountAllChannels() (int64, error) {
	var total int64
	err := r.db.Model(&Channel{}).Count(&total).Error
	return total, err
}

// CountAllTags returns number of non-empty distinct tags
func (r *Runtime) CountAllTags() (int64, error) {
	return r.CountChannelTags(r.db.Model(&Channel{}))
}

func (r *Runtime) CountChannelTags(query *gorm.DB) (int64, error) {
	var total int64
	err := query.Where("tag is not null AND tag != ''").Distinct("tag").Count(&total).Error
	return total, err
}

// Get channels of specified type with pagination
func (r *Runtime) GetChannelsByType(startIdx int, num int, idSort bool, channelType int) ([]*Channel, error) {
	var channels []*Channel
	order := "priority desc"
	if idSort {
		order = "id desc"
	}
	err := r.db.Where("type = ?", channelType).Order(order).Limit(num).Offset(startIdx).Find(&channels).Error
	if err != nil {
		return nil, err
	}
	return channels, r.applyRuntimeStateToChannels(channels, false)
}

// Count channels of specific type
func (r *Runtime) CountChannelsByType(channelType int) (int64, error) {
	var count int64
	err := r.db.Model(&Channel{}).Where("type = ?", channelType).Count(&count).Error
	return count, err
}

// Return map[type]count for all channels
func (r *Runtime) CountChannelsGroupByType() (map[int64]int64, error) {
	type result struct {
		Type  int64 `gorm:"column:type"`
		Count int64 `gorm:"column:count"`
	}
	var results []result
	err := r.db.Model(&Channel{}).Select("type, count(*) as count").Group("type").Find(&results).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]int64)
	for _, r := range results {
		counts[r.Type] = r.Count
	}
	return counts, nil
}
