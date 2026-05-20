package model

import "errors"

// Common errors
var (
	ErrDatabase = errors.New("database error")
)

// Billing errors
var (
	ErrUserQuotaInsufficient         = errors.New("user quota insufficient")
	ErrTokenQuotaInsufficient        = errors.New("token quota insufficient")
	ErrNoActiveSubscription          = errors.New("no active subscription")
	ErrSubscriptionQuotaInsufficient = errors.New("subscription quota insufficient")
)

// User auth errors
var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserEmptyCredentials = errors.New("empty credentials")
)

// Token auth errors
var (
	ErrTokenNotProvided = errors.New("token not provided")
	ErrTokenInvalid     = errors.New("token invalid")
)

// Redemption errors
var ErrRedeemFailed = errors.New("redeem.failed")

// 2FA errors
var ErrTwoFANotEnabled = errors.New("2fa not enabled")
