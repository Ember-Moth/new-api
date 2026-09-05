package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/httpclient"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
)

func (s *Service) SyncBalances(ctx context.Context, frequency int) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(frequency) * time.Minute):
			common.SysLog("updating all channels")
			_ = s.RefreshAllBalances(ctx)
			common.SysLog("channels update done")
		}
	}
}

type OpenAIUsageDailyCost struct {
	Timestamp float64 `json:"timestamp"`
	LineItems []struct {
		Name string  `json:"name"`
		Cost float64 `json:"cost"`
	}
}

type OpenAICreditGrants struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalAvailable float64 `json:"total_available"`
}

const maxAdvancedCustomBalanceResponseBytes = 256 << 10

type BalanceResult struct {
	Balance     float64
	RawResponse string
}

type OpenAISBUsageResponse struct {
	Msg  string `json:"msg"`
	Data *struct {
		Credit string `json:"credit"`
	} `json:"data"`
}

type AIProxyUserOverviewResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	ErrorCode int    `json:"error_code"`
	Data      struct {
		TotalPoints float64 `json:"totalPoints"`
	} `json:"data"`
}

type API2GPTUsageResponse struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalRemaining float64 `json:"total_remaining"`
}

type APGC2DGPTUsageResponse struct {
	//Grants         interface{} `json:"grants"`
	Object         string  `json:"object"`
	TotalAvailable float64 `json:"total_available"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
}

type SiliconFlowUsageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  bool   `json:"status"`
	Data    struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Image         string `json:"image"`
		Email         string `json:"email"`
		IsAdmin       bool   `json:"isAdmin"`
		Balance       string `json:"balance"`
		Status        string `json:"status"`
		Introduction  string `json:"introduction"`
		Role          string `json:"role"`
		ChargeBalance string `json:"chargeBalance"`
		TotalBalance  string `json:"totalBalance"`
		Category      string `json:"category"`
	} `json:"data"`
}

type DeepSeekUsageResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

type OpenRouterCreditResponse struct {
	Data struct {
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	} `json:"data"`
}

// GetAuthHeader get auth header
func GetAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	return h
}

// GetClaudeAuthHeader get claude auth header
func GetClaudeAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("x-api-key", token)
	h.Add("anthropic-version", "2023-06-01")
	return h
}

func (s *Service) balanceResponseBody(ctx context.Context, method, url string, channel *Channel, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	for k := range headers {
		req.Header.Add(k, headers.Get(k))
	}
	client, err := httpclient.GetHttpClientWithProxy(channel.GetSetting().Proxy)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	err = res.Body.Close()
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Service) updateChannelCloseAIBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := fmt.Sprintf("%s/dashboard/billing/credit_grants", channel.GetBaseURL())
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := OpenAICreditGrants{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, response.TotalAvailable)
	return response.TotalAvailable, nil
}

func (s *Service) updateChannelOpenAISBBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := fmt.Sprintf("https://api.openai-sb.com/sb-api/user/status?api_key=%s", channel.Key)
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenAISBUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Data == nil {
		return 0, errors.New(response.Msg)
	}
	balance, err := strconv.ParseFloat(response.Data.Credit, 64)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, balance)
	return balance, nil
}

func (s *Service) updateChannelAIProxyBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://aiproxy.io/api/report/getUserOverview"
	headers := http.Header{}
	headers.Add("Api-Key", channel.Key)
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, headers)
	if err != nil {
		return 0, err
	}
	response := AIProxyUserOverviewResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Success {
		return 0, fmt.Errorf("code: %d, message: %s", response.ErrorCode, response.Message)
	}
	s.UpdateChannelBalance(channel, response.Data.TotalPoints)
	return response.Data.TotalPoints, nil
}

func (s *Service) updateChannelAPI2GPTBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://api.api2gpt.com/dashboard/billing/credit_grants"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := API2GPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, response.TotalRemaining)
	return response.TotalRemaining, nil
}

func (s *Service) updateChannelSiliconFlowBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://api.siliconflow.cn/v1/user/info"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := SiliconFlowUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Code != 20000 {
		return 0, fmt.Errorf("code: %d, message: %s", response.Code, response.Message)
	}
	balance, err := strconv.ParseFloat(response.Data.TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, balance)
	return balance, nil
}

func (s *Service) updateChannelDeepSeekBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://api.deepseek.com/user/balance"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := DeepSeekUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	index := -1
	for i, balanceInfo := range response.BalanceInfos {
		if balanceInfo.Currency == "CNY" {
			index = i
			break
		}
	}
	if index == -1 {
		return 0, errors.New("currency CNY not found")
	}
	balance, err := strconv.ParseFloat(response.BalanceInfos[index].TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, balance)
	return balance, nil
}

func (s *Service) updateChannelAIGC2DBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://api.aigc2d.com/dashboard/billing/credit_grants"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := APGC2DGPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	s.UpdateChannelBalance(channel, response.TotalAvailable)
	return response.TotalAvailable, nil
}

func (s *Service) updateChannelOpenRouterBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://openrouter.ai/api/v1/credits"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenRouterCreditResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance := response.Data.TotalCredits - response.Data.TotalUsage
	s.UpdateChannelBalance(channel, balance)
	return balance, nil
}

func (s *Service) updateChannelMoonshotBalance(ctx context.Context, channel *Channel) (float64, error) {
	url := "https://api.moonshot.cn/v1/users/me/balance"
	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}

	type MoonshotBalanceData struct {
		AvailableBalance float64 `json:"available_balance"`
		VoucherBalance   float64 `json:"voucher_balance"`
		CashBalance      float64 `json:"cash_balance"`
	}

	type MoonshotBalanceResponse struct {
		Code   int                 `json:"code"`
		Data   MoonshotBalanceData `json:"data"`
		Scode  string              `json:"scode"`
		Status bool                `json:"status"`
	}

	response := MoonshotBalanceResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Status || response.Code != 0 {
		return 0, fmt.Errorf("failed to update moonshot balance, status: %v, code: %d, scode: %s", response.Status, response.Code, response.Scode)
	}
	availableBalanceCny := response.Data.AvailableBalance
	availableBalanceUsd := decimal.NewFromFloat(availableBalanceCny).Div(decimal.NewFromFloat(operation_setting.Price)).InexactFloat64()
	s.UpdateChannelBalance(channel, availableBalanceUsd)
	return availableBalanceUsd, nil
}

func (s *Service) fetchAdvancedCustomBalance(ctx context.Context, channel *Channel) (BalanceResult, error) {
	key := strings.TrimSpace(channel.Key)
	if s.providers == nil {
		return BalanceResult{}, fmt.Errorf("provider request integration is not configured")
	}
	requestURL, headers, err := s.providers.AdvancedRequest(channel, channel.GetBaseURL(), key, dto.AdvancedCustomBalancePath)
	if err != nil {
		return BalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	if err := s.providers.ApplyHeaderOverrides(channel, key, headers); err != nil {
		return BalanceResult{}, sanitizeFetchModelsError(err, key)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return BalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
		if strings.EqualFold(name, "Host") {
			request.Host = headers.Get(name)
		}
	}
	client, err := httpclient.GetHttpClientWithProxy(channel.GetSetting().Proxy)
	if err != nil {
		return BalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	response, err := client.Do(request)
	if err != nil {
		return BalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return BalanceResult{}, fmt.Errorf("status code: %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAdvancedCustomBalanceResponseBytes+1))
	if err != nil {
		return BalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	if len(body) > maxAdvancedCustomBalanceResponseBytes {
		return BalanceResult{}, fmt.Errorf("balance response exceeds %d bytes", maxAdvancedCustomBalanceResponseBytes)
	}

	var validated json.RawMessage
	if err := common.Unmarshal(body, &validated); err != nil {
		return BalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	if common.GetJsonType(validated) == "object" {
		var creditSummary struct {
			Object         string          `json:"object"`
			TotalAvailable json.RawMessage `json:"total_available"`
		}
		if err := common.Unmarshal(body, &creditSummary); err != nil {
			return BalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
		}
		if creditSummary.Object == "credit_summary" &&
			common.GetJsonType(creditSummary.TotalAvailable) == "number" {
			var balance float64
			if err := common.Unmarshal(creditSummary.TotalAvailable, &balance); err == nil &&
				balance >= 0 &&
				!math.IsNaN(balance) &&
				!math.IsInf(balance, 0) {
				s.UpdateChannelBalance(channel, balance)
				return BalanceResult{Balance: balance}, nil
			}
		}
	}

	formatted, err := common.IndentJson(body)
	if err != nil {
		return BalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	return BalanceResult{RawResponse: string(formatted)}, nil
}

func (s *Service) RefreshBalance(ctx context.Context, channel *Channel) (BalanceResult, error) {
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		return s.fetchAdvancedCustomBalance(ctx, channel)
	}
	balance, err := s.updateStandardChannelBalance(ctx, channel)
	return BalanceResult{Balance: balance}, err
}

func (s *Service) updateStandardChannelBalance(ctx context.Context, channel *Channel) (float64, error) {
	baseURL := constant.GetChannelBaseURL(channel.Type)
	if channel.GetBaseURL() == "" {
		channel.BaseURL = &baseURL
	}
	switch channel.Type {
	case constant.ChannelTypeOpenAI:
		if channel.GetBaseURL() != "" {
			baseURL = channel.GetBaseURL()
		}
	case constant.ChannelTypeAzure:
		return 0, errors.New("尚未实现")
	case constant.ChannelTypeCustom:
		baseURL = channel.GetBaseURL()
	//case common.ChannelTypeOpenAISB:
	//	return s.updateChannelOpenAISBBalance(ctx, channel)
	case constant.ChannelTypeAIProxy:
		return s.updateChannelAIProxyBalance(ctx, channel)
	case constant.ChannelTypeAPI2GPT:
		return s.updateChannelAPI2GPTBalance(ctx, channel)
	case constant.ChannelTypeAIGC2D:
		return s.updateChannelAIGC2DBalance(ctx, channel)
	case constant.ChannelTypeSiliconFlow:
		return s.updateChannelSiliconFlowBalance(ctx, channel)
	case constant.ChannelTypeDeepSeek:
		return s.updateChannelDeepSeekBalance(ctx, channel)
	case constant.ChannelTypeOpenRouter:
		return s.updateChannelOpenRouterBalance(ctx, channel)
	case constant.ChannelTypeMoonshot:
		return s.updateChannelMoonshotBalance(ctx, channel)
	default:
		return 0, errors.New("尚未实现")
	}
	url := fmt.Sprintf("%s/v1/dashboard/billing/subscription", baseURL)

	body, err := s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	subscription := dto.OpenAISubscriptionResponse{}
	err = common.Unmarshal(body, &subscription)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	startDate := fmt.Sprintf("%s-01", now.Format("2006-01"))
	endDate := now.Format("2006-01-02")
	if !subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	url = fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", baseURL, startDate, endDate)
	body, err = s.balanceResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	usage := dto.OpenAIUsageResponse{}
	err = common.Unmarshal(body, &usage)
	if err != nil {
		return 0, err
	}
	balance := subscription.HardLimitUSD - usage.TotalUsage/100
	s.UpdateChannelBalance(channel, balance)
	return balance, nil
}

func (s *Service) RefreshAllBalances(ctx context.Context) error {
	channels, err := s.GetAllChannels(0, 0, true, false)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if channel.ChannelInfo.IsMultiKey {
			continue // skip multi-key channels
		}
		// TODO: support Azure
		//if channel.Type != common.ChannelTypeOpenAI && channel.Type != common.ChannelTypeCustom {
		//	continue
		//}
		result, err := s.RefreshBalance(ctx, channel)
		if err != nil {
			continue
		} else if result.RawResponse == "" {
			// err is nil & balance <= 0 means quota is used up
			if result.Balance <= 0 && s.disable != nil {
				s.disable(*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, "", channel.GetAutoBan()), "余额不足")
			}
		}
		time.Sleep(common.RequestInterval)
	}
	return nil
}

func sanitizeFetchModelsError(err error, key string) error {
	if err == nil {
		return nil
	}

	// net/http includes the complete request URL in url.Error. Discovery routes
	// may put the API key in a custom query name or value, so never return that
	// wrapper to an API client.
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}

	message := err.Error()
	key = strings.TrimSpace(key)
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
		message = strings.ReplaceAll(message, url.QueryEscape(key), "[REDACTED]")
		message = strings.ReplaceAll(message, url.PathEscape(key), "[REDACTED]")
	}
	return errors.New(message)
}

func sanitizeAdvancedCustomRequestError(err error, key string, requestURL string) error {
	err = sanitizeFetchModelsError(err, key)
	if err == nil {
		return nil
	}
	parsedURL, parseErr := url.Parse(requestURL)
	if parseErr != nil {
		return err
	}
	message := err.Error()
	for _, value := range parsedURL.Query() {
		for _, secret := range value {
			if secret == "" {
				continue
			}
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
			message = strings.ReplaceAll(message, url.QueryEscape(secret), "[REDACTED]")
			message = strings.ReplaceAll(message, url.PathEscape(secret), "[REDACTED]")
		}
	}
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
		message = strings.ReplaceAll(message, url.QueryEscape(key), "[REDACTED]")
		message = strings.ReplaceAll(message, url.PathEscape(key), "[REDACTED]")
	}
	return errors.New(message)
}
