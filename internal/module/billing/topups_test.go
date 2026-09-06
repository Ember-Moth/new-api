package billing_test

import (
	"context"
	"math"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/billing"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/entity"
	"github.com/QuantumNous/new-api/internal/module/billing/topups"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type topupFixture struct {
	db      *gorm.DB
	store   *topups.Store
	user    identityentity.User
	credits atomic.Int64
	events  atomic.Int64
}

func newTopupFixture(t *testing.T, unit float64) *topupFixture {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	f := &topupFixture{db: db, user: identityentity.User{Username: "wallet-user", Password: "fixture", Quota: 10, AuthVersion: 1}}
	require.NoError(t, db.Create(&f.user).Error)
	accounts := identity.New(identity.Dependencies{DB: db})
	f.store = topups.New(topups.Dependencies{DB: db, QuotaPerUnit: func() float64 { return unit }, Customer: accounts.ApplyPaymentCustomer, AfterCredit: func(id, amount int) error { f.credits.Add(int64(amount)); return nil }, Log: func(context.Context, contract.TopUpEvent) { f.events.Add(1) }})
	return f
}

func TestTopupProviderUnitsAndManualCompletionAgree(t *testing.T) {
	for _, test := range []struct {
		provider string
		want     int
	}{{"epay", 20}, {"stripe", 25}, {"creem", 2}, {"waffo", 20}, {"waffo_pancake", 20}} {
		t.Run(test.provider, func(t *testing.T) {
			for _, manual := range []bool{false, true} {
				f := newTopupFixture(t, 10)
				row := entity.TopUp{UserId: f.user.Id, Amount: 2, Money: 2.5, TradeNo: "unit-order", PaymentProvider: test.provider, PaymentMethod: test.provider, Status: common.TopUpStatusPending}
				require.NoError(t, f.store.Create(t.Context(), &row))
				input := contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: test.provider, ActualMethod: "actual", Manual: manual}
				already, err := f.store.Complete(t.Context(), input)
				require.NoError(t, err)
				assert.False(t, already)
				already, err = f.store.Complete(t.Context(), input)
				require.NoError(t, err)
				assert.True(t, already)
				require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
				assert.Equal(t, 10+test.want, f.user.Quota)
				require.NoError(t, f.db.First(&row, row.Id).Error)
				assert.Equal(t, common.TopUpStatusSuccess, row.Status)
				assert.Equal(t, "actual", row.PaymentMethod)
				assert.EqualValues(t, test.want, f.credits.Load())
				assert.EqualValues(t, 1, f.events.Load())
				require.ErrorIs(t, f.store.FinishPending(t.Context(), row.TradeNo, test.provider, common.TopUpStatusFailed), contract.ErrTopUpStatusInvalid)
				require.NoError(t, f.db.First(&row, row.Id).Error)
				assert.Equal(t, common.TopUpStatusSuccess, row.Status)
			}
		})
	}
}

func TestTopupConcurrentCreditCeilingAndRollback(t *testing.T) {
	f := newTopupFixture(t, 10)
	require.NoError(t, f.db.Model(&f.user).Update("quota", common.MaxWalletQuota-60).Error)
	rows := []entity.TopUp{{UserId: f.user.Id, Amount: 5, Money: 5, TradeNo: "first", PaymentProvider: "epay", Status: common.TopUpStatusPending}, {UserId: f.user.Id, Amount: 5, Money: 5, TradeNo: "second", PaymentProvider: "epay", Status: common.TopUpStatusPending}}
	require.NoError(t, f.db.Create(&rows).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, row := range rows {
		go func() {
			<-start
			_, err := f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "epay"})
			results <- err
		}()
	}
	close(start)
	a, b := <-results, <-results
	require.True(t, (a == nil) != (b == nil), "one bounded credit must commit: %v / %v", a, b)
	if a != nil {
		assert.ErrorIs(t, a, contract.ErrTopUpQuotaLimitExceeded)
	} else {
		assert.ErrorIs(t, b, contract.ErrTopUpQuotaLimitExceeded)
	}
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-10, f.user.Quota)
	assert.EqualValues(t, 50, f.credits.Load())
	assert.EqualValues(t, 1, f.events.Load())
	other := newTopupFixture(t, 10)
	row := entity.TopUp{UserId: other.user.Id, Amount: 5, Money: 5, TradeNo: "rollback", PaymentProvider: "stripe", Status: common.TopUpStatusPending}
	require.NoError(t, other.store.Create(t.Context(), &row))
	require.NoError(t, other.db.Exec("ALTER TABLE top_ups ADD CONSTRAINT reject_credit_completion CHECK (status <> 'success')").Error)
	customer := "cus_new"
	_, err := other.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "stripe", StripeCustomerID: &customer})
	require.Error(t, err)
	require.NoError(t, other.db.First(&other.user, other.user.Id).Error)
	assert.Equal(t, 10, other.user.Quota)
	assert.Empty(t, other.user.StripeCustomer)
	require.NoError(t, other.db.First(&row, row.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, row.Status)
	assert.Zero(t, other.credits.Load())
	assert.Zero(t, other.events.Load())
}

func TestTopupRejectsBadAmountsAndPreservesExistingCustomerEmail(t *testing.T) {
	for _, unit := range []float64{math.NaN(), math.Inf(1), -1, float64(common.MaxWalletQuota) + 1} {
		f := newTopupFixture(t, unit)
		row := entity.TopUp{UserId: f.user.Id, Amount: 2, Money: 2.5, TradeNo: "invalid", PaymentProvider: "epay", Status: common.TopUpStatusPending}
		require.NoError(t, f.store.Create(t.Context(), &row))
		_, err := f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "epay"})
		assert.ErrorIs(t, err, contract.ErrInvalidTopUpQuota)
		require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
		assert.Equal(t, 10, f.user.Quota)
	}
	f := newTopupFixture(t, 10)
	require.NoError(t, f.db.Model(&f.user).Update("email", "account@example.test").Error)
	row := entity.TopUp{UserId: f.user.Id, Amount: 123, Money: 2.5, TradeNo: "creem-contact", PaymentProvider: "creem", Status: common.TopUpStatusPending}
	require.NoError(t, f.store.Create(t.Context(), &row))
	_, err := f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "stripe"})
	assert.ErrorIs(t, err, contract.ErrPaymentMethodMismatch)
	_, err = f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "creem", CustomerEmail: "checkout@example.test"})
	require.NoError(t, err)
	require.NoError(t, f.db.First(&f.user, f.user.Id).Error)
	assert.Equal(t, "account@example.test", f.user.Email)
	assert.Equal(t, 133, f.user.Quota)
	require.NoError(t, f.db.Exec("ALTER TABLE top_ups RENAME TO unavailable_topups").Error)
	_, err = f.store.Complete(t.Context(), contract.TopUpCompletion{TradeNo: row.TradeNo, Provider: "creem"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, contract.ErrTopUpNotFound)
}

func TestTopupHistoryScopeEscapingAndBoundedSearchCount(t *testing.T) {
	f := newTopupFixture(t, 10)
	now := common.GetTimestamp()
	rows := []entity.TopUp{{UserId: f.user.Id, Amount: 2, TradeNo: `ref_one!\`, CreateTime: now}, {UserId: f.user.Id, Amount: 3, TradeNo: "old", CreateTime: now - 31*86400}, {UserId: f.user.Id + 1, Amount: 4, TradeNo: "foreign", CreateTime: now}}
	require.NoError(t, f.db.Create(&rows).Error)
	result, count, err := f.store.List(t.Context(), contract.TopUpQuery{UserID: f.user.Id, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.Len(t, result, 1)
	assert.Equal(t, rows[0].TradeNo, result[0].TradeNo)
	result, count, err = f.store.List(t.Context(), contract.TopUpQuery{Admin: true, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 3, count)
	assert.Len(t, result, 3)
	result, count, err = f.store.List(t.Context(), contract.TopUpQuery{UserID: f.user.Id, Keyword: `ref_one!\`, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.Len(t, result, 1)
	assert.EqualValues(t, 2, result[0].Amount)
	// 10,001 matching rows cross the public search-count ceiling. This is a
	// cardinality fixture for the bound, not a timing/performance test.
	require.NoError(t, f.db.Exec("INSERT INTO top_ups (user_id,amount,trade_no,create_time) SELECT ?,1,'bounded-' || n,? FROM generate_series(1,10001) AS n", f.user.Id, now).Error)
	result, count, err = f.store.List(t.Context(), contract.TopUpQuery{UserID: f.user.Id, Keyword: "bounded-%", Limit: 2})
	require.NoError(t, err)
	assert.EqualValues(t, 10000, count)
	require.Len(t, result, 2)
	handler := billinghttp.New(billing.New(billing.Dependencies{DB: f.db, TopUps: f.store}), billinghttp.ManagementHooks{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", f.user.Id) })
	router.GET("/self", handler.GetUserTopUps)
	router.GET("/admin", handler.GetAllTopUps)
	response := redemptionRequest(t, router, http.MethodGet, "/self?keyword=foreign&user_id=999&admin=true", nil)
	require.True(t, response.Success, response.Message)
	assert.Contains(t, string(response.Data), `"total":0`)
	response = redemptionRequest(t, router, http.MethodGet, "/admin?keyword=old", nil)
	require.True(t, response.Success, response.Message)
	assert.Contains(t, string(response.Data), `"total":1`)
}
