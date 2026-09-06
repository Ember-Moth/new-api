package pricesync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

func (s *Service) fetch(ctx context.Context, source contract.UpstreamDTO) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, errors.New("pricing HTTP client is not configured")
	}
	endpoint := source.Endpoint
	fullURL := ""
	openRouter := endpoint == "openrouter"
	if openRouter {
		fullURL = source.BaseURL + "/v1/models"
	} else if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		fullURL = endpoint
	} else {
		if endpoint == "" {
			endpoint = defaultEndpoint
		} else if !strings.HasPrefix(endpoint, "/") {
			endpoint = "/" + endpoint
		}
		fullURL = source.BaseURL + endpoint
	}
	target, err := pricingURL(fullURL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	if openRouter {
		if source.ID <= 0 || s.deps.Credential == nil {
			return nil, errors.New("OpenRouter requires a valid channel with API key")
		}
		base, key, err := s.deps.Credential(ctx, source.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get channel key: %w", err)
		}
		configured, err := pricingURL(base)
		if err != nil || !sameOrigin(configured, target) {
			return nil, errors.New("OpenRouter URL does not match the selected channel")
		}
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("no API key configured for this channel")
		}
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	}
	var response *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		response, err = s.client.Do(request)
		if err == nil {
			break
		}
		if attempt == 2 || ctx.Err() != nil || errors.Is(err, errAuthenticatedRedirect) {
			return nil, err
		}
		timer := time.NewTimer(time.Duration(200*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRatioConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRatioConfigBytes {
		return nil, errors.New("upstream pricing response exceeds 10 MiB")
	}
	return decodePricing(body, openRouter, isModelsDevAPIEndpoint(fullURL))
}
