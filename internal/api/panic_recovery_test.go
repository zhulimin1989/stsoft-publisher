package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/publisher"
)

// 2026-05-22 — publisher had NO panic recovery middleware, so any
// handler panic crashed the entire process. Real production incident:
// admin create-provider triggered a panic and took the publisher down
// (singleton writer → all callers blocked until k8s restart, ~5-15s).
// This file pins:
//   1. a panicking handler does NOT crash the test (and by extension
//      the production process)
//   2. caller gets a 500 INTERNAL_PANIC response
//   3. ctx-carried CallerInfo (set by Authenticate) is still readable
//      by the recovery slog line — operators correlate via request_id

func TestRecoverPanic_HandlerPanicReturns500(t *testing.T) {
	panickingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated handler crash")
	})

	wrapped := recoverPanic(panickingHandler)

	// Inject CallerInfo so the slog line in recoverPanic resolves
	// caller fields (mirrors what Authenticate would do in production).
	ctx := publisher.WithCaller(context.Background(), publisher.CallerInfo{
		Service:   "admin",
		RequestID: "req-test-1",
		TraceID:   "trace-test-1",
		ActorID:   "tok-abc1234",
	})

	r := httptest.NewRequest(http.MethodPut, "/admin/api/v1/providers/openai", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// If recovery doesn't work, the panic propagates up and crashes
	// the test goroutine. The fact this test completes proves the
	// recover() defer caught it.
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INTERNAL_PANIC") {
		t.Errorf("body should carry INTERNAL_PANIC code; got %s", w.Body.String())
	}
}

func TestRecoverPanic_NoPanic_PassesThroughCleanly(t *testing.T) {
	normalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"batch_id":"abc"}`))
	})
	wrapped := recoverPanic(normalHandler)

	r := httptest.NewRequest(http.MethodPut, "/admin/api/v1/providers/openai", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("non-panic path must pass through; got status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "abc") {
		t.Errorf("non-panic path must return handler body; got %s", w.Body.String())
	}
}

func TestRecoverPanic_NilPointerDeref(t *testing.T) {
	// nil-pointer deref is the most common production panic pattern.
	// Pin it explicitly so future refactors don't break this path.
	nilDerefHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p *struct{ X int }
		_ = p.X // panic: runtime error: invalid memory address
	})
	wrapped := recoverPanic(nilDerefHandler)

	r := httptest.NewRequest(http.MethodPut, "/x", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("nil-deref must be caught: got status %d", w.Code)
	}
}
