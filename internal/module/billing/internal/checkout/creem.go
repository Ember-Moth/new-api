package checkout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func (c *Client) Creem(ctx context.Context, request contract.CheckoutRequest) (contract.CheckoutSession, error) {
	cfg := c.options.Config()
	if cfg.CreemKey == "" {
		return contract.CheckoutSession{}, errors.New("未配置Creem API密钥")
	}
	endpoint := c.options.CreemEndpoint
	if endpoint == "" {
		endpoint = "https://api.creem.io/v1/checkouts"
		if cfg.CreemTestMode {
			endpoint = "https://test-api.creem.io/v1/checkouts"
		}
	}
	body, err := common.Marshal(map[string]any{"product_id": request.ProductID, "request_id": request.TradeNo, "customer": map[string]string{"email": request.Email}, "metadata": map[string]string{"username": request.Username, "reference_id": request.TradeNo, "product_name": request.Title, "quota": fmt.Sprint(request.Quota)}})
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.CreemKey)
	response, err := c.options.HTTPClient.Do(req)
	if err != nil {
		return contract.CheckoutSession{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return contract.CheckoutSession{}, fmt.Errorf("Creem API http status %d", response.StatusCode)
	}
	var result struct {
		CheckoutURL string `json:"checkout_url"`
	}
	if err := common.DecodeJson(response.Body, &result); err != nil {
		return contract.CheckoutSession{}, err
	}
	if result.CheckoutURL == "" {
		return contract.CheckoutSession{}, errors.New("Creem API resp no checkout url")
	}
	return contract.CheckoutSession{CheckoutURL: result.CheckoutURL}, nil
}
