package subscription_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/subscription"
	"github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	subscriptionhttp "github.com/QuantumNous/new-api/internal/module/subscription/transport/http"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlanManagementPreservesUpdateAndVisibilityContracts(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	allowed := true
	var invalidated []int
	service := subscription.New(subscription.Dependencies{
		DB:             db,
		PaymentAllowed: func() bool { return allowed },
		GroupExists:    func(group string) bool { return group == "premium" },
		InvalidatePlan: func(id int) { invalidated = append(invalidated, id) },
	})
	handler := subscriptionhttp.New(service)
	router := gin.New()
	router.GET("/plans", handler.ListPlans)
	router.GET("/admin/plans", handler.AdminListPlans)
	router.POST("/admin/plans", handler.CreatePlan)
	router.PUT("/admin/plans/:id", handler.UpdatePlan)
	router.PATCH("/admin/plans/:id", handler.UpdatePlanStatus)

	created := planRequest(t, router, http.MethodPost, "/admin/plans", contract.UpsertPlanRequest{Plan: contract.Plan{
		Id: 999, Title: "Monthly", Subtitle: "Description", PriceAmount: 9.123456, Currency: "CNY", Enabled: true, SortOrder: 2,
		TotalAmount: 3000000000, MaxPurchasePerUser: 2, UpgradeGroup: " premium ", DowngradeGroup: " premium ",
		StripePriceId: "price_monthly", CreemProductId: "creem_monthly", WaffoPancakeProductId: "waffo_monthly",
	}})
	require.True(t, created.Success, created.Message)
	var first contract.Plan
	require.NoError(t, common.Unmarshal(created.Data, &first))
	require.Positive(t, first.Id)
	assert.NotEqual(t, 999, first.Id)
	assert.Equal(t, "USD", first.Currency)
	assert.Equal(t, "month", first.DurationUnit)
	assert.Equal(t, 1, first.DurationValue)
	assert.Equal(t, "never", first.QuotaResetPeriod)
	assert.Equal(t, "premium", first.UpgradeGroup)
	assert.Equal(t, "premium", first.DowngradeGroup)
	require.NotNil(t, first.AllowBalancePay)
	require.NotNil(t, first.AllowWalletOverflow)
	assert.True(t, *first.AllowBalancePay)
	assert.True(t, *first.AllowWalletOverflow)
	assert.Positive(t, first.CreatedAt)
	assert.Equal(t, first.CreatedAt, first.UpdatedAt)
	assert.Equal(t, []int{first.Id}, invalidated)

	second, err := service.CreatePlan(t.Context(), contract.Plan{Title: "Second", SortOrder: 2, Enabled: true})
	require.NoError(t, err)
	third, err := service.CreatePlan(t.Context(), contract.Plan{Title: "Third", SortOrder: 1, Enabled: true})
	require.NoError(t, err)
	listed := planRequest(t, router, http.MethodGet, "/plans", nil)
	require.True(t, listed.Success, listed.Message)
	var plans []contract.PlanItem
	require.NoError(t, common.Unmarshal(listed.Data, &plans))
	require.Len(t, plans, 3)
	assert.Equal(t, []int{second.Id, first.Id, third.Id}, []int{plans[0].Plan.Id, plans[1].Plan.Id, plans[2].Plan.Id})

	path := "/admin/plans/" + strconv.Itoa(first.Id)
	missingStatus := planRequest(t, router, http.MethodPatch, path, map[string]any{})
	assert.False(t, missingStatus.Success)
	assert.Equal(t, "参数错误", missingStatus.Message)
	status := planRequest(t, router, http.MethodPatch, path, map[string]any{"enabled": false})
	require.True(t, status.Success, status.Message)
	var stored entity.SubscriptionPlan
	require.NoError(t, db.First(&stored, first.Id).Error)
	assert.False(t, stored.Enabled)
	assert.Equal(t, first.Title, stored.Title)
	assert.Equal(t, first.PriceAmount, stored.PriceAmount)
	assert.Equal(t, first.TotalAmount, stored.TotalAmount)
	assert.Equal(t, first.UpgradeGroup, stored.UpgradeGroup)
	visible, err := service.ListPlans(t.Context(), true)
	require.NoError(t, err)
	require.Len(t, visible, 2)
	assert.Equal(t, []int{second.Id, third.Id}, []int{visible[0].Plan.Id, visible[1].Plan.Id})

	updated := planRequest(t, router, http.MethodPut, path, contract.UpsertPlanRequest{Plan: contract.Plan{
		Title: "Free", Enabled: false, AllowBalancePay: common.GetPointer(false), AllowWalletOverflow: common.GetPointer(false),
	}})
	require.True(t, updated.Success, updated.Message)
	assert.JSONEq(t, `null`, string(updated.Data))
	stored = entity.SubscriptionPlan{}
	require.NoError(t, db.First(&stored, first.Id).Error)
	assert.Equal(t, "Free", stored.Title)
	assert.Zero(t, stored.PriceAmount)
	assert.Zero(t, stored.TotalAmount)
	assert.Zero(t, stored.MaxPurchasePerUser)
	assert.Zero(t, stored.SortOrder)
	assert.Empty(t, stored.Subtitle)
	assert.Empty(t, stored.StripePriceId)
	assert.Empty(t, stored.CreemProductId)
	assert.Empty(t, stored.WaffoPancakeProductId)
	assert.Empty(t, stored.UpgradeGroup)
	assert.Empty(t, stored.DowngradeGroup)
	assert.False(t, stored.Enabled)
	require.NotNil(t, stored.AllowBalancePay)
	require.NotNil(t, stored.AllowWalletOverflow)
	assert.False(t, *stored.AllowBalancePay)
	assert.False(t, *stored.AllowWalletOverflow)
	assert.Equal(t, first.CreatedAt, stored.CreatedAt)

	// Omitted optional payment flags preserve the existing false values.
	omitted := planRequest(t, router, http.MethodPut, path, map[string]any{"plan": map[string]any{"title": "Renamed", "enabled": true}})
	require.True(t, omitted.Success, omitted.Message)
	stored = entity.SubscriptionPlan{}
	require.NoError(t, db.First(&stored, first.Id).Error)
	require.NotNil(t, stored.AllowBalancePay)
	require.NotNil(t, stored.AllowWalletOverflow)
	assert.False(t, *stored.AllowBalancePay)
	assert.False(t, *stored.AllowWalletOverflow)
	assert.True(t, stored.Enabled)

	beforeInvalid := len(invalidated)
	for _, tc := range []struct {
		name    string
		plan    contract.Plan
		message string
	}{
		{"title", contract.Plan{Title: " "}, "套餐标题不能为空"},
		{"negative price", contract.Plan{Title: "Bad", PriceAmount: -1}, "价格不能为负数"},
		{"maximum price", contract.Plan{Title: "Bad", PriceAmount: 10000}, "价格不能超过9999"},
		{"purchase limit", contract.Plan{Title: "Bad", MaxPurchasePerUser: -1}, "购买上限不能为负数"},
		{"quota", contract.Plan{Title: "Bad", TotalAmount: -1}, "总额度不能为负数"},
		{"upgrade group", contract.Plan{Title: "Bad", UpgradeGroup: "missing"}, "升级分组不存在"},
		{"downgrade group", contract.Plan{Title: "Bad", DowngradeGroup: "missing"}, "降级分组不存在"},
		{"custom reset", contract.Plan{Title: "Bad", QuotaResetPeriod: "custom"}, "自定义重置周期需大于0秒"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.CreatePlan(t.Context(), tc.plan)
			require.EqualError(t, err, tc.message)
			require.EqualError(t, service.UpdatePlan(t.Context(), first.Id, tc.plan), tc.message)
		})
	}
	assert.Len(t, invalidated, beforeInvalid)

	// A real database failure must not publish an invalidation or a successful response.
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_plan_update() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'injected plan write failure'; END;
 $$;
 CREATE TRIGGER reject_plan_update BEFORE UPDATE ON subscription_plans FOR EACH ROW EXECUTE FUNCTION reject_plan_update();`).Error)
	failed := planRequest(t, router, http.MethodPatch, path, map[string]any{"enabled": false})
	assert.False(t, failed.Success)
	assert.Len(t, invalidated, beforeInvalid)
	stored = entity.SubscriptionPlan{}
	require.NoError(t, db.First(&stored, first.Id).Error)
	assert.True(t, stored.Enabled)
	require.NoError(t, db.Exec("DROP TRIGGER reject_plan_update ON subscription_plans").Error)

	allowed = false
	hidden := planRequest(t, router, http.MethodGet, "/plans", nil)
	require.True(t, hidden.Success, hidden.Message)
	assert.JSONEq(t, `[]`, string(hidden.Data))
	admin := planRequest(t, router, http.MethodGet, "/admin/plans", nil)
	require.True(t, admin.Success, admin.Message)
	require.NoError(t, common.Unmarshal(admin.Data, &plans))
	assert.Len(t, plans, 3)
	denied := planRequest(t, router, http.MethodPost, "/admin/plans", contract.UpsertPlanRequest{Plan: contract.Plan{Title: "Denied"}})
	assert.False(t, denied.Success)
	require.ErrorIs(t, service.UpdatePlan(t.Context(), first.Id, contract.Plan{Title: "Denied"}), subscription.ErrPaymentComplianceRequired)
	require.ErrorIs(t, service.UpdatePlanStatus(t.Context(), first.Id, false), subscription.ErrPaymentComplianceRequired)
	assert.Len(t, invalidated, beforeInvalid)
}

type planResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func planRequest(t *testing.T, handler http.Handler, method, path string, body any) planResponse {
	t.Helper()
	data, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result planResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	return result
}
