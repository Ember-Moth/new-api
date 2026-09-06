package accounting

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	channelentity "github.com/QuantumNous/new-api/internal/module/channel/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAccountingFixture(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool))
	require.NoError(t, schema.UpPostgres(pool))
	return New(Dependencies{DB: db}), db
}
func createAccountingUser(t *testing.T, db *gorm.DB, quota int) entity.User {
	t.Helper()
	user := entity.User{Username: "accounting-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: quota}
	require.NoError(t, db.Create(&user).Error)
	return user
}
func createAccountingToken(t *testing.T, db *gorm.DB, quota int) entity.Token {
	t.Helper()
	token := entity.Token{UserId: 1, Key: "accounting-key", Name: "accounting-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: quota}
	require.NoError(t, db.Create(&token).Error)
	return token
}
func userQuota(t *testing.T, db *gorm.DB, id int) int {
	t.Helper()
	var user entity.User
	require.NoError(t, db.First(&user, id).Error)
	return user.Quota
}
func tokenFromDB(t *testing.T, db *gorm.DB, id int) entity.Token {
	t.Helper()
	var token entity.Token
	require.NoError(t, db.First(&token, id).Error)
	return token
}
func TestBatchUpdateAccumulatesTwoMaximumRequestCharges(t *testing.T) {
	s, db := newAccountingFixture(t)
	s.deps.BatchEnabled = func() bool { return true }

	user := createAccountingUser(t, db, common.MaxQuota*2+100)
	require.NoError(t, s.DecreaseUserQuota(t.Context(), user.Id, common.MaxQuota, false))
	require.NoError(t, s.DecreaseUserQuota(t.Context(), user.Id, common.MaxQuota, false))

	require.NoError(t, s.Flush(t.Context()))
	assert.Equal(t, 100, userQuota(t, db, user.Id))
}

func TestBatchFailureRetainsAllCountersAndRetriesWithoutDoubleCharging(t *testing.T) {
	s, db := newAccountingFixture(t)
	s.deps.BatchEnabled = func() bool { return true }
	user := createAccountingUser(t, db, 100)
	token := createAccountingToken(t, db, 100)
	channel := channelentity.Channel{Name: "batch-channel", Key: "fixture", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, s.DecreaseUserQuota(t.Context(), user.Id, 10, false))
	require.NoError(t, s.DecreaseTokenQuota(t.Context(), token.Id, token.Key, 10))
	require.NoError(t, s.RecordUsage(t.Context(), user.Id, 10, 1))
	require.True(t, s.QueueChannelUsage(channel.Id, 10))
	require.NoError(t, db.Exec(`
CREATE FUNCTION fail_quota_batch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected final batch update failure'; END;
$$;
CREATE TRIGGER fail_quota_batch BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION fail_quota_batch();`).Error)
	require.Error(t, s.Flush(t.Context()))
	assert.Equal(t, 100, userQuota(t, db, user.Id))
	assert.Equal(t, 100, tokenFromDB(t, db, token.Id).RemainQuota)
	require.NoError(t, db.Exec("DROP FUNCTION fail_quota_batch() CASCADE").Error)
	require.NoError(t, s.Flush(t.Context()))
	require.NoError(t, s.Flush(t.Context()))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	updated := tokenFromDB(t, db, token.Id)
	assert.Equal(t, 90, updated.RemainQuota)
	assert.Equal(t, 10, updated.UsedQuota)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.EqualValues(t, 10, channel.UsedQuota)
}

func TestQuotaBatchReplayDoesNotRepeatCommittedDeltas(t *testing.T) {
	s, db := newAccountingFixture(t)
	user := createAccountingUser(t, db, 100)
	stores := make([]map[int]int, BatchUpdateTypeCount)
	stores[BatchUpdateTypeUserQuota] = map[int]int{user.Id: -10}
	stores[BatchUpdateTypeUsedQuota] = map[int]int{user.Id: 10}
	stores[BatchUpdateTypeRequestCount] = map[int]int{user.Id: 1}
	batch := &quotaBatch{ID: "00000000-0000-4000-8000-000000000001", Stores: stores}
	require.NoError(t, s.applyQuotaBatch(t.Context(), batch))
	require.NoError(t, s.applyQuotaBatch(t.Context(), batch))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestQuotaBatchRejectsWalletOverflowWithoutApplyingOtherDeltas(t *testing.T) {
	s, db := newAccountingFixture(t)
	user := createAccountingUser(t, db, common.MaxWalletQuota)
	stores := make([]map[int]int, BatchUpdateTypeCount)
	stores[BatchUpdateTypeUserQuota] = map[int]int{user.Id: 1}
	stores[BatchUpdateTypeUsedQuota] = map[int]int{user.Id: 10}
	batch := &quotaBatch{ID: "00000000-0000-4000-8000-000000000002", Stores: stores}
	require.ErrorIs(t, s.applyQuotaBatch(t.Context(), batch), billingcontract.ErrWalletQuotaLimitExceeded)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota, user.Quota)
	assert.Zero(t, user.UsedQuota)
}

func TestBatchOverflowNeverWrapsIntoAWalletCredit(t *testing.T) {
	for _, delta := range []int{math.MaxInt, math.MinInt} {
		s, db := newAccountingFixture(t)
		user := createAccountingUser(t, db, 100)
		s.addNewRecord(BatchUpdateTypeUserQuota, user.Id, delta)
		increment := 1
		if delta < 0 {
			increment = -1
		}
		s.addNewRecord(BatchUpdateTypeUserQuota, user.Id, increment)
		require.Error(t, s.Flush(t.Context()))
		assert.Equal(t, 100, userQuota(t, db, user.Id))
	}
}
func TestLedgerInstancesKeepIndependentBatchesAndStopDrainsPendingUsage(t *testing.T) {
	first, db := newAccountingFixture(t)
	user := createAccountingUser(t, db, 100)
	first.deps.BatchEnabled = func() bool { return true }
	second := New(Dependencies{DB: db, BatchEnabled: func() bool { return true }})
	require.NoError(t, first.DecreaseUserQuota(t.Context(), user.Id, 10, false))
	require.NoError(t, second.DecreaseUserQuota(t.Context(), user.Id, 20, false))
	require.NoError(t, first.Flush(t.Context()))
	assert.Equal(t, 90, userQuota(t, db, user.Id))
	ctx, cancel := context.WithCancel(t.Context())
	second.Start(ctx, time.Hour)
	cancel()
	require.NoError(t, second.Stop(t.Context()))
	assert.Equal(t, 70, userQuota(t, db, user.Id))
	require.NoError(t, second.Stop(t.Context()))
	assert.Equal(t, 70, userQuota(t, db, user.Id))
	require.ErrorIs(t, second.IncreaseUserQuota(ctx, user.Id, 5, false), context.Canceled)
	require.NoError(t, second.Flush(t.Context()))
	assert.Equal(t, 70, userQuota(t, db, user.Id))
}
