package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/module/billing/accounting"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	subcontract "github.com/QuantumNous/new-api/internal/module/subscription/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	"github.com/QuantumNous/new-api/internal/module/subscription/quota"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Dependencies struct {
	Accounting    *accounting.Store
	Users         *usercache.Store
	Tokens        *tokencache.Store
	Subscriptions *quota.Store
	Memberships   *memberships.Store
	Catalog       *catalog.Store
	TrustQuota    func() int
}

type Engine struct{ deps Dependencies }

var errInsufficientWallet = errors.New("wallet quota insufficient")

func New(deps Dependencies) *Engine { return &Engine{deps: deps} }

func failure(kind contract.BillingFailureKind, err error) error {
	return &contract.BillingFailure{Kind: kind, Cause: err}
}

func insufficient(err error) bool {
	var e *contract.BillingFailure
	return errors.As(err, &e) && e.Kind == contract.BillingInsufficientFunds
}

func (e *Engine) Begin(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	if err := validateBeginInput(input, amount); err != nil {
		return nil, err
	}
	if session, found, err := e.findExisting(ctx, input, amount); err != nil {
		return nil, err
	} else if found {
		return session, nil
	}

	switch common.NormalizeBillingPreference(input.Preference) {
	case "wallet_only":
		return e.beginWallet(ctx, input, amount)
	case "subscription_only":
		return e.beginSubscription(ctx, input, amount)
	case "wallet_first":
		session, err := e.beginWallet(ctx, input, amount)
		if insufficient(err) {
			return e.beginSubscription(ctx, input, amount)
		}
		return session, err
	default:
		if e.deps.Memberships == nil {
			return nil, failure(contract.BillingQueryFailure, errors.New("subscription membership store is unavailable"))
		}
		active, err := e.deps.Memberships.HasActiveUserSubscription(ctx, input.UserID)
		if err != nil {
			return nil, failure(contract.BillingQueryFailure, err)
		}
		if !active {
			return e.beginWallet(ctx, input, amount)
		}
		session, err := e.beginSubscription(ctx, input, amount)
		if !insufficient(err) {
			return session, err
		}
		overflow, checkErr := e.deps.Memberships.UserActiveSubscriptionsAllowWalletOverflow(ctx, input.UserID)
		if checkErr != nil {
			return nil, failure(contract.BillingQueryFailure, checkErr)
		}
		if overflow {
			return e.beginWallet(ctx, input, amount)
		}
		return nil, err
	}
}

// RecoverPending retries only terminal actions explicitly recorded before a
// prior settlement/refund attempt. Pure active sessions are intentionally left
// untouched because their upstream outcome is not known.
func (e *Engine) RecoverPending(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var records []billingSessionRecord
	err := e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("status = ? AND pending_action IN ? AND intent_requires_commit = ?", sessionStatusActive, []string{"settle", "refund"}, false).
			Order("updated_at asc, request_id asc").Limit(limit).Find(&records).Error
	})
	if err != nil {
		return 0, err
	}
	recovered := 0
	var recoveryErr error
	for _, record := range records {
		var token identityentity.Token
		err := error(nil)
		if record.Playground {
			token = identityentity.Token{}
		} else {
			err = e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
				return tx.Unscoped().Where("id = ? AND user_id = ?", record.TokenID, record.UserID).First(&token).Error
			})
		}
		if err == nil {
			input := contract.BillingRequest{
				RequestID:       record.RequestID,
				ModelName:       record.ModelName,
				Preference:      record.Preference,
				UserID:          record.UserID,
				TokenID:         record.TokenID,
				TokenKey:        token.Key,
				TokenUnlimited:  record.TokenUnlimited,
				Playground:      record.Playground,
				ForcePreConsume: record.ForcePreConsume,
			}
			session := &Session{engine: e, input: input, state: stateFromRecord(&record)}
			switch record.PendingAction {
			case "settle":
				if record.IntentActual == nil {
					err = errors.New("settlement intent has no actual quota")
				} else if record.IntentUsage {
					err = session.SettleWithUsage(ctx, *record.IntentActual, record.IntentChannel)
				} else {
					err = session.Settle(ctx, *record.IntentActual)
				}
			case "refund":
				err = session.Refund(ctx)
			}
		}
		if err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover billing session %s: %w", record.RequestID, err))
			continue
		}
		recovered++
	}
	return recovered, recoveryErr
}

// Resume reopens a durable billing session by request id. It does not perform
// a new authorization or reserve operation. For an already-authorized
// session, the current token key is loaded only as a write/cache handle so a
// rotation or soft delete cannot strand terminal settlement.
func (e *Engine) Resume(ctx context.Context, requestID string) (*Session, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 64 {
		return nil, failure(contract.BillingInvalidRequest, errors.New("billing request id is required"))
	}
	var record billingSessionRecord
	var tokenKey string
	err := e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestID).First(&record).Error; err != nil {
			return err
		}
		if err := record.validate(); err != nil {
			return failure(contract.BillingStorageFailure, err)
		}
		if record.Playground {
			return nil
		}
		var token identityentity.Token
		if err := tx.Unscoped().Select("id", "user_id", "key").Where("id = ? AND user_id = ?", record.TokenID, record.UserID).First(&token).Error; err != nil {
			return err
		}
		tokenKey = token.Key
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Session{
		engine: e,
		input: contract.BillingRequest{
			RequestID:       record.RequestID,
			ModelName:       record.ModelName,
			Preference:      record.Preference,
			UserID:          record.UserID,
			TokenID:         record.TokenID,
			TokenKey:        tokenKey,
			TokenUnlimited:  record.TokenUnlimited,
			Playground:      record.Playground,
			ForcePreConsume: record.ForcePreConsume,
		},
		state: stateFromRecord(&record),
	}, nil
}

func validateBeginInput(input contract.BillingRequest, amount int) error {
	if strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 64 {
		return failure(contract.BillingInvalidRequest, errors.New("billing request id is required"))
	}
	if input.UserID <= 0 {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is required"))
	}
	if input.TokenID < 0 {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is invalid"))
	}
	if !input.Playground && (input.TokenID <= 0 || strings.TrimSpace(input.TokenKey) == "") {
		return failure(contract.BillingInvalidRequest, errors.New("billing token identity is required"))
	}
	return validateQuota(amount)
}

func (e *Engine) findExisting(ctx context.Context, input contract.BillingRequest, amount int) (*Session, bool, error) {
	var found *billingSessionRecord
	err := e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		var record billingSessionRecord
		queryErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", input.RequestID).First(&record).Error
		if errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		if err := e.validateSessionRecordTx(ctx, tx, &record, input, "", &amount, false); err != nil {
			return err
		}
		found = &record
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if found == nil {
		return nil, false, nil
	}
	return &Session{engine: e, input: input, state: stateFromRecord(found)}, true, nil
}

func (e *Engine) beginWallet(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	var record *billingSessionRecord
	var userChanged, tokenChanged bool
	err := e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		var err error
		var created bool
		record, created, err = e.lockOrCreateSessionTx(ctx, tx, input, contract.BillingSourceWallet, amount)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		accountingTx := e.deps.Accounting.WithTx(tx)
		userQuota, err := accountingTx.UserQuotaTx(ctx, input.UserID)
		if err != nil {
			return failure(contract.BillingQueryFailure, err)
		}
		if userQuota <= 0 {
			return failure(contract.BillingInsufficientFunds, fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)))
		}
		if !input.Playground {
			authoritativeUnlimited, identityErr := accountingTx.ValidateTokenIdentity(ctx, input.UserID, input.TokenID, input.TokenKey)
			if identityErr != nil {
				return failure(contract.BillingSessionConflict, identityErr)
			}
			if authoritativeUnlimited != input.TokenUnlimited {
				return sessionConflict("token unlimited-quota authorization changed")
			}
		}
		trusted := false
		trust := 0
		if e.deps.TrustQuota != nil {
			trust = e.deps.TrustQuota()
		}
		if !input.ForcePreConsume && trust > 0 && userQuota > trust && (input.TokenUnlimited || input.TokenQuota > trust) {
			trusted = true
		}
		reserved := amount
		if trusted {
			reserved = 0
		}
		if reserved > 0 {
			reservedOK, reserveErr := accountingTx.TryReserveUserQuota(ctx, input.UserID, reserved)
			if reserveErr != nil {
				return e.fundingFailure(reserveErr)
			}
			if !reservedOK {
				return failure(contract.BillingInsufficientFunds, fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)))
			}
			userChanged = true
		}
		if !input.Playground && reserved > 0 {
			reservedOK, reserveErr := accountingTx.TryReserveTokenQuota(ctx, input.TokenID, input.TokenKey, reserved, input.TokenUnlimited)
			if reserveErr != nil {
				return failure(contract.BillingInsufficientToken, reserveErr)
			}
			if !reservedOK {
				return failure(contract.BillingInsufficientToken, errors.New("token quota is not enough"))
			}
			tokenChanged = true
		}
		record.UserQuota = userQuota
		record.ReservedQuota = reserved
		record.Trusted = trusted
		if err := tx.Model(record).Updates(map[string]any{"user_quota": userQuota, "reserved_quota": reserved, "trusted": trusted, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	session := &Session{engine: e, input: input, state: stateFromRecord(record)}
	e.publishChanges(ctx, input, userChanged, tokenChanged)
	return session, nil
}

func (e *Engine) beginSubscription(ctx context.Context, input contract.BillingRequest, amount int) (*Session, error) {
	var record *billingSessionRecord
	var tokenChanged bool
	err := e.deps.Accounting.Transaction(ctx, func(tx *gorm.DB) error {
		var err error
		var created bool
		record, created, err = e.lockOrCreateSessionTx(ctx, tx, input, contract.BillingSourceSubscription, amount)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		if _, err := e.deps.Accounting.WithTx(tx).UserQuotaTx(ctx, input.UserID); err != nil {
			return e.fundingFailure(err)
		}
		reservationAmount := max(1, amount)
		if e.deps.Subscriptions == nil {
			return failure(contract.BillingStorageFailure, errors.New("subscription quota store is unavailable"))
		}
		if e.deps.Catalog == nil {
			return failure(contract.BillingStorageFailure, errors.New("subscription catalog store is unavailable"))
		}
		result, err := e.deps.Subscriptions.WithTx(tx).PreConsumeUserSubscriptionTx(ctx, tx, input.RequestID, input.UserID, input.ModelName, 0, int64(reservationAmount))
		if err != nil {
			return e.fundingFailure(err)
		}
		if !input.Playground {
			authoritativeUnlimited, identityErr := e.deps.Accounting.WithTx(tx).ValidateTokenIdentity(ctx, input.UserID, input.TokenID, input.TokenKey)
			if identityErr != nil {
				return failure(contract.BillingSessionConflict, identityErr)
			}
			if authoritativeUnlimited != input.TokenUnlimited {
				return sessionConflict("token unlimited-quota authorization changed")
			}
		}
		if !input.Playground {
			reservedOK, reserveErr := e.deps.Accounting.WithTx(tx).TryReserveTokenQuota(ctx, input.TokenID, input.TokenKey, reservationAmount, input.TokenUnlimited)
			if reserveErr != nil {
				return failure(contract.BillingInsufficientToken, reserveErr)
			}
			if !reservedOK {
				return failure(contract.BillingInsufficientToken, errors.New("token quota is not enough"))
			}
			tokenChanged = true
		}
		record.ReservedQuota = reservationAmount
		record.SubscriptionID = result.UserSubscriptionId
		record.SubscriptionTotal = result.AmountTotal
		record.SubscriptionUsed = result.AmountUsedAfter
		var sub entity.UserSubscription
		if err := tx.Select("id", "plan_id").Where("id = ? AND user_id = ?", record.SubscriptionID, input.UserID).First(&sub).Error; err != nil {
			return err
		}
		plan, err := e.deps.Catalog.Plan(ctx, tx, sub.PlanId)
		if err != nil {
			return err
		}
		record.PlanID = plan.Id
		record.PlanTitle = plan.Title
		if err := tx.Model(record).Updates(map[string]any{"reserved_quota": record.ReservedQuota, "subscription_id": record.SubscriptionID, "plan_id": record.PlanID, "plan_title": record.PlanTitle, "subscription_total": record.SubscriptionTotal, "subscription_used": record.SubscriptionUsed, "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	session := &Session{engine: e, input: input, state: stateFromRecord(record)}
	e.publishChanges(ctx, input, false, tokenChanged)
	return session, nil
}

func (e *Engine) lockOrCreateSessionTx(ctx context.Context, tx *gorm.DB, input contract.BillingRequest, source string, amount int) (*billingSessionRecord, bool, error) {
	if tx == nil {
		return nil, false, errors.New("billing transaction is nil")
	}
	normalizedPreference := common.NormalizeBillingPreference(input.Preference)
	record := &billingSessionRecord{
		RequestID:       input.RequestID,
		UserID:          input.UserID,
		TokenID:         input.TokenID,
		Source:          source,
		ModelName:       input.ModelName,
		Preference:      normalizedPreference,
		RequestedQuota:  amount,
		ReservedQuota:   0,
		SubscriptionID:  0,
		TokenUnlimited:  input.TokenUnlimited,
		Playground:      input.Playground,
		ForcePreConsume: input.ForcePreConsume,
		Status:          sessionStatusActive,
	}
	insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(record)
	if insert.Error != nil {
		return nil, false, insert.Error
	}
	inserted := insert.RowsAffected == 1
	if !inserted {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", input.RequestID).First(record).Error; err != nil {
			return nil, false, err
		}
		if err := e.validateSessionRecordTx(ctx, tx, record, input, source, &amount, false); err != nil {
			return nil, false, err
		}
	}
	return record, inserted, nil
}

func (e *Engine) lockSessionTx(ctx context.Context, tx *gorm.DB, input contract.BillingRequest, source string, amount *int) (*billingSessionRecord, error) {
	var record billingSessionRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", input.RequestID).First(&record).Error; err != nil {
		return nil, err
	}
	if err := e.validateSessionRecordTx(ctx, tx, &record, input, source, amount, false); err != nil {
		return nil, err
	}
	return &record, nil
}

func (e *Engine) validateSessionRecordTx(ctx context.Context, tx *gorm.DB, record *billingSessionRecord, input contract.BillingRequest, source string, amount *int, verifyCurrentToken bool) error {
	if err := record.validate(); err != nil {
		return failure(contract.BillingStorageFailure, err)
	}
	if record.UserID != input.UserID || record.TokenID != input.TokenID {
		return sessionConflict("billing request identity conflicts with the durable session")
	}
	if amount != nil && record.RequestedQuota != *amount {
		return sessionConflict("billing request target conflicts with the durable session")
	}
	if source != "" && record.Source != source {
		return sessionConflict("billing funding source conflicts with the durable session")
	}
	if record.ModelName != input.ModelName || record.Preference != common.NormalizeBillingPreference(input.Preference) ||
		record.TokenUnlimited != input.TokenUnlimited || record.Playground != input.Playground || record.ForcePreConsume != input.ForcePreConsume {
		return sessionConflict("billing authorization attributes conflict with the durable session")
	}
	if verifyCurrentToken && !input.Playground {
		authoritativeUnlimited, err := e.deps.Accounting.WithTx(tx).ValidateTokenIdentity(ctx, input.UserID, input.TokenID, input.TokenKey)
		if err != nil {
			return failure(contract.BillingSessionConflict, err)
		}
		if authoritativeUnlimited != input.TokenUnlimited {
			return sessionConflict("token unlimited-quota authorization changed")
		}
	}
	return nil
}

func (e *Engine) refreshSubscriptionStateTx(ctx context.Context, tx *gorm.DB, record *billingSessionRecord) error {
	if record.SubscriptionID <= 0 {
		return errors.New("billing session subscription is missing")
	}
	var sub entity.UserSubscription
	if err := tx.Where("id = ? AND user_id = ?", record.SubscriptionID, record.UserID).First(&sub).Error; err != nil {
		return err
	}
	record.SubscriptionTotal = sub.AmountTotal
	record.SubscriptionUsed = sub.AmountUsed
	return nil
}

func (e *Engine) publishChanges(ctx context.Context, input contract.BillingRequest, userChanged, tokenChanged bool) {
	if userChanged {
		if err := e.deps.Accounting.PublishUserDelta(context.WithoutCancel(ctx), input.UserID, 0); err != nil {
			common.SysLog("failed to invalidate billing user quota cache: " + err.Error())
		}
	}
	if tokenChanged && input.TokenID > 0 {
		if err := e.deps.Accounting.PublishTokenProjectionByID(context.WithoutCancel(ctx), input.TokenID); err != nil {
			common.SysLog("failed to invalidate billing token quota cache: " + err.Error())
		}
	}
}

// PublishCommitted invalidates projections after an outer transaction has
// committed a tx-bound billing operation. It intentionally resolves the
// token's current key by id, so rotation does not leave stale cache data.
func (e *Engine) PublishCommitted(input contract.BillingRequest) {
	e.publishChanges(context.Background(), input, input.UserID > 0, !input.Playground && input.TokenID > 0)
}

func (e *Engine) reserveToken(ctx context.Context, input contract.BillingRequest, amount int) error {
	if amount == 0 || input.Playground {
		return nil
	}
	reserved, err := e.deps.Accounting.TryReserveTokenQuota(ctx, input.TokenID, input.TokenKey, amount, input.TokenUnlimited)
	if err != nil {
		return failure(contract.BillingInsufficientToken, err)
	}
	if reserved {
		return nil
	}
	remaining := 0
	if e.deps.Tokens != nil {
		if token, err := e.deps.Tokens.GetByKey(input.TokenKey, false); err == nil && token != nil {
			remaining = token.RemainQuota
		}
	}
	return failure(contract.BillingInsufficientToken, fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(remaining), logger.FormatQuota(amount)))
}

func (e *Engine) fundingFailure(err error) error {
	if errors.Is(err, errInsufficientWallet) {
		return failure(contract.BillingInsufficientFunds, errors.New("用户额度不足"))
	}
	if errors.Is(err, subcontract.ErrSubscriptionQuotaInsufficient) {
		return failure(contract.BillingInsufficientFunds, fmt.Errorf("订阅额度不足或未配置订阅: %w", err))
	}
	return failure(contract.BillingStorageFailure, err)
}

// ValidateRequestBindingTx checks the immutable funding provenance of a task
// adjustment. This is a plain MVCC read: locking the parent session while a
// caller holds its task row would invert initial-settlement's lock order.
func (e *Engine) ValidateRequestBindingTx(ctx context.Context, tx *gorm.DB, input contract.BillingRequest, source string, subscriptionID int) error {
	if tx == nil || input.RequestID == "" {
		return operationConflict("parent billing request is missing")
	}
	var parent billingSessionRecord
	if err := tx.WithContext(ctx).Select("request_id", "user_id", "token_id", "source", "subscription_id", "status").Where("request_id = ?", input.RequestID).First(&parent).Error; err != nil {
		return err
	}
	if parent.UserID != input.UserID || parent.TokenID != input.TokenID || parent.Source != source || parent.SubscriptionID != subscriptionID {
		return operationConflict("task funding does not match its parent billing request")
	}
	if parent.Status != sessionStatusSettled {
		return operationConflict("task submission billing has not settled")
	}
	return nil
}
