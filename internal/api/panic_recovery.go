package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/tokenplatform/publisher/internal/publisher"
)

// recoverPanic is the outermost-but-one HTTP middleware. It wraps every
// handler in a deferred recover() so a panic in a handler turns into a
// 500 response instead of taking down the entire publisher process.
//
// Why this matters: even in a multi-instance HA deployment (N replicas
// behind a load balancer), a panic kills ONE pod and its in-flight
// requests; K8s restarts it in 5-15s. During that window the LB will
// route around the failing pod, so client-visible impact is just one
// 5xx per panicked handler — but only IF this middleware catches the
// panic. Without it, the panicked pod crashes mid-Redis-pipeline and
// every in-flight call on that pod is lost without a proper 5xx,
// turning a contained handler bug into "the entire batch of requests
// I sent to that pod never returned".
//
// 2026-05-26: previously labelled "publisher is a singleton" — wrote
// before outbox.ClaimDue used FOR UPDATE SKIP LOCKED. The code path
// was always multi-instance-safe (PG locks + idempotent Redis ops);
// the comment is now updated to match deployment reality.
//
// Logged fields:
//   - event = "panic_recovered"
//   - method / path / service / request_id / trace_id  (context anchors)
//   - panic = fmt.Sprint(r)  (the recovered value)
//   - stack = full goroutine stack (debug.Stack())
//
// Response: 500 INTERNAL with code=INTERNAL_PANIC. Caller treats this
// like any other 5xx — retry-safe-ish (the panic might have left
// partial state in PG if it hit mid-InsertBatch; the tx defer-rollback
// in InsertBatch handles that. Dedup_key still works on retry.)
//
// Recovery must run AFTER auth + accessLog so the recovered log line
// carries caller info — see Handler() wiring in server.go.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rv := recover()
			if rv == nil {
				return
			}
			caller := publisher.CallerFromContext(r.Context())
			slog.Error("publisher handler panic recovered",
				"event", "panic_recovered",
				"method", r.Method,
				"path", r.URL.Path,
				"service", caller.Service,
				"request_id", caller.RequestID,
				"trace_id", caller.TraceID,
				"panic", rv,
				"stack", string(debug.Stack()),
			)
			// Best-effort 500 — if the handler already wrote a partial
			// response WriteHeader will warn but not crash. Caller sees
			// either the partial body the handler did write OR our 500
			// envelope; either way the process keeps serving other
			// requests.
			writeError(w, http.StatusInternalServerError, "INTERNAL_PANIC",
				"internal server error", nil)
		}()
		next.ServeHTTP(w, r)
	})
}
