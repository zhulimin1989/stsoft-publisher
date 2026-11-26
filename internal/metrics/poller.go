package metrics

import (
	"context"
	"log/slog"
	"time"
)

// PendingStatsSource is what Poller needs from the outbox layer: a single
// query that returns both pending-row count and the oldest pending row's
// age. Keeping the interface narrow avoids an import cycle with the
// outbox package and makes the poller trivially mockable in tests.
type PendingStatsSource interface {
	PendingStats(ctx context.Context) (pending int64, oldestAge time.Duration, err error)
}

// Poller refreshes the two gauge metrics (pending count + oldest pending
// age) on a fixed interval. Both gauges are stale between ticks; the
// interval should be short enough that "is the queue draining?" stays
// answerable on Prom's typical 15s scrape — 5s is a reasonable default.
//
// The poller never blocks the worker; if PendingStats errors we log and
// skip the tick (leave the previous gauge values in place).
type Poller struct {
	src      PendingStatsSource
	interval time.Duration
}

// NewPoller constructs a Poller. Pass interval=0 to use the 5s default.
func NewPoller(src PendingStatsSource, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{src: src, interval: interval}
}

// Run drives the polling loop until ctx is cancelled. Errors are logged
// at warn; the previous gauge values are retained on transient failures.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	p.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	pending, age, err := p.src.PendingStats(ctx)
	if err != nil {
		slog.Warn("publisher metrics poller", "error", err)
		return
	}
	SetOutboxPending(pending)
	SetOldestPendingAge(age.Seconds())
}
