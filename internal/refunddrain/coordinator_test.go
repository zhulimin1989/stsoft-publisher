package refunddrain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const gatewayBeginScript = `
if redis.call('GET', KEYS[1]) then
  return 0
end
redis.call('INCR', KEYS[2])
redis.call('EXPIRE', KEYS[2], ARGV[1])
redis.call('SET', KEYS[3], ARGV[2])
return 1
`

func newTestCoordinator(t *testing.T) (*Coordinator, *redis.Client) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client), client
}

func TestEstablishReportsInflightAndHistoricalRequestEvidence(t *testing.T) {
	coordinator, client := newTestCoordinator(t)
	ctx := context.Background()
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("key-1")))
	if err := client.Set(ctx, inflightKey(keyHash), "2", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, requestSeenKey(keyHash), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := coordinator.Establish(ctx, "entitlement-1", []string{keyHash})
	if err != nil {
		t.Fatalf("Establish() error = %v", err)
	}
	if snapshot.ProtocolVersion != ProtocolVersion || snapshot.KeyCount != 1 || snapshot.Inflight != 2 || snapshot.RequestSeenKeys != 1 || snapshot.Ready {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if state, err := client.Get(ctx, stateKey(keyHash)).Result(); err != nil || state != "entitlement-1" {
		t.Fatalf("state = %q, err = %v", state, err)
	}

	status, err := coordinator.Status(ctx, "entitlement-1")
	if err != nil || status != snapshot {
		t.Fatalf("Status() = %+v, %v; want %+v", status, err, snapshot)
	}
}

func TestGatewayBeginAndDrainEstablishHaveNoUntrackedRace(t *testing.T) {
	coordinator, client := newTestCoordinator(t)
	ctx := context.Background()

	for index := 0; index < 100; index++ {
		keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("race-key-%d", index))))
		drainID := fmt.Sprintf("drain-%d", index)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var admitted int64
		var beginErr error
		var snapshot Snapshot
		var establishErr error

		go func() {
			defer wait.Done()
			<-start
			admitted, beginErr = client.Eval(
				ctx,
				gatewayBeginScript,
				[]string{stateKey(keyHash), inflightKey(keyHash), requestSeenKey(keyHash)},
				3600,
				"request-1",
			).Int64()
		}()
		go func() {
			defer wait.Done()
			<-start
			snapshot, establishErr = coordinator.Establish(ctx, drainID, []string{keyHash})
		}()
		close(start)
		wait.Wait()

		if beginErr != nil || establishErr != nil {
			t.Fatalf("iteration %d: begin error = %v, establish error = %v", index, beginErr, establishErr)
		}
		switch admitted {
		case 0:
			if !snapshot.Ready || snapshot.RequestSeenKeys != 0 || snapshot.Inflight != 0 {
				t.Fatalf("iteration %d: blocked begin snapshot = %+v", index, snapshot)
			}
		case 1:
			if snapshot.RequestSeenKeys != 1 || snapshot.Inflight != 1 || snapshot.Ready {
				t.Fatalf("iteration %d: admitted begin escaped snapshot = %+v", index, snapshot)
			}
		default:
			t.Fatalf("iteration %d: admitted = %d", index, admitted)
		}
	}
}

func TestEstablishBindsDrainIDToOneCanonicalKeySet(t *testing.T) {
	coordinator, _ := newTestCoordinator(t)
	ctx := context.Background()
	first := fmt.Sprintf("%x", sha256.Sum256([]byte("first")))
	second := fmt.Sprintf("%x", sha256.Sum256([]byte("second")))

	if _, err := coordinator.Establish(ctx, "drain-1", []string{first, first}); err != nil {
		t.Fatalf("first Establish() error = %v", err)
	}
	if _, err := coordinator.Establish(ctx, "drain-1", []string{first}); err != nil {
		t.Fatalf("idempotent Establish() error = %v", err)
	}
	if _, err := coordinator.Establish(ctx, "drain-1", []string{second}); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("changed key set error = %v", err)
	}
}

func TestReleaseUsesDrainIDCASAndIsIdempotent(t *testing.T) {
	coordinator, client := newTestCoordinator(t)
	ctx := context.Background()
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("key-1")))
	if _, err := coordinator.Establish(ctx, "drain-1", []string{keyHash}); err != nil {
		t.Fatal(err)
	}

	if err := coordinator.Release(ctx, "drain-1"); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if exists, err := client.Exists(ctx, stateKey(keyHash), leaseKey("drain-1")).Result(); err != nil || exists != 0 {
		t.Fatalf("released keys still exist = %d, err = %v", exists, err)
	}
	if err := coordinator.Release(ctx, "drain-1"); err != nil {
		t.Fatalf("idempotent Release() error = %v", err)
	}
}

func TestReleaseNeverDeletesAnotherDrainMarker(t *testing.T) {
	coordinator, client := newTestCoordinator(t)
	ctx := context.Background()
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("key-1")))
	if _, err := coordinator.Establish(ctx, "drain-1", []string{keyHash}); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, stateKey(keyHash), "drain-2", 0).Err(); err != nil {
		t.Fatal(err)
	}

	if err := coordinator.Release(ctx, "drain-1"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Release() error = %v", err)
	}
	if state, err := client.Get(ctx, stateKey(keyHash)).Result(); err != nil || state != "drain-2" {
		t.Fatalf("state = %q, err = %v", state, err)
	}
}

func TestStatusFailsClosedWhenAMarkerIsMissing(t *testing.T) {
	coordinator, client := newTestCoordinator(t)
	ctx := context.Background()
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("key-1")))
	if _, err := coordinator.Establish(ctx, "drain-1", []string{keyHash}); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(ctx, stateKey(keyHash)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Status(ctx, "drain-1"); !errors.Is(err, ErrStateMissing) {
		t.Fatalf("Status() error = %v", err)
	}
}
