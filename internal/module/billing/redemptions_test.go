package billing_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type redemptionFixture struct {
	db      *gorm.DB
	service *billing.Service
	router  *gin.Engine
	allowed bool
	audits  []string
}

func newRedemptionFixture(t *testing.T) *redemptionFixture {
	t.Helper()
	require.NoError(t, i18n.Init())
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	fixture := &redemptionFixture{db: db, allowed: true, router: gin.New()}
	fixture.service = billing.New(billing.Dependencies{DB: db, PaymentAllowed: func() bool { return fixture.allowed }})
	handler := billinghttp.New(fixture.service, billinghttp.ManagementHooks{Audit: func(c *gin.Context, action string, metadata map[string]any) {
		assert.Equal(t, 42, c.GetInt("id"))
		assert.NotEmpty(t, metadata["name"])
		fixture.audits = append(fixture.audits, action)
	}})
	fixture.router.Use(func(c *gin.Context) { c.Set("id", 42); c.Next() })
	fixture.router.GET("/redemptions", handler.ListRedemptions)
	fixture.router.GET("/redemptions/search", handler.SearchRedemptions)
	fixture.router.GET("/redemptions/:id", handler.GetRedemption)
	fixture.router.POST("/redemptions", handler.CreateRedemptions)
	fixture.router.PUT("/redemptions", handler.UpdateRedemption)
	fixture.router.DELETE("/redemptions/invalid", handler.DeleteInvalidRedemptions)
	fixture.router.DELETE("/redemptions/:id", handler.DeleteRedemption)
	return fixture
}

func TestRedemptionManagementPreservesFieldOwnershipAndBatchResults(t *testing.T) {
	fixture := newRedemptionFixture(t)
	empty := redemptionRequest(t, fixture.router, http.MethodGet, "/redemptions", nil)
	require.True(t, empty.Success, empty.Message)
	assert.Contains(t, string(empty.Data), `"items":[]`)

	created := redemptionRequest(t, fixture.router, http.MethodPost, "/redemptions", map[string]any{
		"name": "充值码", "count": 2, "quota": 3000000000,
		"user_id": 999, "key": "client-chosen", "status": 3, "redeemed_time": 99, "used_user_id": 999,
	})
	require.True(t, created.Success, created.Message)
	var keys []string
	require.NoError(t, common.Unmarshal(created.Data, &keys))
	require.Len(t, keys, 2)
	assert.NotEqual(t, keys[0], keys[1])
	assert.Len(t, keys[0], 32)
	require.Error(t, fixture.db.Create(&entity.Redemption{Name: "duplicate", Key: keys[0], Quota: 100}).Error)
	assert.Equal(t, []string{"redemption.create"}, fixture.audits)
	var row entity.Redemption
	require.NoError(t, fixture.db.First(&row, `"key" = ?`, keys[0]).Error)
	assert.Equal(t, 42, row.UserId)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, row.Status)
	assert.Zero(t, row.RedeemedTime)
	assert.Zero(t, row.UsedUserId)
	assert.Equal(t, 3000000000, row.Quota)
	assert.Positive(t, row.CreatedTime)
	path := "/redemptions/" + strconv.Itoa(row.Id)
	detail := redemptionRequest(t, fixture.router, http.MethodGet, path, nil)
	require.True(t, detail.Success, detail.Message)
	assert.Contains(t, string(detail.Data), `"DeletedAt":null`)

	// Reproduce a completed redemption between the management read and its write.
	// The configuration update must preserve the other writer's redemption state.
	require.NoError(t, fixture.db.Callback().Update().Before("gorm:update").Register("test:redeem_before_config_write", func(tx *gorm.DB) {
		require.NoError(t, fixture.db.Exec("UPDATE redemptions SET status = ?, redeemed_time = ?, used_user_id = ? WHERE id = ?", common.RedemptionCodeStatusUsed, 123456, 23, row.Id).Error)
	}))
	updated := redemptionRequest(t, fixture.router, http.MethodPut, "/redemptions", map[string]any{
		"id": row.Id, "name": "", "quota": 600, "expired_time": 0, "status": 1, "key": "changed", "used_user_id": 0,
	})
	require.NoError(t, fixture.db.Callback().Update().Remove("test:redeem_before_config_write"))
	require.True(t, updated.Success, updated.Message)
	var edited contract.Redemption
	require.NoError(t, common.Unmarshal(updated.Data, &edited))
	assert.Equal(t, common.RedemptionCodeStatusUsed, edited.Status)
	assert.Equal(t, int64(123456), edited.RedeemedTime)
	assert.Equal(t, 23, edited.UsedUserId)
	assert.Equal(t, keys[0], edited.Key)
	assert.Equal(t, 42, edited.UserId)
	assert.Equal(t, 600, edited.Quota)
	assert.Empty(t, edited.Name)
	assert.Zero(t, edited.ExpiredTime)

	status := redemptionRequest(t, fixture.router, http.MethodPut, "/redemptions?status_only=true", map[string]any{
		"id": row.Id, "status": 2, "quota": -1, "name": "ignored", "expired_time": 1,
	})
	require.True(t, status.Success, status.Message)
	require.NoError(t, common.Unmarshal(status.Data, &edited))
	assert.Equal(t, common.RedemptionCodeStatusDisabled, edited.Status)
	assert.Equal(t, 600, edited.Quota)
	assert.Empty(t, edited.Name)
	assert.Equal(t, int64(123456), edited.RedeemedTime)
	assert.Equal(t, 23, edited.UsedUserId)

	// A batch may partially succeed; return only committed keys and no success audit.
	require.NoError(t, fixture.db.Exec(`CREATE FUNCTION reject_redemption_batch() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.name = 'partial' AND EXISTS (SELECT 1 FROM redemptions WHERE name = 'partial') THEN
 RAISE EXCEPTION 'injected batch failure';
 END IF;
 RETURN NEW;
 END;
 $$;
 CREATE TRIGGER reject_redemption_batch BEFORE INSERT ON redemptions FOR EACH ROW EXECUTE FUNCTION reject_redemption_batch();`).Error)
	partial := redemptionRequest(t, fixture.router, http.MethodPost, "/redemptions", contract.CreateRedemptionsRequest{Name: "partial", Count: 3, Quota: 100})
	assert.False(t, partial.Success)
	var partialKeys []string
	require.NoError(t, common.Unmarshal(partial.Data, &partialKeys))
	require.Len(t, partialKeys, 1)
	var partialCount int64
	require.NoError(t, fixture.db.Model(&entity.Redemption{}).Where("name = ?", "partial").Count(&partialCount).Error)
	assert.Equal(t, int64(1), partialCount)
	assert.Equal(t, []string{"redemption.create"}, fixture.audits)

	deleted := redemptionRequest(t, fixture.router, http.MethodDelete, path, nil)
	require.True(t, deleted.Success, deleted.Message)
	_, err := fixture.service.GetRedemption(t.Context(), row.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var removed entity.Redemption
	require.NoError(t, fixture.db.Unscoped().First(&removed, row.Id).Error)
	assert.True(t, removed.DeletedAt.Valid)
	assert.Equal(t, 23, removed.UsedUserId)
	repeated := redemptionRequest(t, fixture.router, http.MethodDelete, path, nil)
	assert.False(t, repeated.Success)

	fixture.allowed = false
	denied := redemptionRequest(t, fixture.router, http.MethodPost, "/redemptions", contract.CreateRedemptionsRequest{Name: "denied", Count: 1, Quota: 100})
	assert.False(t, denied.Success)
	assert.Equal(t, []string{"redemption.create"}, fixture.audits)
}

func TestRedemptionValidationRejectsInvalidQuotaAndExpiry(t *testing.T) {
	fixture := newRedemptionFixture(t)
	for _, tc := range []struct {
		name    string
		request contract.CreateRedemptionsRequest
	}{
		{"empty name", contract.CreateRedemptionsRequest{Count: 1, Quota: 100}},
		{"long name", contract.CreateRedemptionsRequest{Name: strings.Repeat("名", 21), Count: 1, Quota: 100}},
		{"zero count", contract.CreateRedemptionsRequest{Name: "bad", Quota: 100}},
		{"max count", contract.CreateRedemptionsRequest{Name: "bad", Count: 101, Quota: 100}},
		{"zero quota", contract.CreateRedemptionsRequest{Name: "bad", Count: 1}},
		{"negative quota", contract.CreateRedemptionsRequest{Name: "bad", Count: 1, Quota: -1}},
		{"wallet overflow", contract.CreateRedemptionsRequest{Name: "bad", Count: 1, Quota: common.MaxWalletQuota + 1}},
		{"past expiry", contract.CreateRedemptionsRequest{Name: "bad", Count: 1, Quota: 100, ExpiredTime: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, err := fixture.service.CreateRedemptions(t.Context(), 42, tc.request)
			require.Error(t, err)
			assert.Empty(t, keys)
		})
	}
	var count int64
	require.NoError(t, fixture.db.Model(&entity.Redemption{}).Count(&count).Error)
	assert.Zero(t, count)
	keys, err := fixture.service.CreateRedemptions(t.Context(), 42, contract.CreateRedemptionsRequest{Name: "valid", Count: 1, Quota: 100})
	require.NoError(t, err)
	var row entity.Redemption
	require.NoError(t, fixture.db.First(&row, `"key" = ?`, keys[0]).Error)
	for _, request := range []contract.UpdateRedemptionRequest{
		{Id: row.Id, Name: "bad", Quota: 0},
		{Id: row.Id, Name: "bad", Quota: common.MaxWalletQuota + 1},
		{Id: row.Id, Name: "bad", Quota: 100, ExpiredTime: 1},
	} {
		_, err := fixture.service.UpdateRedemption(t.Context(), request, false)
		require.Error(t, err)
	}
	stored, err := fixture.service.GetRedemption(t.Context(), row.Id)
	require.NoError(t, err)
	assert.Equal(t, "valid", stored.Name)
	assert.Equal(t, 100, stored.Quota)
}

type redemptionResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func redemptionRequest(t *testing.T, handler http.Handler, method, path string, body any) redemptionResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result redemptionResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func TestSearchRedemptionsFiltersAndPaginates(t *testing.T) {
	fixture := newRedemptionFixture(t)
	db := fixture.db

	now := common.GetTimestamp()
	redemptions := []entity.Redemption{
		{Id: 1, Name: "alpha-active", Key: "00000000000000000000000000000001", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: 0},
		{Id: 2, Name: "alpha-future", Key: "00000000000000000000000000000002", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now + 3600},
		{Id: 3, Name: "alpha-expired", Key: "00000000000000000000000000000003", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now - 10},
		{Id: 4, Name: "beta-disabled", Key: "00000000000000000000000000000004", Status: common.RedemptionCodeStatusDisabled, ExpiredTime: 0},
		{Id: 5, Name: "beta-used", Key: "00000000000000000000000000000005", Status: common.RedemptionCodeStatusUsed, ExpiredTime: 0},
	}
	require.NoError(t, db.Create(&redemptions).Error)

	tests := []struct {
		name      string
		keyword   string
		status    string
		startIdx  int
		num       int
		wantTotal int64
		wantIds   []int
	}{
		{
			name:      "no filters returns all rows",
			num:       10,
			wantTotal: 5,
			wantIds:   []int{5, 4, 3, 2, 1},
		},
		{
			name:      "keyword filters by name prefix",
			keyword:   "alpha",
			num:       10,
			wantTotal: 3,
			wantIds:   []int{3, 2, 1},
		},
		{
			name:      "enabled status excludes expired rows",
			status:    "1",
			num:       10,
			wantTotal: 2,
			wantIds:   []int{2, 1},
		},
		{
			name:      "expired status returns enabled expired rows",
			status:    "expired",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{3},
		},
		{
			name:      "disabled status",
			status:    "2",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{4},
		},
		{
			name:      "used status",
			status:    "3",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{5},
		},
		{
			name:    "numeric keyword matches ID",
			keyword: "1", status: "1", num: 10,
			wantTotal: 1, wantIds: []int{1},
		},
		{
			name:    "numeric keyword still respects status",
			keyword: "1", status: "3", num: 10,
			wantTotal: 0, wantIds: []int{},
		},
		{
			name:      "pagination keeps unpaged total",
			startIdx:  1,
			num:       2,
			wantTotal: 5,
			wantIds:   []int{4, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, total, err := fixture.service.ListRedemptions(t.Context(), tt.keyword, tt.status, tt.startIdx, tt.num)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, total)
			gotIds := make([]int, 0, len(rows))
			for _, row := range rows {
				gotIds = append(gotIds, row.Id)
			}
			assert.Equal(t, tt.wantIds, gotIds)
		})
	}

	removed, err := fixture.service.DeleteInvalidRedemptions(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(3), removed)
	remaining, total, err := fixture.service.ListRedemptions(t.Context(), "", "", 0, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	assert.Equal(t, int64(2), total)
	assert.Equal(t, []int{2, 1}, []int{remaining[0].Id, remaining[1].Id})
	var allCount int64
	require.NoError(t, db.Unscoped().Model(&entity.Redemption{}).Count(&allCount).Error)
	assert.Equal(t, int64(5), allCount)
}

func TestRedeemRuntimeIsAtomicAndCreditsOnlyOneConcurrentCaller(t *testing.T) {
	f := newTopupFixture(t, 10)
	code := entity.Redemption{Name: "atomic code", Key: "10000000000000000000000000000001", Quota: 300, Status: common.RedemptionCodeStatusEnabled}
	require.NoError(t, f.db.Create(&code).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, err := f.store.Redeem(t.Context(), code.Key, f.user.Id); results <- err }()
	}
	close(start)
	a, b := <-results, <-results
	require.True(t, (a == nil) != (b == nil), "one code can only be used once")
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, 310, f.user.Quota)
	require.NoError(t, f.db.First(&code, code.Id).Error)
	assert.Equal(t, common.RedemptionCodeStatusUsed, code.Status)
	assert.Equal(t, f.user.Id, code.UsedUserId)
	assert.EqualValues(t, 300, f.credits.Load())
	assert.EqualValues(t, 1, f.events.Load())
	other := entity.Redemption{Name: "overflow", Key: "10000000000000000000000000000002", Quota: 11, Status: common.RedemptionCodeStatusEnabled}
	require.NoError(t, f.db.Create(&other).Error)
	require.NoError(t, f.db.Model(&f.user).Update("quota", common.MaxWalletQuota-10).Error)
	_, err := f.store.Redeem(t.Context(), other.Key, f.user.Id)
	assert.ErrorIs(t, err, contract.ErrRedeemFailed)
	require.NoError(t, f.db.First(&other, other.Id).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, other.Status)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-10, f.user.Quota)
}
