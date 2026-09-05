package identity_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type providerRegistryFixture struct {
	providers map[string]*entity.CustomOAuthProvider
}

func (*providerRegistryFixture) IsBuiltin(slug string) bool { return slug == "github" }
func (r *providerRegistryFixture) Register(provider *entity.CustomOAuthProvider) {
	r.providers[provider.Slug] = provider
}
func (r *providerRegistryFixture) Unregister(slug string) { delete(r.providers, slug) }

func TestProviderManagementPreservesSecretsAndRegistryConsistency(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	registry := &providerRegistryFixture{providers: make(map[string]*entity.CustomOAuthProvider)}
	service := identity.New(identity.Dependencies{DB: db, Providers: registry})
	handler := identityhttp.New(service)
	router := gin.New()
	router.GET("/providers", handler.ListProviders)
	router.GET("/providers/:id", handler.GetProvider)
	router.POST("/providers", handler.CreateProvider)
	router.PUT("/providers/:id", handler.UpdateProvider)
	router.DELETE("/providers/:id", handler.DeleteProvider)

	request := contract.CreateCustomOAuthProviderRequest{Name: "Acme", Slug: "Acme", Enabled: true, ClientId: "client", ClientSecret: "private-secret", AuthorizationEndpoint: "https://example.test/authorize", TokenEndpoint: "https://example.test/token", UserInfoEndpoint: "https://example.test/user", Icon: "OldIcon", AuthStyle: 2}
	created := providerRequest(t, router, http.MethodPost, "/providers", request)
	require.True(t, created.Success, created.Message)
	assert.NotContains(t, string(created.Data), "private-secret")
	assert.NotContains(t, string(created.Data), "client_secret")
	var provider contract.CustomOAuthProviderResponse
	require.NoError(t, common.Unmarshal(created.Data, &provider))
	assert.Equal(t, "acme", provider.Slug)
	assert.Equal(t, "sub", provider.UserIdField)
	assert.Equal(t, "openid profile email", provider.Scopes)
	require.Contains(t, registry.providers, "acme")
	assert.Equal(t, "private-secret", registry.providers["acme"].ClientSecret)

	duplicate := providerRequest(t, router, http.MethodPost, "/providers", request)
	assert.False(t, duplicate.Success)
	assert.Equal(t, "该 Slug 已被使用", duplicate.Message)
	request.Slug = "GITHUB"
	reserved := providerRequest(t, router, http.MethodPost, "/providers", request)
	assert.False(t, reserved.Success)
	assert.Equal(t, "该 Slug 与内置 OAuth 提供商冲突", reserved.Message)

	path := "/providers/" + strconv.Itoa(provider.Id)
	updated := providerRequest(t, router, http.MethodPut, path, map[string]any{"slug": "acme-new", "enabled": false, "icon": "", "auth_style": 0, "client_secret": ""})
	require.True(t, updated.Success, updated.Message)
	stored, err := service.ProviderConfig(t.Context(), provider.Id)
	require.NoError(t, err)
	assert.Equal(t, "private-secret", stored.ClientSecret)
	assert.False(t, stored.Enabled)
	assert.Empty(t, stored.Icon)
	assert.Zero(t, stored.AuthStyle)
	assert.NotContains(t, registry.providers, "acme")
	require.Contains(t, registry.providers, "acme-new")

	invalid := providerRequest(t, router, http.MethodPut, path, map[string]any{"access_policy": `{"logic":"unsupported","conditions":[{"field":"email","op":"exists"}]}`})
	assert.False(t, invalid.Success)
	stored, err = service.ProviderConfig(t.Context(), provider.Id)
	require.NoError(t, err)
	assert.Empty(t, stored.AccessPolicy)
	assert.Empty(t, registry.providers["acme-new"].AccessPolicy)

	require.NoError(t, db.Exec(`CREATE FUNCTION reject_provider_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected provider write failure'; END;
$$;
CREATE TRIGGER reject_provider_update BEFORE UPDATE ON custom_oauth_providers FOR EACH ROW EXECUTE FUNCTION reject_provider_update();`).Error)
	failed := providerRequest(t, router, http.MethodPut, path, map[string]any{"name": "not-persisted"})
	assert.False(t, failed.Success)
	stored, err = service.ProviderConfig(t.Context(), provider.Id)
	require.NoError(t, err)
	assert.Equal(t, "Acme", stored.Name)
	assert.Equal(t, "Acme", registry.providers["acme-new"].Name)
	require.NoError(t, db.Exec("DROP TRIGGER reject_provider_update ON custom_oauth_providers").Error)

	binding := entity.UserOAuthBinding{UserId: 99, ProviderId: provider.Id, ProviderUserId: "external-user"}
	require.NoError(t, db.Create(&binding).Error)
	blocked := providerRequest(t, router, http.MethodDelete, path, nil)
	assert.False(t, blocked.Success)
	assert.Contains(t, blocked.Message, "还有用户绑定")
	require.Contains(t, registry.providers, "acme-new")
	var bindings int64
	require.NoError(t, db.Model(&entity.UserOAuthBinding{}).Count(&bindings).Error)
	assert.EqualValues(t, 1, bindings)
	require.NoError(t, db.Delete(&binding).Error)
	deleted := providerRequest(t, router, http.MethodDelete, path, nil)
	require.True(t, deleted.Success, deleted.Message)
	assert.Empty(t, registry.providers)
	list := providerRequest(t, router, http.MethodGet, "/providers", nil)
	require.True(t, list.Success)
	assert.JSONEq(t, `[]`, string(list.Data))
}

type providerResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func providerRequest(t *testing.T, router http.Handler, method, path string, body any) providerResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var result providerResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}
