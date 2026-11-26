package outbox

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestKeyPoolEntryProviderAccountRefReachesRedis(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	entry := types.KeyPoolEntry{
		SchemaVersion:      1,
		Revision:           1700000000000000,
		KeyID:              "key-primary",
		ProviderAccountRef: "provider-account-glm-main",
		Ciphertext:         "vault://providers/zhipu/key-primary",
		Priority:           1,
		Weight:             100,
		RPMLimit:           500,
		TPMLimit:           100000,
		Status:             "enabled",
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}

	worker := NewWorker(nil, rdb, WorkerConfig{}, nil)
	row := OutboxRow{
		Op:         OpHSet,
		RedisKey:   "gw:keypool:zhipu",
		RedisField: entry.KeyID,
		Payload:    payload,
	}
	if err := worker.runOne(context.Background(), row, &batchState{}); err != nil {
		t.Fatalf("deliver keypool HSET: %v", err)
	}

	raw, err := rdb.HGet(context.Background(), row.RedisKey, row.RedisField).Bytes()
	if err != nil {
		t.Fatalf("read Redis keypool entry: %v", err)
	}
	var got types.KeyPoolEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode Redis keypool entry: %v payload=%s", err, raw)
	}
	if got.ProviderAccountRef != entry.ProviderAccountRef {
		t.Fatalf("Redis provider_account_ref=%q want %q payload=%s", got.ProviderAccountRef, entry.ProviderAccountRef, raw)
	}
	t.Logf("Redis keypool artifact: %s", raw)
}
