package identity_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	identityhttp "github.com/QuantumNous/new-api/internal/module/identity/transport/http"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedBindingProvider(t *testing.T, db *gorm.DB, slug string) *entity.CustomOAuthProvider {
	t.Helper()
	provider := &entity.CustomOAuthProvider{Name: slug, Slug: slug, Enabled: true, ClientId: "client", ClientSecret: "hidden-provider-secret", Icon: "Key"}
	require.NoError(t, db.Create(provider).Error)
	return provider
}

func TestOAuthBindingManagementScopesResponsesAndUnbinding(t *testing.T) {
	f := newUserFixture(t)
	first := seedManagedUser(t, f, "binding-first", common.RoleCommonUser)
	second := seedManagedUser(t, f, "binding-second", common.RoleAdminUser)
	provider := seedBindingProvider(t, f.db, "binding-provider")
	require.NoError(t, f.service.SetOAuthBinding(t.Context(), first.Id, provider.Id, "subject-first"))
	require.NoError(t, f.service.SetOAuthBinding(t.Context(), second.Id, provider.Id, "subject-second"))
	require.ErrorIs(t, f.service.SetOAuthBinding(t.Context(), first.Id, provider.Id, "subject-second"), identity.ErrOAuthAccountBound)
	owner, err := f.service.UserByOAuthBinding(t.Context(), provider.Id, "subject-first")
	require.NoError(t, err)
	assert.Equal(t, first.Id, owner.Id)
	actor := contract.UserActor{ID: first.Id, Role: common.RoleCommonUser}
	handler := identityhttp.New(f.service)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", actor.ID); c.Set("role", actor.Role); c.Next() })
	router.GET("/bindings", handler.OAuthBindings)
	router.GET("/users/:id/bindings", handler.AdminOAuthBindings)
	router.DELETE("/bindings/:provider_id", handler.UnbindOAuth)
	router.DELETE("/users/:id/bindings/:provider_id", handler.AdminUnbindOAuth)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/bindings", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var list struct {
		Success bool                                `json:"success"`
		Data    []contract.UserOAuthBindingResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &list))
	require.True(t, list.Success)
	require.Len(t, list.Data, 1)
	assert.Equal(t, "subject-first", list.Data[0].ProviderUserId)
	assert.Equal(t, provider.Name, list.Data[0].ProviderName)
	assert.Equal(t, provider.Slug, list.Data[0].ProviderSlug)
	assert.NotContains(t, response.Body.String(), provider.ClientSecret)
	assert.NotContains(t, response.Body.String(), "subject-second")
	actor = contract.UserActor{ID: first.Id, Role: common.RoleAdminUser}
	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, httptest.NewRequest(http.MethodDelete, "/users/"+strconv.Itoa(second.Id)+"/bindings/"+strconv.Itoa(provider.Id), nil))
	assert.Contains(t, denied.Body.String(), `"success":false`)
	taken, err := f.service.IsProviderUserIDTaken(t.Context(), provider.Id, "subject-second")
	require.NoError(t, err)
	assert.True(t, taken)
	actor.Role = common.RoleRootUser
	removed := httptest.NewRecorder()
	router.ServeHTTP(removed, httptest.NewRequest(http.MethodDelete, "/users/"+strconv.Itoa(second.Id)+"/bindings/"+strconv.Itoa(provider.Id), nil))
	assert.Contains(t, removed.Body.String(), `"success":true`)
	taken, err = f.service.IsProviderUserIDTaken(t.Context(), provider.Id, "subject-second")
	require.NoError(t, err)
	assert.False(t, taken)
	actor.Role = common.RoleCommonUser
	own := httptest.NewRecorder()
	router.ServeHTTP(own, httptest.NewRequest(http.MethodDelete, "/bindings/"+strconv.Itoa(provider.Id), nil))
	assert.Contains(t, own.Body.String(), `"success":true`)
	assert.Contains(t, own.Body.String(), "解绑成功")
	rows, err := f.service.OAuthBindings(t.Context(), actor, second.Id, false)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestOAuthBindingTransactionsAndConcurrentOwnership(t *testing.T) {
	f := newUserFixture(t)
	first := seedManagedUser(t, f, "ownership-first", common.RoleCommonUser)
	second := seedManagedUser(t, f, "ownership-second", common.RoleCommonUser)
	provider := seedBindingProvider(t, f.db, "ownership-provider")
	rollback := errors.New("rollback account registration")
	registration := model.User{Username: "rollback-registration", Password: "hash", AffCode: "rollback-registration"}
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&registration).Error; err != nil {
			return err
		}
		if err := identity.CreateUserOAuthBindingWithTx(tx, &entity.UserOAuthBinding{UserId: registration.Id, ProviderId: provider.Id, ProviderUserId: "rollback-subject"}); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var count int64
	require.NoError(t, f.db.Model(&entity.User{}).Where("id = ?", registration.Id).Count(&count).Error)
	assert.Zero(t, count)
	taken, err := f.service.IsProviderUserIDTaken(t.Context(), provider.Id, "rollback-subject")
	require.NoError(t, err)
	assert.False(t, taken)
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, id := range []int{first.Id, second.Id} {
		go func(id int) {
			<-start
			outcomes <- f.service.SetOAuthBinding(t.Context(), id, provider.Id, "shared-subject")
		}(id)
	}
	close(start)
	successes := 0
	for range 2 {
		err := <-outcomes
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, identity.ErrOAuthAccountBound)
		}
	}
	assert.Equal(t, 1, successes)
	owner, err := f.service.UserByOAuthBinding(t.Context(), provider.Id, "shared-subject")
	require.NoError(t, err)
	var binding entity.UserOAuthBinding
	require.NoError(t, f.db.Where("provider_id = ? AND user_id = ?", provider.Id, owner.Id).First(&binding).Error)
	createdAt := binding.CreatedAt
	require.NoError(t, f.service.SetOAuthBinding(t.Context(), owner.Id, provider.Id, "replacement-subject"))
	require.NoError(t, f.db.First(&binding, binding.Id).Error)
	assert.Equal(t, createdAt, binding.CreatedAt)
	assert.Equal(t, "replacement-subject", binding.ProviderUserId)
	taken, err = f.service.IsProviderUserIDTaken(t.Context(), provider.Id, "shared-subject")
	require.NoError(t, err)
	assert.False(t, taken)
}

func TestProviderDeletionCannotRaceBindingCreation(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "provider-race-user", common.RoleCommonUser)
	provider := seedBindingProvider(t, f.db, "provider-race")
	registry := &providerRegistryFixture{providers: map[string]*entity.CustomOAuthProvider{provider.Slug: provider}}
	providers := identity.New(identity.Dependencies{DB: f.db, Providers: registry})
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	results := make(chan outcome, 2)
	go func() {
		<-start
		results <- outcome{"bind", f.service.SetOAuthBinding(t.Context(), user.Id, provider.Id, "race-subject")}
	}()
	go func() { <-start; results <- outcome{"delete", providers.DeleteProvider(t.Context(), provider.Id)} }()
	close(start)
	outcomes := map[string]error{}
	for range 2 {
		result := <-results
		outcomes[result.operation] = result.err
	}
	if outcomes["bind"] == nil {
		require.Error(t, outcomes["delete"])
		require.Contains(t, registry.providers, provider.Slug)
	} else {
		require.NoError(t, outcomes["delete"])
		require.ErrorIs(t, outcomes["bind"], gorm.ErrRecordNotFound)
		assert.NotContains(t, registry.providers, provider.Slug)
	}
	var orphaned int64
	require.NoError(t, f.db.Table("user_oauth_bindings AS b").Joins("LEFT JOIN custom_oauth_providers AS p ON p.id = b.provider_id").Where("p.id IS NULL").Count(&orphaned).Error)
	assert.Zero(t, orphaned)
}

func TestExternalIdentityClaimsRemainAtomicAndReleasable(t *testing.T) {
	f := newUserFixture(t)
	first := seedManagedUser(t, f, "claim-first", common.RoleCommonUser)
	second := seedManagedUser(t, f, "claim-second", common.RoleCommonUser)
	for range 2 {
		require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
			return identity.ClaimExternalIdentityWithTx(tx, identity.ExternalIdentityProviderTelegram, "telegram-123", first.Id)
		}))
	}
	err := f.db.Transaction(func(tx *gorm.DB) error {
		return identity.ClaimExternalIdentityWithTx(tx, identity.ExternalIdentityProviderTelegram, "telegram-123", second.Id)
	})
	require.ErrorIs(t, err, identity.ErrExternalIdentityAlreadyClaimed)
	err = f.db.Transaction(func(tx *gorm.DB) error {
		return identity.ClaimExternalIdentityWithTx(tx, identity.ExternalIdentityProviderTelegram, "telegram-456", first.Id)
	})
	require.ErrorIs(t, err, identity.ErrExternalIdentityAlreadyClaimed)
	var claims []entity.ExternalIdentityClaim
	require.NoError(t, f.db.Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Equal(t, first.Id, claims[0].UserId)
	require.NoError(t, f.db.Model(first).Update("telegram_id", "telegram-123").Error)
	_, err = f.service.ClearUserBinding(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, first.Id, "telegram")
	require.NoError(t, err)
	require.NoError(t, f.db.First(first, first.Id).Error)
	assert.Empty(t, first.TelegramId)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return identity.ClaimExternalIdentityWithTx(tx, identity.ExternalIdentityProviderTelegram, "telegram-123", second.Id)
	}))
}
