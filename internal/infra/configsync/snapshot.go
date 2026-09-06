// Package configsync publishes immutable configuration payloads through DragonflyDB.
// Publishers must serialize source reads with source mutations; the cache does
// not decide which database snapshot is authoritative.
package configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/go-redis/redis/v8"
)

type Snapshot struct {
	Version string
	Data    []byte
}
type Store struct {
	client     *redis.Client
	key, topic string
}

var ErrUnavailable = errors.New("configuration snapshot is unavailable; a control-plane instance must publish it first")

func New(client *redis.Client, namespace string) *Store {
	return &Store{client: client, key: "config:snapshot:" + namespace, topic: "config:changed:" + namespace}
}

var publish = redis.NewScript(`
if redis.call('HGET',KEYS[1],'version') == ARGV[1] and redis.call('HGET',KEYS[1],'data') == ARGV[2] then return 0 end
redis.call('HSET',KEYS[1],'version',ARGV[1],'data',ARGV[2])
redis.call('PUBLISH',ARGV[3],ARGV[1])
return 1
`)

func (s *Store) Publish(ctx context.Context, data []byte) (Snapshot, error) {
	if s.client == nil {
		return Snapshot{}, ErrUnavailable
	}
	digest := sha256.Sum256(data)
	snapshot := Snapshot{Version: hex.EncodeToString(digest[:]), Data: append([]byte(nil), data...)}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := publish.Run(ctx, s.client, []string{s.key}, snapshot.Version, string(data), s.topic).Err()
	return snapshot, err
}
func (s *Store) Read(ctx context.Context) (Snapshot, error) {
	if s.client == nil {
		return Snapshot{}, ErrUnavailable
	}
	values, err := s.client.HGetAll(ctx, s.key).Result()
	if err != nil {
		return Snapshot{}, err
	}
	data, ok := values["data"]
	if !ok || values["version"] == "" {
		return Snapshot{}, ErrUnavailable
	}
	digest := sha256.Sum256([]byte(data))
	if values["version"] != hex.EncodeToString(digest[:]) {
		return Snapshot{}, errors.New("configuration snapshot digest mismatch")
	}
	return Snapshot{Version: values["version"], Data: []byte(data)}, nil
}

// Watch treats notifications as hints: reconnects and missed messages are
// repaired by refreshing on a bounded timer. Payloads always come from Read.
func (s *Store) Watch(ctx context.Context, interval time.Duration, refresh func(context.Context) error, failed func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	if s.client == nil {
		failed(ErrUnavailable)
		return
	}
	subscription := s.client.Subscribe(ctx, s.topic)
	defer subscription.Close()
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := subscription.Receive(readyCtx)
	cancel()
	if err != nil && ctx.Err() == nil {
		failed(err)
	}
	updates := subscription.Channel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := refresh(ctx); err != nil && ctx.Err() == nil {
		failed(err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				updates = nil
			}
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		if err := refresh(ctx); err != nil {
			failed(err)
		}
	}
}
