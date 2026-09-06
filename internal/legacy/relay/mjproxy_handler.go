package relay

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/httpclient"

	"github.com/QuantumNous/new-api/internal/config/setting"
	"github.com/QuantumNous/new-api/internal/config/setting/system_setting"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	relayconstant "github.com/QuantumNous/new-api/internal/legacy/relay/constant"
	"github.com/QuantumNous/new-api/internal/legacy/relay/helper"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/shared/dto"

	"github.com/gin-gonic/gin"
)

func writeMidjourneyResponse(c *gin.Context, statusCode int, body []byte) *dto.MidjourneyResponse {
	c.Writer.WriteHeader(statusCode)
	if _, err := io.Copy(c.Writer, bytes.NewBuffer(body)); err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
	}
	return nil
}

func RelayMidjourneyImage(c *gin.Context) {
	taskId := c.Param("id")
	midjourneyTask := model.GetByOnlyMJId(taskId)
	if midjourneyTask == nil {
		c.JSON(400, gin.H{
			"error": "midjourney_task_not_found",
		})
		return
	}
	var httpClient *http.Client
	var proxy string
	if channel, err := model.CacheGetChannel(midjourneyTask.ChannelId); err == nil {
		proxy = channel.GetSetting().Proxy
		if proxy != "" {
			if httpClient, err = httpclient.GetHttpClientWithProxy(proxy); err != nil {
				c.JSON(400, gin.H{
					"error": "proxy_url_invalid",
				})
				return
			}
		}
	}
	if httpClient == nil {
		httpClient = httpclient.GetSSRFProtectedHTTPClient()
	}
	var validateErr error
	if proxy == "" {
		validateErr = httpclient.ValidateSSRFProtectedFetchURL(midjourneyTask.ImageUrl)
	} else {
		// 渠道代理路径的连接由代理侧建立，无法做拨号时逐 IP 校验，
		// 因此保留请求前的一次性 SSRF 校验。
		fetchSetting := system_setting.GetFetchSetting()
		validateErr = common.ValidateURLWithFetchSetting(midjourneyTask.ImageUrl, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
	}
	if validateErr != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("request blocked: %v", validateErr),
		})
		return
	}
	resp, err := httpClient.Get(midjourneyTask.ImageUrl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		c.JSON(resp.StatusCode, gin.H{
			"error": string(responseBody),
		})
		return
	}
	// 从Content-Type头获取MIME类型
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		// 如果无法确定内容类型，则默认为jpeg
		contentType = "image/jpeg"
	}
	// 设置响应的内容类型
	c.Writer.Header().Set("Content-Type", contentType)
	// 将图片流式传输到响应体
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		log.Println("Failed to stream image:", err)
	}
	return
}

func RelayMidjourneyNotify(c *gin.Context) *dto.MidjourneyResponse {
	var midjRequest dto.MidjourneyDto
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "bind_request_body_failed",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTask := model.GetByOnlyMJId(midjRequest.MjId)
	if midjourneyTask == nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "midjourney_task_not_found",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTask.Progress = midjRequest.Progress
	midjourneyTask.PromptEn = midjRequest.PromptEn
	midjourneyTask.State = midjRequest.State
	midjourneyTask.SubmitTime = midjRequest.SubmitTime
	midjourneyTask.StartTime = midjRequest.StartTime
	midjourneyTask.FinishTime = midjRequest.FinishTime
	midjourneyTask.ImageUrl = midjRequest.ImageUrl
	midjourneyTask.VideoUrl = midjRequest.VideoUrl
	videoUrlsStr, _ := common.Marshal(midjRequest.VideoUrls)
	midjourneyTask.VideoUrls = string(videoUrlsStr)
	midjourneyTask.Status = midjRequest.Status
	midjourneyTask.FailReason = midjRequest.FailReason
	err = midjourneyTask.UpdateNotifyState()
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "update_midjourney_task_failed",
		}
	}

	return nil
}

func coverMidjourneyTaskDto(c *gin.Context, originTask *model.Midjourney) (midjourneyTask dto.MidjourneyDto) {
	midjourneyTask.MjId = originTask.MjId
	midjourneyTask.Progress = originTask.Progress
	midjourneyTask.PromptEn = originTask.PromptEn
	midjourneyTask.State = originTask.State
	midjourneyTask.SubmitTime = originTask.SubmitTime
	midjourneyTask.StartTime = originTask.StartTime
	midjourneyTask.FinishTime = originTask.FinishTime
	midjourneyTask.ImageUrl = ""
	if originTask.ImageUrl != "" && setting.MjForwardUrlEnabled {
		midjourneyTask.ImageUrl = system_setting.ServerAddress + "/mj/image/" + originTask.MjId
		if originTask.Status != "SUCCESS" {
			midjourneyTask.ImageUrl += "?rand=" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	} else {
		midjourneyTask.ImageUrl = originTask.ImageUrl
	}
	if originTask.VideoUrl != "" {
		midjourneyTask.VideoUrl = originTask.VideoUrl
	}
	midjourneyTask.Status = originTask.Status
	midjourneyTask.FailReason = originTask.FailReason
	midjourneyTask.Action = originTask.Action
	midjourneyTask.Description = originTask.Description
	midjourneyTask.Prompt = originTask.Prompt
	if originTask.Buttons != "" {
		var buttons []dto.ActionButton
		err := common.Unmarshal([]byte(originTask.Buttons), &buttons)
		if err == nil {
			midjourneyTask.Buttons = buttons
		}
	}
	if originTask.VideoUrls != "" {
		var videoUrls []dto.ImgUrls
		err := common.Unmarshal([]byte(originTask.VideoUrls), &videoUrls)
		if err == nil {
			midjourneyTask.VideoUrls = videoUrls
		}
	}
	if originTask.Properties != "" {
		var properties dto.Properties
		err := common.Unmarshal([]byte(originTask.Properties), &properties)
		if err == nil {
			midjourneyTask.Properties = &properties
		}
	}
	return
}

func RelaySwapFace(c *gin.Context, info *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	var swapFaceRequest dto.SwapFaceRequest
	err := common.UnmarshalBodyReusable(c, &swapFaceRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	info.InitChannelMeta(c)

	if swapFaceRequest.SourceBase64 == "" || swapFaceRequest.TargetBase64 == "" {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "sour_base64_and_target_base64_is_required")
	}
	modelName := service.CovertMjpActionToModelName(constant.MjActionSwapFace)

	priceData, err := helper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: err.Error(),
		}
	}
	info.ForcePreConsume = true
	billingPrepared, billingErr := service.PreConsumeMidjourneyBilling(c, info, priceData.Quota)
	if billingErr != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "pre_consume_midjourney_billing_failed")
	}
	midjourneyTask := &model.Midjourney{
		UserId:     info.UserId,
		Action:     constant.MjActionSwapFace,
		Prompt:     "InsightFace",
		SubmitTime: info.StartTime.UnixNano() / int64(time.Millisecond),
		StartTime:  time.Now().UnixNano() / int64(time.Millisecond),
		Progress:   "0%",
		ChannelId:  c.GetInt("channel_id"),
	}
	if _, err := service.PrepareMidjourneyTaskBilling(info, midjourneyTask, priceData.Quota, billingPrepared); err != nil {
		_ = service.RefundMidjourneyBilling(c.Request.Context(), info)
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "prepare_midjourney_billing_failed")
	}
	if billingPrepared {
		if err := service.MarkMidjourneyDispatch(c.Request.Context(), info, midjourneyTask.GetBillingChannelId()); err != nil {
			_ = service.RefundMidjourneyBilling(c.Request.Context(), info)
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "mark_midjourney_dispatch_failed")
		}
	}
	requestURL := getMjRequestPath(c.Request.URL.String())
	baseURL := c.GetString("base_url")
	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)
	mjResp, _, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游响应未知，无法确认 swap face 任务是否创建"); persistErr != nil {
			logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
		}
		return &mjResp.Response
	}
	midjResponse := &mjResp.Response
	midjourneyTask.Code = midjResponse.Code
	midjourneyTask.MjId = midjResponse.Result
	midjourneyTask.Description = midjResponse.Description
	if mjResp.StatusCode != http.StatusOK {
		if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游 HTTP 状态异常，无法确认 swap face 任务是否创建"); persistErr != nil {
			logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
		}
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_outcome_unknown")
	}
	if midjResponse.Code != 1 {
		if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), info); refundErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_refund_pending")
		}
		c.Writer.WriteHeader(mjResp.StatusCode)
		respBody, marshalErr := common.Marshal(midjResponse)
		if marshalErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "marshal_response_body_failed")
		}
		_, copyErr := io.Copy(c.Writer, bytes.NewBuffer(respBody))
		if copyErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
		}
		return nil
	}
	if strings.TrimSpace(midjourneyTask.MjId) == "" {
		if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游成功响应缺少稳定任务 ID"); persistErr != nil {
			logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
		}
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_identity_unknown")
	}
	if existing := model.GetByOnlyMJId(midjourneyTask.MjId); existing != nil {
		if existing.UserId != info.UserId || existing.ChannelId != midjourneyTask.ChannelId {
			if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游任务 ID 已属于其他用户"); persistErr != nil {
				logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
			}
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_identity_conflict")
		}
		if billingPrepared && existing.BillingRequestID == info.RequestId {
			if settled, settleErr := service.SettleMidjourneyTaskBilling(info, existing, true); settleErr != nil || !settled {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_settlement_pending")
			}
			body, marshalErr := common.Marshal(midjResponse)
			if marshalErr != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "marshal_response_body_failed")
			}
			return writeMidjourneyResponse(c, mjResp.StatusCode, body)
		}
		if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), info); refundErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_refund_pending")
		}
		c.Writer.WriteHeader(mjResp.StatusCode)
		respBody, marshalErr := common.Marshal(midjResponse)
		if marshalErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "marshal_response_body_failed")
		}
		_, copyErr := io.Copy(c.Writer, bytes.NewBuffer(respBody))
		if copyErr != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
		}
		return nil
	}
	err = service.PersistMidjourneyTaskWithBilling(midjourneyTask, billingPrepared)
	if err != nil {
		if existing := model.GetByOnlyMJId(midjourneyTask.MjId); existing != nil {
			if existing.UserId == info.UserId && existing.ChannelId == midjourneyTask.ChannelId && billingPrepared && existing.BillingRequestID == info.RequestId {
				if settled, settleErr := service.SettleMidjourneyTaskBilling(info, existing, true); settleErr == nil && settled {
					body, marshalErr := common.Marshal(midjResponse)
					if marshalErr == nil {
						return writeMidjourneyResponse(c, mjResp.StatusCode, body)
					}
				}
			} else if existing.UserId == info.UserId && existing.ChannelId == midjourneyTask.ChannelId && billingPrepared {
				if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), info); refundErr == nil {
					body, marshalErr := common.Marshal(midjResponse)
					if marshalErr == nil {
						return writeMidjourneyResponse(c, mjResp.StatusCode, body)
					}
				}
			} else if billingPrepared {
				if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "本地任务写入失败，需核对上游 swap face 结果"); persistErr != nil {
					logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
				}
			}
		} else if billingPrepared {
			if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "本地任务写入失败，需核对上游 swap face 结果"); persistErr != nil {
				logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
			}
		}
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "insert_midjourney_task_failed")
	}
	billingApplied, billingErr := service.SettleMidjourneyTaskBilling(info, midjourneyTask, billingPrepared)
	if billingPrepared && (billingErr != nil || !billingApplied) {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_settlement_pending")
	}
	if billingApplied {
		billingChannelId := midjourneyTask.GetBillingChannelId()
		tokenName := c.GetString("token_name")
		logContent := fmt.Sprintf("模型固定价格 %.2f，分组倍率 %.2f，操作 %s", priceData.ModelPrice, priceData.GroupRatioInfo.GroupRatio, constant.MjActionSwapFace)
		other := service.GenerateMjOtherInfo(info, priceData)
		model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
			ChannelId: billingChannelId,
			ModelName: modelName,
			TokenName: tokenName,
			Quota:     midjourneyTask.Quota,
			Content:   logContent,
			TokenId:   midjourneyTask.TokenId,
			Group:     info.UsingGroup,
			Other:     other,
		})
	}
	c.Writer.WriteHeader(mjResp.StatusCode)
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
	}
	return nil
}

func RelayMidjourneyTaskImageSeed(c *gin.Context) *dto.MidjourneyResponse {
	taskId := c.Param("id")
	userId := c.GetInt("id")
	originTask := model.GetByMJId(userId, taskId)
	if originTask == nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_no_found")
	}
	channel, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
	}
	if channel.Status != common.ChannelStatusEnabled {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
	}
	c.Set("channel_id", originTask.ChannelId)
	c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))

	requestURL := getMjRequestPath(c.Request.URL.String())
	fullRequestURL := fmt.Sprintf("%s%s", channel.GetBaseURL(), requestURL)
	midjResponseWithStatus, _, err := service.DoMidjourneyHttpRequest(c, time.Second*30, fullRequestURL)
	if err != nil {
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	service.IOCopyBytesGracefully(c, nil, respBody)
	return nil
}

func RelayMidjourneyTask(c *gin.Context, relayMode int) *dto.MidjourneyResponse {
	userId := c.GetInt("id")
	var err error
	var respBody []byte
	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch:
		taskId := c.Param("id")
		originTask := model.GetByMJId(userId, taskId)
		if originTask == nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "task_no_found",
			}
		}
		midjourneyTask := coverMidjourneyTaskDto(c, originTask)
		respBody, err = common.Marshal(midjourneyTask)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	case relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		var condition = struct {
			IDs []string `json:"ids"`
		}{}
		err = common.UnmarshalBodyReusable(c, &condition)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "do_request_failed",
			}
		}
		var tasks []dto.MidjourneyDto
		if len(condition.IDs) != 0 {
			originTasks := model.GetByMJIds(userId, condition.IDs)
			for _, originTask := range originTasks {
				midjourneyTask := coverMidjourneyTaskDto(c, originTask)
				tasks = append(tasks, midjourneyTask)
			}
		}
		if tasks == nil {
			tasks = make([]dto.MidjourneyDto, 0)
		}
		respBody, err = common.Marshal(tasks)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "application/json")

	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	return nil
}

func RelayMidjourneySubmit(c *gin.Context, relayInfo *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	consumeQuota := true
	var midjRequest dto.MidjourneyRequest
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	relayInfo.InitChannelMeta(c)
	// Keep the pool identity captured alongside the selected request key.
	// Re-reading a channel here could observe a newer pool than the request uses.
	runtimeChannelID := relayInfo.ChannelId
	runtimeKeyPoolFingerprint := common.GetContextKeyString(c, constant.ContextKeyChannelKeyPoolFingerprint)
	runtimeAutoBan := common.GetContextKeyBool(c, constant.ContextKeyChannelAutoBan)

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyAction { // midjourney plus，需要从customId中获取任务信息
		mjErr := service.CoverPlusActionToNormalAction(&midjRequest)
		if mjErr != nil {
			return mjErr
		}
		relayInfo.RelayMode = relayconstant.RelayModeMidjourneyChange
	}
	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
		midjRequest.Action = constant.MjActionVideo
	}

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyImagine { //绘画任务，此类任务可重复
		if midjRequest.Prompt == "" {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "prompt_is_required")
		}
		midjRequest.Action = constant.MjActionImagine
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyDescribe { //按图生文任务，此类任务可重复
		midjRequest.Action = constant.MjActionDescribe
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyEdits { //编辑任务，此类任务可重复
		midjRequest.Action = constant.MjActionEdits
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyShorten { //缩短任务，此类任务可重复，plus only
		midjRequest.Action = constant.MjActionShorten
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyBlend { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionBlend
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyUpload { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionUpload
	} else if midjRequest.TaskId != "" { //放大、变换任务，此类任务，如果重复且已有结果，远端api会直接返回最终结果
		mjId := ""
		if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyChange {
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			} else if midjRequest.Index == 0 {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
			}
			//action = midjRequest.Action
			mjId = midjRequest.TaskId
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneySimpleChange {
			if midjRequest.Content == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_is_required")
			}
			params := service.ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_parse_failed")
			}
			mjId = params.TaskId
			midjRequest.Action = params.Action
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyModal {
			//if midjRequest.MaskBase64 == "" {
			//	return service.MidjourneyErrorWrapper(constant.MjRequestError, "mask_base64_is_required")
			//}
			mjId = midjRequest.TaskId
			midjRequest.Action = constant.MjActionModal
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
			midjRequest.Action = constant.MjActionVideo
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			}
			mjId = midjRequest.TaskId
		}

		originTask := model.GetByMJId(relayInfo.UserId, mjId)
		if originTask == nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_not_found")
		} else { //原任务的Status=SUCCESS，则可以做放大UPSCALE、变换VARIATION等动作，此时必须使用原来的请求地址才能正确处理
			if setting.MjActionCheckSuccessEnabled {
				if originTask.Status != "SUCCESS" && relayInfo.RelayMode != relayconstant.RelayModeMidjourneyModal {
					return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_status_not_success")
				}
			}
			channel, err := model.GetChannelById(originTask.ChannelId, true)
			if err != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
			}
			if channel.Status != common.ChannelStatusEnabled {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
			}
			c.Set("base_url", channel.GetBaseURL())
			c.Set("channel_id", originTask.ChannelId)
			c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))
			runtimeChannelID = originTask.ChannelId
			runtimeKeyPoolFingerprint = model.ChannelKeyPoolFingerprint(channel)
			runtimeAutoBan = channel.GetAutoBan()
			logger.LogDebug(c, "Midjourney action uses origin channel: id=%s, base_url=%s", strconv.Itoa(originTask.ChannelId), channel.GetBaseURL())
		}
		midjRequest.Prompt = originTask.Prompt

		//if channelType == common.ChannelTypeMidjourneyPlus {
		//	// plus
		//} else {
		//	// 普通版渠道
		//
		//}
	}

	if midjRequest.Action == constant.MjActionInPaint || midjRequest.Action == constant.MjActionCustomZoom {
		consumeQuota = false
	}

	//baseURL := common.ChannelBaseURLs[channelType]
	requestURL := getMjRequestPath(c.Request.URL.String())

	baseURL := c.GetString("base_url")

	//midjRequest.NotifyHook = "http://127.0.0.1:3000/mj/notify"

	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)

	modelName := service.CovertMjpActionToModelName(midjRequest.Action)

	priceData, err := helper.ModelPriceHelperPerCall(c, relayInfo)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: err.Error(),
		}
	}

	billingPrepared := false
	if consumeQuota {
		relayInfo.ForcePreConsume = true
		billingPrepared, err = service.PreConsumeMidjourneyBilling(c, relayInfo, priceData.Quota)
		if err != nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "pre_consume_midjourney_billing_failed")
		}
	}
	midjourneyTask := &model.Midjourney{
		UserId:     relayInfo.UserId,
		Action:     midjRequest.Action,
		Prompt:     midjRequest.Prompt,
		SubmitTime: time.Now().UnixNano() / int64(time.Millisecond),
		Progress:   "0%",
		ChannelId:  c.GetInt("channel_id"),
	}
	if _, err := service.PrepareMidjourneyTaskBilling(relayInfo, midjourneyTask, priceData.Quota, billingPrepared); err != nil {
		_ = service.RefundMidjourneyBilling(c.Request.Context(), relayInfo)
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "prepare_midjourney_billing_failed")
	}
	if billingPrepared {
		if err := service.MarkMidjourneyDispatch(c.Request.Context(), relayInfo, midjourneyTask.GetBillingChannelId()); err != nil {
			_ = service.RefundMidjourneyBilling(c.Request.Context(), relayInfo)
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "mark_midjourney_dispatch_failed")
		}
	}

	midjResponseWithStatus, responseBody, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		if billingPrepared {
			if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游响应未知，无法确认 Midjourney 任务是否创建"); persistErr != nil {
				logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
			}
		}
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response

	// 文档：https://github.com/novicezk/midjourney-proxy/blob/main/docs/api.md
	//1-提交成功
	// 21-任务已存在（处理中或者有结果了） {"code":21,"description":"任务已存在","result":"0741798445574458","properties":{"status":"SUCCESS","imageUrl":"https://xxxx"}}
	// 22-排队中 {"code":22,"description":"排队中，前面还有1个任务","result":"0741798445574458","properties":{"numberOfQueues":1,"discordInstanceId":"1118138338562560102"}}
	// 23-队列已满，请稍后再试 {"code":23,"description":"队列已满，请稍后尝试","result":"14001929738841620","properties":{"discordInstanceId":"1118138338562560102"}}
	// 24-prompt包含敏感词 {"code":24,"description":"可能包含敏感词","properties":{"promptEn":"nude body","bannedWord":"nude"}}
	// other: 提交错误，description为错误描述
	providerCode := midjResponse.Code
	midjourneyTask.Code = providerCode
	midjourneyTask.MjId = midjResponse.Result
	midjourneyTask.Description = midjResponse.Description
	if midjResponse.Code == 3 {
		//无实例账号自动禁用渠道（No available account instance）
		if runtimeKeyPoolFingerprint != "" && runtimeAutoBan && common.AutomaticDisableChannelEnabled {
			model.UpdateChannelStatusForKeyPool(runtimeChannelID, "", common.ChannelStatusAutoDisabled, "No available account instance", runtimeKeyPoolFingerprint)
		}
	}
	if midjResponse.Code != 1 && midjResponse.Code != 21 && midjResponse.Code != 22 {
		//非1-提交成功,21-任务已存在和22-排队中，则记录错误原因
		midjourneyTask.FailReason = midjResponse.Description
		consumeQuota = false
	}

	if midjResponse.Code == 21 { //21-任务已存在（处理中或者有结果了）
		// 将 properties 转换为一个 map
		properties, ok := midjResponse.Properties.(map[string]interface{})
		if ok {
			imageUrl, ok1 := properties["imageUrl"].(string)
			status, ok2 := properties["status"].(string)
			if ok1 && ok2 {
				midjourneyTask.ImageUrl = imageUrl
				midjourneyTask.Status = status
				if status == "SUCCESS" {
					midjourneyTask.Progress = "100%"
					midjourneyTask.StartTime = time.Now().UnixNano() / int64(time.Millisecond)
					midjourneyTask.FinishTime = time.Now().UnixNano() / int64(time.Millisecond)
					midjResponse.Code = 1
				}
			}
		}
		//修改返回值
		if midjRequest.Action != constant.MjActionInPaint && midjRequest.Action != constant.MjActionCustomZoom {
			newBody := strings.Replace(string(responseBody), `"code":21`, `"code":1`, -1)
			responseBody = []byte(newBody)
		}
	}
	if midjResponse.Code == 1 && midjRequest.Action == "UPLOAD" {
		midjourneyTask.Progress = "100%"
		midjourneyTask.Status = "SUCCESS"
	}
	accepted := providerCode == 1 || providerCode == 21 || providerCode == 22
	if midjResponseWithStatus.StatusCode != http.StatusOK {
		accepted = false
	}
	if billingPrepared && midjResponseWithStatus.StatusCode != http.StatusOK {
		if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游 HTTP 状态异常，无法确认 Midjourney 任务是否创建"); persistErr != nil {
			logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
		}
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_outcome_unknown")
	}
	if !accepted {
		if billingPrepared {
			if strings.TrimSpace(midjourneyTask.MjId) != "" {
				if existing := model.GetByOnlyMJId(midjourneyTask.MjId); existing != nil && existing.UserId != relayInfo.UserId || existing.ChannelId != midjourneyTask.ChannelId {
					if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游错误响应的任务 ID 已属于其他用户"); persistErr != nil {
						logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
					}
					return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_identity_conflict")
				}
			}
			if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), relayInfo); refundErr != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_refund_pending")
			}
		}
		if !billingPrepared {
			midjourneyTask.BillingPending = false
			midjourneyTask.BillingAction = ""
			midjourneyTask.BillingOperationID = ""
			midjourneyTask.BillingTargetQuota = 0
			midjourneyTask.BillingDelta = 0
			if err := service.PersistMidjourneyTaskWithBilling(midjourneyTask, false); err != nil {
				return &dto.MidjourneyResponse{Code: 4, Description: "insert_midjourney_task_failed"}
			}
		}
		return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
	}
	if strings.TrimSpace(midjourneyTask.MjId) == "" {
		if billingPrepared {
			if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游成功响应缺少稳定任务 ID"); persistErr != nil {
				logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
			}
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_identity_unknown")
		}
		return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
	}
	if existing := model.GetByOnlyMJId(midjourneyTask.MjId); existing != nil {
		if existing.UserId != relayInfo.UserId || existing.ChannelId != midjourneyTask.ChannelId {
			if billingPrepared {
				if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "上游任务 ID 已属于其他用户"); persistErr != nil {
					logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
				}
			}
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_task_identity_conflict")
		}
		if billingPrepared && existing.BillingRequestID == relayInfo.RequestId {
			if settled, settleErr := service.SettleMidjourneyTaskBilling(relayInfo, existing, true); settleErr != nil || !settled {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_settlement_pending")
			}
			return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
		}
		if billingPrepared {
			if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), relayInfo); refundErr != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_refund_pending")
			}
		}
		return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
	}
	if err := service.PersistMidjourneyTaskWithBilling(midjourneyTask, billingPrepared); err != nil {
		if existing := model.GetByOnlyMJId(midjourneyTask.MjId); existing != nil {
			if existing.UserId == relayInfo.UserId && existing.ChannelId == midjourneyTask.ChannelId && billingPrepared && existing.BillingRequestID == relayInfo.RequestId {
				if settled, settleErr := service.SettleMidjourneyTaskBilling(relayInfo, existing, true); settleErr == nil && settled {
					return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
				}
			} else if existing.UserId == relayInfo.UserId && existing.ChannelId == midjourneyTask.ChannelId && billingPrepared {
				if refundErr := service.RefundConfirmedMidjourneyBilling(c.Request.Context(), relayInfo); refundErr == nil {
					return writeMidjourneyResponse(c, midjResponseWithStatus.StatusCode, responseBody)
				}
			} else if billingPrepared {
				if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "本地任务写入失败，需核对上游 Midjourney 结果"); persistErr != nil {
					logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
				}
			}
		} else if billingPrepared {
			if persistErr := service.PersistMidjourneyUnknownTask(c.Request.Context(), midjourneyTask, priceData.Quota, "本地任务写入失败，需核对上游 Midjourney 结果"); persistErr != nil {
				logger.LogWarn(c, fmt.Sprintf("persist unknown Midjourney task failed: %v", persistErr))
			}
		}
		return &dto.MidjourneyResponse{Code: 4, Description: "insert_midjourney_task_failed"}
	}
	billingApplied, billingErr := service.SettleMidjourneyTaskBilling(relayInfo, midjourneyTask, billingPrepared)
	if billingPrepared && (billingErr != nil || !billingApplied) {
		return &dto.MidjourneyResponse{Code: 4, Description: "midjourney_billing_settlement_pending"}
	}
	if billingApplied {
		billingChannelId := midjourneyTask.GetBillingChannelId()
		tokenName := c.GetString("token_name")
		logContent := fmt.Sprintf("模型固定价格 %.2f，分组倍率 %.2f，操作 %s，ID %s", priceData.ModelPrice, priceData.GroupRatioInfo.GroupRatio, midjRequest.Action, midjResponse.Result)
		other := service.GenerateMjOtherInfo(relayInfo, priceData)
		model.RecordConsumeLog(c, relayInfo.UserId, model.RecordConsumeLogParams{
			ChannelId: billingChannelId,
			ModelName: modelName,
			TokenName: tokenName,
			Quota:     midjourneyTask.Quota,
			Content:   logContent,
			TokenId:   midjourneyTask.TokenId,
			Group:     relayInfo.UsingGroup,
			Other:     other,
		})
	}

	if midjResponse.Code == 22 { //22-排队中，说明任务已存在
		//修改返回值
		newBody := strings.Replace(string(responseBody), `"code":22`, `"code":1`, -1)
		responseBody = []byte(newBody)
	}
	//resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	bodyReader := io.NopCloser(bytes.NewBuffer(responseBody))

	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)

	_, err = io.Copy(c.Writer, bodyReader)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	err = bodyReader.Close()
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "close_response_body_failed",
		}
	}
	return nil
}

type taskChangeParams struct {
	ID     string
	Action string
	Index  int
}

func getMjRequestPath(path string) string {
	requestURL := path
	if strings.Contains(requestURL, "/mj-") {
		urls := strings.Split(requestURL, "/mj/")
		if len(urls) < 2 {
			return requestURL
		}
		requestURL = "/mj/" + urls[1]
	}
	return requestURL
}
