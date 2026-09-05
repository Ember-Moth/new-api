package payments

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/subscription/catalog"
	"github.com/QuantumNous/new-api/internal/module/subscription/entity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
	"gorm.io/gorm"
)

var ErrOrderNotFound = errors.New("subscription order not found")
var ErrOrderStatusInvalid = errors.New("subscription order status invalid")

type Billing interface {
	DebitWalletInTx(*gorm.DB, int, int) error
	RecordSubscriptionReceipt(*gorm.DB, billingcontract.SubscriptionReceipt) error
}

type Dependencies struct {
	DB           *gorm.DB
	Catalog      *catalog.Store
	Members      *memberships.Store
	Billing      Billing
	QuotaPerUnit func() float64
	AfterDebit   func(int, int) error
	Log          func(context.Context, int, string)
}

type Store struct{ deps Dependencies }

func New(deps Dependencies) *Store { return &Store{deps: deps} }

func (s *Store) Create(ctx context.Context, order *entity.SubscriptionOrder) error {
	if order == nil || order.UserId <= 0 || order.PlanId <= 0 || order.TradeNo == "" || order.Status != common.TopUpStatusPending || order.Money < 0 || math.IsNaN(order.Money) || math.IsInf(order.Money, 0) {
		return errors.New("invalid subscription order")
	}
	if order.CreateTime == 0 {
		order.CreateTime = common.GetTimestamp()
	}
	return s.deps.DB.WithContext(ctx).Create(order).Error
}

func (s *Store) Get(ctx context.Context, tradeNo string) (*entity.SubscriptionOrder, error) {
	var order entity.SubscriptionOrder
	err := s.deps.DB.WithContext(ctx).Where("trade_no = ?", tradeNo).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}
