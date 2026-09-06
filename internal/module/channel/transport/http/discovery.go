package channelhttp

import (
	"fmt"
	"net/http"

	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"

	"github.com/QuantumNous/new-api/internal/shared/common"

	"github.com/gin-gonic/gin"
)

type applyChannelUpstreamModelUpdatesRequest struct {
	ID           int      `json:"id"`
	AddModels    []string `json:"add_models"`
	RemoveModels []string `json:"remove_models"`
	IgnoreModels []string `json:"ignore_models"`
}

type applyAllChannelUpstreamModelUpdatesResult struct {
	ChannelID             int      `json:"channel_id"`
	ChannelName           string   `json:"channel_name"`
	AddedModels           []string `json:"added_models"`
	RemovedModels         []string `json:"removed_models"`
	RemainingModels       []string `json:"remaining_models"`
	RemainingRemoveModels []string `json:"remaining_remove_models"`
}

type detectChannelUpstreamModelUpdatesResult struct {
	ChannelID       int      `json:"channel_id"`
	ChannelName     string   `json:"channel_name"`
	AddModels       []string `json:"add_models"`
	RemoveModels    []string `json:"remove_models"`
	LastCheckTime   int64    `json:"last_check_time"`
	AutoAddedModels int      `json:"auto_added_models"`
}

func (h *Handler) ApplyChannelUpstreamModelUpdates(c *gin.Context) {
	var req applyChannelUpstreamModelUpdatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.ID <= 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "invalid channel id",
		})
		return
	}

	channel, err := h.channel.GetChannelById(req.ID, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	beforeSettings := channel.GetOtherSettings()
	ignoredModels := channelmodule.IntersectModelNames(req.IgnoreModels, beforeSettings.UpstreamModelUpdateLastDetectedModels)

	addedModels, removedModels, remainingModels, remainingRemoveModels, modelsChanged, err := h.channel.ApplyUpstreamModelChanges(c.Request.Context(),
		channel,
		req.AddModels,
		req.IgnoreModels,
		req.RemoveModels,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if modelsChanged {
		h.channel.RefreshRuntimeCache()
	}

	h.recordAudit(c, "channel.upstream_apply", map[string]interface{}{
		"id": channel.Id,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"id":                      channel.Id,
			"added_models":            addedModels,
			"removed_models":          removedModels,
			"ignored_models":          ignoredModels,
			"remaining_models":        remainingModels,
			"remaining_remove_models": remainingRemoveModels,
			"models":                  channel.Models,
			"settings":                channel.OtherSettings,
		},
	})
}

func (h *Handler) DetectChannelUpstreamModelUpdates(c *gin.Context) {
	var req applyChannelUpstreamModelUpdatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.ID <= 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "invalid channel id",
		})
		return
	}

	channel, err := h.channel.GetChannelById(req.ID, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	settings := channel.GetOtherSettings()
	modelsChanged, autoAdded, err := h.channel.CheckUpstreamModels(c.Request.Context(), channel, &settings, true, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if modelsChanged {
		h.channel.RefreshRuntimeCache()
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": detectChannelUpstreamModelUpdatesResult{
			ChannelID:       channel.Id,
			ChannelName:     channel.Name,
			AddModels:       channelmodule.NormalizeModelNames(settings.UpstreamModelUpdateLastDetectedModels),
			RemoveModels:    channelmodule.NormalizeModelNames(settings.UpstreamModelUpdateLastRemovedModels),
			LastCheckTime:   settings.UpstreamModelUpdateLastCheckTime,
			AutoAddedModels: autoAdded,
		},
	})
}

func (h *Handler) ApplyAllChannelUpstreamModelUpdates(c *gin.Context) {
	results := make([]applyAllChannelUpstreamModelUpdatesResult, 0)
	failed := make([]int, 0)
	refreshNeeded := false
	addedModelCount := 0
	removedModelCount := 0

	lastID := 0
	for {
		channels, err := h.channel.EnabledChannelBatch(lastID, channelmodule.UpstreamModelUpdateBatchSize)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if len(channels) == 0 {
			break
		}
		lastID = channels[len(channels)-1].Id

		for _, channel := range channels {
			if channel == nil {
				continue
			}

			settings := channel.GetOtherSettings()
			if !settings.UpstreamModelUpdateCheckEnabled {
				continue
			}

			pendingAddModels, pendingRemoveModels := channelmodule.PendingApplyModelChanges(settings)
			if len(pendingAddModels) == 0 && len(pendingRemoveModels) == 0 {
				continue
			}

			addedModels, removedModels, remainingModels, remainingRemoveModels, modelsChanged, err := h.channel.ApplyUpstreamModelChanges(c.Request.Context(),
				channel,
				pendingAddModels,
				nil,
				pendingRemoveModels,
			)
			if err != nil {
				failed = append(failed, channel.Id)
				continue
			}
			if modelsChanged {
				refreshNeeded = true
			}
			addedModelCount += len(addedModels)
			removedModelCount += len(removedModels)
			results = append(results, applyAllChannelUpstreamModelUpdatesResult{
				ChannelID:             channel.Id,
				ChannelName:           channel.Name,
				AddedModels:           addedModels,
				RemovedModels:         removedModels,
				RemainingModels:       remainingModels,
				RemainingRemoveModels: remainingRemoveModels,
			})
		}

		if len(channels) < channelmodule.UpstreamModelUpdateBatchSize {
			break
		}
	}

	if refreshNeeded {
		h.channel.RefreshRuntimeCache()
	}

	h.recordAudit(c, "channel.upstream_apply_all", map[string]interface{}{
		"count": len(results),
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"processed_channels": len(results),
			"added_models":       addedModelCount,
			"removed_models":     removedModelCount,
			"failed_channel_ids": failed,
			"results":            results,
		},
	})
}

// DetectAllChannelUpstreamModelUpdates enqueues a model_update system task
// (manual variant) instead of scanning inline. Routing the manual trigger
// through the framework gives it the same cross-instance lease dedup and run
// history as the scheduled scan. If any model_update task is already active, the
// manual run is rejected so the caller does not mistake a scheduled run for this
// manual one.
func (h *Handler) DetectAllChannelUpstreamModelUpdates(c *gin.Context) {
	if h.hooks.EnqueueModelUpdate == nil {
		common.ApiError(c, fmt.Errorf("system task integration is not configured"))
		return
	}
	task, err := h.hooks.EnqueueModelUpdate()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !task.Created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有模型更新任务正在运行或等待中，不能启动本次手动任务",
			"data": gin.H{
				"task_id": task.TaskID,
				"status":  task.Status,
				"type":    task.Type,
			},
		})
		return
	}

	h.recordAudit(c, "channel.upstream_detect_all", map[string]interface{}{
		"task_id": task.TaskID,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
		},
	})
}
