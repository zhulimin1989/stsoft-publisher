package api

import (
	"net/http"

	"github.com/tokenplatform/publisher/internal/metrics"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/refunddrain"
)

// Server wires routes to handlers. Constructed once per process, passed to
// http.Server.Handler.
type Server struct {
	pub                    publisher.Publisher
	auth                   *TokenAuth
	readyChecks            []ReadinessCheck
	keyAdmissionStrictMode bool
	refundDrains           refunddrain.Service
}

// NewServer wires routes to a Publisher impl plus a TokenAuth middleware.
func NewServer(pub publisher.Publisher, auth *TokenAuth) *Server {
	return &Server{pub: pub, auth: auth}
}

// SetKeyAdmissionStrictMode flips the legacy compatibility surface into the
// explicit decision_id + decision_version contract. It is wired from config at
// startup so rollout can be staged Billing-first without changing endpoints.
func (s *Server) SetKeyAdmissionStrictMode(strict bool) {
	s.keyAdmissionStrictMode = strict
}

func (s *Server) SetRefundDrainCoordinator(coordinator refunddrain.Service) {
	s.refundDrains = coordinator
}

// Handler builds the net/http ServeMux with all service endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.HandleHealthz)
	mux.HandleFunc("GET /livez", s.HandleLivez)
	mux.HandleFunc("GET /readyz", s.HandleReadyz)
	mux.Handle("GET /metrics", metrics.Handler())

	authed := http.NewServeMux()
	registerUserRoutes(authed, s)
	registerBillingRoutes(authed, s)
	registerAdminRoutes(authed, s)
	registerSRERoutes(authed, s)

	// Middleware stack(outermost → innermost):
	//   Authenticate → accessLog → recoverPanic → limitRequestBody → [dumpRequest] → authed
	//
	// - Authenticate: mints request_id + populates ctx.CallerInfo
	// - accessLog: emits one Info line per request with status + duration
	// - recoverPanic: turns handler panics into 500 INTERNAL_PANIC instead
	//   of crashing the process. In a multi-instance HA deployment the
	//   LB routes around the panicked pod for 5-15s during restart;
	//   recovery contains the blast radius to ONE in-flight request
	//   instead of the entire pod's request queue. MUST sit outside
	//   the handler tree. (2026-05-26: comment updated to reflect
	//   multi-instance reality — outbox.ClaimDue's FOR UPDATE SKIP
	//   LOCKED has always made publisher multi-instance-safe.)
	// - limitRequestBody: after auth, before dump/decode, caps request
	//   bodies at REQUEST_BODY_MAX_BYTES (default 2 MiB) and rejects
	//   oversized payloads with 413.
	// - dumpRequest: opt-in debug log (REQUEST_DUMP=1/true/on/yes) for
	//   method/path/redacted-body/caller. Disabled by default.
	inner := http.Handler(authed)
	if requestDumpEnabledFromEnv() {
		inner = dumpRequest(inner)
	}
	logged := accessLog(recoverPanic(limitRequestBody(inner, requestBodyMaxBytesFromEnv())))
	mux.Handle("/user/", s.auth.Authenticate(logged))
	mux.Handle("/billing/", s.auth.Authenticate(logged))
	mux.Handle("/admin/", s.auth.Authenticate(logged))
	mux.Handle("/sre/", s.auth.Authenticate(logged))
	return mux
}

func registerUserRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("PUT /user/api/v1/keys", s.HandleUserPutKey)
	mux.HandleFunc("POST /user/api/v1/keys/bulk", s.HandleUserPutKeyBulk)
	mux.HandleFunc("DELETE /user/api/v1/keys/{key_hash}", s.HandleUserDeleteKey)
	mux.HandleFunc("DELETE /user/api/v1/keys/bulk", s.HandleUserDeleteKeyBulk)
	mux.HandleFunc("POST /user/api/v1/revoked-keys", s.HandleUserAddRevoked)
	mux.HandleFunc("POST /user/api/v1/revoked-keys/bulk", s.HandleUserAddRevokedBulk)
	mux.HandleFunc("DELETE /user/api/v1/revoked-keys/{key_hash}", s.HandleUserRemoveRevoked)
	mux.HandleFunc("DELETE /user/api/v1/revoked-keys/bulk", s.HandleUserRemoveRevokedBulk)
	mux.HandleFunc("PUT /user/api/v1/revoked-keys", s.HandleUserReplaceRevoked)
}

func registerBillingRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("PUT /billing/api/v1/key-activation/{key_id}", s.HandleBillingActivateKey)
	mux.HandleFunc("PUT /billing/api/v1/key-balance/{key_id}", s.HandleBillingSetBalance)
	mux.HandleFunc("PUT /billing/api/v1/key-balance/bulk", s.HandleBillingSetBalanceBulk)
	mux.HandleFunc("DELETE /billing/api/v1/key-balance/{key_id}", s.HandleBillingDeleteBalance)
	mux.HandleFunc("GET /billing/api/v1/key-admission-deliveries/{batch_id}", s.HandleBillingGetAdmissionDelivery)
	mux.HandleFunc("PUT /billing/api/v1/refund-drains/{drain_id}", s.HandleBillingEstablishRefundDrain)
	mux.HandleFunc("GET /billing/api/v1/refund-drains/{drain_id}", s.HandleBillingGetRefundDrain)
	mux.HandleFunc("DELETE /billing/api/v1/refund-drains/{drain_id}", s.HandleBillingReleaseRefundDrain)
}

func registerAdminRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /admin/api/v1/providers/compile-preview", s.HandleAdminCompileProviderPreview)
	mux.HandleFunc("POST /admin/api/v1/providers:from-intent", s.HandleAdminPublishIntent)
	mux.HandleFunc("PUT /admin/api/v1/providers/{provider_id}", s.HandleAdminPutProvider)
	mux.HandleFunc("DELETE /admin/api/v1/providers/{provider_id}", s.HandleAdminDeleteProvider)
	mux.HandleFunc("PUT /admin/api/v1/models/{model_id}/catalog", s.HandleAdminPutModelCatalog)
	mux.HandleFunc("POST /admin/api/v1/models/catalog/bulk", s.HandleAdminPutModelCatalogBulk)
	mux.HandleFunc("DELETE /admin/api/v1/models/{model_id}/catalog", s.HandleAdminDeleteModelCatalog)
	mux.HandleFunc("PUT /admin/api/v1/models/{model_id}/route", s.HandleAdminPutModelRoute)
	mux.HandleFunc("POST /admin/api/v1/models/routes/bulk", s.HandleAdminPutModelRouteBulk)
	mux.HandleFunc("DELETE /admin/api/v1/models/{model_id}/route", s.HandleAdminDeleteModelRoute)
	mux.HandleFunc("PUT /admin/api/v1/key-pools/{provider_id}", s.HandleAdminPutKeyPool)
	mux.HandleFunc("POST /admin/api/v1/key-pools/bulk", s.HandleAdminPutKeyPoolBulk)
	mux.HandleFunc("DELETE /admin/api/v1/key-pools/{provider_id}", s.HandleAdminDeleteKeyPool)
	mux.HandleFunc("PUT /admin/api/v1/key-pools/{provider_id}/keys/{key_id}", s.HandleAdminPutKeyPoolEntry)
	mux.HandleFunc("POST /admin/api/v1/key-pools/keys/bulk", s.HandleAdminPutKeyPoolEntryBulk)
	mux.HandleFunc("DELETE /admin/api/v1/key-pools/{provider_id}/keys/{key_id}", s.HandleAdminDeleteKeyPoolEntry)
	mux.HandleFunc("DELETE /admin/api/v1/key-pools/keys/bulk", s.HandleAdminDeleteKeyPoolEntryBulk)
	mux.HandleFunc("PUT /admin/api/v1/platform-config", s.HandleAdminPutPlatformConfig)
	mux.HandleFunc("PUT /admin/api/v1/product-policies/{product_id}", s.HandleAdminPutProductRuntimePolicy)
	mux.HandleFunc("DELETE /admin/api/v1/product-policies/{product_id}", s.HandleAdminDeleteProductRuntimePolicy)
}

func registerSRERoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("POST /sre/api/v1/keys/{key_hash}/disable", s.HandleSREDisableKey)
	mux.HandleFunc("POST /sre/api/v1/keys/{key_hash}/enable", s.HandleSREEnableKey)
	mux.HandleFunc("POST /sre/api/v1/keys/bulk/disable", s.HandleSREDisableKeyBulk)
	mux.HandleFunc("POST /sre/api/v1/keys/bulk/enable", s.HandleSREEnableKeyBulk)
	mux.HandleFunc("POST /sre/api/v1/blocked-ips", s.HandleSREAddIP)
	mux.HandleFunc("POST /sre/api/v1/blocked-ips/bulk", s.HandleSREAddIPBulk)
	mux.HandleFunc("DELETE /sre/api/v1/blocked-ips/{ip}", s.HandleSRERemoveIP)
	mux.HandleFunc("DELETE /sre/api/v1/blocked-ips/bulk", s.HandleSRERemoveIPBulk)
	mux.HandleFunc("PUT /sre/api/v1/blocked-ips", s.HandleSREReplaceIPs)
	mux.HandleFunc("PUT /sre/api/v1/overrides", s.HandleSREPutOverride)
	mux.HandleFunc("POST /sre/api/v1/overrides/bulk", s.HandleSREPutOverrideBulk)
	mux.HandleFunc("DELETE /sre/api/v1/overrides", s.HandleSREDeleteOverride)
	mux.HandleFunc("DELETE /sre/api/v1/overrides/bulk", s.HandleSREDeleteOverrideBulk)
	mux.HandleFunc("POST /sre/api/v1/replay", s.HandleSREReplay)
}
