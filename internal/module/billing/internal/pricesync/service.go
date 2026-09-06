package pricesync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/httpclient"
	"github.com/QuantumNous/new-api/internal/module/billing/contract"
)

const (
	defaultTimeoutSeconds      = 10
	defaultEndpoint            = "/api/pricing"
	maxConcurrentFetches       = 8
	maxRatioConfigBytes        = 10 << 20
	officialRatioPresetID      = -100
	officialRatioPresetName    = "官方倍率预设"
	officialRatioPresetBaseURL = "https://basellm.github.io"
	modelsDevPresetID          = -101
	modelsDevPresetName        = "models.dev 价格预设"
	modelsDevPresetBaseURL     = "https://models.dev"
)

var ErrNoUpstreams = errors.New("无有效上游渠道")
var errAuthenticatedRedirect = errors.New("authenticated pricing redirect changed origin")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

type QueryError struct{ Cause error }

func (e *QueryError) Error() string { return "查询渠道失败" }
func (e *QueryError) Unwrap() error { return e.Cause }

// Source selection is injected independently of the comparison/conversion core.
type Dependencies struct {
	Sources    func(context.Context, []int) ([]contract.SyncableChannel, error)
	Credential func(context.Context, int) (string, string, error)
	LocalData  func() map[string]any
	HTTPClient *http.Client
}
type Service struct {
	deps   Dependencies
	client *http.Client
}

func New(deps Dependencies) *Service {
	client := deps.HTTPClient
	if client == nil {
		client = httpclient.GetSSRFProtectedHTTPClient()
	}
	if client != nil {
		copy := *client
		previous := copy.CheckRedirect
		copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[0].Header.Get("Authorization") != "" && !sameOrigin(via[0].URL, request.URL) {
				return errAuthenticatedRedirect
			}
			if previous != nil {
				return previous(request, via)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		}
		client = &copy
	}
	return &Service{deps: deps, client: client}
}
func (s *Service) Sources(ctx context.Context) ([]contract.SyncableChannel, error) {
	sources, err := s.deps.Sources(ctx, nil)
	if err != nil {
		return nil, err
	}
	result := make([]contract.SyncableChannel, 0, len(sources)+2)
	for _, source := range sources {
		if source.BaseURL != "" {
			result = append(result, source)
		}
	}
	result = append(result, contract.SyncableChannel{ID: officialRatioPresetID, Name: officialRatioPresetName, BaseURL: officialRatioPresetBaseURL, Status: 1}, contract.SyncableChannel{ID: modelsDevPresetID, Name: modelsDevPresetName, BaseURL: modelsDevPresetBaseURL, Status: 1})
	return result, nil
}
func (s *Service) Fetch(ctx context.Context, request contract.UpstreamRequest) (contract.PricingSyncResult, error) {
	if request.Timeout <= 0 {
		request.Timeout = defaultTimeoutSeconds
	}
	if request.Timeout > 120 {
		return contract.PricingSyncResult{}, &InputError{Message: "同步超时时间不能超过 120 秒"}
	}
	var upstreams []contract.UpstreamDTO
	if len(request.Upstreams) > 0 {
		for _, source := range request.Upstreams {
			if _, err := pricingURL(source.BaseURL); err != nil {
				continue
			}
			source.BaseURL = strings.TrimRight(source.BaseURL, "/")
			upstreams = append(upstreams, source)
		}
	} else if len(request.ChannelIDs) > 0 {
		ids := make([]int, 0, len(request.ChannelIDs))
		for _, id := range request.ChannelIDs {
			if id > 0 {
				ids = append(ids, int(id))
			}
		}
		if len(ids) > 0 {
			sources, err := s.deps.Sources(ctx, ids)
			if err != nil {
				return contract.PricingSyncResult{}, &QueryError{Cause: err}
			}
			for _, source := range sources {
				if _, err := pricingURL(source.BaseURL); err == nil {
					upstreams = append(upstreams, contract.UpstreamDTO{ID: source.ID, Name: source.Name, BaseURL: strings.TrimRight(source.BaseURL, "/")})
				}
			}
		}
	}
	if len(upstreams) == 0 {
		return contract.PricingSyncResult{}, ErrNoUpstreams
	}
	local := map[string]any{}
	if s.deps.LocalData != nil {
		local = s.deps.LocalData()
	}
	local, err := validatedPricingData(local)
	if err != nil {
		return contract.PricingSyncResult{}, fmt.Errorf("本地定价配置无效: %w", err)
	}
	results := make([]upstreamResult, len(upstreams))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(maxConcurrentFetches, len(upstreams)) {
		workers.Go(func() {
			for index := range jobs {
				source := upstreams[index]
				name := source.Name
				if source.ID != 0 {
					name = fmt.Sprintf("%s(%d)", name, source.ID)
				}
				jobCtx, cancel := context.WithTimeout(ctx, time.Duration(request.Timeout)*time.Second)
				data, err := s.fetch(jobCtx, source)
				cancel()
				results[index] = upstreamResult{Name: name, Data: data}
				if err != nil {
					results[index].Err = err.Error()
				}
			}
		})
	}
	for index := range upstreams {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	report := contract.PricingSyncResult{TestResults: make([]contract.TestResult, 0, len(results))}
	successful := make([]struct {
		name string
		data map[string]any
	}, 0, len(results))
	for _, result := range results {
		if result.Err != "" {
			report.TestResults = append(report.TestResults, contract.TestResult{Name: result.Name, Status: "error", Error: result.Err})
			continue
		}
		report.TestResults = append(report.TestResults, contract.TestResult{Name: result.Name, Status: "success"})
		successful = append(successful, struct {
			name string
			data map[string]any
		}{result.Name, result.Data})
	}
	report.Differences = buildDifferences(local, successful)
	return report, nil
}
func pricingURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("invalid upstream URL")
	}
	return parsed, nil
}
func sameOrigin(a, b *url.URL) bool {
	portA, portB := a.Port(), b.Port()
	if portA == "" {
		if a.Scheme == "https" {
			portA = "443"
		} else {
			portA = "80"
		}
	}
	if portB == "" {
		if b.Scheme == "https" {
			portB = "443"
		} else {
			portB = "80"
		}
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && portA == portB
}
