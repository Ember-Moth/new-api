package config

import (
	"sync"
	"sync/atomic"
)

// RequestConfigSnapshot is the immutable configuration view captured by a
// request. A snapshot is published only after a complete options generation
// has been applied, so a request never observes a mixture of generations.
//
// The fields are exported for consumers in the relay and service packages.
// Publishers and consumers must treat a value returned by
// CurrentRequestConfigSnapshot as read-only; PublishRequestConfigSnapshot
// defensively clones the complete object before making it visible.
type RequestConfigSnapshot struct {
	Generation    uint64
	OptionVersion string

	Pricing RequestPricingSnapshot
	Billing RequestBillingSnapshot
	// Conversion is a pointer so the whole conversion configuration is kept as
	// one request-owned object and can be replaced without mutating old requests.
	Conversion *RequestConversionSnapshot
}

type RequestPricingSnapshot struct {
	ModelRatio                    map[string]float64
	ModelPrice                    map[string]float64
	DefaultModelRatio             map[string]float64
	DefaultModelPrice             map[string]float64
	CompletionRatio               map[string]float64
	CacheRatio                    map[string]float64
	CreateCacheRatio              map[string]float64
	ImageRatio                    map[string]float64
	AudioRatio                    map[string]float64
	AudioCompletionRatio          map[string]float64
	ToolPrices                    map[string]float64
	GroupRatio                    map[string]float64
	GroupGroupRatio               map[string]map[string]float64
	QuotaPerUnit                  float64
	PreConsumedQuota              int
	SelfUseModeEnabled            bool
	EnableFreeModelPreConsume     bool
	GrokViolationDeductionEnabled bool
	GrokViolationDeductionAmount  float64
}

type RequestBillingSnapshot struct {
	BillingMode map[string]string
	BillingExpr map[string]string
}

type RequestConversionSnapshot struct {
	GlobalPassThroughRequestEnabled  bool
	ThinkingModelBlacklist           []string
	EffortTailModelIDs               []string
	ChatCompletionsToResponsesPolicy ChatCompletionsToResponsesPolicySnapshot
	Claude                           *ClaudeConversionSnapshot
	Gemini                           *GeminiConversionSnapshot
}

type ChatCompletionsToResponsesPolicySnapshot struct {
	Enabled       bool
	AllChannels   bool
	ChannelIDs    []int
	ChannelTypes  []int
	ModelPatterns []string
}

type ClaudeConversionSnapshot struct {
	HeadersSettings                       map[string]map[string][]string
	DefaultMaxTokens                      map[string]int
	ThinkingAdapterEnabled                bool
	ThinkingAdapterBudgetTokensPercentage float64
}

type GeminiConversionSnapshot struct {
	SafetySettings                        map[string]string
	VersionSettings                       map[string]string
	SupportedImagineModels                []string
	ThinkingAdapterEnabled                bool
	ThinkingAdapterBudgetTokensPercentage float64
	FunctionCallThoughtSignatureEnabled   bool
	RemoveFunctionResponseIdEnabled       bool
}

var (
	requestConfigMu         sync.RWMutex
	requestConfig           atomic.Pointer[RequestConfigSnapshot]
	requestConfigGeneration atomic.Uint64
)

// CurrentRequestConfigSnapshot returns the latest fully published request
// configuration. The returned pointer and everything reachable from it are
// immutable. It is nil during the short bootstrap interval before the first
// successful options application.
func CurrentRequestConfigSnapshot() *RequestConfigSnapshot {
	return requestConfig.Load()
}

// PublishRequestConfigSnapshot atomically replaces the request configuration.
// The source is cloned before publication so later source-map mutations cannot
// change an in-flight request's view. A nil value explicitly returns the
// process to the pre-publication bootstrap state; failed options applications
// preserve the previous pointer by skipping this function entirely.
func PublishRequestConfigSnapshot(snapshot *RequestConfigSnapshot) {
	if snapshot == nil {
		requestConfig.Store(nil)
		return
	}
	copy := CloneRequestConfigSnapshot(snapshot)
	copy.Generation = requestConfigGeneration.Add(1)
	requestConfig.Store(copy)
}

// WithRequestConfigRead serializes a source read against a source update. The
// options publisher uses this around the copy operation; request execution
// itself only performs the lock-free immutable pointer read above.
func WithRequestConfigRead(fn func() error) error {
	requestConfigMu.RLock()
	defer requestConfigMu.RUnlock()
	return fn()
}

// WithRequestConfigWrite serializes a source update against a source read.
// Callers that also hold common.OptionMapRWMutex must acquire that lock first
// and then this lock, matching the options publisher's lock order.
func WithRequestConfigWrite(fn func() error) error {
	requestConfigMu.Lock()
	defer requestConfigMu.Unlock()
	return fn()
}

func CloneRequestConfigSnapshot(src *RequestConfigSnapshot) *RequestConfigSnapshot {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Pricing = cloneRequestPricingSnapshot(src.Pricing)
	dst.Billing = RequestBillingSnapshot{
		BillingMode: cloneStringMap(src.Billing.BillingMode),
		BillingExpr: cloneStringMap(src.Billing.BillingExpr),
	}
	dst.Conversion = cloneRequestConversionSnapshot(src.Conversion)
	return &dst
}

func cloneRequestPricingSnapshot(src RequestPricingSnapshot) RequestPricingSnapshot {
	return RequestPricingSnapshot{
		ModelRatio:                    cloneFloatMap(src.ModelRatio),
		ModelPrice:                    cloneFloatMap(src.ModelPrice),
		DefaultModelRatio:             cloneFloatMap(src.DefaultModelRatio),
		DefaultModelPrice:             cloneFloatMap(src.DefaultModelPrice),
		CompletionRatio:               cloneFloatMap(src.CompletionRatio),
		CacheRatio:                    cloneFloatMap(src.CacheRatio),
		CreateCacheRatio:              cloneFloatMap(src.CreateCacheRatio),
		ImageRatio:                    cloneFloatMap(src.ImageRatio),
		AudioRatio:                    cloneFloatMap(src.AudioRatio),
		AudioCompletionRatio:          cloneFloatMap(src.AudioCompletionRatio),
		ToolPrices:                    cloneFloatMap(src.ToolPrices),
		GroupRatio:                    cloneFloatMap(src.GroupRatio),
		GroupGroupRatio:               cloneNestedFloatMap(src.GroupGroupRatio),
		QuotaPerUnit:                  src.QuotaPerUnit,
		PreConsumedQuota:              src.PreConsumedQuota,
		SelfUseModeEnabled:            src.SelfUseModeEnabled,
		EnableFreeModelPreConsume:     src.EnableFreeModelPreConsume,
		GrokViolationDeductionEnabled: src.GrokViolationDeductionEnabled,
		GrokViolationDeductionAmount:  src.GrokViolationDeductionAmount,
	}
}

func cloneRequestConversionSnapshot(src *RequestConversionSnapshot) *RequestConversionSnapshot {
	if src == nil {
		return nil
	}
	dst := *src
	dst.ThinkingModelBlacklist = append([]string(nil), src.ThinkingModelBlacklist...)
	dst.EffortTailModelIDs = append([]string(nil), src.EffortTailModelIDs...)
	dst.ChatCompletionsToResponsesPolicy = ChatCompletionsToResponsesPolicySnapshot{
		Enabled:       src.ChatCompletionsToResponsesPolicy.Enabled,
		AllChannels:   src.ChatCompletionsToResponsesPolicy.AllChannels,
		ChannelIDs:    append([]int(nil), src.ChatCompletionsToResponsesPolicy.ChannelIDs...),
		ChannelTypes:  append([]int(nil), src.ChatCompletionsToResponsesPolicy.ChannelTypes...),
		ModelPatterns: append([]string(nil), src.ChatCompletionsToResponsesPolicy.ModelPatterns...),
	}
	if src.Claude != nil {
		claude := *src.Claude
		claude.HeadersSettings = cloneHeadersSettings(src.Claude.HeadersSettings)
		claude.DefaultMaxTokens = cloneIntMap(src.Claude.DefaultMaxTokens)
		dst.Claude = &claude
	}
	if src.Gemini != nil {
		gemini := *src.Gemini
		gemini.SafetySettings = cloneStringMap(src.Gemini.SafetySettings)
		gemini.VersionSettings = cloneStringMap(src.Gemini.VersionSettings)
		gemini.SupportedImagineModels = append([]string(nil), src.Gemini.SupportedImagineModels...)
		dst.Gemini = &gemini
	}
	return &dst
}

func cloneFloatMap(src map[string]float64) map[string]float64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]float64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneIntMap(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneNestedFloatMap(src map[string]map[string]float64) map[string]map[string]float64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]map[string]float64, len(src))
	for key, values := range src {
		dst[key] = cloneFloatMap(values)
	}
	return dst
}

func cloneHeadersSettings(src map[string]map[string][]string) map[string]map[string][]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]map[string][]string, len(src))
	for model, headers := range src {
		copiedHeaders := make(map[string][]string, len(headers))
		for key, values := range headers {
			copiedHeaders[key] = append([]string(nil), values...)
		}
		dst[model] = copiedHeaders
	}
	return dst
}
