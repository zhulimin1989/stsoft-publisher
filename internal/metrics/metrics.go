// Package metrics is publisher's Prometheus instrumentation layer.
//
// The six metrics exposed here target the two risks publisher carries as
// the sole writer of gateway data-plane Redis:
//
//  1. Token leak / abuse — auth events surface as auth_ok_total{service}
//     and auth_fail_total{reason}. Rate spikes on auth_fail or sudden
//     deltas in auth_ok per service feed SIEM anomaly detection.
//
//  2. Outbox backlog / Redis outage — worker_deliver_total{status},
//     outbox_pending (gauge), outbox_oldest_pending_age_seconds (gauge),
//     and outbox_dead_letter_total cover both rate-of-delivery and
//     stuck-row visibility. The dead-letter counter is the page-on signal
//     (data is lost without operator intervention once a row crosses
//     MaxAttempts).
//
// Cardinality is deliberately bounded: service labels come from a 4-row
// allowlist (portal/billing/ops/sre); reason labels are a fixed-size set
// drawn from the middleware. Histograms are intentionally not exported
// here — SLO validation latency histograms are tracked under the
// "推荐" tier in docs and not yet committed.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	authOK = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_publisher_auth_ok_total",
		Help: "Successful authenticated requests, by caller service identity.",
	}, []string{"service"})

	authFail = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_publisher_auth_fail_total",
		Help: "Authentication failures grouped by reason (missing_bearer / unknown_token / cross_namespace / service_no_namespace).",
	}, []string{"reason"})

	workerDeliver = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_publisher_worker_deliver_total",
		Help: "Outbox rows the worker has attempted to deliver, grouped by terminal status (delivered / failed).",
	}, []string{"status"})

	deadLetter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gw_publisher_outbox_dead_letter_total",
		Help: "Outbox rows promoted to dead_letter status (exhausted MaxAttempts). Page-on signal: data is lost without operator intervention.",
	})

	outboxPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gw_publisher_outbox_pending",
		Help: "Current count of outbox rows in status='pending'. Updated by a PG poller (see Poller).",
	})

	oldestPendingAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gw_publisher_outbox_oldest_pending_age_seconds",
		Help: "Age, in seconds, of the oldest pending outbox row. Maps to the e2e p99 SLO (spec target: < 0.5s healthy). 0 when queue is empty.",
	})

	janitorSweep = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_publisher_outbox_janitor_sweep_total",
		Help: "Retention sweeps by outcome (drained / capped / contended / failed). A sweep that stays 'capped' means retention is not keeping up.",
	}, []string{"outcome"})

	janitorDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gw_publisher_outbox_janitor_deleted_total",
		Help: "Terminal outbox rows removed by retention. Flat at zero while the table grows means the janitor is disabled or contended.",
	})
)

// AuthOK records a successful authenticated request for the given service.
func AuthOK(service string) {
	authOK.WithLabelValues(service).Inc()
}

// AuthFail records an auth failure with a fixed-set reason.
func AuthFail(reason string) {
	authFail.WithLabelValues(reason).Inc()
}

// WorkerDelivered records one successful Redis op delivery.
func WorkerDelivered() {
	workerDeliver.WithLabelValues("delivered").Inc()
}

// WorkerFailed records one transient delivery failure (will retry).
func WorkerFailed() {
	workerDeliver.WithLabelValues("failed").Inc()
}

// DeadLetter records a row crossing MaxAttempts and moving to dead_letter.
func DeadLetter() {
	deadLetter.Inc()
}

// SetOutboxPending sets the gauge for current pending-row count.
// Called by the Poller goroutine.
func SetOutboxPending(n int64) {
	outboxPending.Set(float64(n))
}

// SetOldestPendingAge sets the gauge for oldest-pending-row age, in
// seconds. Pass 0 when the queue is empty.
func SetOldestPendingAge(seconds float64) {
	oldestPendingAge.Set(seconds)
}

// JanitorSweep records the outcome of one retention sweep.
func JanitorSweep(outcome string) {
	janitorSweep.WithLabelValues(outcome).Inc()
}

// JanitorDeleted adds the rows removed by one retention batch.
func JanitorDeleted(n int64) {
	if n > 0 {
		janitorDeleted.Add(float64(n))
	}
}

// Handler returns the /metrics http.Handler. Mounted by api.Server
// alongside /healthz, outside the auth middleware.
func Handler() http.Handler {
	return promhttp.Handler()
}
