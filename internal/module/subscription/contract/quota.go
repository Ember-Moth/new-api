package contract

import "errors"

var ErrSubscriptionQuotaInsufficient = errors.New("subscription quota insufficient")

type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}
