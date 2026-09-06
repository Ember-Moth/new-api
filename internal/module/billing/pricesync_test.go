package billing_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/pricesync"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type priceSyncTransport func(*http.Request) (*http.Response, error)

func (f priceSyncTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPriceSyncConvertsSupportedFormatsAndPreservesZeroPrices(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, body string
		cache                float64
	}{
		{"ratio map", "/api/ratio_config", `{"success":true,"data":{"model_ratio":{"priced":2,"free":0},"completion_ratio":{"priced":3},"cache_ratio":{"priced":0},"billing_mode":{"priced":"tiered_expr"},"billing_expr":{"priced":"p * 2"}}}`, 0},
		{"pricing list", "", `{"success":true,"data":[{"model_name":"priced","model_ratio":2,"completion_ratio":3,"cache_ratio":0,"create_cache_ratio":0,"image_ratio":0,"audio_ratio":0,"audio_completion_ratio":0,"billing_mode":"tiered_expr","billing_expr":"p * 2"},{"model_name":"free","model_ratio":0}]}`, 0},
		{"openrouter", "openrouter", `{"data":[{"id":"priced","pricing":{"prompt":"0.000004","completion":"0.000012","input_cache_read":"0.000001"}},{"id":"free","pricing":{"prompt":"0","completion":"0"}},{"id":"dynamic","pricing":{"prompt":"-1","completion":"-1"}},{"id":"invalid","pricing":{"prompt":"NaN","completion":"0"}}]}`, 0.25},
		{"models.dev", "http://models.dev/api.json", `{"costly":{"models":{"priced":{"cost":{"input":8,"output":40}}}},"preferred":{"models":{"priced":{"cost":{"input":4,"output":12,"cache_read":1}},"free":{"cost":{"input":0,"output":0}},"invalid":{"cost":{"input":0,"output":1}}}},"free-provider":{"models":{"priced":{"cost":{"input":0,"output":0}}}}}`, 0.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.endpoint == "openrouter" {
					assert.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
					assert.Equal(t, "/v1/models", r.URL.Path)
				} else {
					assert.Empty(t, r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := server.Client()
			if tc.name == "models.dev" {
				original := client.Transport
				fixtureURL, err := url.Parse(server.URL)
				require.NoError(t, err)
				client = &http.Client{Transport: priceSyncTransport(func(r *http.Request) (*http.Response, error) {
					copy := r.Clone(r.Context())
					u := *r.URL
					u.Scheme = fixtureURL.Scheme
					u.Host = fixtureURL.Host
					copy.URL = &u
					return original.RoundTrip(copy)
				})}
			}
			svc := pricesync.New(pricesync.Dependencies{HTTPClient: client, Credential: func(context.Context, int) (string, string, error) { return server.URL, "fixture-key", nil }, LocalData: func() map[string]any { return map[string]any{"model_ratio": map[string]float64{"priced": 1}} }})
			result, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Upstreams: []contract.UpstreamDTO{{ID: 7, Name: "fixture", BaseURL: server.URL, Endpoint: tc.endpoint}}})
			require.NoError(t, err)
			require.Len(t, result.TestResults, 1)
			assert.Equal(t, "success", result.TestResults[0].Status, result.TestResults[0].Error)
			require.Contains(t, result.Differences, "priced")
			assert.Equal(t, 2.0, result.Differences["priced"]["model_ratio"].Upstreams["fixture(7)"])
			assert.Equal(t, 1.0, result.Differences["priced"]["model_ratio"].Current)
			assert.Equal(t, 3.0, result.Differences["priced"]["completion_ratio"].Upstreams["fixture(7)"])
			assert.Equal(t, tc.cache, result.Differences["priced"]["cache_ratio"].Upstreams["fixture(7)"])
			assert.Equal(t, 0.0, result.Differences["free"]["model_ratio"].Upstreams["fixture(7)"])
			assert.NotContains(t, result.Differences, "invalid")
			assert.NotContains(t, result.Differences, "dynamic")
			if tc.name == "pricing list" {
				for _, field := range []string{"create_cache_ratio", "image_ratio", "audio_ratio", "audio_completion_ratio"} {
					assert.Equal(t, 0.0, result.Differences["priced"][field].Upstreams["fixture(7)"])
				}
				assert.Equal(t, "p * 2", result.Differences["priced"]["billing_expr"].Upstreams["fixture(7)"])
			}
		})
	}
}

func TestPriceSyncComparisonKeepsInputOrderConfidenceAndOnlyChangedSources(t *testing.T) {
	local := map[string]any{"model_ratio": map[string]float64{"priced": 1, "placeholder": 5}, "completion_ratio": map[string]float64{"placeholder": 2}, "billing_expr": map[string]string{"priced": "p"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := local
		if r.URL.Path == "/changed" {
			data = map[string]any{"model_ratio": map[string]float64{"priced": 1, "placeholder": 37.5}, "completion_ratio": map[string]float64{"placeholder": 1}, "billing_expr": map[string]string{"priced": "p * 2"}}
		}
		encoded, err := common.Marshal(map[string]any{"success": true, "data": data})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	svc := pricesync.New(pricesync.Dependencies{HTTPClient: server.Client(), LocalData: func() map[string]any { return local }})
	report, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Upstreams: []contract.UpstreamDTO{{Name: "unchanged", BaseURL: server.URL, Endpoint: "/same"}, {Name: "changed", BaseURL: server.URL, Endpoint: "/changed"}}})
	require.NoError(t, err)
	assert.Equal(t, []contract.TestResult{{Name: "unchanged", Status: "success"}, {Name: "changed", Status: "success"}}, report.TestResults)
	assert.NotContains(t, report.Differences["priced"], "model_ratio")
	assert.Equal(t, map[string]any{"changed": "p * 2"}, report.Differences["priced"]["billing_expr"].Upstreams)
	assert.Equal(t, map[string]bool{"changed": false}, report.Differences["placeholder"]["model_ratio"].Confidence)
	assert.Equal(t, map[string]string{"priced": "p"}, local["billing_expr"], "comparison must not mutate local configuration")
}

func TestPriceSyncRejectsMalformedPricesAndOversizedResponses(t *testing.T) {
	for _, body := range []string{`{"success":true,"data":{"model_ratio":{"m":-1}}}`, `{"success":true,"data":{"model_ratio":{"m":{"amount":1}}}}`, `{"success":true,"data":{"billing_expr":{"m":7}}}`, `{"success":true,"data":{"model_ratio":{"m":"NaN"}}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		svc := pricesync.New(pricesync.Dependencies{HTTPClient: server.Client()})
		report, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Upstreams: []contract.UpstreamDTO{{Name: "malformed", BaseURL: server.URL}}})
		require.NoError(t, err)
		assert.Equal(t, "error", report.TestResults[0].Status)
		assert.Empty(t, report.Differences)
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat(" ", (10<<20)+1)))
	}))
	defer server.Close()
	svc := pricesync.New(pricesync.Dependencies{HTTPClient: server.Client()})
	report, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Upstreams: []contract.UpstreamDTO{{Name: "oversized", BaseURL: server.URL}}})
	require.NoError(t, err)
	assert.Equal(t, "upstream pricing response exceeds 10 MiB", report.TestResults[0].Error)
}

func TestPriceSyncSourcesAndOpenRouterCredentialsStayBoundToSavedChannel(t *testing.T) {
	f := newPricingFixture(t)
	var authorized atomic.Int32
	var untrusted atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { untrusted.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authorized.Add(1)
			assert.Equal(t, "Bearer saved-key", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/redirect/v1/models" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m","pricing":{"prompt":"0.000002","completion":"0.000002"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"model_ratio":{"m":1}}}`))
	}))
	defer server.Close()
	base := server.URL
	row := channelmodule.Channel{Name: "saved channel", Key: "saved-key", Type: constant.ChannelTypeOpenRouter, Status: common.ChannelStatusEnabled, BaseURL: &base}
	require.NoError(t, f.db.Create(&row).Error)
	svc := pricesync.New(pricesync.Dependencies{HTTPClient: server.Client(), Sources: func(ctx context.Context, ids []int) ([]contract.SyncableChannel, error) {
		rows, err := f.channels.PricingSyncChannels(ctx, ids)
		if err != nil {
			return nil, err
		}
		result := make([]contract.SyncableChannel, 0, len(rows))
		for _, row := range rows {
			assert.Empty(t, row.Key)
			result = append(result, contract.SyncableChannel{ID: row.Id, Name: row.Name, BaseURL: row.GetBaseURL(), Status: row.Status, Type: row.Type})
		}
		return result, nil
	}, Credential: f.channels.PricingSyncCredential})
	handler := billinghttp.New(billing.New(billing.Dependencies{PriceSync: svc}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.GET("/sources", handler.GetSyncableChannels)
	router.POST("/fetch", handler.FetchUpstreamRatios)
	response := redemptionRequest(t, router, http.MethodGet, "/sources", nil)
	assert.True(t, response.Success)
	assert.NotContains(t, string(response.Data), "saved-key")
	var sources []contract.SyncableChannel
	require.NoError(t, common.Unmarshal(response.Data, &sources))
	require.Len(t, sources, 3)
	assert.Equal(t, -100, sources[1].ID)
	assert.Equal(t, -101, sources[2].ID)
	idResult := redemptionRequest(t, router, http.MethodPost, "/fetch", contract.UpstreamRequest{ChannelIDs: []int64{int64(row.Id)}})
	assert.True(t, idResult.Success)
	for _, tc := range []struct {
		base string
		ok   bool
	}{{server.URL, true}, {other.URL, false}, {server.URL + "/redirect", false}} {
		report, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Upstreams: []contract.UpstreamDTO{{ID: row.Id, Name: row.Name, BaseURL: tc.base, Endpoint: "openrouter"}}})
		require.NoError(t, err)
		if tc.ok {
			assert.Equal(t, "success", report.TestResults[0].Status)
		} else {
			assert.Equal(t, "error", report.TestResults[0].Status)
		}
	}
	assert.EqualValues(t, 2, authorized.Load())
	assert.Zero(t, untrusted.Load(), "neither a mismatched URL nor a redirect may receive the stored key")
	_, err := svc.Fetch(t.Context(), contract.UpstreamRequest{Timeout: 121, Upstreams: []contract.UpstreamDTO{{Name: "timeout", BaseURL: base}}})
	var invalid *pricesync.InputError
	require.ErrorAs(t, err, &invalid)
	for _, input := range []string{`{"timeout":121,"upstreams":[{"name":"timeout","base_url":"` + base + `"}]}`, `{`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/fetch", strings.NewReader(input))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
	empty := redemptionRequest(t, router, http.MethodPost, "/fetch", contract.UpstreamRequest{})
	assert.False(t, empty.Success)
	assert.Equal(t, "无有效上游渠道", empty.Message)
	require.NoError(t, f.db.Exec("ALTER TABLE channels RENAME TO unavailable_channels").Error)
	responseRecorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/fetch", strings.NewReader(fmt.Sprintf(`{"channel_ids":[%d]}`, row.Id)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(responseRecorder, request)
	assert.Equal(t, http.StatusInternalServerError, responseRecorder.Code)
	assert.Contains(t, responseRecorder.Body.String(), "查询渠道失败")
}

func TestPriceSyncCancellationStopsActiveAndQueuedFetches(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 8)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	svc := pricesync.New(pricesync.Dependencies{HTTPClient: server.Client()})
	request := contract.UpstreamRequest{}
	// Eight active slots plus one queued source exercises both cancellation paths.
	for i := range 9 {
		request.Upstreams = append(request.Upstreams, contract.UpstreamDTO{Name: fmt.Sprint(i), BaseURL: server.URL})
	}
	type outcome struct {
		report contract.PricingSyncResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { report, err := svc.Fetch(ctx, request); done <- outcome{report, err} }()
	for range 8 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("fetch workers did not start")
		}
	}
	cancel()
	result := <-done
	require.NoError(t, result.err)
	require.Len(t, result.report.TestResults, 9)
	for _, item := range result.report.TestResults {
		assert.Equal(t, "error", item.Status)
	}
	assert.EqualValues(t, 8, calls.Load())
}
