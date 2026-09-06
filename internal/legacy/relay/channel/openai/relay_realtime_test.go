package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimeCompletedEventsAreCountedOnceBeforeStreamFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		closeCode int
		terminal  bool
	}{{"normal_close", websocket.CloseNormalClosure, true}, {"upstream_failure", websocket.CloseInternalServerErr, true}, {"missing_terminal", websocket.CloseGoingAway, false}} {
		t.Run(test.name, func(t *testing.T) {
			closeCode := test.closeCode
			upgrader := websocket.Upgrader{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				if !test.terminal {
					data, err := common.Marshal(dto.RealtimeEvent{Type: "response.created", Response: &dto.RealtimeResponse{}})
					if err == nil {
						_ = conn.WriteMessage(websocket.TextMessage, data)
					}
					_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, "lost outcome"))
					return
				}
				for _, item := range []struct {
					id    string
					count int
				}{{"response-a", 5}, {"response-a", 5}, {"response-b", 3}} {
					data, err := common.Marshal(dto.RealtimeEvent{EventId: item.id, Type: dto.RealtimeEventTypeResponseDone, Response: &dto.RealtimeResponse{Usage: &dto.RealtimeUsage{TotalTokens: item.count, InputTokens: item.count, InputTokenDetails: dto.InputTokenDetails{TextTokens: item.count}}}})
					if err != nil || conn.WriteMessage(websocket.TextMessage, data) != nil {
						return
					}
				}
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, "finished"))
			}))
			defer upstream.Close()
			type outcome struct {
				usage     *dto.RealtimeUsage
				err       *types.NewAPIError
				uncertain bool
			}
			finished := make(chan outcome, 1)
			engine := gin.New()
			engine.GET("/", func(c *gin.Context) {
				client, err := upgrader.Upgrade(c.Writer, c.Request, nil)
				if err != nil {
					finished <- outcome{}
					return
				}
				defer client.Close()
				target, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
				if err != nil {
					finished <- outcome{}
					return
				}
				defer target.Close()
				info := &relaycommon.RelayInfo{ClientWs: client, TargetWs: target}
				info.PriceData.UsePrice = true // Per-call billing does not reserve each event.
				apiErr, usage := OpenaiRealtimeHandler(c, info)
				finished <- outcome{usage: usage, err: apiErr, uncertain: info.RealtimeOutcomeUncertain}
			})
			proxy := httptest.NewServer(engine)
			defer proxy.Close()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(proxy.URL, "http"), nil)
			require.NoError(t, err)
			defer client.Close()
			var received int
			for {
				if _, _, err := client.ReadMessage(); err != nil {
					break
				}
				received++
			}
			result := <-finished
			require.NotNil(t, result.usage)
			if test.terminal {
				assert.Equal(t, 8, result.usage.TotalTokens)
				assert.Equal(t, 8, result.usage.InputTokens)
				assert.Equal(t, 2, received)
				assert.False(t, result.uncertain)
			} else {
				assert.Zero(t, result.usage.TotalTokens)
				assert.Equal(t, 1, received)
				assert.True(t, result.uncertain, "missing terminal outcome must retain the billing reservation")
			}
			if closeCode == websocket.CloseNormalClosure {
				assert.Nil(t, result.err)
			} else {
				assert.NotNil(t, result.err)
			}
		})
	}
}

func TestRealtimeUsageRejectsInvalidCountsWithoutChangingPreviousUsage(t *testing.T) {
	previous := &dto.RealtimeUsage{TotalTokens: 7, InputTokens: 7}
	for _, extra := range []*dto.RealtimeUsage{
		{InputTokens: -1},
		{InputTokens: common.MaxQuota},
		{InputTokenDetails: dto.InputTokenDetails{AudioTokens: common.MaxQuota, TextTokens: 1}},
	} {
		_, err := accumulateRealtimeUsage(previous, extra)
		require.Error(t, err)
		assert.Equal(t, 7, previous.TotalTokens)
		assert.Equal(t, 7, previous.InputTokens)
	}
}
