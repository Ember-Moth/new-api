package routing

import (
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/internal/infra/configsync"

	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"github.com/QuantumNous/new-api/internal/module/channel/entity"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

type Channel = entity.Channel
type ChannelInfo = entity.ChannelInfo
type Ability = entity.Ability
type AbilityWithChannel = entity.AbilityWithChannel
type StringList = value.StringList

const commonGroupCol = `"group"`
const commonKeyCol = `"key"`

// Runtime owns channel persistence and the routing snapshot for one database.
// Its callbacks expose the two cross-module effects: pricing invalidation and
// accounting's optional channel-usage batching.
type Runtime struct {
	snapshot                     snapshotState
	snapshotAbilities            []Ability
	db                           *gorm.DB
	changed                      func()
	queueQuota                   func(int, int) bool
	channelStatusLock            sync.Mutex
	channelPollingLocks          sync.Map
	fixLock                      sync.Mutex
	group2model2channels         map[string]map[string][]int
	channelsIDM                  map[int]*Channel
	channel2advancedCustomConfig map[int]*kitdto.AdvancedCustomConfig
	channelSyncLock              sync.RWMutex
	taskAliasViewPtr             atomic.Pointer[taskAliasView]
	taskAliasRebuildMu           sync.Mutex
}

func New(db *gorm.DB, changed func(), queueQuota func(int, int) bool, configs ...SnapshotConfig) *Runtime {
	if changed == nil {
		changed = func() {}
	}
	config := SnapshotConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	return &Runtime{db: db, changed: changed, queueQuota: queueQuota, snapshot: snapshotState{store: configsync.New(config.Cache, "channels"), configured: config.Cache != nil, readOnly: config.ReadOnly}}
}

// AdvancedConfigs returns the parsed routing configurations needed to build a
// pricing projection without exposing the cache lock or its mutable map.
func (r *Runtime) AdvancedConfigs(ids []int) map[int]*kitdto.AdvancedCustomConfig {
	r.channelSyncLock.RLock()
	defer r.channelSyncLock.RUnlock()
	result := make(map[int]*kitdto.AdvancedCustomConfig, len(ids))
	for _, id := range ids {
		if config := r.channel2advancedCustomConfig[id]; config != nil {
			result[id] = config
		}
	}
	return result
}
