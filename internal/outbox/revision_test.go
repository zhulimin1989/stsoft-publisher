package outbox

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNextRevisionDoesNotFallbackToWallClockWhenPersistentSequenceFails(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://publisher:publisher@127.0.0.1:1/publisher?sslmode=disable")
	if err != nil {
		t.Fatalf("construct pool: %v", err)
	}
	pool.Close()

	revision, err := NewStore(pool).NextRevision(context.Background())
	if err == nil {
		t.Fatalf("closed persistent sequence returned revision=%d; wall-clock fallback can poison monotonic ordering", revision)
	}
	if revision != 0 {
		t.Fatalf("failed sequence revision=%d want 0", revision)
	}
}
