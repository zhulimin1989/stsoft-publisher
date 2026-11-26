package metrics

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsHandler asserts the six required metrics show up in the
// /metrics text exposition after at least one increment of each.
func TestMetricsHandler(t *testing.T) {
	AuthOK("user-service")
	AuthFail("unknown_token")
	WorkerDelivered()
	WorkerFailed()
	DeadLetter()
	SetOutboxPending(7)
	SetOldestPendingAge(2.5)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	s := string(body)

	for _, want := range []string{
		`gw_publisher_auth_ok_total{service="user-service"}`,
		`gw_publisher_auth_fail_total{reason="unknown_token"}`,
		`gw_publisher_worker_deliver_total{status="delivered"}`,
		`gw_publisher_worker_deliver_total{status="failed"}`,
		`gw_publisher_outbox_dead_letter_total`,
		`gw_publisher_outbox_pending 7`,
		`gw_publisher_outbox_oldest_pending_age_seconds 2.5`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

// TestPoller asserts the gauges follow PendingStats output.
func TestPoller(t *testing.T) {
	src := &stubSrc{pending: 42, age: 3 * time.Second}
	p := NewPoller(src, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	// Force a fresh tick to ensure the gauge value is the stub's, not
	// leftovers from other tests in this package.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	s := string(body)
	if !strings.Contains(s, "gw_publisher_outbox_pending 42") {
		t.Errorf("expected pending=42 from poller, got: %s", excerpt(s, "gw_publisher_outbox_pending"))
	}
	if !strings.Contains(s, "gw_publisher_outbox_oldest_pending_age_seconds 3") {
		t.Errorf("expected age=3 from poller, got: %s", excerpt(s, "gw_publisher_outbox_oldest_pending_age_seconds"))
	}
}

type stubSrc struct {
	pending int64
	age     time.Duration
}

func (s *stubSrc) PendingStats(ctx context.Context) (int64, time.Duration, error) {
	return s.pending, s.age, nil
}

func excerpt(haystack, needle string) string {
	i := strings.Index(haystack, needle)
	if i < 0 {
		return "<missing>"
	}
	end := i + 80
	if end > len(haystack) {
		end = len(haystack)
	}
	return haystack[i:end]
}
