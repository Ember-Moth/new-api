package routing

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPluginChannelSelectionFiltersBothCachePaths(t *testing.T) {
	_, runtime := setupChannelStatusTest(t)
	priority := int64(0)
	weight := uint(1)
	baseURL := "https://example.com"
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	channels := []Channel{
		{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "alpha", Models: StringList{"shared"}, Group: StringList{"default"}, Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &alphaSetting},
		{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "beta", Models: StringList{"shared"}, Group: StringList{"default"}, Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &betaSetting},
		{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ordinary", Models: StringList{"ordinary"}, Group: StringList{"default"}, Priority: &priority, Weight: &weight},
		{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled, Name: "legacy-alpha", Models: StringList{"legacy"}, Group: StringList{"default"}, Priority: &priority, Weight: &weight},
		{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled, Name: "legacy-beta", Models: StringList{"legacy"}, Group: StringList{"default"}, Priority: &priority, Weight: &weight},
	}
	for i := range channels {
		require.NoError(t, runtime.InsertChannel(&(channels[i])))
	}

	for _, memoryCache := range []bool{false, true} {
		t.Run(fmt.Sprintf("memory_cache_%t", memoryCache), func(t *testing.T) {
			common.MemoryCacheEnabled = memoryCache
			runtime.InitChannelCache()
			selected, err := runtime.GetRandomSatisfiedChannel("default", "shared", 0, identityFilters("alpha", nil))
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Equal(t, "alpha", selected.Name)
			selected, err = runtime.GetRandomSatisfiedChannel("default", "shared", 0, identityFilters("", nil))
			require.NoError(t, err)
			assert.Nil(t, selected)
			selected, err = runtime.GetRandomSatisfiedChannel("default", "ordinary", 0, identityFilters("", nil))
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Equal(t, "ordinary", selected.Name)
			selected, err = runtime.GetRandomSatisfiedChannel("default", "legacy", 0, identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}))
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Equal(t, "legacy-alpha", selected.Name)
			selected, err = runtime.GetRandomSatisfiedChannel("default", "legacy", 0, identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}))
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Contains(t, []string{"legacy-alpha", "legacy-beta"}, selected.Name)
		})
	}

}

func identityFilters(key string, channelTypes []int) []dto.ChannelFilter {
	return []dto.ChannelFilter{{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          key,
		TaskPluginChannelTypes: channelTypes,
	}}
}
