package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

// 2026-05-22 — User reported "admin 创建新 provider 导致 publisher 崩了"
// then said the core issue is "publisher 不健壮 / 所有接口参数处理没考虑
// 边界,比如空,比如数组空等等".
//
// This file boundary-fuzzes every admin endpoint with degenerate inputs
// and pins:
//   1. No panic propagates out (recoverPanic middleware catches anything)
//   2. Every degenerate input returns a clean 4xx (NEVER a 5xx without
//      body, NEVER an unhandled panic)
//   3. Provider routing schema edges:
//      - legacy api_type/api_types presence → 400
//      - missing protocol_bindings → 400
//      - valid protocol_bindings-only payload → 202

type stubPublisher struct {
	// publish funcs return ErrInvalidArgument by default so tests don't
	// have to wire each one; specific tests override via the *Func fields.
	publishProviderFunc             func(ctx context.Context, id string, info *types.ProviderInfo) (types.BatchID, error)
	publishModelCatalogFunc         func(ctx context.Context, id string, e *types.ModelCatalogEntry) (types.BatchID, error)
	publishModelRouteFunc           func(ctx context.Context, id string, r *types.RouteConfig) (types.BatchID, error)
	publishKeyPoolFunc              func(ctx context.Context, id string, p *types.KeyPoolConfig) (types.BatchID, error)
	publishKeyPoolEntryFunc         func(ctx context.Context, p string, e *types.KeyPoolEntry) (types.BatchID, error)
	publishKeyPoolEntryBulkFunc     func(ctx context.Context, items []types.KeyPoolEntryItem) (types.BatchID, error)
	publishPlatformFunc             func(ctx context.Context, c *types.PlatformConfig) (types.BatchID, error)
	publishKeyFunc                  func(ctx context.Context, m *types.KeyMeta) (types.BatchID, error)
	activateKeyFunc                 func(ctx context.Context, m *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error)
	publishProductRuntimePolicyFunc func(ctx context.Context, id string, policy *types.ProductRuntimePolicy) (types.BatchID, error)
	publishIntentFunc               func(ctx context.Context, req publisher.IntentPublishRequest) (publisher.IntentPublishResult, error)
	getAdmissionDeliveryFunc        func(ctx context.Context, batchID types.BatchID) (publisher.AdmissionDeliveryReceipt, error)
	setKeyAdmissionBulkFunc         func(ctx context.Context, items []types.KeyAdmissionItem) (types.BatchID, error)
}

func (s *stubPublisher) acceptAlways() types.BatchID { return uuid.New() }

// errInvalidStub wraps msg with publisher.ErrInvalidArgument so the
// HTTP layer maps it to 400 (mirrors real publisher.PublishX behavior).
func errInvalidStub(msg string) error {
	return fmt.Errorf("%w: %s", publisher.ErrInvalidArgument, msg)
}

func (s *stubPublisher) PublishKey(ctx context.Context, m *types.KeyMeta) (types.BatchID, error) {
	if s.publishKeyFunc != nil {
		return s.publishKeyFunc(ctx, m)
	}
	return uuid.Nil, errors.New("not stubbed")
}
func (s *stubPublisher) ActivateKey(ctx context.Context, m *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error) {
	if s.activateKeyFunc != nil {
		return s.activateKeyFunc(ctx, m, hasBalance, reason, decision)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishKeyBulk(_ context.Context, _ []*types.KeyMeta) (types.BatchID, error) {
	return uuid.Nil, errors.New("not stubbed")
}
func (s *stubPublisher) DeleteKey(_ context.Context, _ types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyBulk(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) SetKeyBalance(_ context.Context, _ types.KeyID, _ bool, _ string, _ int64) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) SetKeyBalanceBulk(_ context.Context, _ []types.KeyBalanceItem, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyBalance(_ context.Context, _ types.KeyID) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) SetKeyAdmission(_ context.Context, _ types.KeyID, _ bool, _ string, _ types.KeyAdmissionDecision) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) SetKeyAdmissionBulk(ctx context.Context, items []types.KeyAdmissionItem) (types.BatchID, error) {
	if s.setKeyAdmissionBulkFunc != nil {
		return s.setKeyAdmissionBulkFunc(ctx, items)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyAdmission(_ context.Context, _ types.KeyID, _ types.KeyAdmissionDecision) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) GetKeyAdmissionDelivery(ctx context.Context, batchID types.BatchID) (publisher.AdmissionDeliveryReceipt, error) {
	if s.getAdmissionDeliveryFunc != nil {
		return s.getAdmissionDeliveryFunc(ctx, batchID)
	}
	return publisher.AdmissionDeliveryReceipt{}, publisher.ErrAdmissionDeliveryNotFound
}
func (s *stubPublisher) AddRevokedKey(_ context.Context, _ types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) AddRevokedKeysBulk(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) RemoveRevokedKey(_ context.Context, _ types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) RemoveRevokedKeysBulk(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishRevokedKeys(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DisableKey(_ context.Context, _ types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DisableKeysBulk(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) EnableKey(_ context.Context, _ types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) EnableKeysBulk(_ context.Context, _ []types.KeyHash) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) AddBlockedIP(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) AddBlockedIPsBulk(_ context.Context, _ []string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) RemoveBlockedIP(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) RemoveBlockedIPsBulk(_ context.Context, _ []string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishBlockedIPs(_ context.Context, _ []string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishModelCatalog(ctx context.Context, id string, e *types.ModelCatalogEntry) (types.BatchID, error) {
	if s.publishModelCatalogFunc != nil {
		return s.publishModelCatalogFunc(ctx, id, e)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishModelCatalogBulk(_ context.Context, _ []types.ModelCatalogItem) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteModelCatalog(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishModelRoute(ctx context.Context, id string, r *types.RouteConfig) (types.BatchID, error) {
	if s.publishModelRouteFunc != nil {
		return s.publishModelRouteFunc(ctx, id, r)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishModelRouteBulk(_ context.Context, _ []types.ModelRouteItem) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteModelRoute(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishKeyPool(ctx context.Context, id string, p *types.KeyPoolConfig) (types.BatchID, error) {
	if s.publishKeyPoolFunc != nil {
		return s.publishKeyPoolFunc(ctx, id, p)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishKeyPoolBulk(_ context.Context, _ []types.KeyPoolItem) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyPool(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishKeyPoolEntry(ctx context.Context, p string, e *types.KeyPoolEntry) (types.BatchID, error) {
	if s.publishKeyPoolEntryFunc != nil {
		return s.publishKeyPoolEntryFunc(ctx, p, e)
	}
	return s.acceptAlways(), nil
}

func (s *stubPublisher) PublishKeyPoolEntryBulk(ctx context.Context, items []types.KeyPoolEntryItem) (types.BatchID, error) {
	if s.publishKeyPoolEntryBulkFunc != nil {
		return s.publishKeyPoolEntryBulkFunc(ctx, items)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyPoolEntry(_ context.Context, _ string, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteKeyPoolEntryBulk(_ context.Context, _ []types.KeyPoolEntryDeleteItem) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishProvider(ctx context.Context, id string, info *types.ProviderInfo) (types.BatchID, error) {
	if s.publishProviderFunc != nil {
		return s.publishProviderFunc(ctx, id, info)
	}
	// Mirror real publisher.validateProviderInfo so boundary tests
	// actually exercise the contract handlers rely on.
	if strings.TrimSpace(id) == "" {
		return uuid.Nil, errInvalidStub("provider_id required")
	}
	if info == nil {
		return uuid.Nil, errInvalidStub("provider info required")
	}
	if len(info.ProtocolBindings) == 0 {
		return uuid.Nil, errInvalidStub("protocol_bindings required")
	}
	for i, b := range info.ProtocolBindings {
		if strings.TrimSpace(b.Name) == "" {
			return uuid.Nil, errInvalidStub("protocol_bindings[i].name required")
		}
		if len(b.Accepts) == 0 {
			return uuid.Nil, errInvalidStub("protocol_bindings[i].accepts required")
		}
		if strings.TrimSpace(b.OutboundProtocol) == "" {
			return uuid.Nil, errInvalidStub("protocol_bindings[i].outbound_protocol required")
		}
		if strings.TrimSpace(b.AuthScheme) == "" {
			return uuid.Nil, errInvalidStub("protocol_bindings[i].auth_scheme required")
		}
		_ = i
	}
	switch info.Status {
	case "active", "disabled":
		// allowed
	default:
		return uuid.Nil, errInvalidStub("unknown status")
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteProvider(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishPlatformConfig(ctx context.Context, c *types.PlatformConfig) (types.BatchID, error) {
	if s.publishPlatformFunc != nil {
		return s.publishPlatformFunc(ctx, c)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishProductRuntimePolicy(ctx context.Context, id string, policy *types.ProductRuntimePolicy) (types.BatchID, error) {
	if s.publishProductRuntimePolicyFunc != nil {
		return s.publishProductRuntimePolicyFunc(ctx, id, policy)
	}
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteProductRuntimePolicy(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishProviderOverride(_ context.Context, _ string, _ *types.ProviderOverride) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishProviderOverrideBulk(_ context.Context, _ []types.ProviderOverrideItem) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteProviderOverride(_ context.Context, _ string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) DeleteProviderOverrideBulk(_ context.Context, _ []string) (types.BatchID, error) {
	return s.acceptAlways(), nil
}
func (s *stubPublisher) PublishIntent(ctx context.Context, req publisher.IntentPublishRequest) (publisher.IntentPublishResult, error) {
	if s.publishIntentFunc != nil {
		return s.publishIntentFunc(ctx, req)
	}
	var result publisher.IntentPublishResult
	for i := range req.Providers {
		provider := &req.Providers[i]
		id, err := s.PublishProvider(ctx, provider.ID, provider)
		if err != nil {
			return result, err
		}
		result.ProviderBatches = append(result.ProviderBatches, id)
	}
	for i := range req.KeyPools {
		pool := &req.KeyPools[i]
		id, err := s.PublishKeyPool(ctx, pool.Meta.ProviderID, pool)
		if err != nil {
			return result, err
		}
		result.KeyPoolBatches = append(result.KeyPoolBatches, id)
	}
	for i := range req.ModelCatalog {
		entry := &req.ModelCatalog[i]
		id, err := s.PublishModelCatalog(ctx, entry.ModelID, entry)
		if err != nil {
			return result, err
		}
		result.ModelCatalogBatches = append(result.ModelCatalogBatches, id)
	}
	for i := range req.Routes {
		route := &req.Routes[i]
		id, err := s.PublishModelRoute(ctx, route.ModelID, route)
		if err != nil {
			return result, err
		}
		result.ModelRouteBatches = append(result.ModelRouteBatches, id)
	}
	for i := range req.ProductPolicies {
		policy := &req.ProductPolicies[i]
		id, err := s.PublishProductRuntimePolicy(ctx, policy.ProductID, policy)
		if err != nil {
			return result, err
		}
		result.ProductPolicyBatches = append(result.ProductPolicyBatches, id)
	}
	for i := range req.ProductPolicyDeletes {
		deleted := req.ProductPolicyDeletes[i]
		id, err := s.DeleteProductRuntimePolicy(ctx, deleted.ProductID)
		if err != nil {
			return result, err
		}
		result.ProductPolicyDeleteBatches = append(result.ProductPolicyDeleteBatches, id)
	}
	return result, nil
}
func (s *stubPublisher) Replay(_ context.Context, _ publisher.ReplayRequest) (publisher.ReplayResult, error) {
	return publisher.ReplayResult{}, errors.New("not stubbed")
}

func newAdminBoundaryTestServer(t *testing.T, pub publisher.Publisher) http.Handler {
	t.Helper()
	auth := fixtureAuth(t)
	srv := NewServer(pub, auth)
	return srv.Handler()
}

func adminPut(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func adminPost(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestProviderBoundary_AllInputsReturn4xxOr202_NoPanic 跑 admin provider
// 创建路径的边界用例。每个用例期望:
//   - 永不 panic(panic recovery 兜底也会变 500,任何 5xx 都视为 fail)
//   - 永不返回 5xx 无 body
//   - 返回 400 / 422 / 202 中的一个
func TestProviderBoundary_AllInputsReturn4xxOr202_NoPanic(t *testing.T) {
	pub := &stubPublisher{}
	h := newAdminBoundaryTestServer(t, pub)

	cases := []struct {
		name       string
		body       string
		acceptable []int // allowed status codes
	}{
		{"empty_body", `{}`, []int{400}},
		{"api_types_rejected", `{"api_types":["openai"],"status":"active"}`, []int{400}},
		{"api_type_rejected", `{"api_type":"openai","status":"active"}`, []int{400}},
		{"api_types_null_rejected", `{"api_types":null}`, []int{400}},
		{"protocol_bindings_normal", `{"name":"openai","base_url":"https://api.openai.com","status":"active","protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai","auth_scheme":"bearer"}],"default_protocol_binding":"openai-chat"}`, []int{202}},
		{"unknown_fields_ignored", `{"name":"openai","base_url":"https://api.openai.com","status":"active","protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai","auth_scheme":"bearer"}],"blah":"blah","nested":{"a":1}}`, []int{202}},
		{"weird_status", `{"status":"unknown_value","protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai","auth_scheme":"bearer"}]}`, []int{400}},
		{"id_mismatch", `{"id":"different","status":"active","protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai","auth_scheme":"bearer"}]}`, []int{400}},
		{"id_same_as_path", `{"id":"openai","status":"active","protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai","auth_scheme":"bearer"}]}`, []int{202}},

		// Wire-malformed JSON — must 400 (decodeJSON parses fail)
		{"malformed_json_trailing", `{"protocol_bindings":[{"name":"openai-chat"}]`, []int{400}},
		{"malformed_json_garbage", `not even json`, []int{400}},
		{"empty_string_body", ``, []int{400}},
		{"null_body", `null`, []int{400, 202}}, // null decodes to zero-value struct; validation should still reject
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// stub Publisher accepts whatever validateProviderInfo lets through,
			// so happy-path inputs reach acceptAlways and return 202.
			pub.publishProviderFunc = nil
			w := adminPut(t, h, "/admin/api/v1/providers/openai", c.body)

			gotAcceptable := false
			for _, ok := range c.acceptable {
				if w.Code == ok {
					gotAcceptable = true
					break
				}
			}
			if !gotAcceptable {
				t.Errorf("body=%q: status=%d not in %v; response=%s",
					c.body, w.Code, c.acceptable, w.Body.String())
			}
			// Panic recovery middleware → 500 INTERNAL_PANIC if a panic
			// reaches it. Bug: if a 500 with INTERNAL_PANIC fired, the
			// handler panicked, which violates the robustness contract.
			if strings.Contains(w.Body.String(), "INTERNAL_PANIC") {
				t.Errorf("HANDLER PANICKED on body=%q — recovered but contract violation", c.body)
			}
		})
	}
}

// TestProviderBoundary_EmptyPathID 路径变量为空(理论上 net/http mux 不会
// 触发,但 PathValue 返回 "" 时 handler 应该不崩。
func TestProviderBoundary_EmptyPathID(t *testing.T) {
	pub := &stubPublisher{}
	h := newAdminBoundaryTestServer(t, pub)
	w := adminPut(t, h, "/admin/api/v1/providers/", `{"protocol_bindings":[{"name":"openai-chat","accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],"outbound_protocol":"openai"}],"status":"active"}`)
	// Go's mux refuses to match the empty path segment — 404 is fine.
	// What we care about: NOT a panic.
	if strings.Contains(w.Body.String(), "INTERNAL_PANIC") {
		t.Errorf("empty path id panicked recovery: %s", w.Body.String())
	}
}

func TestProviderBoundary_PreservesBridgeThroughAdminPut(t *testing.T) {
	var captured *types.ProviderInfo
	pub := &stubPublisher{
		publishProviderFunc: func(_ context.Context, id string, info *types.ProviderInfo) (types.BatchID, error) {
			if id != "openai" {
				t.Fatalf("provider id=%q want openai", id)
			}
			captured = info
			return uuid.New(), nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPut(t, h, "/admin/api/v1/providers/openai", `{
		"name": "OpenAI compatible",
		"base_url": "https://api.openai.example",
		"status": "active",
		"protocol_bindings": [{
			"name": "anthropic-to-openai",
			"accepts": [{"protocol": "anthropic", "capabilities": ["chat"], "request_contract": "anthropic_messages"}],
			"outbound_protocol": "openai",
			"path_prefix": "/v1",
			"auth_scheme": "bearer",
			"translator": "standard_to_openai",
			"bridge": "anthropic_to_openai_chat"
		}],
		"default_protocol_binding": "anthropic-to-openai"
	}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("admin put status=%d body=%s", w.Code, w.Body.String())
	}
	if captured == nil || len(captured.ProtocolBindings) != 1 {
		t.Fatalf("publisher did not receive one provider binding: %+v", captured)
	}
	if captured.ProtocolBindings[0].Bridge != "anthropic_to_openai_chat" {
		t.Fatalf("bridge lost through handler: %+v", captured.ProtocolBindings[0])
	}
}

func TestProviderBoundary_PreservesProviderEntitlementAccountRefThroughAdminPut(t *testing.T) {
	const accountRef = "pa_550e8400-e29b-41d4-a716-446655440040"
	var captured *types.ProviderInfo
	pub := &stubPublisher{
		publishProviderFunc: func(_ context.Context, id string, info *types.ProviderInfo) (types.BatchID, error) {
			if id != "entitlement-provider" {
				t.Fatalf("provider id=%q want entitlement-provider", id)
			}
			captured = info
			return uuid.New(), nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPut(t, h, "/admin/api/v1/providers/entitlement-provider", `{
		"name":"Entitlement provider",
		"base_url":"https://api.entitlement.example",
		"status":"active",
		"provider_profile":"openai_compatible",
		"provider_account_ref":"`+accountRef+`",
		"visibility":"dedicated_only",
		"credential_source":"provider_entitlement",
		"allowed_route_kinds":["dedicated_route"],
		"protocol_bindings":[{
			"name":"openai-chat",
			"accepts":[{"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}],
			"outbound_protocol":"openai",
			"auth_scheme":"bearer"
		}]
	}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if captured == nil || captured.CredentialSource != types.ProviderCredentialSourceProviderEntitlement || captured.ProviderAccountRef != accountRef {
		t.Fatalf("admin provider request dropped provider_account_ref: %+v", captured)
	}
}

func TestKeyMetaBoundary_PreservesDedicatedAccountRefThroughUserPut(t *testing.T) {
	const accountRef = "pa_550e8400-e29b-41d4-a716-446655440030"
	var captured *types.KeyMeta
	pub := &stubPublisher{
		publishKeyFunc: func(_ context.Context, meta *types.KeyMeta) (types.BatchID, error) {
			captured = meta
			return uuid.New(), nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)
	req := httptest.NewRequest(http.MethodPut, "/user/api/v1/keys", strings.NewReader(`{
		"key_id":"8f0c57d8-77b5-4a83-a89f-26ebc356d910",
		"key_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"status":"active",
		"expires_at":1893456000,
		"ip_whitelist":[],
		"allowed_models":[],
		"dedicated_upstream":{
			"upstream_key_id":"coding-plan-key-001",
			"provider_account_ref":"`+accountRef+`",
			"ciphertext":"kms:projects/demo/key:ciphertext",
			"ciphertext_version":1,
			"ciphertext_sha256":"sha256:dedicated-ciphertext",
			"kms_key_id":"kms-key-coding",
			"status":"active",
			"expires_at":1893456000,
			"updated_at":"2026-06-15T00:00:00Z",
			"fallback_allowed":false,
			"endpoints":[{"client_protocol":"anthropic","provider_id":"glm-anthropic","protocol_strategy":"passthrough"}]
		}
	}`))
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if captured == nil || captured.DedicatedUpstream == nil || captured.DedicatedUpstream.ProviderAccountRef != accountRef {
		t.Fatalf("user dedicated request dropped provider_account_ref: %+v", captured)
	}
}

func TestKeyPoolEntryAdminRequestsPreserveProviderAccountRef(t *testing.T) {
	const accountRef = "pa_550e8400-e29b-41d4-a716-446655440000"

	t.Run("per-entry", func(t *testing.T) {
		var captured *types.KeyPoolEntry
		pub := &stubPublisher{
			publishKeyPoolEntryFunc: func(_ context.Context, providerID string, entry *types.KeyPoolEntry) (types.BatchID, error) {
				if providerID != "zhipu" {
					t.Fatalf("provider_id=%q want zhipu", providerID)
				}
				captured = entry
				return uuid.New(), nil
			},
		}
		h := newAdminBoundaryTestServer(t, pub)
		w := adminPut(t, h, "/admin/api/v1/key-pools/zhipu/keys/key-primary", `{
			"provider_account_ref":"`+accountRef+`",
			"ciphertext":"vault://providers/zhipu/key-primary",
			"priority":1,
			"weight":100,
			"rpm_limit":500,
			"tpm_limit":100000,
			"status":"enabled"
		}`)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if captured == nil || captured.KeyID != "key-primary" || captured.ProviderAccountRef != accountRef {
			t.Fatalf("admin per-entry request dropped provider_account_ref: %+v", captured)
		}
	})

	t.Run("bulk", func(t *testing.T) {
		var captured []types.KeyPoolEntryItem
		pub := &stubPublisher{
			publishKeyPoolEntryBulkFunc: func(_ context.Context, items []types.KeyPoolEntryItem) (types.BatchID, error) {
				captured = items
				return uuid.New(), nil
			},
		}
		h := newAdminBoundaryTestServer(t, pub)
		w := adminPost(t, h, "/admin/api/v1/key-pools/keys/bulk", `{
			"items":[{
				"provider_id":"zhipu",
				"entry":{
					"key_id":"key-primary",
					"provider_account_ref":"`+accountRef+`",
					"ciphertext":"vault://providers/zhipu/key-primary",
					"priority":1,
					"weight":100,
					"rpm_limit":500,
					"tpm_limit":100000,
					"status":"enabled"
				}
			}]
		}`)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if len(captured) != 1 || captured[0].Entry == nil || captured[0].Entry.ProviderAccountRef != accountRef {
			t.Fatalf("admin bulk request dropped provider_account_ref: %+v", captured)
		}
	})
}

func TestProviderCompilePreview_CompilesProviderProfileWithoutPublishing(t *testing.T) {
	pub := &stubPublisher{}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPost(t, h, "/admin/api/v1/providers/compile-preview", `{
		"id": "gemini-prod",
		"name": "Gemini",
		"base_url": "https://generativelanguage.googleapis.com",
		"status": "active",
		"provider_profile": "gemini_native",
		"client_compatibility": ["gemini_cli"],
		"token_count_strategy": "unsupported"
	}`)

	if w.Code != http.StatusOK {
		t.Fatalf("compile preview status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "batch_id") {
		t.Fatalf("compile preview must not publish outbox batch: %s", w.Body.String())
	}
	for _, want := range []string{"gemini-generate", "protocol_bindings", "default_protocol_binding"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("compile preview response missing %q: %s", want, w.Body.String())
		}
	}
}
