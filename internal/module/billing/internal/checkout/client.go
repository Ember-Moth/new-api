package checkout

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/stripe/stripe-go/v81"
	waffonet "github.com/waffo-com/waffo-go/net"
)

type Options struct {
	WaffoTransport              waffonet.HttpTransport
	Config                      func() contract.GatewayConfig
	HTTPClient                  *http.Client
	StripeBackend               stripe.Backend
	CreemEndpoint, WaffoBaseURL string
}

type Client struct{ options Options }

func New(options Options) *Client {
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if options.StripeBackend == nil {
		options.StripeBackend = stripe.GetBackend(stripe.APIBackend)
	}
	return &Client{options: options}
}

func (c *Client) ValidateSubscription(provider, method string) error {
	cfg := c.options.Config()
	switch provider {
	case "stripe":
		if !strings.HasPrefix(cfg.StripeKey, "sk_") && !strings.HasPrefix(cfg.StripeKey, "rk_") {
			return errors.New("Stripe 未配置或密钥无效")
		}
		if cfg.StripeWebhookSecret == "" {
			return errors.New("Stripe Webhook 未配置")
		}
	case "creem":
		if cfg.CreemWebhookSecret == "" && !cfg.CreemTestMode {
			return errors.New("Creem Webhook 未配置")
		}
		if cfg.CreemKey == "" {
			return errors.New("未配置Creem API密钥")
		}
	case "waffo_pancake":
		if strings.TrimSpace(cfg.WaffoMerchantID) == "" || strings.TrimSpace(cfg.WaffoPrivateKey) == "" {
			return errors.New("Waffo Pancake 未配置或密钥无效")
		}
	case "epay":
		if !slices.Contains(cfg.EpayMethods, method) {
			return errors.New("支付方式不存在")
		}
		if _, err := url.Parse(cfg.CallbackAddress + "/api/subscription/epay/return"); err != nil {
			return errors.New("回调地址配置错误")
		}
		if _, err := c.epay(cfg); err != nil {
			return errors.New("当前管理员未配置支付信息")
		}
	default:
		return errors.New("unsupported payment provider")
	}
	return nil
}

func (c *Client) ReturnURL(suffix string) string {
	return strings.TrimRight(c.options.Config().ServerAddress, "/") + suffix
}

func (c *Client) CreateSubscription(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	if request.Price < 0 || math.IsNaN(request.Price) || math.IsInf(request.Price, 0) {
		return contract.CheckoutSession{}, errors.New("invalid checkout price")
	}
	switch request.Provider {
	case "stripe":
		return c.stripeSubscription(ctx, request)
	case "creem":
		return c.Creem(ctx, request)
	case "epay":
		return c.epaySubscription(ctx, request)
	case "waffo_pancake":
		return c.PancakeWallet(ctx, request)
	default:
		return contract.CheckoutSession{}, errors.New("unsupported payment provider")
	}
}
