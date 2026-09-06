package model

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/module/identity/tokencache"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenAutoGroupsRoundTripThroughRedisHashCache(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{
		Id:         42,
		UserId:     7,
		Key:        "token-auto-groups-cache-key",
		Name:       "auto-cache",
		Group:      "auto",
		AutoGroups: StringList{"vip", "default"},
	}

	require.NoError(t, cacheSetTokenForTest(token))
	cached, err := tokencache.New(DB).Cached(token.Key)
	require.NoError(t, err)
	assert.Equal(t, token.AutoGroups, cached.AutoGroups)
	groups, err := cached.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups)
}

func TestTokenUpdateSynchronouslyNarrowsPreheatedAutoGroupsCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := Token{
		UserId:          7,
		Key:             "token-auto-groups-update-cache-key",
		Name:            "auto-cache-update",
		Status:          common.TokenStatusEnabled,
		ExpiredTime:     -1,
		UnlimitedQuota:  true,
		Group:           "auto",
		CrossGroupRetry: true,
		AutoGroups:      StringList{"default", "vip"},
	}
	require.NoError(t, InsertToken(&token))
	require.NoError(t, cacheSetTokenForTest(token))

	preheated, err := tokencache.New(DB).Cached(token.Key)
	require.NoError(t, err)
	assert.Equal(t, StringList{"default", "vip"}, preheated.AutoGroups)

	management := identity.New(identity.Dependencies{
		DB: DB, InvalidateTokenCache: InvalidateTokenCacheForMutation,
		TokenPolicy: identity.TokenPolicy{
			MaxAutoGroups:     func() int { return 5 },
			IsSelectableGroup: func(userGroup, group string) bool { return group == "vip" },
		},
	})
	_, err = management.UpdateToken(t.Context(), contract.TokenActor{ID: token.UserId, Group: "default"}, contract.TokenRequest{
		TokenSettings: contract.TokenSettings{Id: token.Id, Name: token.Name, ExpiredTime: token.ExpiredTime, RemainQuota: token.RemainQuota, UnlimitedQuota: token.UnlimitedQuota, Group: token.Group, CrossGroupRetry: token.CrossGroupRetry},
		AutoGroups:    contract.TokenAutoGroupsInput{Set: true, Groups: []string{"vip"}},
	}, false)
	require.NoError(t, err)
	// Update 是限制性变更：写库前删除缓存并设置 fence。缓存不再提供旧的
	// 宽分组值，下一次读取必须看到收紧后的分组。
	_, cacheErr := tokencache.New(DB).Cached(token.Key)
	require.Error(t, cacheErr, "the pre-update cache entry must be invalidated")
	reloaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, StringList{"vip"}, reloaded.AutoGroups)
}

// cacheSetTokenForTest 以测试身份写入完整 token 缓存（含额度字段），
// 模拟“已水合”的缓存状态。
func cacheSetTokenForTest(token Token) error {
	_, err := tokencache.New(DB).Initialize(token)
	return err
}
