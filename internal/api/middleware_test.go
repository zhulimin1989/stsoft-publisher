package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/config"
	"github.com/tokenplatform/publisher/internal/publisher"
)

// fixtureAuth builds a TokenAuth with the standard test fixture:
// user-service → /user/*, billing-service → /billing/*, admin-service → /admin/*,
// sre-oncall → /sre/* (kept here so the cross-namespace 403 cases retain
// a 4th identity to test against).
func fixtureAuth(t *testing.T) *TokenAuth {
	t.Helper()
	auth, err := NewTokenAuth(map[string]config.Caller{
		"user-service":    {Namespace: "user", Tokens: []string{"user-token"}},
		"billing-service": {Namespace: "billing", Tokens: []string{"billing-token"}},
		"admin-service":   {Namespace: "admin", Tokens: []string{"admin-token"}},
		"sre-oncall":      {Namespace: "sre", Tokens: []string{"sre-token"}},
	})
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	return auth
}

// TestAuthenticate_NamespaceBinding asserts the middleware refuses to
// admit a user-service token to /admin/api/v1/* and vice-versa.
func TestAuthenticate_NamespaceBinding(t *testing.T) {
	auth := fixtureAuth(t)
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	cases := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{"user-on-user-OK", "/user/api/v1/keys", "user-token", http.StatusAccepted},
		{"billing-on-billing-OK", "/billing/api/v1/key-balance/x", "billing-token", http.StatusAccepted},
		{"admin-on-admin-OK", "/admin/api/v1/providers/x", "admin-token", http.StatusAccepted},
		{"sre-on-sre-OK", "/sre/api/v1/replay", "sre-token", http.StatusAccepted},

		{"user-on-admin-403", "/admin/api/v1/providers/x", "user-token", http.StatusForbidden},
		{"billing-on-user-403", "/user/api/v1/keys", "billing-token", http.StatusForbidden},
		{"admin-on-sre-403", "/sre/api/v1/replay", "admin-token", http.StatusForbidden},
		{"sre-on-billing-403", "/billing/api/v1/key-balance/x", "sre-token", http.StatusForbidden},

		{"unknown-token-401", "/user/api/v1/keys", "garbage", http.StatusUnauthorized},
		{"no-header-401", "/user/api/v1/keys", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", c.path, strings.NewReader("{}"))
			if c.token != "" {
				req.Header.Set("Authorization", "Bearer "+c.token)
			}
			rec := httptest.NewRecorder()
			auth.Authenticate(downstream).ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Errorf("path=%s token=%s: got %d, want %d (body=%s)",
					c.path, c.token, rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestAuthenticate_RotationArraySupport asserts a service can have two
// concurrently-valid tokens, which is the whole point of the cfg's
// comma-separated array shape (rotation).
func TestAuthenticate_RotationArraySupport(t *testing.T) {
	auth, err := NewTokenAuth(map[string]config.Caller{
		"user-service": {Namespace: "user", Tokens: []string{"old-token", "new-token"}},
	})
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	for _, tok := range []string{"old-token", "new-token"} {
		req := httptest.NewRequest("POST", "/user/api/v1/keys", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		auth.Authenticate(downstream).ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Errorf("token %q: got %d, want 202 (rotation array should accept both)", tok, rec.Code)
		}
	}
}

// TestAuthenticate_StampsRequestID_AndEchoesHeader pins the 2026-05-21
// contract: every authenticated request gets a server-minted UUID v7
// request_id stamped on ctx (publisher.CallerInfo.RequestID) AND echoed
// back in the X-Request-Id response header. Auth failures still get a
// response header? No — auth failures terminate BEFORE the mint step,
// so they DON'T carry a request_id. That's by design: minting is a
// post-auth contract (every successful request has an id; failed auth
// is a separate axis).
func TestAuthenticate_StampsRequestID_AndEchoesHeader(t *testing.T) {
	auth := fixtureAuth(t)

	var stampedRequestID string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stampedRequestID = publisher.CallerFromContext(r.Context()).RequestID
		w.WriteHeader(http.StatusAccepted)
	})

	req := httptest.NewRequest("POST", "/user/api/v1/keys", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()
	auth.Authenticate(downstream).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rec.Code)
	}
	hdr := rec.Header().Get("X-Request-Id")
	if hdr == "" {
		t.Fatal("X-Request-Id response header must be set on successful auth")
	}
	if stampedRequestID == "" {
		t.Fatal("CallerInfo.RequestID must be stamped on ctx")
	}
	if hdr != stampedRequestID {
		t.Errorf("ctx RequestID (%q) must match X-Request-Id header (%q)", stampedRequestID, hdr)
	}

	// UUID v7 shape: 36 chars with 4 dashes, version digit 7 in the 15th
	// position (0-indexed: idx 14). RFC 9562 §5.7.
	if len(hdr) != 36 {
		t.Errorf("UUID v7 must be 36 chars, got %d (%q)", len(hdr), hdr)
	}
	if hdr[14] != '7' {
		t.Errorf("UUID v7 version digit (idx 14) must be '7', got %q (full=%q)", hdr[14:15], hdr)
	}
}

// TestAuthenticate_RequestID_NeverFromClient pins the security contract:
// even if the client supplies its own X-Request-Id header, publisher
// ignores it and mints fresh. (The client's id COULD live under
// CallerInfo.TraceID — see X-Request-ID handling — but never under the
// audit-grade RequestID. This prevents a malicious caller from forging
// audit-trail correlations.)
func TestAuthenticate_RequestID_NeverFromClient(t *testing.T) {
	auth := fixtureAuth(t)

	var stampedRequestID string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stampedRequestID = publisher.CallerFromContext(r.Context()).RequestID
		w.WriteHeader(http.StatusAccepted)
	})

	clientSupplied := "00000000-0000-0000-0000-000000000000"
	req := httptest.NewRequest("POST", "/user/api/v1/keys", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("X-Request-Id", clientSupplied)
	rec := httptest.NewRecorder()
	auth.Authenticate(downstream).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rec.Code)
	}
	if stampedRequestID == clientSupplied {
		t.Fatalf("client-supplied X-Request-Id (%q) must NOT be used as audit RequestID; publisher must mint fresh", clientSupplied)
	}
	if rec.Header().Get("X-Request-Id") == clientSupplied {
		t.Fatalf("response X-Request-Id must be publisher-minted, not echo of client's")
	}
}

// TestAuthenticate_UniqueRequestIDPerCall: minted ids must differ even
// on identical back-to-back requests.
func TestAuthenticate_UniqueRequestIDPerCall(t *testing.T) {
	auth := fixtureAuth(t)

	var got []string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, publisher.CallerFromContext(r.Context()).RequestID)
		w.WriteHeader(http.StatusAccepted)
	})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/user/api/v1/keys", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer user-token")
		rec := httptest.NewRecorder()
		auth.Authenticate(downstream).ServeHTTP(rec, req)
	}

	seen := make(map[string]struct{}, len(got))
	for _, id := range got {
		if _, dup := seen[id]; dup {
			t.Errorf("RequestID collision across calls: %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 5 {
		t.Errorf("want 5 unique ids, got %d", len(seen))
	}
}

func TestMaskTokenHead(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "<empty>"},
		{"abc", "abc"},                              // <10 → as-is
		{"abcdefghi", "abcdefghi"},                  // 9 → as-is (boundary)
		{"abcdefghij", "abcdefghij**"},              // 10 → masked (boundary)
		{"pubtok_0123456789abcdef", "pubtok_012**"}, // realistic
	}
	for _, c := range cases {
		if got := maskTokenHead(c.in); got != c.want {
			t.Errorf("maskTokenHead(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNsFromPath(t *testing.T) {
	cases := []struct {
		in     string
		wantNS Namespace
		wantOK bool
	}{
		{"/user/api/v1/keys", NSUser, true},
		{"/billing/api/v1/key-balance/x", NSBilling, true},
		{"/admin/", NSAdmin, true},
		{"/sre", NSSRE, true}, // no trailing slash still resolves
		{"/foobar/api/v1/x", "", false},
		{"/", "", false},
		{"", "", false},
		{"user/api/v1/keys", "", false}, // missing leading slash
		{"/healthz", "", false},
	}
	for _, c := range cases {
		gotNS, gotOK := nsFromPath(c.in)
		if gotNS != c.wantNS || gotOK != c.wantOK {
			t.Errorf("nsFromPath(%q) = (%q,%v), want (%q,%v)", c.in, gotNS, gotOK, c.wantNS, c.wantOK)
		}
	}
}

func TestRenderCallers(t *testing.T) {
	// Fixture has two callers per ns where useful + multi-token rotation
	// in admin to exercise the [m1,m2] listing.
	auth, err := NewTokenAuth(map[string]config.Caller{
		"user-service":    {Namespace: "user", Tokens: []string{"pubtok_user1xxxxxxxx"}},
		"webhook":         {Namespace: "user", Tokens: []string{"pubtok_userwebhookxx"}},
		"billing-service": {Namespace: "billing", Tokens: []string{"pubtok_billing1xxxx"}},
		"admin-service":   {Namespace: "admin", Tokens: []string{"pubtok_adminoldxxxx", "pubtok_adminnewxxxx"}},
		"sre-oncall":      {Namespace: "sre", Tokens: []string{"pubtok_sreoncallxxx"}},
	})
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}

	// Single-ns render: only the user-ns callers, alphabetical by service.
	wantUser := "user/user-service:[pubtok_use**];user/webhook:[pubtok_use**]"
	if got := auth.renderCallers(NSUser, true); got != wantUser {
		t.Errorf("renderCallers(user, true):\n got = %q\nwant = %q", got, wantUser)
	}

	// All-ns render when URL didn't resolve to a known ns: canonical
	// order (user, billing, admin, sre); within each, callers alphabetical.
	wantAll := "user/user-service:[pubtok_use**];user/webhook:[pubtok_use**]" +
		";billing/billing-service:[pubtok_bil**]" +
		";admin/admin-service:[pubtok_adm**,pubtok_adm**]" +
		";sre/sre-oncall:[pubtok_sre**]"
	if got := auth.renderCallers("", false); got != wantAll {
		t.Errorf("renderCallers(\"\", false):\n got = %q\nwant = %q", got, wantAll)
	}

	// Repeat call returns identical output — guards against accidental
	// reliance on map iteration order, which would surface as flake.
	if got1, got2 := auth.renderCallers(NSUser, true), auth.renderCallers(NSUser, true); got1 != got2 {
		t.Errorf("renderCallers not deterministic across calls: %q vs %q", got1, got2)
	}
}

func TestNewTokenAuth_Validation(t *testing.T) {
	cases := []struct {
		name    string
		callers map[string]config.Caller
		wantErr string
	}{
		{
			name:    "unknown namespace",
			callers: map[string]config.Caller{"svc": {Namespace: "bogus", Tokens: []string{"t1"}}},
			wantErr: "unknown namespace",
		},
		{
			name:    "empty token list",
			callers: map[string]config.Caller{"svc": {Namespace: "user"}},
			wantErr: "empty token list",
		},
		{
			name:    "empty bearer",
			callers: map[string]config.Caller{"svc": {Namespace: "user", Tokens: []string{""}}},
			wantErr: "empty bearer",
		},
		{
			name: "bearer collision",
			callers: map[string]config.Caller{
				"a": {Namespace: "user", Tokens: []string{"shared"}},
				"b": {Namespace: "billing", Tokens: []string{"shared"}},
			},
			wantErr: "bearer collision",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTokenAuth(c.callers)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}
