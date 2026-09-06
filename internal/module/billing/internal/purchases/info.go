package purchases

import (
	"maps"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func Information(cfg contract.WalletConfig) contract.TopUpInfo {
	gateway := cfg.Gateway
	stripe := cfg.PaymentAllowed && strings.TrimSpace(gateway.StripeKey) != "" && strings.TrimSpace(gateway.StripeWebhookSecret) != "" && strings.TrimSpace(cfg.StripePriceID) != ""
	creem := cfg.PaymentAllowed && strings.TrimSpace(gateway.CreemKey) != "" && strings.TrimSpace(cfg.CreemProducts) != "" && strings.TrimSpace(cfg.CreemProducts) != "[]"
	waffo := cfg.PaymentAllowed && cfg.WaffoEnabled && cfg.WaffoConfigured
	pancake := cfg.PaymentAllowed && strings.TrimSpace(gateway.WaffoMerchantID) != "" && strings.TrimSpace(gateway.WaffoPrivateKey) != "" && strings.TrimSpace(cfg.PancakeProductID) != ""
	online := cfg.PaymentAllowed && strings.TrimSpace(gateway.EpayAddress) != "" && strings.TrimSpace(gateway.EpayID) != "" && strings.TrimSpace(gateway.EpayKey) != "" && len(cfg.PayMethods) > 0
	methods := make([]map[string]string, 0, len(cfg.PayMethods)+3)
	if cfg.PaymentAllowed {
		for _, method := range cfg.PayMethods {
			methods = append(methods, maps.Clone(method))
		}
	}
	for _, entry := range []struct {
		enabled           bool
		kind, name, color string
		minimum           int
	}{{stripe, "stripe", "Stripe", "#635BFF", cfg.StripeMinimum}, {pancake, "waffo_pancake", "Waffo Pancake", "#F97316", cfg.PancakeMinimum}, {waffo, "waffo", "Waffo (Global Payment)", "#3B82F6", cfg.WaffoMinimum}} {
		if !entry.enabled {
			continue
		}
		present := false
		for _, method := range methods {
			if method["type"] == entry.kind {
				present = true
				break
			}
		}
		if !present {
			methods = append(methods, map[string]string{"name": entry.name, "type": entry.kind, "color": entry.color, "min_topup": strconv.Itoa(entry.minimum)})
		}
	}
	var waffoMethods []constant.WaffoPayMethod
	if waffo {
		waffoMethods = append([]constant.WaffoPayMethod(nil), cfg.WaffoMethods...)
	}
	return contract.TopUpInfo{Online: online, Stripe: stripe, Creem: creem, Waffo: waffo, Pancake: pancake, Redemption: cfg.PaymentAllowed, PaymentAllowed: cfg.PaymentAllowed, TermsVersion: cfg.TermsVersion, WaffoMethods: waffoMethods, CreemProducts: cfg.CreemProducts, PayMethods: methods, Minimum: cfg.Minimum, StripeMinimum: cfg.StripeMinimum, WaffoMinimum: cfg.WaffoMinimum, PancakeMinimum: cfg.PancakeMinimum, AmountOptions: append([]int(nil), cfg.AmountOptions...), Discounts: maps.Clone(cfg.Discounts), TopupLink: cfg.TopupLink}
}

func (s *Service) Info() contract.TopUpInfo { return Information(s.deps.Config()) }
