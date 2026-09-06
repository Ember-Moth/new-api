package openai

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/legacy/relay/helper"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Realtime readers only deliver frames. One loop owns usage and billing so
// shutdown cannot race an in-flight reservation or count the same usage twice.
func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}
	info.IsStream = true
	ctx, cancel := context.WithCancel(c.Request.Context())
	type frame struct {
		client bool
		data   []byte
		err    error
	}
	frames := make(chan frame, 32)
	var readers sync.WaitGroup
	for _, endpoint := range []struct {
		client bool
		conn   *websocket.Conn
	}{{true, info.ClientWs}, {false, info.TargetWs}} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				_, data, err := endpoint.conn.ReadMessage()
				select {
				case frames <- frame{client: endpoint.client, data: data, err: err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	defer func() {
		cancel()
		// Interrupt reads while leaving the client write side available for the
		// caller's final error frame. The connection owners close them afterward.
		_ = info.ClientWs.SetReadDeadline(time.Now())
		_ = info.TargetWs.SetReadDeadline(time.Now())
		readers.Wait()
	}()

	total := &dto.RealtimeUsage{}
	pending := &dto.RealtimeUsage{}
	seenResponses := make(map[string]struct{})
	var streamErr error
	completedResponses, openResponses, explicitResponses := 0, 0, 0
	sawWork, sawCreated, incompleteResponse, committedInput := false, false, false, false
loop:
	for {
		select {
		case <-ctx.Done():
			streamErr = ctx.Err()
			break loop
		case received := <-frames:
			if received.err != nil {
				if !websocket.IsCloseError(received.err, websocket.CloseNormalClosure) {
					streamErr = received.err
				}
				break loop
			}
			var event dto.RealtimeEvent
			if err := common.Unmarshal(received.data, &event); err != nil {
				streamErr = fmt.Errorf("decode realtime frame: %w", err)
				break loop
			}
			if received.client {
				switch event.Type {
				case "response.create":
					explicitResponses++
					sawWork = true
				case "input_audio_buffer.commit":
					committedInput, sawWork = true, true
				case "input_audio_buffer.append", "conversation.item.create":
					sawWork = true
				}
				if event.Type == dto.RealtimeEventTypeSessionUpdate && event.Session != nil && event.Session.Tools != nil {
					info.RealtimeTools = event.Session.Tools
				}
				if err := helper.WssString(c, info.TargetWs, string(received.data)); err != nil {
					streamErr = err
					break loop
				}
			} else {
				info.SetFirstResponseTime()
				if event.Type == "input_audio_buffer.committed" {
					committedInput, sawWork = true, true
				}
				if strings.HasPrefix(event.Type, "response.") {
					sawWork = true
					if event.Type != dto.RealtimeEventTypeResponseDone {
						incompleteResponse = true
					}
				}
				if event.Type == "response.created" {
					openResponses++
					sawCreated, committedInput = true, false
				}
			}

			if !received.client && event.Type == dto.RealtimeEventTypeResponseDone {
				if event.EventId != "" {
					if _, duplicate := seenResponses[event.EventId]; duplicate {
						continue
					}
					seenResponses[event.EventId] = struct{}{}
				}
				if event.Response == nil {
					streamErr = fmt.Errorf("realtime terminal event has no response")
					break loop
				}
				completed := pending
				if event.Response != nil && event.Response.Usage != nil {
					completed = event.Response.Usage
				} else {
					textTokens, audioTokens, err := service.CountTokenRealtime(info, event, info.UpstreamModelName)
					if err != nil {
						streamErr = err
						break loop
					}
					completed, err = accumulateRealtimeUsage(pending, &dto.RealtimeUsage{InputTokens: textTokens, InputTokenDetails: dto.InputTokenDetails{TextTokens: textTokens, AudioTokens: audioTokens}})
					if err != nil {
						streamErr = err
						break loop
					}
				}
				next, err := accumulateRealtimeUsage(total, completed)
				if err != nil {
					streamErr = err
					break loop
				}
				// These tokens were already consumed upstream. Retain them for the
				// final settlement even if reserving more funds now fails.
				total, pending = next, &dto.RealtimeUsage{}
				completedResponses++
				openResponses = max(0, openResponses-1)
				explicitResponses = max(0, explicitResponses-1)
				incompleteResponse = false
				if !sawCreated {
					committedInput = false
				}
				info.IsFirstRequest = false
				if err := service.PreWssConsumeQuota(c, info, total); err != nil {
					streamErr = fmt.Errorf("reserve cumulative realtime usage: %w", err)
					break loop
				}
			} else if !received.client && (event.Type == dto.RealtimeEventTypeSessionUpdated || event.Type == dto.RealtimeEventTypeSessionCreated) {
				if event.Session != nil {
					info.InputAudioFormat = common.GetStringIfEmpty(event.Session.InputAudioFormat, info.InputAudioFormat)
					info.OutputAudioFormat = common.GetStringIfEmpty(event.Session.OutputAudioFormat, info.OutputAudioFormat)
				}
			} else {
				textTokens, audioTokens, err := service.CountTokenRealtime(info, event, info.UpstreamModelName)
				if err != nil {
					streamErr = err
					break loop
				}
				increment := &dto.RealtimeUsage{}
				if received.client {
					increment.InputTokenDetails.TextTokens, increment.InputTokenDetails.AudioTokens = textTokens, audioTokens
				} else {
					increment.OutputTokenDetails.TextTokens, increment.OutputTokenDetails.AudioTokens = textTokens, audioTokens
				}
				next, err := accumulateRealtimeUsage(pending, increment)
				if err != nil {
					streamErr = err
					break loop
				}
				pending = next
			}
			if !received.client {
				if err := helper.WssString(c, info.ClientWs, string(received.data)); err != nil {
					streamErr = err
					break loop
				}
			}
		}
	}
	// Buffered input or nonterminal deltas are not a confirmed final charge.
	// Retain the reservation for reconciliation instead of turning an unknown
	// upstream result into a zero-cost settlement and refund.
	info.RealtimeOutcomeUncertain = openResponses > 0 || explicitResponses > 0 || committedInput || incompleteResponse || (sawWork && completedResponses == 0) || (streamErr != nil && completedResponses == 0)

	if streamErr != nil {
		logger.LogError(c, "realtime stream ended: "+streamErr.Error())
		return types.NewError(streamErr, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry()), total
	}
	return nil, total
}

// accumulateRealtimeUsage bounds every upstream count before addition. It
// returns a new value so a rejected frame cannot partly modify billed usage.
func accumulateRealtimeUsage(current, extra *dto.RealtimeUsage) (*dto.RealtimeUsage, error) {
	if current == nil || extra == nil {
		return nil, fmt.Errorf("realtime usage is missing")
	}
	result := *current
	for _, field := range []struct {
		target *int
		value  int
	}{
		{&result.TotalTokens, extra.TotalTokens},
		{&result.InputTokens, extra.InputTokens},
		{&result.OutputTokens, extra.OutputTokens},
		{&result.InputTokenDetails.CachedTokens, extra.InputTokenDetails.CachedTokens},
		{&result.InputTokenDetails.TextTokens, extra.InputTokenDetails.TextTokens},
		{&result.InputTokenDetails.AudioTokens, extra.InputTokenDetails.AudioTokens},
		{&result.OutputTokenDetails.TextTokens, extra.OutputTokenDetails.TextTokens},
		{&result.OutputTokenDetails.AudioTokens, extra.OutputTokenDetails.AudioTokens},
	} {
		if field.value < 0 || field.value > common.MaxQuota || *field.target < 0 || *field.target > common.MaxQuota-field.value {
			return nil, fmt.Errorf("realtime usage exceeds accounting limits")
		}
		*field.target += field.value
	}
	inputDetails := int64(result.InputTokenDetails.TextTokens) + int64(result.InputTokenDetails.AudioTokens)
	outputDetails := int64(result.OutputTokenDetails.TextTokens) + int64(result.OutputTokenDetails.AudioTokens)
	input := max(int64(result.InputTokens), inputDetails)
	output := max(int64(result.OutputTokens), outputDetails)
	if input > int64(common.MaxQuota) || output > int64(common.MaxQuota)-input {
		return nil, fmt.Errorf("realtime total usage exceeds accounting limits")
	}
	result.InputTokens, result.OutputTokens = int(input), int(output)
	result.TotalTokens = max(result.TotalTokens, result.InputTokens+result.OutputTokens)
	return &result, nil
}
