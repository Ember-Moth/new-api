package routing

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/go-redis/redis/v8"
)

// The cursor is bounded by pool size and expires after a day without traffic.
// Key-pool fingerprints isolate requests using older channel snapshots.
var nextPollingKey = redis.NewScript(`
local count = tonumber(ARGV[1])
local start = tonumber(redis.call('GET',KEYS[1]) or '0')
if not start or start < 0 or start >= count then start = 0 end
local selected = tonumber(ARGV[3])
for i=3,#ARGV do
    local index = tonumber(ARGV[i])
    if index >= start then selected = index; break end
end
redis.call('SET',KEYS[1],(selected+1)%count,'EX',ARGV[2])
return selected
`)

func (r *Runtime) nextPollingIndex(channelID int, keys []string, enabled []int) (int, error) {
	if r.cache == nil {
		return 0, fmt.Errorf("DragonflyDB is required for multi-key polling")
	}
	payload, err := common.Marshal(keys)
	if err != nil {
		return 0, err
	}
	fingerprint := common.GenerateHMACWithKey([]byte("channel-polling:"+common.CryptoSecret), string(payload))
	key := "channel:poll:" + strconv.Itoa(channelID) + ":" + fingerprint
	args := make([]any, 0, len(enabled)+2)
	args = append(args, len(keys), 86400)
	for _, index := range enabled {
		args = append(args, index)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return nextPollingKey.Run(ctx, r.cache, []string{key}, args...).Int()
}
