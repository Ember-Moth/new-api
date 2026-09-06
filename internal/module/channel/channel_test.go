package channel_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/app/channelprovider"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	channelhttp "github.com/QuantumNous/new-api/internal/module/channel/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPrefillGroupHTTPContractWithSQLSchema(t *testing.T) {
	router, _, _ := newChannelTestRouter(t, nil)

	empty := channelRequest(t, router, http.MethodGet, "/groups", "")
	require.True(t, empty.Success)
	assert.JSONEq(t, `[]`, string(empty.Data))
	missing := channelRequest(t, router, http.MethodPost, "/groups", `{"name":"missing type"}`)
	assert.False(t, missing.Success)
	assert.Equal(t, "组名称和类型不能为空", missing.Message)
	missingID := channelRequest(t, router, http.MethodPut, "/groups", `{"name":"missing id","type":"model"}`)
	assert.False(t, missingID.Success)
	assert.Equal(t, "缺少组 ID", missingID.Message)

	created := channelRequest(t, router, http.MethodPost, "/groups", `{"name":"中文模型组","type":"model","items":["gpt-one"],"description":"initial"}`)
	require.True(t, created.Success, created.Message)
	var group contract.PrefillGroup
	require.NoError(t, common.Unmarshal(created.Data, &group))
	assert.Positive(t, group.Id)
	assert.Positive(t, group.CreatedTime)
	assert.JSONEq(t, `["gpt-one"]`, string(group.Items))
	duplicate := channelRequest(t, router, http.MethodPost, "/groups", `{"name":"中文模型组","type":"tag","items":[]}`)
	assert.False(t, duplicate.Success)
	assert.Equal(t, "组名称已存在", duplicate.Message)

	group.Items = json.RawMessage(`["gpt-two","gpt-three"]`)
	group.Description = ""
	body, err := common.Marshal(group)
	require.NoError(t, err)
	updated := channelRequest(t, router, http.MethodPut, "/groups", string(body))
	require.True(t, updated.Success, updated.Message)
	listed := channelRequest(t, router, http.MethodGet, "/groups?type=model", "")
	require.True(t, listed.Success, listed.Message)
	var groups []contract.PrefillGroup
	require.NoError(t, common.Unmarshal(listed.Data, &groups))
	require.Len(t, groups, 1)
	assert.Equal(t, group.Id, groups[0].Id)
	assert.Equal(t, group.CreatedTime, groups[0].CreatedTime)
	assert.Empty(t, groups[0].Description)
	assert.JSONEq(t, string(group.Items), string(groups[0].Items))
	filtered := channelRequest(t, router, http.MethodGet, "/groups?type=tag", "")
	assert.JSONEq(t, `[]`, string(filtered.Data))

	deleted := channelRequest(t, router, http.MethodDelete, "/groups/"+strconv.Itoa(group.Id), "")
	require.True(t, deleted.Success, deleted.Message)
	listed = channelRequest(t, router, http.MethodGet, "/groups", "")
	assert.JSONEq(t, `[]`, string(listed.Data))
	recreated := channelRequest(t, router, http.MethodPost, "/groups", `{"name":"中文模型组","type":"model","items":[]}`)
	require.True(t, recreated.Success, recreated.Message)
}

type channelResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func channelRequest(t *testing.T, router http.Handler, method, path, body string) channelResponse {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var result channelResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}

func newChannelTestRouter(t *testing.T, pricing channel.CatalogPricing) (*gin.Engine, *gorm.DB, *channel.Service) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	service := channel.New(channel.Dependencies{DB: db, Pricing: pricing, Providers: channelprovider.Adapter{}})
	handler := channelhttp.New(service, channelhttp.ManagementHooks{})
	router := gin.New()
	router.GET("/groups", handler.GetPrefillGroups)
	router.POST("/groups", handler.CreatePrefillGroup)
	router.PUT("/groups", handler.UpdatePrefillGroup)
	router.DELETE("/groups/:id", handler.DeletePrefillGroup)
	router.GET("/vendors", handler.GetAllVendors)
	router.GET("/vendors/search", handler.SearchVendors)
	router.GET("/vendors/:id", handler.GetVendorMeta)
	router.POST("/vendors", handler.CreateVendorMeta)
	router.PUT("/vendors", handler.UpdateVendorMeta)
	router.DELETE("/vendors/:id", handler.DeleteVendorMeta)
	router.GET("/models", handler.GetAllModelsMeta)
	router.GET("/models/search", handler.SearchModelsMeta)
	router.GET("/models/missing", handler.GetMissingModels)
	router.GET("/models/preview", handler.SyncUpstreamPreview)
	router.POST("/models/sync", handler.SyncUpstreamModels)
	router.GET("/models/:id", handler.GetModelMeta)
	router.POST("/models", handler.CreateModelMeta)
	router.PUT("/models", handler.UpdateModelMeta)
	router.DELETE("/models/:id", handler.DeleteModelMeta)
	router.GET("/balance/:id", handler.UpdateChannelBalance)
	return router, db, service
}

func TestAdvancedBalanceUsesProviderAdapterAndPreservesUnknownResponse(t *testing.T) {
	var payload atomic.Value
	payload.Store(`{"object":"credit_summary","total_available":7.25}`)
	headers := make(chan http.Header, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	defer upstream.Close()
	router, db, service := newChannelTestRouter(t, nil)
	entry := channel.Channel{Name: "balance", Type: constant.ChannelTypeAdvancedCustom, Key: "secret", BaseURL: &upstream.URL}
	entry.SetOtherSettings(kitdto.ChannelOtherSettings{AdvancedCustom: &kitdto.AdvancedCustomConfig{Routes: []kitdto.AdvancedCustomRoute{{
		IncomingPath: kitdto.AdvancedCustomBalancePath, UpstreamPath: "/credits", Converter: "none",
		Auth: &kitdto.AdvancedCustomRouteAuth{Type: kitdto.AdvancedCustomAuthTypeHeader, Name: "X-Key", Value: "route-{api_key}"},
	}}}})
	overrides := `{"X-Key":"override-{api_key}"}`
	entry.HeaderOverride = &overrides
	require.NoError(t, service.InsertChannel(&entry))
	path := "/balance/" + strconv.Itoa(entry.Id)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success     bool    `json:"success"`
		Balance     float64 `json:"balance"`
		RawResponse string  `json:"raw_response"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, 7.25, response.Balance)
	assert.Equal(t, "override-secret", (<-headers).Get("X-Key"))
	var stored channel.Channel
	require.NoError(t, db.First(&stored, entry.Id).Error)
	assert.Equal(t, 7.25, stored.Balance)
	payload.Store(`{"credits":"unrecognized format"}`)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	response = struct {
		Success     bool    `json:"success"`
		Balance     float64 `json:"balance"`
		RawResponse string  `json:"raw_response"`
	}{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.JSONEq(t, payload.Load().(string), response.RawResponse)
	require.NoError(t, db.First(&stored, entry.Id).Error)
	assert.Equal(t, 7.25, stored.Balance, "unrecognized provider data must not replace the numeric balance")
}

type catalogPricingFixture struct {
	refreshes int
}

func (*catalogPricingFixture) ModelEndpointTypes(string) []constant.EndpointType {
	return []constant.EndpointType{constant.EndpointTypeOpenAI}
}
func (*catalogPricingFixture) ModelGroups(string) []string  { return []string{"default"} }
func (*catalogPricingFixture) ModelQuotaTypes(string) []int { return []int{0} }
func (*catalogPricingFixture) Models() []contract.ModelPricing {
	return []contract.ModelPricing{{ModelName: "catalog-fixture", SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI}, EnableGroup: []string{"default"}, QuotaType: 0}}
}
func (p *catalogPricingFixture) Refresh() { p.refreshes++ }

func TestCatalogHTTPPreservesFiltersPartialUpdatesAndPricingProjection(t *testing.T) {
	pricing := &catalogPricingFixture{}
	router, db, _ := newChannelTestRouter(t, pricing)
	created := channelRequest(t, router, http.MethodPost, "/vendors", `{"name":"Catalog vendor","description":"needle"}`)
	require.True(t, created.Success, created.Message)
	var vendor contract.Vendor
	require.NoError(t, common.Unmarshal(created.Data, &vendor))
	assert.Positive(t, vendor.Id)
	assert.Equal(t, 1, vendor.Status)
	duplicate := channelRequest(t, router, http.MethodPost, "/vendors", `{"name":"Catalog vendor"}`)
	assert.False(t, duplicate.Success)
	assert.Equal(t, "供应商名称已存在", duplicate.Message)
	search := channelRequest(t, router, http.MethodGet, "/vendors/search?keyword=needle", "")
	require.True(t, search.Success, search.Message)
	var vendors struct {
		Items []contract.Vendor `json:"items"`
		Total int               `json:"total"`
	}
	require.NoError(t, common.Unmarshal(search.Data, &vendors))
	assert.Equal(t, 1, vendors.Total)
	require.Len(t, vendors.Items, 1)
	assert.Equal(t, vendor.Id, vendors.Items[0].Id)

	created = channelRequest(t, router, http.MethodPost, "/models", fmt.Sprintf(`{"model_name":"catalog-fixture","description":"original","vendor_id":%d,"status":0,"sync_official":0}`, vendor.Id))
	require.True(t, created.Success, created.Message)
	var metadata contract.Model
	require.NoError(t, common.Unmarshal(created.Data, &metadata))
	require.Positive(t, metadata.Id)
	search = channelRequest(t, router, http.MethodGet, "/models/search?status=disabled&sync_official=no&vendor=Catalog", "")
	require.True(t, search.Success, search.Message)
	var models struct {
		Items        []contract.Model `json:"items"`
		Total        int              `json:"total"`
		VendorCounts map[int]int      `json:"vendor_counts"`
	}
	require.NoError(t, common.Unmarshal(search.Data, &models))
	require.Len(t, models.Items, 1)
	assert.Equal(t, 1, models.Total)
	assert.Equal(t, 1, models.VendorCounts[vendor.Id])
	assert.Zero(t, models.Items[0].Status)
	assert.Zero(t, models.Items[0].SyncOfficial)
	assert.Equal(t, []string{"default"}, models.Items[0].EnableGroups)
	assert.Equal(t, []int{0}, models.Items[0].QuotaTypes)

	var channelID int
	require.NoError(t, db.Raw(`INSERT INTO channels (name, "key", type, status, models) VALUES ('Bound channel', 'fixture', 1, 1, ARRAY['catalog-fixture']) RETURNING id`).Scan(&channelID).Error)
	require.NoError(t, db.Exec(`INSERT INTO abilities ("group", model, channel_id, enabled) VALUES ('default', 'catalog-fixture', ?, true)`, channelID).Error)
	updated := channelRequest(t, router, http.MethodPut, "/models?status_only=true", fmt.Sprintf(`{"id":%d,"status":1}`, metadata.Id))
	require.True(t, updated.Success, updated.Message)
	loaded := channelRequest(t, router, http.MethodGet, "/models/"+strconv.Itoa(metadata.Id), "")
	require.True(t, loaded.Success, loaded.Message)
	require.NoError(t, common.Unmarshal(loaded.Data, &metadata))
	assert.Equal(t, "catalog-fixture", metadata.ModelName)
	assert.Equal(t, "original", metadata.Description)
	assert.Equal(t, 1, metadata.Status)
	assert.Equal(t, []contract.BoundChannel{{Name: "Bound channel", Type: 1}}, metadata.BoundChannels)
	var endpoints []constant.EndpointType
	require.NoError(t, common.UnmarshalJsonStr(metadata.Endpoints, &endpoints))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, endpoints)
	metadata.Description, metadata.Endpoints = "", ""
	body, err := common.Marshal(metadata)
	require.NoError(t, err)
	updated = channelRequest(t, router, http.MethodPut, "/models", string(body))
	require.True(t, updated.Success, updated.Message)
	assert.Equal(t, 3, pricing.refreshes, "committed metadata mutations invalidate the pricing projection")
	var stored struct{ Description, Endpoints string }
	require.NoError(t, db.Table("models").Select("description", "endpoints").Where("id = ?", metadata.Id).Scan(&stored).Error)
	assert.Empty(t, stored.Description)
	assert.Empty(t, stored.Endpoints)
	deleted := channelRequest(t, router, http.MethodDelete, "/models/"+strconv.Itoa(metadata.Id), "")
	require.True(t, deleted.Success, deleted.Message)
	created = channelRequest(t, router, http.MethodPost, "/models", `{"model_name":"catalog-fixture"}`)
	require.True(t, created.Success, created.Message)
	deleted = channelRequest(t, router, http.MethodDelete, "/vendors/"+strconv.Itoa(vendor.Id), "")
	require.True(t, deleted.Success, deleted.Message)
	created = channelRequest(t, router, http.MethodPost, "/vendors", `{"name":"Catalog vendor"}`)
	require.True(t, created.Success, created.Message)
}

func TestCatalogSyncUsesOwnedStorageAndConditionalUpstreamRequests(t *testing.T) {
	var conditionalRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"catalog-v1"` {
			conditionalRequests.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"catalog-v1"`)
		if strings.HasSuffix(r.URL.Path, "vendors.json") {
			_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"Upstream vendor","status":1}]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"model_name":"catalog-existing","description":"upstream","vendor_name":"Upstream vendor","status":1},{"model_name":"catalog-missing","description":"new metadata","vendor_name":"Upstream vendor","status":1}]`))
	}))
	defer upstream.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", upstream.URL)
	t.Setenv("SYNC_HTTP_RETRY", "1")
	router, db, service := newChannelTestRouter(t, nil)
	existing := contract.Model{ModelName: "catalog-existing", Description: "local", Icon: "OriginalIcon", Status: 1, SyncOfficial: 1}
	require.NoError(t, service.CreateModel(t.Context(), &existing))
	var channelID int
	require.NoError(t, db.Raw(`INSERT INTO channels (name, "key", type, status, models) VALUES ('Sync channel', 'fixture', 1, 1, ARRAY['catalog-missing']) RETURNING id`).Scan(&channelID).Error)
	require.NoError(t, db.Exec(`INSERT INTO abilities ("group", model, channel_id, enabled) VALUES ('default', 'catalog-missing', ?, true)`, channelID).Error)
	preview := channelRequest(t, router, http.MethodGet, "/models/preview", "")
	require.True(t, preview.Success, preview.Message)
	var previewData struct {
		Missing   []string `json:"missing"`
		Conflicts []struct {
			ModelName string `json:"model_name"`
		} `json:"conflicts"`
	}
	require.NoError(t, common.Unmarshal(preview.Data, &previewData))
	assert.Equal(t, []string{"catalog-missing"}, previewData.Missing)
	require.Len(t, previewData.Conflicts, 1)
	assert.Equal(t, existing.ModelName, previewData.Conflicts[0].ModelName)
	result := channelRequest(t, router, http.MethodPost, "/models/sync", `{"overwrite":[{"model_name":"catalog-existing","fields":["description"]}]}`)
	require.True(t, result.Success, result.Message)
	var resultData struct {
		CreatedModels  int `json:"created_models"`
		CreatedVendors int `json:"created_vendors"`
		UpdatedModels  int `json:"updated_models"`
	}
	require.NoError(t, common.Unmarshal(result.Data, &resultData))
	assert.Equal(t, 1, resultData.CreatedModels)
	assert.Equal(t, 1, resultData.CreatedVendors)
	assert.Equal(t, 1, resultData.UpdatedModels)
	assert.EqualValues(t, 2, conditionalRequests.Load())
	loaded, err := service.Model(t.Context(), existing.Id)
	require.NoError(t, err)
	assert.Equal(t, "upstream", loaded.Description)
	assert.Equal(t, "OriginalIcon", loaded.Icon)
	metadata, err := service.AllModelMetadata(t.Context())
	require.NoError(t, err)
	require.Len(t, metadata, 2)
}
