package checkout

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func (c *Client) epay(cfg contract.GatewayConfig) (*epay.Client, error) {
	if cfg.EpayAddress == "" || cfg.EpayID == "" || cfg.EpayKey == "" {
		return nil, errors.New("Epay is not configured")
	}
	return epay.NewClient(&epay.Config{PartnerID: cfg.EpayID, Key: cfg.EpayKey}, cfg.EpayAddress)
}

func (c *Client) epaySubscription(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	if err := ctx.Err(); err != nil {
		return contract.CheckoutSession{}, err
	}
	cfg := c.options.Config()
	client, err := c.epay(cfg)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	returnURL, err := url.Parse(cfg.CallbackAddress + "/api/subscription/epay/return")
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	notifyURL, err := url.Parse(cfg.CallbackAddress + "/api/subscription/epay/notify")
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	target, params, err := client.Purchase(&epay.PurchaseArgs{Type: request.PaymentMethod, ServiceTradeNo: request.TradeNo, Name: fmt.Sprintf("SUB:%s", request.Title), Money: strconv.FormatFloat(request.Price, 'f', 2, 64), Device: epay.PC, NotifyUrl: notifyURL, ReturnUrl: returnURL})
	return contract.CheckoutSession{EpayURL: target, EpayParams: params}, err
}

func (c *Client) VerifyEpay(params map[string]string) (contract.VerifiedPayment, error) {
	client, err := c.epay(c.options.Config())
	if err != nil {
		return contract.VerifiedPayment{}, err
	}
	result, err := client.Verify(params)
	if err != nil {
		return contract.VerifiedPayment{}, err
	}
	if !result.VerifyStatus {
		return contract.VerifiedPayment{}, errors.New("invalid Epay signature")
	}
	payload, err := common.Marshal(result)
	if err != nil {
		return contract.VerifiedPayment{}, err
	}
	return contract.VerifiedPayment{TradeNo: result.ServiceTradeNo, PaymentMethod: result.Type, Paid: result.TradeStatus == epay.StatusTradeSuccess, Payload: string(payload)}, nil
}
