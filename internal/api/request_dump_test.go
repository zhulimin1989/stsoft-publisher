package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

func TestDumpRequest_RedactsSensitiveFields(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	body := `{
		"ciphertext": "ciphertext-secret",
		"nested": {
			"key_hash": "hash-secret",
			"items": [
				{"Authorization": "Bearer auth-secret", "safe": "visible"}
			]
		},
		"safe": "keep"
	}`
	handler := dumpRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/key-pools/openai/keys/k1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer should-never-log")
	ctx := publisher.WithCaller(req.Context(), publisher.CallerInfo{Service: "control-server"})

	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))

	logLine := decodeSingleLogLine(t, buf.String())
	raw, ok := logLine["body_raw"].(string)
	if !ok {
		t.Fatalf("request_dump log must include redacted body_raw string: %s", buf.String())
	}
	for _, forbidden := range []string{"ciphertext-secret", "hash-secret", "auth-secret", "should-never-log"} {
		if strings.Contains(raw, forbidden) || strings.Contains(buf.String(), forbidden) {
			t.Fatalf("request_dump leaked %q in log: %s", forbidden, buf.String())
		}
	}
	for _, want := range []string{`"ciphertext":"[REDACTED]"`, `"key_hash":"[REDACTED]"`, `"Authorization":"[REDACTED]"`, `"safe":"keep"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("redacted body missing %s: %s", want, raw)
		}
	}
}

func TestDumpRequest_DisabledByDefault(t *testing.T) {
	t.Setenv("REQUEST_DUMP", "")
	buf, restore := withCapturedSlog(t)
	defer restore()

	pub := &stubPublisher{}
	pub.publishKeyFunc = func(ctx context.Context, m *types.KeyMeta) (types.BatchID, error) {
		return pub.acceptAlways(), nil
	}
	h := NewServer(pub, fixtureAuth(t)).Handler()
	body := `{"key_id":"kid-1","key_hash":"hash-secret","status":"active","alias":"secret-alias"}`
	req := httptest.NewRequest(http.MethodPut, "/user/api/v1/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := buf.String()
	if strings.Contains(got, `"event":"request_dump"`) {
		t.Fatalf("REQUEST_DUMP default/empty must not emit request_dump logs: %s", got)
	}
	if strings.Contains(got, "hash-secret") || strings.Contains(got, "secret-alias") {
		t.Fatalf("disabled request dump leaked request body: %s", got)
	}
}

func TestBodyCap_OversizeRejected(t *testing.T) {
	t.Setenv("REQUEST_DUMP", "1")
	t.Setenv("REQUEST_BODY_MAX_BYTES", "64")
	buf, restore := withCapturedSlog(t)
	defer restore()

	var called bool
	pub := &stubPublisher{}
	pub.publishKeyFunc = func(ctx context.Context, m *types.KeyMeta) (types.BatchID, error) {
		called = true
		return pub.acceptAlways(), nil
	}
	h := NewServer(pub, fixtureAuth(t)).Handler()
	largeAlias := strings.Repeat("x", 256)
	body := fmt.Sprintf(`{"key_id":"kid-1","key_hash":"%s","status":"active","alias":"%s"}`, strings.Repeat("a", 64), largeAlias)
	req := httptest.NewRequest(http.MethodPut, "/user/api/v1/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413 body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("oversize request must be rejected before handler/publisher is called")
	}
	if strings.Contains(buf.String(), largeAlias) {
		t.Fatalf("oversize request log must not include full body: %s", buf.String())
	}
}

func TestAuthFail_DoesNotLogRawToken(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	auth := fixtureAuth(t)
	rawToken := "unknown-bearer-token-1234567890"
	req := httptest.NewRequest(http.MethodPost, "/user/api/v1/keys", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()

	auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unknown token must not reach downstream")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := buf.String()
	if strings.Contains(got, rawToken) || strings.Contains(got, "token_raw") {
		t.Fatalf("auth failure log leaked raw token: %s", got)
	}
	sum := sha256.Sum256([]byte(rawToken))
	want := fmt.Sprintf(`"token_fingerprint":"sha256:%x(len=%d)"`, sum[:4], len(rawToken))
	if !strings.Contains(got, want) {
		t.Fatalf("auth failure log missing token fingerprint %s: %s", want, got)
	}
}

func decodeSingleLogLine(t *testing.T, line string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &out); err != nil {
		t.Fatalf("decode slog JSON line: %v\n%s", err, line)
	}
	return out
}
