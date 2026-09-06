package testdb

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

// UseCache isolates cache-backed unit fixtures without enabling unrelated
// business caches. Real DragonflyDB contracts are exercised by the e2e suite.
func UseCache(t testing.TB) *redis.Client {
	t.Helper()
	previous := common.RDB
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	common.RDB = client
	t.Cleanup(func() { common.RDB = previous; _ = client.Close() })
	return client
}
