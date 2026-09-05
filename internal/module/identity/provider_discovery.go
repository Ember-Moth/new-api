package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
)

func (s *Service) DiscoverProvider(ctx context.Context, request contract.FetchCustomOAuthDiscoveryRequest) (*contract.ProviderDiscovery, error) {
	target := strings.TrimSpace(request.WellKnownURL)
	issuer := strings.TrimSpace(request.IssuerURL)
	if target == "" && issuer == "" {
		return nil, errors.New("请先填写 Discovery URL 或 Issuer URL")
	}
	if target == "" {
		target = strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Discovery URL 无效，仅支持 http/https")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Discovery 请求失败: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("获取 Discovery 配置失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return nil, fmt.Errorf("获取 Discovery 配置失败: %s", message)
	}
	var discovery map[string]any
	if err := common.DecodeJson(response.Body, &discovery); err != nil {
		return nil, fmt.Errorf("解析 Discovery 配置失败: %w", err)
	}
	return &contract.ProviderDiscovery{WellKnownURL: target, Discovery: discovery}, nil
}
