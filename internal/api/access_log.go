package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/tokenplatform/publisher/internal/publisher"
)

// accessLog emits one structured slog.Info line per *authenticated* request
// after the handler returns. Mounted between TokenAuth.Authenticate and the
// route mux so every line is guaranteed to carry a resolved caller service
// identity. Auth failures never reach this middleware — they are logged at
// Warn level inside Authenticate (logAuthFail) and short-circuit there.
//
// Fields:
//   - event       = "request"
//   - service     resolved caller (e.g. "control-server")
//   - method      HTTP method
//   - path        URL path (no query string; publisher's surface is PUT/POST/
//                 DELETE with no meaningful query params)
//   - status      HTTP response status code (defaults to 200 if the handler
//                 wrote a body without an explicit WriteHeader call)
//   - duration_ms wall-clock latency
//   - trace_id    caller-supplied X-Request-ID (may be "")
//   - outcome     "success" for 2xx, "failure" for everything else
//
// Deliberately does NOT log request or response bodies — publisher's surface
// carries credentials (key_hash / ciphertext / bearers leaked into the wrong
// header). Body-level audit is a separate concern tracked by the DB-side
// audit-log TODO.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		caller := publisher.CallerFromContext(r.Context())
		outcome := "success"
		if rec.status >= 400 {
			outcome = "failure"
		}
		slog.Info("publisher request",
			"event", "request",
			"service", caller.Service,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", caller.TraceID,
			"outcome", outcome,
		)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code for
// the access-log line. Defaults to 200 because net/http writes an implicit
// 200 on the first Write() when WriteHeader is never called. Multiple
// WriteHeader calls are a Go runtime warning; we keep the *first* status
// (matches what the client sees) and let the underlying ResponseWriter
// emit its usual duplicate-WriteHeader warning.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}
