package checkout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/types/order"
)

func (c *Client) WaffoWallet(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	if request.Price <= 0 || math.IsNaN(request.Price) || math.IsInf(request.Price, 0) {
		return contract.CheckoutSession{}, errors.New("invalid checkout price")
	}
	gateway := c.options.Config()
	cfg := gateway.DirectWaffo
	environment := config.Production
	if cfg.Sandbox {
		environment = config.Sandbox
	}
	sdkConfig, err := config.NewConfigBuilder().APIKey(cfg.APIKey).PrivateKey(cfg.PrivateKey).WaffoPublicKey(cfg.PublicKey).Environment(environment).MerchantID(cfg.MerchantID).CustomTransport(c.options.WaffoTransport).Build()
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	currency := cfg.Currency
	if currency == "" {
		currency = "USD"
	}
	amount := fmt.Sprintf("%.2f", request.Price)
	switch currency {
	case "IDR", "JPY", "KRW", "VND":
		amount = fmt.Sprintf("%.0f", request.Price)
	}
	notifyURL := cfg.NotifyURL
	if notifyURL == "" {
		notifyURL = strings.TrimRight(gateway.CallbackAddress, "/") + "/api/waffo/webhook"
	}
	returnURL := cfg.ReturnURL
	if returnURL == "" {
		returnURL = strings.TrimRight(gateway.ServerAddress, "/") + "/wallet?show_history=true"
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "New API"
	}
	goods := &order.GoodsInfo{GoodsName: fmt.Sprintf("Recharge %d credits", request.InputAmount), AppName: appName}
	result, err := waffo.New(sdkConfig).Order().Create(ctx, &order.CreateOrderParams{
		PaymentRequestID: request.TradeNo, MerchantOrderID: request.TradeNo,
		OrderAmount: amount, OrderCurrency: currency, OrderDescription: goods.GoodsName,
		OrderRequestedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), NotifyURL: notifyURL,
		MerchantInfo: &order.MerchantInfo{MerchantID: cfg.MerchantID},
		UserInfo:     &order.UserInfo{UserID: strconv.Itoa(request.UserID), UserEmail: fmt.Sprintf("%d@examples.com", request.UserID), UserTerminal: "WEB"},
		PaymentInfo:  &order.PaymentInfo{ProductName: "ONE_TIME_PAYMENT", PayMethodType: request.PayMethodType, PayMethodName: request.PayMethodName},
		GoodsInfo:    goods, SuccessRedirectURL: returnURL, FailedRedirectURL: returnURL,
	}, nil)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	if result == nil || !result.IsSuccess() {
		return contract.CheckoutSession{}, errors.New("Waffo rejected checkout")
	}
	data := result.GetData()
	if data == nil {
		return contract.CheckoutSession{}, errors.New("Waffo returned empty checkout")
	}
	paymentURL := data.FetchRedirectURL()
	if paymentURL == "" {
		paymentURL = data.OrderAction
	}
	if strings.TrimSpace(paymentURL) == "" {
		return contract.CheckoutSession{}, errors.New("Waffo returned empty payment URL")
	}
	return contract.CheckoutSession{PaymentURL: paymentURL}, nil
}
