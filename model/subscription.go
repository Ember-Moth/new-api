package model

import (
	"context"

	subscriptionpayments "github.com/QuantumNous/new-api/internal/module/subscription/payments"

	subscriptioncontract "github.com/QuantumNous/new-api/internal/module/subscription/contract"

	"github.com/QuantumNous/new-api/common"
	subscriptionentity "github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"gorm.io/gorm"
)

// Subscription duration units
const (
	SubscriptionDurationYear   = subscriptionentity.SubscriptionDurationYear
	SubscriptionDurationMonth  = subscriptionentity.SubscriptionDurationMonth
	SubscriptionDurationDay    = subscriptionentity.SubscriptionDurationDay
	SubscriptionDurationHour   = subscriptionentity.SubscriptionDurationHour
	SubscriptionDurationCustom = subscriptionentity.SubscriptionDurationCustom
)

// Subscription quota reset period
const (
	SubscriptionResetNever   = subscriptionentity.SubscriptionResetNever
	SubscriptionResetDaily   = subscriptionentity.SubscriptionResetDaily
	SubscriptionResetWeekly  = subscriptionentity.SubscriptionResetWeekly
	SubscriptionResetMonthly = subscriptionentity.SubscriptionResetMonthly
	SubscriptionResetCustom  = subscriptionentity.SubscriptionResetCustom
)

// SubscriptionPlan is the legacy view of the subscription module's plan entity.
type SubscriptionPlan = subscriptionentity.SubscriptionPlan

type UserSubscription = subscriptionentity.UserSubscription
type SubscriptionSummary = subscriptioncontract.SubscriptionSummary
type SubscriptionResetResult = subscriptioncontract.SubscriptionResetResult

func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	return SubscriptionMemberships().CountUserSubscriptionsByPlan(context.Background(), userId, planId)
}

// GetAllActiveUserSubscriptions returns all active subscriptions for a user.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	return SubscriptionMemberships().GetAllActiveUserSubscriptions(context.Background(), userId)
}

// GetAllUserSubscriptions returns all subscriptions (active and expired) for a user.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	return SubscriptionMemberships().GetAllUserSubscriptions(context.Background(), userId)
}

// HasActiveUserSubscription returns whether the user has any active subscription.
// This is a lightweight existence check to avoid heavy pre-consume transactions.
func HasActiveUserSubscription(userId int) (bool, error) {
	return SubscriptionMemberships().HasActiveUserSubscription(context.Background(), userId)
}

// UserActiveSubscriptionsAllowWalletOverflow returns whether wallet balance may be used
// after the user's subscription quota is exhausted. A single active subscription that
// disallows wallet overflow (allow_wallet_overflow = false) blocks the fallback.
func UserActiveSubscriptionsAllowWalletOverflow(userId int) (bool, error) {
	return SubscriptionMemberships().UserActiveSubscriptionsAllowWalletOverflow(context.Background(), userId)
}

type SubscriptionPreConsumeRecord = subscriptionentity.SubscriptionPreConsumeRecord
type SubscriptionPreConsumeResult = subscriptioncontract.SubscriptionPreConsumeResult
type SubscriptionPlanInfo = subscriptioncontract.SubscriptionPlanInfo

func GetSubscriptionPlanById(id int) (*SubscriptionPlan, error) {
	return SubscriptionCatalog().Plan(context.Background(), nil, id)
}
func getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error) {
	ctx := context.Background()
	if tx != nil {
		ctx = tx.Statement.Context
	}
	return SubscriptionCatalog().Plan(ctx, tx, id)
}
func InvalidateSubscriptionPlanCache(id int) {
	if err := SubscriptionCatalog().Invalidate(id); err != nil {
		common.SysError("failed to invalidate subscription plan cache: " + err.Error())
	}
}
func GetSubscriptionPlanInfoByUserSubscriptionId(id int) (*SubscriptionPlanInfo, error) {
	return SubscriptionCatalog().PlanInfo(context.Background(), id)
}

// PreConsumeUserSubscription pre-consumes from any active subscription total quota.
func PreConsumeUserSubscription(requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	return SubscriptionQuota().PreConsumeUserSubscription(context.Background(), requestId, userId, modelName, quotaType, amount)
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func RefundSubscriptionPreConsume(requestId string) error {
	return SubscriptionQuota().RefundSubscriptionPreConsume(context.Background(), requestId)
}

// AdjustSubscriptionPreConsume changes the refundable reservation and usage in
// the same transaction, including extra reservations made during channel retries.
func AdjustSubscriptionPreConsume(requestId string, delta int64) (*SubscriptionPreConsumeResult, error) {
	return SubscriptionQuota().AdjustSubscriptionPreConsume(context.Background(), requestId, delta)
}

// Update subscription used amount by delta (positive consume more, negative refund).
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	return SubscriptionQuota().PostConsumeUserSubscriptionDelta(context.Background(), userSubscriptionId, delta)
}

type SubscriptionOrder = subscriptionentity.SubscriptionOrder

var ErrSubscriptionOrderNotFound = subscriptionpayments.ErrOrderNotFound
var ErrSubscriptionOrderStatusInvalid = subscriptionpayments.ErrOrderStatusInvalid

func CreateSubscriptionOrder(order *SubscriptionOrder) error {
	return SubscriptionPayments().Create(context.Background(), order)
}
func GetSubscriptionOrderByTradeNo(tradeNo string) *SubscriptionOrder {
	order, err := SubscriptionPayments().Get(context.Background(), tradeNo)
	if err != nil {
		return nil
	}
	return order
}
func CompleteSubscriptionOrder(tradeNo, providerPayload, expectedPaymentProvider, actualPaymentMethod string) error {
	return SubscriptionPayments().Complete(context.Background(), tradeNo, providerPayload, expectedPaymentProvider, actualPaymentMethod)
}
func ExpireSubscriptionOrder(tradeNo, expectedPaymentProvider string) error {
	return SubscriptionPayments().FinishPending(context.Background(), tradeNo, expectedPaymentProvider, common.TopUpStatusExpired)
}
func FailSubscriptionOrder(tradeNo, expectedPaymentProvider string) error {
	return SubscriptionPayments().FinishPending(context.Background(), tradeNo, expectedPaymentProvider, common.TopUpStatusFailed)
}
