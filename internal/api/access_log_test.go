package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/publisher"
)

// withCapturedSlog swaps slog.Default for a JSON handler writing to buf and
// returns a restore func. Used to assert access-log field shape without
// coupling tests to the production handler's formatting.
func withCapturedSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { slog.SetDefault(old) }
}

// --------------------------------------------------------------------------
// statusRecorder: defaults to 200 when handler writes body without an
// explicit WriteHeader; captures the first WriteHeader.
// --------------------------------------------------------------------------

func TestStatusRecorder_DefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	if _, err := sr.Write([]byte("ok")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sr.status != 200 {
		t.Errorf("default status should be 200; got %d", sr.status)
	}
}

func TestStatusRecorder_CapturesWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.WriteHeader(http.StatusNotFound)
	if sr.status != 404 {
		t.Errorf("expected captured 404; got %d", sr.status)
	}
	if rec.Code != 404 {
		t.Errorf("underlying ResponseWriter should see 404; got %d", rec.Code)
	}
}

// First WriteHeader is what the client sees — match that.
func TestStatusRecorder_FirstWriteHeaderWins(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.WriteHeader(http.StatusBadRequest)
	sr.WriteHeader(http.StatusInternalServerError) // would also fire a net/http warning
	if sr.status != 400 {
		t.Errorf("first WriteHeader should win; got %d", sr.status)
	}
}

// --------------------------------------------------------------------------
// accessLog: emits a structured Info line with the expected fields.
// --------------------------------------------------------------------------

func TestAccessLog_SuccessRequest_EmitsOutcomeSuccess(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/providers/zhipu", nil)
	ctx := publisher.WithCaller(req.Context(), publisher.CallerInfo{
		Service: "control-server",
		TraceID: "req-abc",
	})
	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))

	got := buf.String()
	for _, want := range []string{
		`"event":"request"`,
		`"service":"control-server"`,
		`"method":"PUT"`,
		`"path":"/admin/api/v1/providers/zhipu"`,
		`"status":202`,
		`"outcome":"success"`,
		`"trace_id":"req-abc"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing field %q in log line: %s", want, got)
		}
	}
}

func TestAccessLog_FailureRequest_EmitsOutcomeFailure(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	req := httptest.NewRequest(http.MethodPost, "/sre/api/v1/replay", nil)
	ctx := publisher.WithCaller(req.Context(), publisher.CallerInfo{Service: "sre-cli"})
	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))

	got := buf.String()
	if !strings.Contains(got, `"status":400`) {
		t.Errorf("expected status=400 in log; got %s", got)
	}
	if !strings.Contains(got, `"outcome":"failure"`) {
		t.Errorf("expected outcome=failure in log; got %s", got)
	}
}

// Handler that writes body without WriteHeader → must log status=200, not 0.
func TestAccessLog_ImplicitOK_LogsStatus200(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest(http.MethodGet, "/user/api/v1/foo", nil)
	ctx := publisher.WithCaller(req.Context(), publisher.CallerInfo{Service: "user-svc"})
	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))

	got := buf.String()
	if !strings.Contains(got, `"status":200`) {
		t.Errorf("implicit OK should log status=200; got %s", got)
	}
	if !strings.Contains(got, `"outcome":"success"`) {
		t.Errorf("implicit OK should be success; got %s", got)
	}
}
