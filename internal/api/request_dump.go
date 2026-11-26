package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tokenplatform/publisher/internal/publisher"
)

// dumpRequest is a debug middleware that logs EVERY authenticated publisher
// HTTP request with:
//   - method / path / query / content-type
//   - body length + body raw bytes (string form)
//   - all caller-context fields populated by Authenticate
//     (service / trace_id / request_id / actor_id)
//   - Idempotency-Key header value
//
// Logged at slog.Info so it survives production log levels. User asked
// for this 2026-05-22 after a multi-day hunt for the source of
// "other-c43727195d63" provider_id that couldn't be traced from code
// alone — full request dumps let operators replay any past invocation
// from log alone without instrumenting individual handlers.
//
// Wiring(server.go Handler()):
//
//	Authenticate ─► accessLog ─► recoverPanic ─► dumpRequest ─► authed mux
//	  ↑ mints request_id            ↑ catches panic    ↑ dumps everything
//	    + ctx.CallerInfo              + INTERNAL_PANIC
//
// dumpRequest sits AFTER recoverPanic so a panic during dump itself can't
// crash the process, and AFTER Authenticate so caller context fields are
// already populated.
//
// Security:
//
//   - Authorization header is NEVER logged. The middleware does not
//     include it in the slog line. Caller identity is already in
//     CallerInfo (service / actor_id), so the raw token would be
//     redundant + dangerous (memory rule: feedback_no_apikey_in_logs).
//   - Request body is logged only after JSON redaction. Sensitive field
//     names are matched case-insensitively and recursively. If the body
//     is not valid JSON, only body_len is logged.
//   - This middleware is opt-in via REQUEST_DUMP=1/true/on/yes. It is
//     disabled by default.
//
// Performance: body is read into memory + restored to r.Body via
// io.NopCloser(bytes.NewReader(buf)). For 100-item bulk POSTs (~50KB)
// this adds an allocation per request. The server wraps authenticated
// request bodies with a configurable cap before dumpRequest runs.
func dumpRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bodyBytes []byte
		if r.Body != nil {
			bodyBytes, _ = io.ReadAll(r.Body)
			// Replace r.Body so downstream handlers can read it.
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		caller := publisher.CallerFromContext(r.Context())
		args := []any{
			"event", "request_dump",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"content_type", r.Header.Get("Content-Type"),
			"body_len", len(bodyBytes),
			"caller_service", caller.Service,
			"caller_trace_id", caller.TraceID,
			"caller_request_id", caller.RequestID,
			"actor_id", caller.ActorID,
			"idempotency_key", r.Header.Get("Idempotency-Key"),
		}
		if redactedBody, ok := redactRequestBodyForLog(bodyBytes); ok {
			args = append(args, "body_raw", redactedBody)
		}
		slog.Info("publisher request dump", args...)

		next.ServeHTTP(w, r)
	})
}

const redactedLogValue = "[REDACTED]"

var sensitiveLogFields = map[string]struct{}{
	"ciphertext":    {},
	"key_hash":      {},
	"secret":        {},
	"token":         {},
	"bearer":        {},
	"password":      {},
	"api_key":       {},
	"authorization": {},
}

func redactRequestBodyForLog(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var payload any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return "", false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return "", false
	}
	payload = redactSensitiveJSON(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func redactSensitiveJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if isSensitiveLogField(key) {
				x[key] = redactedLogValue
				continue
			}
			x[key] = redactSensitiveJSON(value)
		}
		return x
	case []any:
		for i, value := range x {
			x[i] = redactSensitiveJSON(value)
		}
		return x
	default:
		return v
	}
}

func isSensitiveLogField(key string) bool {
	_, ok := sensitiveLogFields[strings.ToLower(key)]
	return ok
}
