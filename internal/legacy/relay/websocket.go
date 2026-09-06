package relay

import (
	"fmt"

	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func WssHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	//var requestBody io.Reader
	//firstWssRequest, _ := c.Get("first_wss_request")
	//requestBody = bytes.NewBuffer(firstWssRequest.([]byte))

	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}

	if resp != nil {
		info.TargetWs = resp.(*websocket.Conn)
		defer info.TargetWs.Close()
	}

	usage, newAPIError := adaptor.DoResponse(c, nil, info)
	if info.RealtimeOutcomeUncertain {
		if err := service.PreserveUncertainBilling(info); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		return types.NewError(fmt.Errorf("realtime billing outcome requires reconciliation"), types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
	}
	// Settle observed usage even when the stream ended with an error. Refunding
	// the whole reservation after partial upstream consumption would lose charges.
	if realtimeUsage, ok := usage.(*dto.RealtimeUsage); ok && realtimeUsage != nil {
		if err := service.PostWssConsumeQuota(c, info, info.UpstreamModelName, realtimeUsage, ""); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
	}
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	return nil
}
