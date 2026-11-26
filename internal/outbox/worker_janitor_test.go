package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

// janitorStub records the sweep calls and replays a scripted result per batch.
type janitorStub struct {
	results []struct {
		deleted int64
		locked  bool
		err     error
	}
	calls  int
	limits []int
}

func (s *janitorStub) cleanup(
	_ context.Context,
	_, _ time.Duration,
	limit int,
) (int64, bool, error) {
	s.limits = append(s.limits, limit)
	i := s.calls
	s.calls++
	if i >= len(s.results) {
		return 0, true, nil
	}
	r := s.results[i]
	return r.deleted, r.locked, r.err
}

func workerWithJanitor(stub *janitorStub, batch, maxBatches int) *Worker {
	w := NewWorker(nil, nil, WorkerConfig{
		JanitorInterval:   time.Hour,
		JanitorBatch:      batch,
		JanitorMaxBatches: maxBatches,
	}, nil)
	w.cleanupFn = stub.cleanup
	return w
}

// A batch shorter than the configured size means the eligible backlog is gone,
// so the sweep must stop instead of spinning the remaining batches.
func TestJanitorTick_StopsOnShortBatch(t *testing.T) {
	stub := &janitorStub{results: []struct {
		deleted int64
		locked  bool
		err     error
	}{
		{deleted: 100, locked: true},
		{deleted: 7, locked: true},
	}}
	w := workerWithJanitor(stub, 100, 20)

	if err := w.janitorTick(context.Background()); err != nil {
		t.Fatalf("janitorTick: %v", err)
	}
	if stub.calls != 2 {
		t.Errorf("cleanup calls = %d, want 2", stub.calls)
	}
	for _, limit := range stub.limits {
		if limit != 100 {
			t.Errorf("limit = %d, want the configured batch size 100", limit)
		}
	}
}

// The per-sweep cap is the guard that keeps a misconfigured retention window
// from becoming an unbounded production delete.
func TestJanitorTick_StopsAtCap(t *testing.T) {
	full := make([]struct {
		deleted int64
		locked  bool
		err     error
	}, 10)
	for i := range full {
		full[i] = struct {
			deleted int64
			locked  bool
			err     error
		}{deleted: 50, locked: true}
	}
	stub := &janitorStub{results: full}
	w := workerWithJanitor(stub, 50, 3)

	if err := w.janitorTick(context.Background()); err != nil {
		t.Fatalf("janitorTick: %v", err)
	}
	if stub.calls != 3 {
		t.Errorf("cleanup calls = %d, want the 3-batch cap", stub.calls)
	}
}

// Another replica holding the advisory lock must end this sweep rather than
// queue behind it; both replicas otherwise fight over the same terminal rows.
func TestJanitorTick_YieldsWhenContended(t *testing.T) {
	stub := &janitorStub{results: []struct {
		deleted int64
		locked  bool
		err     error
	}{
		{deleted: 0, locked: false},
	}}
	w := workerWithJanitor(stub, 100, 20)

	if err := w.janitorTick(context.Background()); err != nil {
		t.Fatalf("janitorTick: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("cleanup calls = %d, want 1 before yielding", stub.calls)
	}
}

func TestJanitorTick_ReturnsError(t *testing.T) {
	wantErr := errors.New("delete failed")
	stub := &janitorStub{results: []struct {
		deleted int64
		locked  bool
		err     error
	}{
		{deleted: 100, locked: true},
		{deleted: 0, locked: true, err: wantErr},
	}}
	w := workerWithJanitor(stub, 100, 20)

	err := w.janitorTick(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if stub.calls != 2 {
		t.Errorf("cleanup calls = %d, want 2", stub.calls)
	}
}

// JanitorInterval=0 is honoured as "retention off" so the pre-wiring behaviour
// stays reachable without a rollback.
func TestRun_JanitorDisabledWhenIntervalZero(t *testing.T) {
	stub := &janitorStub{}
	w := NewWorker(nil, nil, WorkerConfig{
		// Long enough that the delivery tick never fires during this test.
		ScanInterval:    time.Hour,
		JanitorInterval: 0,
		JanitorBatch:    100,
	}, nil)
	w.cleanupFn = stub.cleanup

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if stub.calls != 0 {
		t.Errorf("cleanup calls = %d, want 0 while retention is disabled", stub.calls)
	}
}

// NewWorker must supply the bounds even when a caller leaves them unset, so a
// partially-configured worker cannot issue an unbounded delete.
func TestNewWorker_JanitorBoundsDefaulted(t *testing.T) {
	w := NewWorker(nil, nil, WorkerConfig{}, nil)
	if w.cfg.JanitorBatch != 5000 {
		t.Errorf("JanitorBatch = %d, want 5000", w.cfg.JanitorBatch)
	}
	if w.cfg.JanitorMaxBatches != 20 {
		t.Errorf("JanitorMaxBatches = %d, want 20", w.cfg.JanitorMaxBatches)
	}
	if w.cfg.JanitorInterval != 0 {
		t.Errorf("JanitorInterval = %v, want 0 so retention stays explicitly opt-in", w.cfg.JanitorInterval)
	}
}
