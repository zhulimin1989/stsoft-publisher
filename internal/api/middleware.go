// Package api hosts the HTTP layer: middleware (auth / namespace / idempotency)
// and handler groups for the 5 namespaces defined in publisher-api-reference.md
// §1.1 (user / billing / admin / sre / public).
//
// All handlers funnel into the Publisher interface (see internal/publisher).
// The middleware is responsible for:
//
//  1. Parsing and validating the `Authorization: Bearer <token>` header,
//     mapping the token to a configured caller service identity.
//  2. Enforcing namespace-token binding (a user-service token must not
//     reach /admin/api/v1/*) — the binding is loaded from the cfg file's
//     [namespaces] section at startup.
//  3. Stamping CallerInfo onto ctx so the Publisher impl can record
//     caller_service / caller_trace_id / dedup_key on outbox rows.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/config"
	"github.com/tokenplatform/publisher/internal/metrics"
	"github.com/tokenplatform/publisher/internal/publisher"
)

// Namespace describes the URL prefix a caller is bound to.
type Namespace string

const (
	NSUser    Namespace = "user"
	NSBilling Namespace = "billing"
	NSAdmin   Namespace = "admin"
	NSSRE     Namespace = "sre"
)

// TokenAuth is the shared-secret check publisher uses for internal
// service-to-service calls. Two gates must pass:
//
//  1. Bearer token must resolve to a configured caller service identity.
//  2. URL prefix must match the service identity's configured namespace.
//
// Both fields live together per service in the cfg's [caller:<service>]
// sub-sections (or the CALLERS_JSON env override):
//
//	[caller:user-service]
//	namespace = user
//	tokens    = tok_a, tok_b
//
// Every auth event (success OR failure) is emitted as a structured slog
// line so downstream SIEM (ELK / Loki / Datadog) can run anomaly
// detection — see logAuthFail. On failure paths the token itself is never
// logged; branches that received a bearer record only an irreversible short
// fingerprint. The success path retains the original "never log tokens"
// stance to bound the blast radius of log exfiltration.
type TokenAuth struct {
	// lookups is the flattened bearer → resolved(service, namespace) map
	// built at startup. Keeping a single map keeps the hot path one O(1)
	// access; rotation cost lives at config-reload time, not per-request.
	lookups map[string]resolvedToken

	// byNamespace is the reverse index used by auth_fail logs to dump
	// configured callers (head-masked) of the URL's namespace, so an
	// operator debugging "unknown_token" can eyeball-compare what was
	// sent against what's configured. Built once at startup; service
	// slice within each ns is alphabetically sorted for stable output.
	byNamespace map[Namespace][]CallerView
}

type resolvedToken struct {
	Service   string
	Namespace Namespace
}

// CallerView is the auth-fail-log shape of a configured caller: the
// service identity plus its tokens already head-masked at startup
// (maskTokenHead). NEVER carries raw tokens — these structs land in
// slog output so any unmasked value would leak.
type CallerView struct {
	Service      string
	MaskedTokens []string
}

// validNamespaces gates what [namespaces] section values are accepted.
// Must match the URL prefixes registered in server.go — adding a new
// namespace requires registering handlers AND adding the const here.
var validNamespaces = map[string]Namespace{
	"user":    NSUser,
	"billing": NSBilling,
	"admin":   NSAdmin,
	"sre":     NSSRE,
}

// NewTokenAuth flattens the per-service Caller entries into the
// bearer-keyed lookup the middleware uses on every request. Fails fast at
// startup if:
//   - a caller's namespace is not one of user/billing/admin/sre
//   - a caller declares no tokens (operator typo / empty value)
//   - the same bearer is reused across two services
func NewTokenAuth(callers map[string]config.Caller) (*TokenAuth, error) {
	// Iterate in alphabetical service order so byNamespace slices end up
	// stably ordered (map range is randomized in Go), giving deterministic
	// auth_fail log lines that downstream alerting can string-match.
	services := make([]string, 0, len(callers))
	for s := range callers {
		services = append(services, s)
	}
	sort.Strings(services)

	lookups := make(map[string]resolvedToken)
	byNamespace := make(map[Namespace][]CallerView)
	for _, service := range services {
		c := callers[service]
		ns, ok := validNamespaces[c.Namespace]
		if !ok {
			return nil, fmt.Errorf("auth: service %q has unknown namespace %q (expected user/billing/admin/sre)", service, c.Namespace)
		}
		if len(c.Tokens) == 0 {
			return nil, fmt.Errorf("auth: service %q has empty token list", service)
		}
		masks := make([]string, len(c.Tokens))
		for i, b := range c.Tokens {
			if b == "" {
				return nil, fmt.Errorf("auth: service %q has an empty bearer in its token list", service)
			}
			if prev, dup := lookups[b]; dup {
				return nil, fmt.Errorf("auth: bearer collision between services %q and %q", prev.Service, service)
			}
			lookups[b] = resolvedToken{Service: service, Namespace: ns}
			masks[i] = maskTokenHead(b)
		}
		byNamespace[ns] = append(byNamespace[ns], CallerView{
			Service:      service,
			MaskedTokens: masks,
		})
	}
	return &TokenAuth{lookups: lookups, byNamespace: byNamespace}, nil
}

// Authenticate is the 2-layer auth middleware. The chain enforces (in
// order, fail-fast):
//
//  1. Bearer token presence + resolve to a configured service identity. 401 miss.
//  2. URL prefix matches the service identity's configured namespace. 403 mismatch.
//
// Every auth event is emitted as a structured slog line so downstream
// SIEM (ELK / Loki / Datadog) can run anomaly detection on auth_fail
// rate, unusual remote_addr, etc.
//
// Incoming-token policy on failure paths (debugging > strict secrecy):
//   - missing_bearer:  no `token_*` field — nothing was sent
//   - unknown_token:   `token_fingerprint` — irreversible short sha256 id
//   - cross_namespace: `token_fingerprint` — irreversible short sha256 id
//   - auth_ok:         no token field; the high-volume success path
//     retains the original "never log" stance
//
// Configured-side dump on every auth_fail:
//
//	`expected_callers` — head-masked listing (maskTokenHead: first-10
//	plus "**") of OUR callers so operators can eyeball-compare against
//	what was sent. Format: "<ns>/<service>:[mask1,mask2];...". URL path
//	in a known namespace ⇒ only that ns's callers; otherwise (defensive)
//	all 4 ns in canonical order. Our tokens are real credentials so the
//	mask is tighter than maskToken (no trailing chars revealed).
//
// /healthz is mounted outside the middleware in server.go.
func (a *TokenAuth) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pre-compute the expected_callers field used by every auth_fail
		// branch below. Path-derived ns drives whether we dump a single
		// ns's callers or all 4. Empty string ⇒ caller config is empty
		// (operator misconfig) — we still let the field be omitted in
		// that degenerate case to avoid noise.
		nsFromURL, nsKnown := nsFromPath(r.URL.Path)
		expectedCallers := a.renderCallers(nsFromURL, nsKnown)
		nsExtras := func(rest ...any) []any {
			out := make([]any, 0, 2+len(rest))
			if expectedCallers != "" {
				out = append(out, "expected_callers", expectedCallers)
			}
			out = append(out, rest...)
			return out
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			metrics.AuthFail("missing_bearer")
			a.logAuthFail("missing_bearer", "", r, nsExtras()...)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", nil)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		info, ok := a.lookups[token]
		if !ok {
			metrics.AuthFail("unknown_token")
			a.logAuthFail("unknown_token", "", r, nsExtras("token_fingerprint", tokenFingerprint(token))...)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unknown token", nil)
			return
		}
		service := info.Service
		wantPrefix := "/" + string(info.Namespace) + "/api/v1/"
		if !strings.HasPrefix(r.URL.Path, wantPrefix) {
			metrics.AuthFail("cross_namespace")
			a.logAuthFail("cross_namespace", service, r, nsExtras("token_fingerprint", tokenFingerprint(token))...)
			writeError(w, http.StatusForbidden, "FORBIDDEN",
				"caller's service identity does not match the endpoint namespace",
				map[string]any{"expected_prefix": wantPrefix, "got_service": service})
			return
		}
		metrics.AuthOK(service)
		// Mint a per-request server-side UUID v7. NEVER trust a client-
		// supplied X-Request-Id for this field — caller's X-Request-ID
		// stays under TraceID (best-effort correlation). RequestID is
		// the publisher's source-of-truth audit handle that lands on
		// every outbox row and echoes back in the response header so
		// caller logs can pivot to publisher audit. uuid.NewV7 is time-
		// ordered (Unix millis prefix) so PG btree indices on
		// caller_request_id stay locality-friendly.
		rid, err := uuid.NewV7()
		if err != nil {
			// Reading the random source can in principle fail on
			// catastrophic platform issues; surface as 500 rather than
			// quietly serving with an empty id — the contract is "every
			// authenticated request has an id".
			slog.Error("publisher request_id mint failed",
				"event", "request_id_mint_fail",
				"error", err,
				"service", service,
				"path", r.URL.Path,
			)
			writeError(w, http.StatusInternalServerError, "REQUEST_ID_MINT_FAILED",
				"failed to generate request id", nil)
			return
		}
		requestID := rid.String()
		// Set header BEFORE next.ServeHTTP so even handler-written 4xx /
		// 5xx error bodies carry it (header is part of the response
		// state machine — once WriteHeader/Write fires, Header() is
		// snapshot already).
		w.Header().Set("X-Request-Id", requestID)
		slog.Debug("publisher auth ok",
			"event", "auth_ok",
			"service", service,
			"remote_addr", r.RemoteAddr,
			"method", r.Method,
			"path", r.URL.Path,
			"trace_id", r.Header.Get("X-Request-ID"),
			"request_id", requestID,
		)
		caller := publisher.CallerInfo{
			Service:       service,
			TraceID:       r.Header.Get("X-Request-ID"),
			IdempotencyID: r.Header.Get("Idempotency-Key"),
			RequestID:     requestID,
			// Bearer token prefix as user-account attribution. Token is
			// already a server-minted unguessable secret (see
			// cmd/publisher-token); 10 chars is enough to deduplicate
			// distinct actors without leaking the full token (forge
			// attacks still need full token to pass Authenticate).
			// Lands on every outbox row this request produces.
			ActorID: bearerTokenActorID(token),
		}
		ctx := publisher.WithCaller(r.Context(), caller)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *TokenAuth) logAuthFail(reason, service string, r *http.Request, extra ...any) {
	args := []any{
		"event", "auth_fail",
		"reason", reason,
		"service", service,
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
		"trace_id", r.Header.Get("X-Request-ID"),
		"user_agent", r.UserAgent(),
	}
	args = append(args, extra...)
	slog.Warn("publisher auth fail", args...)
}

// bearerTokenActorID returns the first 10 chars of a bearer token, used as
// the user-account attribution stamp on every outbox row (CallerInfo.ActorID
// → publisher_outbox.actor_id). Publisher tokens are server-minted
// unguessable secrets (see cmd/publisher-token), so the 10-char prefix
// uniquely identifies the actor without exposing enough to forge auth (a
// forge attack requires the full token to pass Authenticate's lookup).
//
//   - len(s) == 0   → ""  (no Bearer prefix — defensive; should never hit
//     this path since Authenticate already rejected)
//   - len(s) <= 10  → s as-is (caller's malformed token; just store what
//     we got — never returned to caller in any
//     response, only persisted for audit)
//   - len(s) > 10   → s[:10]
//
// Switching to a real user_id claim later is a non-breaking column-level
// change (actor_id TEXT stays the same shape).
func bearerTokenActorID(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10]
}

// maskTokenHead is the tighter mask used for OUR configured tokens
// emitted in `expected_callers`. Real credentials so we never expose
// trailing chars: just first-10 + "**" is enough for an operator to
// match against the one they sent.
//
//   - len(s) == 0  → "<empty>"
//   - len(s) < 10  → s as-is (clearly a misconfigured short value;
//     debug visibility beats hiding what isn't secret)
//   - len(s) >= 10 → s[:10] + "**"
func maskTokenHead(s string) string {
	if len(s) == 0 {
		return "<empty>"
	}
	if len(s) < 10 {
		return s
	}
	return s[:10] + "**"
}

func tokenFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("sha256:%x(len=%d)", sum[:4], len(s))
}

// nsFromPath extracts the first URL-path segment and resolves it to one
// of the 4 known namespaces. Returns ("", false) on anything else (root,
// empty, healthz, unknown prefix). Defensive: server.go currently only
// routes /<ns>/* through Authenticate, but this middleware should not
// depend on that mux layout for correctness.
func nsFromPath(p string) (Namespace, bool) {
	if len(p) < 2 || p[0] != '/' {
		return "", false
	}
	seg := p[1:]
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	ns, ok := validNamespaces[seg]
	return ns, ok
}

// canonicalNSOrder is the stable enumeration order used when dumping
// callers across all 4 namespaces (the "unknown URL ns" branch). Order
// matches the §1.1 namespace listing in publisher-api-reference.md.
var canonicalNSOrder = []Namespace{NSUser, NSBilling, NSAdmin, NSSRE}

// ConfiguredCallers returns the same `<ns>/<service>:[mask1,mask2];...`
// listing the auth-fail logs emit as `expected_callers`, but for all 4
// namespaces. Intended as a one-shot startup log so operators can
// verify CALLERS_JSON / [caller:*] really applied — the historical
// silent-failure path otherwise leaves "did my config land?" invisible
// until requests start 401-ing.
func (a *TokenAuth) ConfiguredCallers() string {
	return a.renderCallers("", false)
}

// renderCallers produces the `expected_callers` slog field value.
//
// only / hasNS contract:
//   - hasNS=true:  dump callers of `only` ns ONLY
//   - hasNS=false: dump callers of all 4 ns in canonicalNSOrder
//
// Format: "<ns>/<service>:[mask1,mask2];<ns>/<service>:[mask3]". Always
// namespaced so parsers don't need separate handling for the two cases.
// Empty result when there is nothing to dump (no callers configured).
func (a *TokenAuth) renderCallers(only Namespace, hasNS bool) string {
	var sb strings.Builder
	first := true
	emitNS := func(ns Namespace) {
		for _, v := range a.byNamespace[ns] {
			if !first {
				sb.WriteByte(';')
			}
			first = false
			sb.WriteString(string(ns))
			sb.WriteByte('/')
			sb.WriteString(v.Service)
			sb.WriteString(":[")
			for i, m := range v.MaskedTokens {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(m)
			}
			sb.WriteByte(']')
		}
	}
	if hasNS {
		emitNS(only)
	} else {
		for _, ns := range canonicalNSOrder {
			emitNS(ns)
		}
	}
	return sb.String()
}

// writeAccepted writes the 202 response envelope per publisher-api-reference.md §2.3.
func writeAccepted(w http.ResponseWriter, batchID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"batch_id": batchID,
		"status":   "accepted",
	})
}

// writeError is the central error response builder. Maps code → http
// status using the table in publisher-api-reference.md §8. details may
// be nil.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if details != nil {
		body["error"].(map[string]any)["details"] = details
	}
	_ = json.NewEncoder(w).Encode(body)
}

// mapPublisherError translates a publisher-layer error into a 4xx/5xx
// response. Any error that wraps publisher.ErrInvalidArgument becomes
// 400 INVALID_ARGUMENT.
func mapPublisherError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, publisher.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
	default:
		// Outbox store unavailable / unknown — surface as 500 with the
		// underlying error message stripped of internals.
		writeError(w, http.StatusInternalServerError, "OUTBOX_UNAVAILABLE", "publisher persistence layer error", nil)
	}
}

// decodeJSON enforces Content-Type and unmarshals into dst.
//
// 2026-05-18 (local dev unblock): DisallowUnknownFields temporarily
// disabled — control-server's publisherclient struct carries internal
// fields (revision / account_id / per-binding flags) that leak onto the
// wire as control-server hasn't pruned them. Strict decode causes 400
// on every admin write and blocks the entire startup seed.
// TODO: re-enable DisallowUnknownFields after a dedicated PR cleans
// control-server's publisherclient package to stop serializing
// publisher-internal fields. Tracked separately so the strictness
// regression here doesn't get forgotten.
func decodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return ctErr
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

var ctErr = errors.New("Content-Type must be application/json")

// Stamp the caller info on ctx — re-export of publisher.WithCaller for the
// tests under api/ that bypass the middleware.
func StampCaller(ctx context.Context, info publisher.CallerInfo) context.Context {
	return publisher.WithCaller(ctx, info)
}
