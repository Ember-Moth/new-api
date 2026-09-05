package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type tokenFixture struct {
	db                   *gorm.DB
	service              *identity.Service
	router               *gin.Engine
	maxTokens, maxGroups int
	invalidated          []string
	beforeMutation       func(string)
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	require.NoError(t, i18n.Init())
	originalAuto, originalUsable, originalRatios := setting.AutoGroups2JsonString(), setting.UserUsableGroups2JSONString(), ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","missing","default"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAuto))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsable))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	f := &tokenFixture{db: db, router: gin.New(), maxTokens: 100, maxGroups: 5}
	f.service = identity.New(identity.Dependencies{
		DB: db,
		TokenPolicy: identity.TokenPolicy{
			MaxTokens: func() int { return f.maxTokens }, MaxAutoGroups: func() int { return f.maxGroups },
			UserGroup:         func(ctx context.Context, userID int) (string, error) { return "default", nil },
			IsSelectableGroup: service.IsUserSelectableGroup, AutoGroups: service.GetUserAutoGroup,
		},
		InvalidateTokenCache: func(key string) error {
			f.invalidated = append(f.invalidated, key)
			if f.beforeMutation != nil {
				f.beforeMutation(key)
			}
			return nil
		},
	})
	h := identityhttp.New(f.service)
	f.router.Use(func(c *gin.Context) { id, _ := strconv.Atoi(c.GetHeader("X-Test-User")); c.Set("id", id); c.Next() })
	f.router.GET("/tokens", h.ListTokens)
	f.router.GET("/tokens/search", h.SearchTokens)
	f.router.GET("/tokens/auto-groups", h.TokenAutoGroups)
	f.router.GET("/tokens/:id", h.GetToken)
	f.router.POST("/tokens/:id/key", h.TokenKey)
	f.router.POST("/tokens/keys", h.TokenKeys)
	f.router.POST("/tokens", h.CreateToken)
	f.router.PUT("/tokens", h.UpdateToken)
	f.router.DELETE("/tokens/:id", h.DeleteToken)
	f.router.POST("/tokens/batch", h.DeleteTokens)
	return f
}

func seedIdentityToken(t *testing.T, f *tokenFixture, userID int, name, key string) *entity.Token {
	t.Helper()
	token := &entity.Token{UserId: userID, Name: name, Key: key, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100, Group: "auto", CrossGroupRetry: true, AutoGroups: value.StringList{"vip", "default"}, ModelLimits: value.StringList{"model-a", "model-b"}}
	require.NoError(t, f.db.Create(token).Error)
	return token
}

func TestTokenManagementMasksSecretsAndEnforcesOwnership(t *testing.T) {
	f := newTokenFixture(t)
	// Exercise the SQL-owned varchar(128) and native array storage contracts.
	owned := seedIdentityToken(t, f, 1, "owned", strings.Repeat("a1", 64))
	foreign := seedIdentityToken(t, f, 2, "foreign", strings.Repeat("b2", 64))
	path := "/tokens/" + strconv.Itoa(owned.Id)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/tokens", nil},
		{http.MethodGet, "/tokens/search?keyword=owned", nil},
		{http.MethodGet, path, nil},
		{http.MethodPut, "/tokens", map[string]any{"id": owned.Id, "name": "updated", "expired_time": -1, "remain_quota": 100, "group": "auto"}},
	} {
		response := tokenRequest(t, f.router, 1, tc.method, tc.path, tc.body)
		require.True(t, response.Success, response.Message)
		assert.NotContains(t, string(response.Data), owned.Key)
		assert.Contains(t, string(response.Data), owned.GetMaskedKey())
		assert.NotContains(t, string(response.Data), foreign.GetMaskedKey())
	}
	keyResponse := tokenRequest(t, f.router, 1, http.MethodPost, path+"/key", nil)
	require.True(t, keyResponse.Success, keyResponse.Message)
	assert.JSONEq(t, `{"key":"`+owned.Key+`"}`, string(keyResponse.Data))
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, path, nil},
		{http.MethodPost, path + "/key", nil},
		{http.MethodPut, "/tokens", map[string]any{"id": owned.Id, "name": "not-owned", "unlimited_quota": true}},
		{http.MethodDelete, path, nil},
	} {
		denied := tokenRequest(t, f.router, 2, tc.method, tc.path, tc.body)
		assert.False(t, denied.Success)
		assert.NotContains(t, string(denied.Data), owned.Key)
	}
	batch := tokenRequest(t, f.router, 1, http.MethodPost, "/tokens/keys", contract.TokenBatch{Ids: []int{owned.Id, foreign.Id, owned.Id}})
	require.True(t, batch.Success, batch.Message)
	var batchData struct {
		Keys map[int]string `json:"keys"`
	}
	require.NoError(t, common.Unmarshal(batch.Data, &batchData))
	assert.Equal(t, map[int]string{owned.Id: owned.Key}, batchData.Keys)
	deleted := tokenRequest(t, f.router, 1, http.MethodPost, "/tokens/batch", contract.TokenBatch{Ids: []int{owned.Id, foreign.Id, owned.Id}})
	require.True(t, deleted.Success, deleted.Message)
	assert.Equal(t, "1", string(deleted.Data))
	_, err := f.service.TokenRecord(t.Context(), owned.Id, 1)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.service.TokenRecord(t.Context(), foreign.Id, 2)
	require.NoError(t, err)
	var softDeleted entity.Token
	require.NoError(t, f.db.Unscoped().First(&softDeleted, owned.Id).Error)
	assert.True(t, softDeleted.DeletedAt.Valid)
	assert.Equal(t, []string{owned.Key, owned.Key}, f.invalidated)
}

func TestTokenMutationsPreserveConcurrentAccountingAndStatus(t *testing.T) {
	f := newTokenFixture(t)
	token := seedIdentityToken(t, f, 1, "concurrent", "concurrent-token-key")
	f.beforeMutation = func(key string) {
		assert.Equal(t, token.Key, key)
		require.NoError(t, f.db.Exec("UPDATE tokens SET status = ?, accessed_time = 123, used_quota = 7 WHERE id = ?", common.TokenStatusDisabled, token.Id).Error)
	}
	response, err := f.service.UpdateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{
		TokenSettings: contract.TokenSettings{Id: token.Id, Name: "", ExpiredTime: -1, RemainQuota: 0, Group: "default", ModelLimits: []string{}},
	}, false)
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusDisabled, response.Status)
	assert.Equal(t, int64(123), response.AccessedTime)
	assert.Equal(t, 7, response.UsedQuota)
	assert.Zero(t, response.RemainQuota)
	assert.Empty(t, response.Name)
	assert.Empty(t, response.ModelLimits)
	assert.Nil(t, response.AutoGroups)
	assert.False(t, response.CrossGroupRetry)
	f.beforeMutation = func(key string) {
		require.NoError(t, f.db.Exec("UPDATE tokens SET remain_quota = 73, used_quota = 27 WHERE id = ?", token.Id).Error)
	}
	response, err = f.service.UpdateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{
		TokenSettings: contract.TokenSettings{Id: token.Id, Status: common.TokenStatusEnabled, Name: "ignored", RemainQuota: 1000, Group: "auto"},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, common.TokenStatusEnabled, response.Status)
	assert.Equal(t, 73, response.RemainQuota)
	assert.Equal(t, 27, response.UsedQuota)
	assert.Empty(t, response.Name)
	assert.Equal(t, "default", response.Group)
	f.beforeMutation = nil
	require.NoError(t, f.service.DeleteToken(t.Context(), token.Id, 1))
	assert.Equal(t, []string{token.Key, token.Key, token.Key}, f.invalidated)
}

func TestTokenAutoGroupsPreserveOmissionAndExplicitInheritance(t *testing.T) {
	f := newTokenFixture(t)
	for _, tc := range []struct {
		name   string
		set    bool
		groups any
	}{
		{name: "omitted"}, {name: "null", set: true}, {name: "empty", set: true, groups: []string{}},
	} {
		t.Run("create "+tc.name, func(t *testing.T) {
			body := map[string]any{"name": tc.name, "expired_time": -1, "unlimited_quota": true, "group": "auto", "cross_group_retry": true}
			if tc.set {
				body["auto_groups"] = tc.groups
			}
			result := tokenRequest(t, f.router, 1, http.MethodPost, "/tokens", body)
			require.True(t, result.Success, result.Message)
			var token entity.Token
			require.NoError(t, f.db.First(&token, "name = ?", tc.name).Error)
			assert.Empty(t, token.AutoGroups)
			assert.True(t, token.CrossGroupRetry)
			masked, err := f.service.GetToken(t.Context(), token.Id, 1)
			require.NoError(t, err)
			assert.Nil(t, masked.AutoGroups)
		})
	}
	ordered := tokenRequest(t, f.router, 1, http.MethodPost, "/tokens", map[string]any{
		"id": 999, "user_id": 2, "key": "injected", "status": 2, "used_quota": 99,
		"name": "ordered", "expired_time": -1, "unlimited_quota": true, "group": "auto", "cross_group_retry": true,
		"auto_groups": []string{"vip", "default"}, "model_limits": []string{"model-a", "model-b"},
	})
	require.True(t, ordered.Success, ordered.Message)
	var stored entity.Token
	require.NoError(t, f.db.First(&stored, "name = ?", "ordered").Error)
	assert.NotEqual(t, 999, stored.Id)
	assert.Equal(t, 1, stored.UserId)
	assert.NotEqual(t, "injected", stored.Key)
	assert.Equal(t, common.TokenStatusEnabled, stored.Status)
	assert.Zero(t, stored.UsedQuota)
	assert.Equal(t, value.StringList{"vip", "default"}, stored.AutoGroups)
	assert.Equal(t, value.StringList{"model-a", "model-b"}, stored.ModelLimits)
	for _, tc := range []struct {
		name, group string
		set         bool
		groups      any
		want        []string
		retry       bool
	}{
		{name: "omitted", group: "auto", want: []string{"vip", "default"}, retry: true},
		{name: "null", group: "auto", set: true, retry: true},
		{name: "empty", group: "auto", set: true, groups: []string{}, retry: true},
		{name: "non-auto", group: "default", set: true, groups: []string{"vip"}},
	} {
		t.Run("update "+tc.name, func(t *testing.T) {
			token := seedIdentityToken(t, f, 1, "update-"+tc.name, "update-"+tc.name+"-key")
			body := map[string]any{"id": token.Id, "name": "updated", "expired_time": -1, "unlimited_quota": true, "group": tc.group, "cross_group_retry": true}
			if tc.set {
				body["auto_groups"] = tc.groups
			}
			result := tokenRequest(t, f.router, 1, http.MethodPut, "/tokens", body)
			require.True(t, result.Success, result.Message)
			var response contract.TokenResponse
			require.NoError(t, common.Unmarshal(result.Data, &response))
			assert.Equal(t, tc.want, response.AutoGroups)
			assert.Equal(t, tc.retry, response.CrossGroupRetry)
		})
	}
	// The selectable global list keeps its full order, even when the per-token limit is smaller.
	f.maxGroups = 1
	options := tokenRequest(t, f.router, 1, http.MethodGet, "/tokens/auto-groups", nil)
	require.True(t, options.Success, options.Message)
	assert.JSONEq(t, `{"groups":["vip","default"],"max_count":1}`, string(options.Data))
}

func TestTokenValidationAndSearchContracts(t *testing.T) {
	f := newTokenFixture(t)
	f.maxGroups = 1
	for _, tc := range []struct {
		name     string
		settings contract.TokenSettings
		groups   []string
	}{
		{name: "name", settings: contract.TokenSettings{Name: strings.Repeat("a", 51)}},
		{name: "negative", settings: contract.TokenSettings{RemainQuota: -1}},
		{name: "too much quota", settings: contract.TokenSettings{RemainQuota: common.MaxWalletQuota}},
		{name: "too many groups", settings: contract.TokenSettings{Group: "auto"}, groups: []string{"default", "vip"}},
		{name: "unavailable", settings: contract.TokenSettings{Group: "auto"}, groups: []string{"missing"}},
		{name: "auto pseudo-group", settings: contract.TokenSettings{Group: "auto"}, groups: []string{"auto"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.service.CreateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{TokenSettings: tc.settings, AutoGroups: contract.TokenAutoGroupsInput{Set: true, Groups: tc.groups}})
			require.Error(t, err)
		})
	}
	f.maxGroups = 5
	err := f.service.CreateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{TokenSettings: contract.TokenSettings{Group: "auto"}, AutoGroups: contract.TokenAutoGroupsInput{Set: true, Groups: []string{"default", "default"}}})
	var validation *identity.TokenValidationError
	require.ErrorAs(t, err, &validation)
	assert.Equal(t, "auto_groups_duplicate", validation.Code)
	var count int64
	require.NoError(t, f.db.Model(&entity.Token{}).Count(&count).Error)
	assert.Zero(t, count)
	literal := seedIdentityToken(t, f, 1, "team_1", "literal_key!1234")
	seedIdentityToken(t, f, 1, "teamX1", "other-key1234")
	seedIdentityToken(t, f, 2, "team_1", "foreign-key1234")
	for _, tc := range []struct {
		name, key string
		total     int64
	}{
		{name: "team_1", total: 1},
		{name: "team%", total: 2},
		{key: "sk-literal_key!1234", total: 1},
	} {
		rows, total, err := f.service.SearchTokens(t.Context(), 1, tc.name, tc.key, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, tc.total, total)
		require.Len(t, rows, int(tc.total))
		if tc.total == 1 {
			assert.Equal(t, literal.Id, rows[0].Id)
		}
	}
	rows, total, err := f.service.ListTokens(t.Context(), 1, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 1)
	assert.Equal(t, literal.Id, rows[0].Id)
	for _, pattern := range []string{"%%", "%x%", "%ab%cd%"} {
		_, _, err := f.service.SearchTokens(t.Context(), 1, pattern, "", 0, 10)
		require.Error(t, err)
	}
	f.maxTokens = 1
	_, _, err = f.service.SearchTokens(t.Context(), 1, "team%", "", 0, 10)
	require.EqualError(t, err, "令牌数量超过上限，仅允许精确搜索，请勿使用 % 通配符")
	rows, total, err = f.service.SearchTokens(t.Context(), 1, "team_1", "", 0, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Equal(t, int64(1), total)
	require.EqualError(t, f.service.CreateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{}), "已达到最大令牌数量限制 (1)")
	_, err = f.service.TokenKeys(t.Context(), nil, 1)
	require.Error(t, err)
	_, err = f.service.TokenKeys(t.Context(), make([]int, 101), 1)
	require.Error(t, err)
	for _, status := range []int{common.TokenStatusExpired, common.TokenStatusExhausted} {
		require.NoError(t, f.db.Model(literal).Updates(map[string]any{"status": status, "expired_time": 1, "remain_quota": 0, "unlimited_quota": false}).Error)
		_, err = f.service.UpdateToken(t.Context(), contract.TokenActor{ID: 1}, contract.TokenRequest{TokenSettings: contract.TokenSettings{Id: literal.Id, Status: common.TokenStatusEnabled}}, true)
		require.Error(t, err)
	}
	assert.Empty(t, f.invalidated)
}

type tokenAPIResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func tokenRequest(t *testing.T, handler http.Handler, userID int, method, path string, body any) tokenAPIResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-User", strconv.Itoa(userID))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response tokenAPIResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return response
}
