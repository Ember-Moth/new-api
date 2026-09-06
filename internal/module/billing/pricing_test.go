package billing_test

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/internal/module/billing"
	billinghttp "github.com/QuantumNous/new-api/internal/module/billing/transport/http"
	"github.com/QuantumNous/new-api/internal/module/identity"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"github.com/QuantumNous/new-api/internal/migration/schema"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/pricing"
	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pricingFixture struct {
	db       *gorm.DB
	channels *channelmodule.Service
	service  *pricing.Service
}

func newPricingFixture(t *testing.T) *pricingFixture {
	t.Helper()
	previous, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	common.MemoryCacheEnabled, common.RedisEnabled = true, false
	t.Cleanup(func() { common.MemoryCacheEnabled, common.RedisEnabled = previous, previousRedis })
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	require.NoError(t, schema.UpPostgres(pool, schema.Main))
	f := &pricingFixture{db: db}
	f.channels = channelmodule.New(channelmodule.Dependencies{DB: db, RoutingChanged: func() {
		if f.service != nil {
			f.service.Invalidate()
		}
	}})
	f.service = pricing.New(pricing.Dependencies{Channels: f.channels, Users: usercache.New(db)})
	f.channels.InitChannelCache()
	return f
}
func (f *pricingFixture) prices(t *testing.T) []billingcontract.Pricing {
	t.Helper()
	snapshot, err := f.service.Snapshot(t.Context())
	require.NoError(t, err)
	return snapshot.Prices
}

func (f *pricingFixture) insertPricingEndpointChannel(t *testing.T, channelID int, channelType int, settings dto.ChannelOtherSettings) {
	t.Helper()
	channel := &channelmodule.Channel{
		Id:     channelID,
		Type:   channelType,
		Key:    fmt.Sprintf("key-%d", channelID),
		Status: common.ChannelStatusEnabled,
		Name:   fmt.Sprintf("channel-%d", channelID),
	}
	if settings.AdvancedCustom != nil {
		channel.SetOtherSettings(settings)
	}
	require.NoError(t, f.db.Create(channel).Error)
}

func (f *pricingFixture) insertPricingEndpointAbility(t *testing.T, channelID int, modelName string) {
	t.Helper()
	require.NoError(t, f.db.Create(&channelmodule.Ability{
		Group:     "default",
		Model:     modelName,
		ChannelId: channelID,
		Enabled:   true,
	}).Error)
}

func pricingEndpointAdvancedCustomConfig(routes ...dto.AdvancedCustomRoute) dto.ChannelOtherSettings {
	return dto.ChannelOtherSettings{
		AdvancedCustom: &dto.AdvancedCustomConfig{
			Routes: routes,
		},
	}
}

func (f *pricingFixture) pricingEndpointTypesByModel(t *testing.T) map[string][]constant.EndpointType {
	t.Helper()
	f.channels.InitChannelCache()
	return pricingEndpointTypesFromPricing(f.prices(t))
}

func pricingEndpointTypesFromPricing(pricings []billingcontract.Pricing) map[string][]constant.EndpointType {
	byModel := make(map[string][]constant.EndpointType)
	for _, pricing := range pricings {
		byModel[pricing.ModelName] = pricing.SupportedEndpointTypes
	}
	return byModel
}

func TestPricingAdvancedCustomUsesConfiguredEndpointTypes(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 101, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	f.insertPricingEndpointAbility(t, 101, "gemini-2.5-flash")
	f.insertPricingEndpointAbility(t, 101, "gpt-4o")

	byModel := f.pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
	}, byModel["gpt-4o"])
}

func TestPricingModelMetadataEndpointsMergeWithAdvancedCustomInference(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 103, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	f.insertPricingEndpointAbility(t, 103, "gemini-2.5-flash")
	require.NoError(t, f.db.Create(&entity.Model{
		ModelName: "gemini-2.5-flash",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: contract.NameRuleExact,
	}).Error)

	byModel := f.pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAI,
	}, byModel["gemini-2.5-flash"])
}

func TestPricingModelMetadataEndpointsCanProvideEndpointWithoutChannelInference(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 104, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	f.insertPricingEndpointAbility(t, 104, "metadata-only-model")
	require.NoError(t, f.db.Create(&entity.Model{
		ModelName: "metadata-only-model",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: contract.NameRuleExact,
	}).Error)

	byModel := f.pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["metadata-only-model"])
}

func TestPricingAdvancedCustomMissingConfigFallsBackToChannelType(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 102, constant.ChannelTypeAdvancedCustom, dto.ChannelOtherSettings{})
	f.insertPricingEndpointAbility(t, 102, "gpt-4o")

	byModel := f.pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
}

func TestPricingNativeChannelEndpointTypesUnchanged(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 201, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	f.insertPricingEndpointChannel(t, 202, constant.ChannelTypeGemini, dto.ChannelOtherSettings{})
	f.insertPricingEndpointChannel(t, 203, constant.ChannelTypeAnthropic, dto.ChannelOtherSettings{})
	f.insertPricingEndpointAbility(t, 201, "gpt-4o")
	f.insertPricingEndpointAbility(t, 202, "gemini-2.5-flash")
	f.insertPricingEndpointAbility(t, 203, "claude-3-5-sonnet")

	byModel := f.pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}, byModel["claude-3-5-sonnet"])
}

func TestInitChannelCacheInvalidatesPricingCache(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 301, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
	))
	f.insertPricingEndpointAbility(t, 301, "gemini-3.5-flash")
	f.channels.InitChannelCache()

	initial := f.pricingEndpointTypesByModel(t)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, initial["gemini-3.5-flash"])

	var channel channelmodule.Channel
	require.NoError(t, f.db.First(&channel, "id = ?", 301).Error)
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	require.NoError(t, f.db.Model(&channelmodule.Channel{}).Where("id = ?", 301).Update("settings", channel.OtherSettings).Error)
	f.channels.InitChannelCache()

	updated := f.pricingEndpointTypesByModel(t)
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, updated["gemini-3.5-flash"])
}

func TestInitChannelCacheInvalidatesStartupPricingBuiltBeforeChannelCache(t *testing.T) {
	f := newPricingFixture(t)

	f.insertPricingEndpointChannel(t, 302, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	f.insertPricingEndpointAbility(t, 302, "gemini-3.5-flash")

	staleByModel := pricingEndpointTypesFromPricing(f.prices(t))
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, staleByModel["gemini-3.5-flash"])

	f.channels.InitChannelCache()

	rebuiltByModel := pricingEndpointTypesFromPricing(f.prices(t))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, rebuiltByModel["gemini-3.5-flash"])
}

func TestCacheUpdateChannelSyncsAdvancedCustomConfig(t *testing.T) {
	f := newPricingFixture(t)

	channel := &channelmodule.Channel{
		Id:     401,
		Type:   constant.ChannelTypeAdvancedCustom,
		Key:    "key-401",
		Status: common.ChannelStatusEnabled,
		Name:   "channel-401",
	}
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/responses",
		UpstreamPath: "/v1beta/models/{model}:generateContent",
		Converter:    "openai_responses_to_gemini_generate_content",
	}))
	f.channels.CacheUpdateChannel(channel)

	require.NotNil(t, f.channels.AdvancedConfigs([]int{401})[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}, f.channels.AdvancedConfigs([]int{401})[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
	}))
	f.channels.CacheUpdateChannel(channel)

	require.NotNil(t, f.channels.AdvancedConfigs([]int{401})[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, f.channels.AdvancedConfigs([]int{401})[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.Type = constant.ChannelTypeOpenAI
	f.channels.CacheUpdateChannel(channel)

	assert.Nil(t, f.channels.AdvancedConfigs([]int{401})[401])
}

func pricingUsagePluginSource(version, usageSchema string) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1, key: "pricing-usage-probe", name: "billingcontract.Pricing Usage Probe", version: %q, author: {name: "Test"},
  models: ["pricing-usage-model"], fetchMode: "per_task", usageSchema: %s
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, version, usageSchema)
}

func TestPricingCarriesTaskUsageSchemaAndRefreshesWithPluginGeneration(t *testing.T) {
	f := newPricingFixture(t)
	const pluginKey = "pricing-usage-probe"
	initialSource := pricingUsagePluginSource("1.0.0", `{
  seconds: {type: "number", unit: "second", description: "Estimated duration."}
}`)
	_, err := jsplugin.DefaultRegistry.Register(initialSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	f.insertPricingEndpointChannel(t, 901, constant.ChannelTypeTaskPlugin, dto.ChannelOtherSettings{})
	f.insertPricingEndpointAbility(t, 901, "pricing-usage-model")
	f.insertPricingEndpointAbility(t, 901, "ordinary-model")

	initialPricing := pricingByModel(f.prices(t))
	require.Contains(t, initialPricing, "pricing-usage-model")
	require.Contains(t, initialPricing, "ordinary-model")
	assert.Equal(t, "second", initialPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Unit)
	assert.Equal(t, "Estimated duration.", initialPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Nil(t, initialPricing["ordinary-model"].BillingUsageSchema)
	initialPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"] = "mutated"
	assert.Equal(t, "Estimated duration.", pricingByModel(f.prices(t))["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"])

	updatedSource := pricingUsagePluginSource("1.1.0", `{
  seconds: {type: "number", unit: "second", description: "Measured duration."},
  clips: {type: "number", unit: "count", description: "Generated clip count."}
}`)
	_, err = jsplugin.DefaultRegistry.Register(updatedSource, jsplugin.Options{})
	require.NoError(t, err)
	// A new registry generation invalidates pricing immediately, without a timer override.

	refreshedPricing := pricingByModel(f.prices(t))
	require.Len(t, refreshedPricing["pricing-usage-model"].BillingUsageSchema, 2)
	assert.Equal(t, "Measured duration.", refreshedPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Equal(t, "count", refreshedPricing["pricing-usage-model"].BillingUsageSchema["clips"].Unit)
}

func TestPricingAliasCarriesPluginUsageSchemaAndTailExpr(t *testing.T) {
	f := newPricingFixture(t)
	const pluginKey = "pricing-usage-probe"
	source := pricingUsagePluginSource("1.0.0", `{
  seconds: {type: "number", unit: "second", description: "Estimated duration."}
}`)
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	mapping := `{"alias-model":"pricing-usage-model"}`
	channel := &channelmodule.Channel{
		Id:           910,
		Type:         constant.ChannelTypeTaskPlugin,
		Key:          "key-910",
		Status:       1,
		Name:         "channel-910",
		Models:       value.StringList{"alias-model", "pricing-usage-model"},
		ModelMapping: &mapping,
	}
	require.NoError(t, f.db.Create(channel).Error)
	f.insertPricingEndpointAbility(t, 910, "alias-model")
	f.insertPricingEndpointAbility(t, 910, "pricing-usage-model")
	f.channels.InitChannelCache()

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"pricing-usage-model":"tiered_expr","alias-own-expr":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"pricing-usage-model":"u(\"seconds\")","alias-own-expr":"u(\"seconds\") * 2"}`,
	}))
	f.service.Invalidate()

	pricing := pricingByModel(f.prices(t))
	require.Contains(t, pricing, "alias-model")
	require.Contains(t, pricing, "pricing-usage-model")
	assert.Equal(t, "second", pricing["alias-model"].BillingUsageSchema["seconds"].Unit)
	assert.Equal(t, "Estimated duration.", pricing["alias-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Equal(t, "tiered_expr", pricing["alias-model"].BillingMode)
	assert.Equal(t, `u("seconds")`, pricing["alias-model"].BillingExpr)
	assert.Equal(t, "tiered_expr", pricing["pricing-usage-model"].BillingMode)
	assert.Equal(t, `u("seconds")`, pricing["pricing-usage-model"].BillingExpr)

	ownMapping := `{"alias-own-expr":"pricing-usage-model"}`
	own := &channelmodule.Channel{
		Id:           911,
		Type:         constant.ChannelTypeTaskPlugin,
		Key:          "key-911",
		Status:       1,
		Name:         "channel-911",
		Models:       value.StringList{"alias-own-expr", "pricing-usage-model"},
		ModelMapping: &ownMapping,
	}
	require.NoError(t, f.db.Create(own).Error)
	f.insertPricingEndpointAbility(t, 911, "alias-own-expr")
	f.channels.InitChannelCache()
	f.service.Invalidate()

	refreshed := pricingByModel(f.prices(t))
	assert.Equal(t, `u("seconds") * 2`, refreshed["alias-own-expr"].BillingExpr)
	assert.Equal(t, "second", refreshed["alias-own-expr"].BillingUsageSchema["seconds"].Unit)
}

func pricingByModel(pricings []billingcontract.Pricing) map[string]billingcontract.Pricing {
	result := make(map[string]billingcontract.Pricing, len(pricings))
	for _, pricing := range pricings {
		result[pricing.ModelName] = pricing
	}
	return result
}

func TestPricingSnapshotIsolationAndFailedRefreshRetainCompleteCatalog(t *testing.T) {
	f := newPricingFixture(t)
	previousCacheRatio := ratio_setting.CacheRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(previousCacheRatio)) })
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"gpt-snapshot":0.5}`))
	f.insertPricingEndpointChannel(t, 1001, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	f.insertPricingEndpointAbility(t, 1001, "gpt-snapshot")
	first, err := f.service.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, first.Prices, 1)
	require.NotNil(t, first.Prices[0].CacheRatio)
	*first.Prices[0].CacheRatio = 9
	first.Prices[0].EnableGroup[0] = "forged"
	first.Prices[0].SupportedEndpointTypes[0] = "forged"
	first.Vendors[0].Name = "forged"
	first.Endpoints["openai"] = common.EndpointInfo{Path: "/forged"}
	second, err := f.service.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 0.5, *second.Prices[0].CacheRatio)
	assert.Equal(t, []string{"default"}, second.Prices[0].EnableGroup)
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, second.Prices[0].SupportedEndpointTypes)
	assert.Equal(t, "OpenAI", second.Vendors[0].Name)
	assert.Equal(t, "/v1/chat/completions", second.Endpoints["openai"].Path)
	require.NoError(t, f.db.Exec("ALTER TABLE models RENAME TO unavailable_models").Error)
	f.service.Invalidate()
	retained, err := f.service.Snapshot(t.Context())
	require.Error(t, err)
	assert.Equal(t, second, retained)
	require.NoError(t, f.db.Exec("ALTER TABLE unavailable_models RENAME TO models").Error)
	require.NoError(t, f.db.Create(&entity.Model{ModelName: "gpt-snapshot", Status: 1, NameRule: contract.NameRuleExact}).Error)
	require.NoError(t, f.db.Model(&entity.Model{}).Where("model_name = ?", "gpt-snapshot").Update("status", 0).Error)
	refreshed, err := f.service.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Empty(t, refreshed.Prices, "failed refresh must not mark the invalidation as handled")
	other := newPricingFixture(t)
	independent, err := other.service.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Empty(t, independent.Prices)
	assert.Empty(t, independent.Vendors)
}

func TestPricingConcurrentDefaultVendorInitializationUsesOneActiveName(t *testing.T) {
	f := newPricingFixture(t)
	f.insertPricingEndpointChannel(t, 1002, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	f.insertPricingEndpointAbility(t, 1002, "gpt-concurrent")
	other := pricing.New(pricing.Dependencies{Channels: f.channels})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, service := range []*pricing.Service{f.service, other} {
		go func() { <-start; _, err := service.Snapshot(t.Context()); results <- err }()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	var count int64
	require.NoError(t, f.db.Model(&entity.Vendor{}).Where("name = ?", "OpenAI").Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.Error(t, f.db.Create(&entity.Vendor{Name: "OpenAI", Status: 1}).Error)
	var vendor entity.Vendor
	require.NoError(t, f.db.Where("name = ?", "OpenAI").First(&vendor).Error)
	require.NoError(t, f.db.Delete(&vendor).Error)
	require.NoError(t, f.db.Create(&entity.Vendor{Name: "OpenAI", Status: 1}).Error, "soft-deleted names can be reused")
}

func TestPricingViewAndGroupChoicesApplyTheSameUserPermissions(t *testing.T) {
	f := newPricingFixture(t)
	oldUsable, oldAuto := setting.UserUsableGroups2JSONString(), setting.AutoGroups2JsonString()
	oldRatios, oldOverrides := ratio_setting.GroupRatio2JSONString(), ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldUsable))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(oldAuto))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(oldOverrides))
	})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","auto":"Automatic"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","default","vip","secret"]`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2,"secret":5}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"default":0.5,"vip":1.5}}`))
	user := identityentity.User{Username: "pricing-vip", Group: "vip", AuthVersion: 1}
	require.NoError(t, f.db.Create(&user).Error)
	f.insertPricingEndpointChannel(t, 1003, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	require.NoError(t, f.db.Create(&[]channelmodule.Ability{{Group: "default", Model: "public", ChannelId: 1003, Enabled: true}, {Group: "vip", Model: "vip-only", ChannelId: 1003, Enabled: true}, {Group: "secret", Model: "hidden", ChannelId: 1003, Enabled: true}, {Group: "all", Model: "all-groups", ChannelId: 1003, Enabled: true}}).Error)
	handler := billinghttp.New(billing.New(billing.Dependencies{Pricing: f.service}), billinghttp.ManagementHooks{})
	router := gin.New()
	userID := 0
	router.Use(func(c *gin.Context) {
		if userID > 0 {
			c.Set("id", userID)
		}
	})
	router.GET("/pricing", handler.GetPricing)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	var guest billingcontract.PricingView
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &guest))
	assert.True(t, guest.Success)
	assert.ElementsMatch(t, []string{"public", "all-groups"}, slices.Collect(maps.Keys(pricingByModel(guest.Data))))
	assert.Equal(t, []string{"default"}, guest.AutoGroups)
	userID = user.Id
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	var member billingcontract.PricingView
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &member))
	assert.True(t, member.Success)
	assert.ElementsMatch(t, []string{"public", "vip-only", "all-groups"}, slices.Collect(maps.Keys(pricingByModel(member.Data))))
	assert.Equal(t, map[string]float64{"default": 0.5, "vip": 1.5}, member.GroupRatio)
	assert.Equal(t, []string{"vip", "default"}, member.AutoGroups)
	accounts := identity.New(identity.Dependencies{DB: f.db})
	choices, err := accounts.UserGroupChoices(t.Context(), user.Id)
	require.NoError(t, err)
	assert.Equal(t, 1.5, choices["vip"].Ratio)
	assert.Equal(t, "自动", choices["auto"].Ratio)
	public, err := accounts.UserGroupChoices(t.Context(), 0)
	require.NoError(t, err)
	assert.NotContains(t, public, "vip")
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{}`))
	empty, err := f.service.View(t.Context(), 0)
	require.NoError(t, err)
	assert.Empty(t, empty.Data, "an empty allowed group set also hides all-groups models")
}
