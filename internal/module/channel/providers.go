package channel

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
)

// ProviderRequests isolates protocol-adapter request construction from channel
// management and storage. The application supplies its gateway integration.
type ProviderRequests interface {
	ApplyHeaderOverrides(channel *Channel, key string, headers http.Header) error
	AdvancedRequest(channel *Channel, baseURL, key, path string) (string, http.Header, error)
	NativeModels(ctx context.Context, channel *Channel, baseURL, key string) ([]string, error)
	RefreshCredential(ctx context.Context, id int) (*contract.RefreshedCredential, error)
	PullModel(ctx context.Context, baseURL, key, name string, progress func([]byte)) error
	DeleteModel(ctx context.Context, baseURL, key, name string) error
	ModelServerVersion(ctx context.Context, baseURL, key string) (string, error)
}

func (s *Service) RefreshProviderCredential(ctx context.Context, id int) (*contract.RefreshedCredential, error) {
	if s.providers == nil {
		return nil, fmt.Errorf("provider integration is not configured")
	}
	return s.providers.RefreshCredential(ctx, id)
}

func (s *Service) PullProviderModel(ctx context.Context, baseURL, key, name string, progress func([]byte)) error {
	if s.providers == nil {
		return fmt.Errorf("provider integration is not configured")
	}
	return s.providers.PullModel(ctx, baseURL, key, name, progress)
}

func (s *Service) DeleteProviderModel(ctx context.Context, baseURL, key, name string) error {
	if s.providers == nil {
		return fmt.Errorf("provider integration is not configured")
	}
	return s.providers.DeleteModel(ctx, baseURL, key, name)
}

func (s *Service) ProviderVersion(ctx context.Context, baseURL, key string) (string, error) {
	if s.providers == nil {
		return "", fmt.Errorf("provider integration is not configured")
	}
	return s.providers.ModelServerVersion(ctx, baseURL, key)
}

func (s *Service) BuildFetchModelsHeaders(channel *Channel, key string) (http.Header, error) {
	var headers http.Header
	if channel.Type == constant.ChannelTypeAnthropic {
		headers = GetClaudeAuthHeader(key)
	} else {
		headers = GetAuthHeader(key)
	}
	if s.providers == nil {
		return nil, fmt.Errorf("provider request integration is not configured")
	}
	if err := s.providers.ApplyHeaderOverrides(channel, key, headers); err != nil {
		return nil, err
	}
	return headers, nil
}
