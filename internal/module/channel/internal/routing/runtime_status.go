package routing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
)

// Channel runtime status is derived from the channel configuration and is
// shared by all application planes. The key pool fingerprint is part of the
// Redis key so a request using an older channel snapshot cannot change the
// state of a newly configured pool.
const channelRuntimeStatusPrefix = "channel:runtime:"

const (
	channelRuntimeStatusField       = "status"
	channelRuntimeReasonField       = "reason"
	channelRuntimeTimeField         = "time"
	channelRuntimeKeyStatusPrefix   = "key_status:"
	channelRuntimeKeyReasonPrefix   = "key_reason:"
	channelRuntimeKeyDisabledPrefix = "key_time:"
)

type channelRuntimeKeyState struct {
	Status       int
	DisabledTime int64
	Reason       string
}

type channelRuntimeState struct {
	Status       int
	DisabledTime int64
	Reason       string
	Keys         map[int]channelRuntimeKeyState
}

func normalizeChannelConfiguration(channel *Channel) {
	if channel == nil {
		return
	}
	// Auto key entries are runtime projections. Keep the top-level status as it
	// was stored: a persisted value must not be silently reclassified as a
	// manual decision or as enabled configuration.
	if !channel.ChannelInfo.IsMultiKey {
		return
	}
	for index, status := range channel.ChannelInfo.MultiKeyStatusList {
		if status == common.ChannelStatusAutoDisabled {
			delete(channel.ChannelInfo.MultiKeyStatusList, index)
			if channel.ChannelInfo.MultiKeyDisabledReason != nil {
				delete(channel.ChannelInfo.MultiKeyDisabledReason, index)
			}
			if channel.ChannelInfo.MultiKeyDisabledTime != nil {
				delete(channel.ChannelInfo.MultiKeyDisabledTime, index)
			}
		}
	}
}

func (r *Runtime) normalizeChannelForWrite(channel *Channel) error {
	if channel == nil {
		return errors.New("channel is nil")
	}
	if channel.Status == common.ChannelStatusAutoDisabled && !r.snapshot.readOnly && r.db != nil {
		configured, err := r.getConfiguredChannelByID(channel.Id, true)
		if err != nil {
			return err
		}
		switch configured.Status {
		case common.ChannelStatusEnabled, common.ChannelStatusManuallyDisabled:
			// The caller supplied a runtime projection obtained from
			// GetChannelById. Persist the latest configuration baseline only;
			// this also preserves a concurrent manual stop.
			info := channel.GetOtherInfo()
			configuredInfo := configured.GetOtherInfo()
			for _, field := range []string{"status_reason", "status_time"} {
				if value, exists := configuredInfo[field]; exists {
					info[field] = value
				} else {
					delete(info, field)
				}
			}
			channel.SetOtherInfo(info)
			channel.Status = configured.Status
		case common.ChannelStatusAutoDisabled:
			return errors.New("automatic channel status is runtime-only and cannot be persisted")
		default:
			return fmt.Errorf("invalid configured channel status %d", configured.Status)
		}
	}
	normalizeChannelConfiguration(channel)
	return nil
}

func (r *Runtime) applyChannelRuntimeState(channel *Channel) error {
	if channel == nil {
		return nil
	}
	normalizeChannelConfiguration(channel)
	state, err := r.readChannelRuntimeState(context.Background(), channel)
	if err != nil {
		return err
	}
	if channel.Status == common.ChannelStatusManuallyDisabled {
		// A manual channel stop always wins over an automatic state. Keep the
		// auto key details visible for administration, but never make the
		// channel appear enabled to routing.
		r.applyRuntimeKeyStates(channel, state)
		return nil
	}
	if state.Status == common.ChannelStatusAutoDisabled {
		channel.Status = common.ChannelStatusAutoDisabled
		info := channel.GetOtherInfo()
		if state.Reason != "" {
			info["status_reason"] = state.Reason
		}
		if state.DisabledTime != 0 {
			info["status_time"] = state.DisabledTime
		}
		channel.SetOtherInfo(info)
	}
	if !channel.ChannelInfo.IsMultiKey {
		return nil
	}
	r.applyRuntimeKeyStates(channel, state)
	if channel.Status == common.ChannelStatusEnabled && hasDisabledAutoKey(channel, state) && !hasEnabledMultiKey(channel.GetKeys(), channel.ChannelInfo.MultiKeyStatusList) {
		channel.Status = common.ChannelStatusAutoDisabled
		info := channel.GetOtherInfo()
		info["status_reason"] = "All keys are disabled"
		if disabledTime := latestAutomaticKeyTime(channel, state); disabledTime != 0 {
			info["status_time"] = disabledTime
		}
		channel.SetOtherInfo(info)
	}
	return nil
}

func (r *Runtime) applyRuntimeStateToChannels(channels []*Channel, selectAll bool) error {
	for _, channel := range channels {
		if err := r.applyChannelRuntimeState(channel); err != nil {
			return err
		}
		if !selectAll {
			channel.Key = ""
			channel.Keys = nil
		}
	}
	return nil
}

func (r *Runtime) applyRuntimeKeyStates(channel *Channel, state channelRuntimeState) {
	if channel == nil || !channel.ChannelInfo.IsMultiKey || len(state.Keys) == 0 {
		return
	}
	if channel.ChannelInfo.MultiKeyStatusList == nil {
		channel.ChannelInfo.MultiKeyStatusList = make(map[int]int)
	}
	if channel.ChannelInfo.MultiKeyDisabledReason == nil {
		channel.ChannelInfo.MultiKeyDisabledReason = make(map[int]string)
	}
	if channel.ChannelInfo.MultiKeyDisabledTime == nil {
		channel.ChannelInfo.MultiKeyDisabledTime = make(map[int]int64)
	}
	for index, keyState := range state.Keys {
		if keyState.Status != common.ChannelStatusAutoDisabled {
			continue
		}
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status == common.ChannelStatusManuallyDisabled {
			continue
		}
		channel.ChannelInfo.MultiKeyStatusList[index] = keyState.Status
		channel.ChannelInfo.MultiKeyDisabledReason[index] = keyState.Reason
		channel.ChannelInfo.MultiKeyDisabledTime[index] = keyState.DisabledTime
	}
}

func hasDisabledAutoKey(channel *Channel, state channelRuntimeState) bool {
	for index, keyState := range state.Keys {
		if keyState.Status == common.ChannelStatusAutoDisabled {
			if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status == common.ChannelStatusManuallyDisabled {
				continue
			}
			return true
		}
	}
	return false
}

func latestAutomaticKeyTime(channel *Channel, state channelRuntimeState) int64 {
	var latest int64
	for index, keyState := range state.Keys {
		if keyState.Status != common.ChannelStatusAutoDisabled {
			continue
		}
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status == common.ChannelStatusManuallyDisabled {
			continue
		}
		if keyState.DisabledTime > latest {
			latest = keyState.DisabledTime
		}
	}
	return latest
}

func channelKeyPoolFingerprint(keys []string) (string, error) {
	payload, err := common.Marshal(keys)
	if err != nil {
		return "", err
	}
	return common.GenerateHMACWithKey([]byte("channel-runtime:"+common.CryptoSecret), string(payload)), nil
}

// ChannelKeyPoolFingerprint identifies the exact key pool represented by a
// channel snapshot. It contains no key material and is carried with channel
// errors so a delayed request cannot update a newer pool that happens to reuse
// one key or index.
func ChannelKeyPoolFingerprint(channel *Channel) string {
	if channel == nil {
		return ""
	}
	fingerprint, _ := channelKeyPoolFingerprint(channel.GetKeys())
	return fingerprint
}

func channelRuntimeStatusKey(channelID int, fingerprint string) string {
	return channelRuntimeStatusPrefix + strconv.Itoa(channelID) + ":" + fingerprint
}

func channelRuntimeKeyFields(index int) (string, string, string) {
	indexString := strconv.Itoa(index)
	return channelRuntimeKeyStatusPrefix + indexString, channelRuntimeKeyReasonPrefix + indexString, channelRuntimeKeyDisabledPrefix + indexString
}

func (r *Runtime) channelRuntimeStatusKey(channel *Channel) (string, error) {
	if channel == nil {
		return "", errors.New("channel is nil")
	}
	fingerprint, err := channelKeyPoolFingerprint(channel.GetKeys())
	if err != nil {
		return "", err
	}
	return channelRuntimeStatusKey(channel.Id, fingerprint), nil
}

func (r *Runtime) readChannelRuntimeState(ctx context.Context, channel *Channel) (channelRuntimeState, error) {
	state := channelRuntimeState{Keys: make(map[int]channelRuntimeKeyState)}
	if r.cache == nil {
		if r.snapshot.readOnly {
			return state, errors.New("DragonflyDB is required for channel runtime status")
		}
		return state, nil
	}
	key, err := r.channelRuntimeStatusKey(channel)
	if err != nil {
		return state, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	values, err := r.cache.HGetAll(ctx, key).Result()
	if err != nil {
		return state, err
	}
	state.Status, err = runtimeStatusInt(values[channelRuntimeStatusField])
	if err != nil {
		return state, err
	}
	state.Reason = values[channelRuntimeReasonField]
	state.DisabledTime, err = runtimeStatusInt64(values[channelRuntimeTimeField])
	if err != nil {
		return state, err
	}
	for field, value := range values {
		if !strings.HasPrefix(field, channelRuntimeKeyStatusPrefix) {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(field, channelRuntimeKeyStatusPrefix))
		if err != nil || index < 0 {
			return state, fmt.Errorf("invalid channel runtime key index %q", field)
		}
		status, err := runtimeStatusInt(value)
		if err != nil {
			return state, err
		}
		_, reasonField, timeField := channelRuntimeKeyFields(index)
		disabledTime, err := runtimeStatusInt64(values[timeField])
		if err != nil {
			return state, err
		}
		state.Keys[index] = channelRuntimeKeyState{Status: status, DisabledTime: disabledTime, Reason: values[reasonField]}
	}
	return state, nil
}

func runtimeStatusInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid channel runtime status %q", value)
	}
	return parsed, nil
}

func runtimeStatusInt64(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid channel runtime timestamp %q", value)
	}
	return parsed, nil
}

// Channel runtime state is intentionally changed by a script. A read followed
// by HSET would allow concurrent data-plane errors to lose one another's key
// state. The script also deletes an empty hash so replacing a key pool does not
// leave an immortal empty marker behind.
var updateChannelRuntimeStatus = redis.NewScript(`
local mode = ARGV[1]
local index = tonumber(ARGV[2])
local statusField = ARGV[3]
local reasonField = ARGV[4]
local timeField = ARGV[5]
local status = ARGV[6]
local reason = ARGV[7]
local now = ARGV[8]

local current
if index >= 0 then
    current = redis.call('HGET', KEYS[1], statusField)
else
    current = redis.call('HGET', KEYS[1], 'status')
end

if mode == 'disable' then
    if current == status then return 0 end
    if index >= 0 then
        redis.call('HSET', KEYS[1], statusField, status, reasonField, reason, timeField, now)
    else
        redis.call('HSET', KEYS[1], 'status', status, 'reason', reason, 'time', now)
    end
else
    if current ~= status then return 0 end
    if index >= 0 then
        redis.call('HDEL', KEYS[1], statusField, reasonField, timeField)
    else
        redis.call('HDEL', KEYS[1], 'status', 'reason', 'time')
    end
end

if redis.call('HLEN', KEYS[1]) == 0 then redis.call('DEL', KEYS[1]) end
return 1
`)

var clearChannelRuntimeStatus = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
return redis.call('DEL', KEYS[1])
`)

var clearChannelRuntimeKeyStatus = redis.NewScript(`
redis.call('HDEL', KEYS[1], ARGV[1], ARGV[2], ARGV[3])
if redis.call('HLEN', KEYS[1]) == 0 then redis.call('DEL', KEYS[1]) end
return 1
`)

func (r *Runtime) changeChannelRuntimeStatus(channel *Channel, usingKey string, status int, reason string) (bool, error) {
	if r.cache == nil {
		return false, errors.New("DragonflyDB is required for channel runtime status")
	}
	key, err := r.channelRuntimeStatusKey(channel)
	if err != nil {
		return false, err
	}
	index := -1
	if channel.ChannelInfo.IsMultiKey && usingKey != "" {
		for i, candidate := range channel.GetKeys() {
			if candidate == usingKey {
				index = i
				break
			}
		}
		if index < 0 {
			// The request may have used a previous key pool. Refuse the update
			// instead of applying its index to the current pool.
			return false, nil
		}
	}
	mode := "disable"
	if status == common.ChannelStatusEnabled {
		mode = "enable"
	} else if status != common.ChannelStatusAutoDisabled {
		return false, fmt.Errorf("unsupported runtime channel status %d", status)
	}
	statusField, reasonField, timeField := "", "", ""
	if index >= 0 {
		statusField, reasonField, timeField = channelRuntimeKeyFields(index)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := updateChannelRuntimeStatus.Run(ctx, r.cache, []string{key}, mode, index, statusField, reasonField, timeField, common.ChannelStatusAutoDisabled, reason, common.GetTimestamp()).Int()
	return result == 1, err
}

func (r *Runtime) clearChannelRuntimeState(channel *Channel) error {
	if r.cache == nil {
		if r.snapshot.readOnly {
			return errors.New("DragonflyDB is required for channel runtime status")
		}
		return nil
	}
	key, err := r.channelRuntimeStatusKey(channel)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return clearChannelRuntimeStatus.Run(ctx, r.cache, []string{key}).Err()
}

func (r *Runtime) clearChannelRuntimeKeyState(channel *Channel, index int) error {
	if r.cache == nil {
		if r.snapshot.readOnly {
			return errors.New("DragonflyDB is required for channel runtime status")
		}
		return nil
	}
	key, err := r.channelRuntimeStatusKey(channel)
	if err != nil {
		return err
	}
	statusField, reasonField, timeField := channelRuntimeKeyFields(index)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return clearChannelRuntimeKeyStatus.Run(ctx, r.cache, []string{key}, statusField, reasonField, timeField).Err()
}

// ClearChannelRuntimeKeyStatus is used by an administrator explicitly
// re-enabling a key. Automatic recovery only clears the automatic marker when
// the provider reports success; this method lets the management operation
// clear the same marker immediately.
func (r *Runtime) ClearChannelRuntimeKeyStatus(channel *Channel, index int) error {
	if channel == nil || !channel.ChannelInfo.IsMultiKey || index < 0 || index >= len(channel.GetKeys()) {
		return errors.New("invalid channel key index")
	}
	return r.clearChannelRuntimeKeyState(channel, index)
}

// ClearChannelRuntimeStatus clears automatic state for the current key pool.
// It is used by explicit manual channel enable/disable operations.
func (r *Runtime) ClearChannelRuntimeStatus(channel *Channel) error {
	return r.clearChannelRuntimeState(channel)
}
