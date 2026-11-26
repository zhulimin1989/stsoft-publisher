package outbox

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestDeliverBatchSortsClaimedRowsWithoutMutatingCallerSlice(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	rows := []OutboxRow{
		{ID: 20, Seq: 2, Op: OpSet, RedisKey: "gw:test:ordered-batch", Payload: []byte("second"), MethodName: "IntegrationTest"},
		{ID: 10, Seq: 1, Op: OpSet, RedisKey: "gw:test:ordered-batch", Payload: []byte("first"), MethodName: "IntegrationTest"},
	}
	worker := NewWorker(nil, rdb, WorkerConfig{}, nil)
	worker.deliverBatch(ctx, uuid.New(), rows)

	got, err := rdb.Get(ctx, "gw:test:ordered-batch").Result()
	if err != nil {
		t.Fatalf("read ordered batch result: %v", err)
	}
	if got != "second" {
		t.Fatalf("final value=%q want second (Seq 1 then Seq 2)", got)
	}
	if rows[0].Seq != 2 || rows[0].ID != 20 || rows[1].Seq != 1 || rows[1].ID != 10 {
		t.Fatalf("deliverBatch mutated caller slice: %+v", rows)
	}
}
