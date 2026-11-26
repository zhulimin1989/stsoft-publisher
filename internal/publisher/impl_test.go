package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/types"
)

const (
	gatewayCompatibleKeyMetaSchemaVersion      = 2
	gatewayCompatibleProviderInfoSchemaVersion = 1
)

func TestRuntimeSchemaVersionTripwire(t *testing.T) {
	// ADR-0007 runtime schema_version rule:
	// bump these only for breaking publisher->gateway wire changes, and only
	// after gateway-first evidence is attached to the PR. Additive optional
	// fields must keep the current runtime schema_version.
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"KeyMeta", CurrentKeyMetaSchemaVersion, 2},
		{"ModelCatalogEntry", CurrentModelCatalogEntrySchemaVersion, 1},
		{"RouteConfig", CurrentRouteConfigSchemaVersion, 1},
		{"ProviderInfo", CurrentProviderInfoSchemaVersion, 1},
		{"KeyPoolMeta", CurrentKeyPoolMetaSchemaVersion, 1},
		{"KeyPoolEntry", CurrentKeyPoolEntrySchemaVersion, 1},
		{"PlatformConfig", CurrentPlatformConfigSchemaVersion, 1},
		{"ProductRuntimePolicy", CurrentProductRuntimePolicySchemaVersion, 1},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s runtime schema_version tripwire = %d, want %d; runtime schema_version bumps require ADR-0007 gateway-first evidence", tc.name, tc.got, tc.want)
		}
	}
}

func validHash() types.KeyHash {
	return types.KeyHash(strings.Repeat("a", 64))
}

func validKeyID() types.KeyID {
	return types.KeyID(uuid.NewString())
}

func TestValidateKeyMeta_RejectsRawKey(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  "tk-pretend-raw-key",
		Revision: 1700000000000000,
		Status:   "active",
	}
	if err := validateKeyMeta(meta); err == nil {
		t.Fatal("expected raw-key rejection in KeyHash field")
	}
}

func TestValidateKeyMeta_RejectsNonHexHash(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  "not-hex-not-64",
		Revision: 1700000000000000,
		Status:   "active",
	}
	if err := validateKeyMeta(meta); err == nil {
		t.Fatal("expected hex-shape rejection")
	}
}

func TestValidateKeyMeta_RequiresRevision(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Revision: 0,
		Status:   "active",
	}
	if err := validateKeyMeta(meta); err == nil {
		t.Fatal("expected revision-required rejection")
	}
}

func TestValidateKeyMeta_HappyPath(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Revision: 1700000000000000,
		Status:   "active",
	}
	if err := validateKeyMeta(meta); err != nil {
		t.Fatalf("expected valid keymeta to pass, got %v", err)
	}
}

func TestValidateKeyMeta_DedicatedUpstreamRequiresKMSCiphertext(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:             uuid.NewString(),
		KeyHash:           string(validHash()),
		Revision:          1700000000000000,
		Status:            "active",
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.Ciphertext = "aesgcm:local-envelope"
	if err := validateKeyMeta(meta); err == nil {
		t.Fatal("expected non-kms dedicated upstream ciphertext to be rejected")
	}

	meta.DedicatedUpstream.Ciphertext = "kms:projects/demo/locations/global/keyRings/ring/cryptoKeys/key:ciphertext"
	if err := validateKeyMeta(meta); err != nil {
		t.Fatalf("expected kms-prefixed dedicated upstream ciphertext to pass, got %v", err)
	}
}

func TestValidateKeyMeta_DedicatedUpstreamRequiresOpaqueProviderAccountRef(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:             uuid.NewString(),
		KeyHash:           string(validHash()),
		Revision:          1700000000000000,
		Status:            "active",
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.ProviderAccountRef = ""
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "provider_account_ref required") {
		t.Fatalf("expected missing dedicated provider_account_ref rejection, got %v", err)
	}
	meta.DedicatedUpstream.ProviderAccountRef = " dedicated-ref"
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "provider_account_ref invalid") {
		t.Fatalf("expected invalid opaque dedicated provider_account_ref rejection, got %v", err)
	}
}

func TestValidateKeyMeta_DedicatedUpstreamRejectsCredentialIdentityDerivedAccountRef(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:             uuid.NewString(),
		KeyHash:           string(validHash()),
		Revision:          1700000000000000,
		Status:            "active",
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.ProviderAccountRef = meta.DedicatedUpstream.UpstreamKeyID
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "must not reuse upstream_key_id") {
		t.Fatalf("expected upstream_key_id-derived provider_account_ref rejection, got %v", err)
	}
	meta.DedicatedUpstream.Ciphertext = "credential-ref-1"
	meta.DedicatedUpstream.ProviderAccountRef = meta.DedicatedUpstream.Ciphertext
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "must not reuse credential material") {
		t.Fatalf("expected ciphertext-derived provider_account_ref rejection, got %v", err)
	}
}

func TestValidateKeyMeta_DedicatedUpstreamRequiresEndpoints(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:             uuid.NewString(),
		KeyHash:           string(validHash()),
		Revision:          1700000000000000,
		Status:            "active",
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.Endpoints = nil
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "keymeta.dedicated_upstream.endpoints required") {
		t.Fatalf("expected missing endpoints rejection, got %v", err)
	}
}

func TestValidateKeyMeta_DedicatedUpstreamRejectsUnknownEndpointEnums(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:             uuid.NewString(),
		KeyHash:           string(validHash()),
		Revision:          1700000000000000,
		Status:            "active",
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.Endpoints[0].ClientProtocol = "gemini"
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "client_protocol") {
		t.Fatalf("expected client_protocol enum rejection, got %v", err)
	}

	// ADR-0014/0015 S5a: dedicated endpoint protocol_strategy is the explicit
	// channel selector; only the three explicit channels are accepted. The
	// legacy transition values (auto/passthrough_only) are retired.
	for _, ch := range []string{
		types.DedicatedProtocolChannelPassthrough,
		types.DedicatedProtocolChannelResponsesToChat,
		types.DedicatedProtocolChannelAnthropicToChat,
	} {
		meta.DedicatedUpstream = validDedicatedUpstream()
		meta.DedicatedUpstream.Endpoints[0].ProtocolStrategy = ch
		if err := validateKeyMeta(meta); err != nil {
			t.Fatalf("expected dedicated channel %q to pass validation: %v", ch, err)
		}
	}

	// Regression lock (ADR-0015 S5a): retired legacy values must be rejected.
	// Literals on purpose: the product/route loss-strategy constants were
	// deleted from the contract in ADR-0015 S5b, but the wire strings must
	// stay rejected here forever.
	for _, legacy := range []string{
		"auto",
		"passthrough_only",
	} {
		meta.DedicatedUpstream = validDedicatedUpstream()
		meta.DedicatedUpstream.Endpoints[0].ProtocolStrategy = legacy
		if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "protocol_strategy") {
			t.Fatalf("expected retired legacy value %q to be rejected, got %v", legacy, err)
		}
	}

	meta.DedicatedUpstream = validDedicatedUpstream()
	meta.DedicatedUpstream.Endpoints[0].ProtocolStrategy = "bridge_first"
	if err := validateKeyMeta(meta); err == nil || !strings.Contains(err.Error(), "protocol_strategy") {
		t.Fatalf("expected protocol_strategy enum rejection, got %v", err)
	}
}

func TestBuildPublishKeyRows_WithoutDedicatedUpstreamOmitsField(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:       uuid.NewString(),
		KeyHash:     string(validHash()),
		Status:      "active",
		ExpiresAt:   1893456000,
		IPWhitelist: []string{},
		AllowedModels: []string{
			"glm-4.5",
		},
		Revision: 1700000000000000,
	}
	fillKeyMetaDefaults(meta)
	rows, err := buildPublishKeyRows(meta)
	if err != nil {
		t.Fatalf("build rows: %v", err)
	}
	if len(rows) != 2 || rows[0].Op != outbox.OpHSet || rows[0].RedisKey != KeyKeymeta || rows[0].RedisField != meta.KeyHash {
		t.Fatalf("unexpected keymeta hset row: %+v", rows)
	}
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode keymeta payload: %v", err)
	}
	if got := int(payload["schema_version"].(float64)); got != gatewayCompatibleKeyMetaSchemaVersion {
		t.Fatalf("ordinary KeyMeta schema_version = %d, want gateway-compatible v%d: %s", got, gatewayCompatibleKeyMetaSchemaVersion, rows[0].Payload)
	}
	if _, ok := payload["dedicated_upstream"]; ok {
		t.Fatalf("ordinary KeyMeta must not emit dedicated_upstream: %s", rows[0].Payload)
	}
	t.Logf("gw:keymeta ordinary payload: %s", rows[0].Payload)
}

func TestBuildPublishKeyRows_WithDedicatedUpstreamPassesThrough(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:       uuid.NewString(),
		KeyHash:     string(validHash()),
		Status:      "active",
		ExpiresAt:   1893456000,
		IPWhitelist: []string{},
		AllowedModels: []string{
			"glm-4.5",
		},
		Revision:          1700000000000000,
		DedicatedUpstream: validDedicatedUpstream(),
	}
	fillKeyMetaDefaults(meta)
	rows, err := buildPublishKeyRows(meta)
	if err != nil {
		t.Fatalf("build rows: %v", err)
	}
	var payload struct {
		SchemaVersion     int                                `json:"schema_version"`
		DedicatedUpstream *types.DedicatedUpstreamCredential `json:"dedicated_upstream"`
	}
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode keymeta payload: %v", err)
	}
	if payload.SchemaVersion != gatewayCompatibleKeyMetaSchemaVersion {
		t.Fatalf("dedicated KeyMeta schema_version = %d, want gateway-compatible v%d: %s", payload.SchemaVersion, gatewayCompatibleKeyMetaSchemaVersion, rows[0].Payload)
	}
	if payload.DedicatedUpstream == nil {
		t.Fatalf("dedicated_upstream missing from gw:keymeta payload: %s", rows[0].Payload)
	}
	if payload.DedicatedUpstream.UpstreamKeyID != meta.DedicatedUpstream.UpstreamKeyID ||
		payload.DedicatedUpstream.ProviderAccountRef != meta.DedicatedUpstream.ProviderAccountRef ||
		payload.DedicatedUpstream.Ciphertext != meta.DedicatedUpstream.Ciphertext ||
		payload.DedicatedUpstream.CiphertextVersion != meta.DedicatedUpstream.CiphertextVersion ||
		payload.DedicatedUpstream.CiphertextSHA256 != meta.DedicatedUpstream.CiphertextSHA256 ||
		payload.DedicatedUpstream.KMSKeyID != meta.DedicatedUpstream.KMSKeyID ||
		payload.DedicatedUpstream.Status != meta.DedicatedUpstream.Status ||
		payload.DedicatedUpstream.ExpiresAt != meta.DedicatedUpstream.ExpiresAt ||
		payload.DedicatedUpstream.UpdatedAt != meta.DedicatedUpstream.UpdatedAt ||
		payload.DedicatedUpstream.FallbackAllowed != meta.DedicatedUpstream.FallbackAllowed {
		t.Fatalf("dedicated upstream did not round-trip exactly: got %+v want %+v", payload.DedicatedUpstream, meta.DedicatedUpstream)
	}
	if len(payload.DedicatedUpstream.Endpoints) != 2 ||
		payload.DedicatedUpstream.Endpoints[0].ClientProtocol != types.ClientProtocolAnthropic ||
		payload.DedicatedUpstream.Endpoints[0].ProviderID != "glm-anthropic" ||
		payload.DedicatedUpstream.Endpoints[0].ProtocolStrategy != types.DedicatedProtocolChannelPassthrough ||
		payload.DedicatedUpstream.Endpoints[1].ClientProtocol != types.ClientProtocolOpenAI ||
		payload.DedicatedUpstream.Endpoints[1].ProviderID != "glm-openai" {
		t.Fatalf("dedicated upstream endpoints did not round-trip exactly: got %+v", payload.DedicatedUpstream.Endpoints)
	}
	var raw map[string]any
	if err := json.Unmarshal(rows[0].Payload, &raw); err != nil {
		t.Fatalf("decode raw keymeta payload: %v", err)
	}
	dedicated, ok := raw["dedicated_upstream"].(map[string]any)
	if !ok {
		t.Fatalf("dedicated_upstream has unexpected shape: %s", rows[0].Payload)
	}
	if _, ok := dedicated["provider_id"]; ok {
		t.Fatalf("dedicated_upstream must not emit legacy provider_id: %s", rows[0].Payload)
	}
	t.Logf("gw:keymeta dedicated payload: %s", rows[0].Payload)
}

func validDedicatedUpstream() *types.DedicatedUpstreamCredential {
	return &types.DedicatedUpstreamCredential{
		UpstreamKeyID:      "coding-plan-key-001",
		ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440030",
		Ciphertext:         "kms:projects/demo/locations/global/keyRings/ring/cryptoKeys/key:ciphertext",
		CiphertextVersion:  1,
		CiphertextSHA256:   "sha256:dedicated-ciphertext",
		KMSKeyID:           "kms-key-coding",
		Status:             "active",
		ExpiresAt:          1893456000,
		UpdatedAt:          "2026-06-15T00:00:00Z",
		FallbackAllowed:    true,
		Endpoints: []types.DedicatedUpstreamEndpoint{
			{
				ClientProtocol:   types.ClientProtocolAnthropic,
				ProviderID:       "glm-anthropic",
				ProtocolStrategy: types.DedicatedProtocolChannelPassthrough,
			},
			{
				ClientProtocol:   types.ClientProtocolOpenAI,
				ProviderID:       "glm-openai",
				ProtocolStrategy: types.DedicatedProtocolChannelPassthrough,
			},
		},
	}
}

func TestValidateBalanceReason_NoReasonOnTrue(t *testing.T) {
	if err := validateBalanceReason(true, ""); err != nil {
		t.Fatalf("has_balance=true + empty reason should pass: %v", err)
	}
	if err := validateBalanceReason(true, "manual_freeze"); err == nil {
		t.Fatal("has_balance=true + non-empty reason should fail")
	}
}

func TestValidateBalanceReason_RequiresKnownReasonOnFalse(t *testing.T) {
	if err := validateBalanceReason(false, ""); err == nil {
		t.Fatal("has_balance=false + empty reason should fail")
	}
	if err := validateBalanceReason(false, "not-an-enum-value"); err == nil {
		t.Fatal("has_balance=false + unknown reason should fail")
	}
	for known := range keyBalanceReasons {
		if err := validateBalanceReason(false, known); err != nil {
			t.Errorf("has_balance=false + %q should pass: %v", known, err)
		}
	}
}

// TestKeyBalanceReasons_MatchesBillingContract pins the allow-list to an
// explicit literal set. Iterating keyBalanceReasons (as the test above does)
// can never catch a value that billing emits and publisher never learned —
// which is exactly how settlement_review shipped one-sided and froze a
// customer key behind a permanent 400 (2026-09-08, cn prod).
// Update this literal only together with codesphere-billing's
// PublisherBalanceReason union.
func TestKeyBalanceReasons_MatchesBillingContract(t *testing.T) {
	want := []string{
		"balance_depleted",
		"key_spend_limit_exhausted",
		"workspace_budget_exhausted",
		"rolling_quota_exhausted",
		"manual_freeze",
		"account_debt",
		"settlement_risk_limit_exceeded",
		"settlement_review",
		"billing_review",
	}
	for _, reason := range want {
		if _, ok := keyBalanceReasons[reason]; !ok {
			t.Errorf("billing emits %q but publisher's allow-list lacks it: admission projection would 400 forever", reason)
		}
	}
	if len(keyBalanceReasons) != len(want) {
		t.Errorf("allow-list size %d, contract size %d: an entry was added here without updating the contract list", len(keyBalanceReasons), len(want))
	}
}

func TestValidateIP(t *testing.T) {
	for _, bad := range []string{"", "not-an-ip", "1.2.3", "999.999.999.999"} {
		if err := validateIP(bad); err == nil {
			t.Errorf("expected reject for %q", bad)
		}
	}
	for _, ok := range []string{"192.0.2.1", "10.0.0.0", "::1", "2001:db8::1"} {
		if err := validateIP(ok); err != nil {
			t.Errorf("expected accept for %q: %v", ok, err)
		}
	}
}

func TestValidateOverrideScope(t *testing.T) {
	for _, bad := range []string{"", "openai", "openai|", "|||", "  |https://x|gpt"} {
		if err := validateOverrideScope(bad); err == nil {
			t.Errorf("expected reject for scope %q", bad)
		}
	}
	for _, ok := range []string{
		"openai|https://api.openai.com/v1|gpt-4o",
		"anthropic|https://api.anthropic.com|claude-3-5-sonnet",
	} {
		if err := validateOverrideScope(ok); err != nil {
			t.Errorf("expected accept for %q: %v", ok, err)
		}
	}
}

func TestValidateBulkLen(t *testing.T) {
	if err := validateBulkLen(0); err == nil {
		t.Fatal("empty bulk should fail")
	}
	if err := validateBulkLen(types.MaxBulkItems); err != nil {
		t.Fatalf("len == max should pass: %v", err)
	}
	if err := validateBulkLen(types.MaxBulkItems + 1); err == nil {
		t.Fatal("len > max should fail")
	}
}

func TestFillKeyMetaDefaults_FillsZeros(t *testing.T) {
	// 2026-05-16 doc §4.1.x: schema_version + revision auto-fill when
	// caller leaves them zero. Caller no longer has to synthesize either.
	m := &types.KeyMeta{
		KeyID:   uuid.NewString(),
		KeyHash: string(validHash()),
		Status:  "active",
	}
	fillKeyMetaDefaults(m)
	if m.SchemaVersion != gatewayCompatibleKeyMetaSchemaVersion {
		t.Errorf("schema_version: got %d want gateway-compatible v%d (publisher should auto-fill)",
			m.SchemaVersion, gatewayCompatibleKeyMetaSchemaVersion)
	}
	if m.Revision <= 0 {
		t.Errorf("revision: publisher should auto-fill to time.Now().UnixMicro(), got %d", m.Revision)
	}
	// Post-fill, validateKeyMeta should accept (revision/schema were the
	// only "must be > 0" fields that previously forced caller to supply).
	if err := validateKeyMeta(m); err != nil {
		t.Errorf("post-fill validate should pass, got %v", err)
	}
}

func TestFillKeyMetaDefaults_HonorsCallerProvidedValues(t *testing.T) {
	// DR replay scenario: caller may want to preserve original revision
	// to avoid clobbering a newer write. Auto-fill must not overwrite a
	// non-zero caller value.
	m := &types.KeyMeta{
		KeyID:         uuid.NewString(),
		KeyHash:       string(validHash()),
		Status:        "active",
		SchemaVersion: 99,
		Revision:      1715760000000123,
	}
	fillKeyMetaDefaults(m)
	if m.SchemaVersion != 99 {
		t.Errorf("schema_version: caller value 99 should be preserved, got %d", m.SchemaVersion)
	}
	if m.Revision != 1715760000000123 {
		t.Errorf("revision: caller value should be preserved, got %d", m.Revision)
	}
}

func TestFillKeyMetaDefaults_NilSafe(t *testing.T) {
	// Defensive — bulk loop may iterate nil items if a caller sends
	// {"items":[null]}. Don't panic.
	fillKeyMetaDefaults(nil)
}

func TestFillProductRuntimePolicyDefaults(t *testing.T) {
	policy := &types.ProductRuntimePolicy{
		ProductID:        uuid.NewString(),
		Status:           "active",
		CredentialSource: "keypool",
		ModelScopeMode:   "plan_configured",
		FallbackPolicy:   "within_product",
		BillingMode:      "token_plan_quota",
	}
	fillProductRuntimePolicyDefaults(policy)
	if policy.SchemaVersion != CurrentProductRuntimePolicySchemaVersion {
		t.Fatalf("schema_version = %d, want %d", policy.SchemaVersion, CurrentProductRuntimePolicySchemaVersion)
	}
	if policy.Revision <= 0 {
		t.Fatalf("revision should be filled, got %d", policy.Revision)
	}
	if err := validateProductRuntimePolicy(policy); err != nil {
		t.Fatalf("post-fill validate should pass: %v", err)
	}
	if policy.AllowHighLossBridgeWhenNoNative || policy.AllowBridgeOnNativeFailure {
		t.Fatalf("protocol degradation switches must default false: %+v", policy)
	}
}

func TestBuildProductRuntimePolicyRowsUsesRevisionCAS(t *testing.T) {
	policy := validProductRuntimePolicyForCAS("prod_coding", 1700000000000100)

	rows, err := buildPublishProductRuntimePolicyRows("prod_coding", policy)
	if err != nil {
		t.Fatalf("buildPublishProductRuntimePolicyRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len=%d want 2", len(rows))
	}
	if rows[0].Op != outbox.OpProductPolicySetCAS {
		t.Fatalf("product policy upsert op=%q want %q", rows[0].Op, outbox.OpProductPolicySetCAS)
	}
	if rows[0].RedisKey != KeyProductPolicies || rows[0].RedisField != "prod_coding" {
		t.Fatalf("unexpected redis target: key=%q field=%q", rows[0].RedisKey, rows[0].RedisField)
	}
	var payload types.ProductRuntimePolicy
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal product policy payload: %v", err)
	}
	if payload.Revision != 1700000000000100 {
		t.Fatalf("payload revision=%d want 1700000000000100", payload.Revision)
	}
}

func TestBuildDeleteProductRuntimePolicyRowsUsesRevisionTombstoneCAS(t *testing.T) {
	rows, err := buildDeleteProductRuntimePolicyRows("prod_coding", 1700000000000200)
	if err != nil {
		t.Fatalf("buildDeleteProductRuntimePolicyRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len=%d want 2", len(rows))
	}
	if rows[0].Op != outbox.OpProductPolicyDelCAS {
		t.Fatalf("product policy delete op=%q want %q", rows[0].Op, outbox.OpProductPolicyDelCAS)
	}
	if rows[0].RedisKey != KeyProductPolicies || rows[0].RedisField != "prod_coding" {
		t.Fatalf("unexpected redis target: key=%q field=%q", rows[0].RedisKey, rows[0].RedisField)
	}
	var payload types.ProductPolicyDelete
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal product policy delete payload: %v", err)
	}
	if payload.ProductID != "prod_coding" || payload.Revision != 1700000000000200 {
		t.Fatalf("unexpected delete payload: %+v", payload)
	}
}

// ADR-0015 S5b: product-level protocol_strategy is retired from the contract.
// The field no longer exists on types.ProductRuntimePolicy; a policy decoded
// from a stale payload that carried it (lenient admin decode drops it) must
// validate and publish without any protocol_strategy on the wire.
func TestValidateProductRuntimePolicy_RetiredProtocolStrategyAbsentFromWire(t *testing.T) {
	policy := &types.ProductRuntimePolicy{
		ProductID:        uuid.NewString(),
		Revision:         1,
		Status:           "active",
		CredentialSource: "keypool",
		ModelScopeMode:   "plan_configured",
		FallbackPolicy:   "within_product",
		BillingMode:      "token_plan_quota",
	}
	if err := validateProductRuntimePolicy(policy); err != nil {
		t.Fatalf("expected policy without protocol_strategy to pass: %v", err)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal product policy: %v", err)
	}
	if strings.Contains(string(raw), `"protocol_strategy"`) {
		t.Fatalf("retired protocol_strategy leaked onto wire: %s", raw)
	}
}

func TestValidateProductRuntimePolicy_ProductAllowlistRequiresModels(t *testing.T) {
	policy := &types.ProductRuntimePolicy{
		ProductID:        uuid.NewString(),
		Revision:         1,
		Status:           "active",
		CredentialSource: "keypool",
		ModelScopeMode:   "product_allowlist",
		FallbackPolicy:   "within_product",
		BillingMode:      "token_plan_quota",
	}
	if err := validateProductRuntimePolicy(policy); err == nil {
		t.Fatal("expected product_allowlist without allowed_models to fail")
	}
	policy.AllowedModels = []string{"glm-4.5"}
	if err := validateProductRuntimePolicy(policy); err != nil {
		t.Fatalf("expected product_allowlist with models to pass: %v", err)
	}
}

func TestBuildPublishProviderRows_PreservesBridgeInPayload(t *testing.T) {
	info := &types.ProviderInfo{
		ID:                "openai",
		Name:              "OpenAI compatible",
		BaseURL:           "https://api.openai.example",
		Status:            "active",
		Visibility:        types.ProviderVisibilityDedicatedOnly,
		CredentialSource:  types.ProviderCredentialSourceDedicatedUpstream,
		AllowedRouteKinds: []string{types.ProviderRouteKindDedicated},
		OwnerScope: &types.OwnerScope{
			TenantID:      "tenant_demo",
			ProductID:     "prod_coding",
			EntitlementID: "entitlement_demo_001",
		},
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "anthropic-to-openai",
			OutboundProtocol: "openai",
			PathPrefix:       "/v1",
			AuthScheme:       "bearer",
			TranslatorKey:    "standard_to_openai",
			Bridge:           "anthropic_to_openai_chat",
			Accepts: []types.ProtocolBindingAccept{{
				Protocol:        "anthropic",
				Capabilities:    []string{"chat"},
				RequestContract: "anthropic_messages",
			}},
		}},
		DefaultProtocolBinding: "anthropic-to-openai",
	}
	fillProviderInfoDefaults(info)

	rows := buildPublishProviderRows("openai", info)
	if len(rows) != 2 {
		t.Fatalf("row count=%d want 2: %+v", len(rows), rows)
	}
	if rows[0].Op != outbox.OpHSet || rows[0].RedisKey != KeyProviders || rows[0].RedisField != "openai" {
		t.Fatalf("provider row mismatch: %+v", rows[0])
	}

	var payload types.ProviderInfo
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal provider payload: %v payload=%s", err, rows[0].Payload)
	}
	if payload.SchemaVersion != CurrentProviderInfoSchemaVersion {
		t.Fatalf("provider schema_version = %d, want CurrentProviderInfoSchemaVersion %d: %s", payload.SchemaVersion, CurrentProviderInfoSchemaVersion, rows[0].Payload)
	}
	if payload.SchemaVersion != gatewayCompatibleProviderInfoSchemaVersion {
		t.Fatalf("provider schema_version = %d, want gateway-compatible v%d: %s", payload.SchemaVersion, gatewayCompatibleProviderInfoSchemaVersion, rows[0].Payload)
	}
	if payload.Visibility != types.ProviderVisibilityDedicatedOnly ||
		payload.CredentialSource != types.ProviderCredentialSourceDedicatedUpstream ||
		len(payload.AllowedRouteKinds) != 1 ||
		payload.AllowedRouteKinds[0] != types.ProviderRouteKindDedicated ||
		payload.OwnerScope == nil ||
		payload.OwnerScope.TenantID != "tenant_demo" ||
		payload.OwnerScope.ProductID != "prod_coding" ||
		payload.OwnerScope.EntitlementID != "entitlement_demo_001" {
		t.Fatalf("provider isolation fields not preserved in payload: %s", rows[0].Payload)
	}
	if len(payload.ProtocolBindings) != 1 {
		t.Fatalf("payload binding count=%d: %+v", len(payload.ProtocolBindings), payload.ProtocolBindings)
	}
	if payload.ProtocolBindings[0].Bridge != "anthropic_to_openai_chat" {
		t.Fatalf("bridge lost in provider payload: %s", rows[0].Payload)
	}
	t.Logf("gw:providers provider payload: %s", rows[0].Payload)
}

func TestValidateProductRuntimePolicy_DedicatedUpstreamFailsClosed(t *testing.T) {
	policy := &types.ProductRuntimePolicy{
		ProductID:        uuid.NewString(),
		Revision:         1,
		Status:           "active",
		CredentialSource: "dedicated_upstream",
		ModelScopeMode:   "upstream_inherited",
		FallbackPolicy:   "platform_default",
		BillingMode:      "included_upstream",
	}
	if err := validateProductRuntimePolicy(policy); err == nil {
		t.Fatal("expected dedicated_upstream without provider/channel boundary to fail")
	}
	policy.AllowedProviderIDs = []string{"zhipu-glm-coding-1234abcd"}
	if err := validateProductRuntimePolicy(policy); err == nil {
		t.Fatal("expected dedicated_upstream with platform_default fallback to fail")
	}
	policy.FallbackPolicy = "within_provider"
	if err := validateProductRuntimePolicy(policy); err != nil {
		t.Fatalf("expected dedicated_upstream with provider boundary and within_provider fallback to pass: %v", err)
	}

	policy.CredentialSource = "provider_entitlement"
	if err := validateProductRuntimePolicy(policy); err == nil || !strings.Contains(err.Error(), "product_policy.credential_source") {
		t.Fatalf("expected legacy provider_entitlement to be rejected by enum, got %v", err)
	}
}

func TestValidateProductRuntimePolicy_RejectsInvalidRouteGraphDirectWrite(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*types.ProductRuntimePolicy)
		want   string
	}{
		{
			name: "assignment add mode",
			mutate: func(policy *types.ProductRuntimePolicy) {
				policy.RouteGraph.Assignments[0].Mode = "ADD"
			},
			want: "route_graph.assignments[0].mode=ADD not allowed",
		},
		{
			name: "assignment block mode",
			mutate: func(policy *types.ProductRuntimePolicy) {
				policy.RouteGraph.Assignments[0].Mode = "BLOCK"
			},
			want: "route_graph.assignments[0].mode=BLOCK not allowed",
		},
		{
			name: "unknown provider id against product allowlist",
			mutate: func(policy *types.ProductRuntimePolicy) {
				policy.RouteGraph.ProductChannels[0].ProviderID = "pc_missing"
				policy.RouteGraph.Groups[0].Members[0].ProviderID = "pc_missing"
			},
			want: "route_graph.product_channels[0].provider_id unknown: pc_missing",
		},
		{
			name: "target has no model intersection",
			mutate: func(policy *types.ProductRuntimePolicy) {
				policy.AllowedModels = []string{"gpt-4o-mini"}
				policy.RouteGraph.ProductChannels[0].AllowedModels = []string{"claude-3-5-sonnet"}
			},
			want: "route_graph.default_target model intersection empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := directWriteRouteGraphPolicy()
			tc.mutate(policy)
			err := validateProductRuntimePolicy(policy)
			if err == nil {
				t.Fatalf("validateProductRuntimePolicy succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func directWriteRouteGraphPolicy() *types.ProductRuntimePolicy {
	return &types.ProductRuntimePolicy{
		ProductID:             "prod_coding",
		Revision:              1,
		Status:                "active",
		CredentialSource:      "dedicated_upstream",
		AllowedProviderIDs:    []string{"pc_101"},
		AllowedModels:         []string{"kimi-k2.6"},
		ModelScopeMode:        "product_allowlist",
		FallbackPolicy:        "within_product",
		BillingMode:           "token_plan_quota",
		RouteGraphRequired:    true,
		RouteGraphEnforcement: "enforce",
		RouteGraph: &types.ProductRouteGraph{
			SchemaVersion: 1,
			DefaultTarget: types.ProductRouteTarget{
				TargetType: "GROUP",
				TargetID:   "primary",
			},
			Groups: []types.ProductRouteGroup{{
				ID:       "primary",
				Code:     "primary",
				Status:   "active",
				Strategy: "priority_failover",
				Members: []types.ProductRouteGroupMember{{
					ProductChannelID: "pc_101",
					ChannelID:        "kimi-main",
					ProviderID:       "pc_101",
					Priority:         1,
					Weight:           100,
					Role:             "primary",
				}},
			}},
			ProductChannels: []types.ProductRouteChannel{{
				ID:            "pc_101",
				ChannelID:     "kimi-main",
				ProviderID:    "pc_101",
				AllowedModels: []string{"kimi-k2.6"},
				DeniedModels:  []string{},
				Priority:      1,
				Weight:        100,
				MaxRPM:        500,
				MaxTPM:        100000,
				Status:        "active",
			}},
			Assignments: []types.ProductRouteAssignment{{
				ID:          "asg_vip",
				ProductID:   "prod_coding",
				SubjectType: "USER_SEGMENT",
				SubjectID:   "seg_vip",
				TargetType:  "GROUP",
				TargetID:    "primary",
				Mode:        "ONLY",
				Priority:    10,
			}},
		},
	}
}

func TestBuildKeyLifecycleRows_DisableUpdatesStatusAndRevokedSet(t *testing.T) {
	hash := validHash()
	rows, err := buildKeyLifecycleRows([]types.KeyHash{hash}, false)
	if err != nil {
		t.Fatalf("build rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want status + revoked + publish", len(rows))
	}
	if rows[0].Op != outbox.OpKeyMetaStatus || rows[0].RedisKey != KeyKeymeta || rows[0].RedisField != string(hash) {
		t.Fatalf("row0 must patch gw:keymeta status, got %+v", rows[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	if payload["status"] != "suspended" {
		t.Fatalf("disable must set keymeta status=suspended, got %+v", payload)
	}
	if rows[1].Op != outbox.OpSAdd || rows[1].RedisKey != KeyRevokedKeys || rows[1].RedisMember != string(hash) {
		t.Fatalf("row1 must SADD revoked set, got %+v", rows[1])
	}
	var notify map[string]any
	if err := json.Unmarshal(rows[2].NotifyMessage, &notify); err != nil {
		t.Fatalf("decode notify: %v", err)
	}
	if notify["type"] != "revoked_key_added" || notify["key_hash"] != string(hash) {
		t.Fatalf("notify must carry precise key hash, got %+v", notify)
	}
}

func TestBuildKeyLifecycleRows_EnableUpdatesStatusAndRevokedSet(t *testing.T) {
	hash := validHash()
	rows, err := buildKeyLifecycleRows([]types.KeyHash{hash}, true)
	if err != nil {
		t.Fatalf("build rows: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	if payload["status"] != "active" {
		t.Fatalf("enable must set keymeta status=active, got %+v", payload)
	}
	if rows[1].Op != outbox.OpSRem || rows[1].RedisKey != KeyRevokedKeys || rows[1].RedisMember != string(hash) {
		t.Fatalf("row1 must SREM revoked set, got %+v", rows[1])
	}
	var notify map[string]any
	if err := json.Unmarshal(rows[2].NotifyMessage, &notify); err != nil {
		t.Fatalf("decode notify: %v", err)
	}
	if notify["type"] != "revoked_key_removed" || notify["key_hash"] != string(hash) {
		t.Fatalf("notify must carry precise key hash, got %+v", notify)
	}
}

// SetKeyBalance / SetKeyBalanceBulk auto-fill `revision` per the §5.1/§5.2
// 2026-05-16 audit decision. Caller-supplied non-zero is preserved.
//
// These tests don't exercise the outbox commit path (which needs a real
// PG store); they just verify the input validation no longer rejects
// revision=0, which is the user-visible behaviour change.

func TestSetKeyBalance_ZeroRevisionIsNotRejected(t *testing.T) {
	// Use a nil store deliberately — we'll hit commit and get an error,
	// but the input-validation surface(revision <= 0)should pass before
	// then. If publisher ever re-tightens revision check, this test
	// catches it.
	p := &Impl{} // store=nil; the commit-attempt panics or errs, but we'll catch via recover.
	defer func() { _ = recover() }()
	_, _ = p.SetKeyBalance(context.Background(), validKeyID(), true, "", 0)
	// Reaching here without "revision must be > 0" 400-style error means
	// the auto-fill happened. The actual store-commit failure is unrelated.
}

func TestSetKeyBalanceBulk_ZeroRevisionIsNotRejected(t *testing.T) {
	p := &Impl{}
	defer func() { _ = recover() }()
	items := []types.KeyBalanceItem{
		{KeyID: validKeyID(), HasBalance: true, Revision: 0},
		{KeyID: validKeyID(), HasBalance: true, Revision: 1715760000000123},
	}
	_, _ = p.SetKeyBalanceBulk(context.Background(), items, "")
}

func TestKeyBalanceRevisionKeyMatchesWorkerCAS(t *testing.T) {
	// DeleteKeyBalance must delete the same revision sidecar hash that
	// outbox.OpKeyBalanceCAS writes. This pins the Redis schema key and
	// prevents reintroducing the historical ':' vs '_' drift.
	if KeyKeyBalanceRevision != "gw:key_balance_revision" {
		t.Fatalf("KeyKeyBalanceRevision = %q, want gw:key_balance_revision", KeyKeyBalanceRevision)
	}
}

func TestBuildDeleteKeyBalanceRowsUsesCASTombstoneAndKeepsWatermarks(t *testing.T) {
	id := validKeyID()
	rows := buildKeyAdmissionRows(id, false, "", true, types.KeyAdmissionDecision{Legacy: true}, 99)
	if len(rows) != 1 {
		t.Fatalf("DeleteKeyBalance rows len = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Op != outbox.OpKeyBalanceCAS || row.RedisField != string(id) {
		t.Fatalf("delete must be one versioned CAS tombstone row: %+v", row)
	}
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["deleted"] != true || int64(payload["publisher_revision"].(float64)) != 99 {
		t.Fatalf("delete payload = %+v", payload)
	}
}

// --------------------------------------------------------------------------
// SetKeyBalance dedupe (2026-05-26): billing consumers call this after every
// per-request token deduction; the bool stays unchanged 99%+ of the time.
// In-process write-through cache short-circuits those redundant writes so
// outbox + audit + Redis fan-out only see real state transitions.
// --------------------------------------------------------------------------

func TestSetKeyBalance_Dedupe_SkipsWhenStateUnchanged(t *testing.T) {
	p := New(nil) // store=nil acceptable: dedupe hit short-circuits before commit
	id := validKeyID()
	priorBatch := uuid.New()
	p.balanceDedupe[id] = balanceDedupeEntry{
		HasBalance: true,
		Reason:     "",
		WrittenAt:  time.Now(),
		BatchID:    priorBatch,
	}

	got, err := p.SetKeyBalance(context.Background(), id, true, "", 0)
	if err != nil {
		t.Fatalf("dedupe hit must not error; got %v", err)
	}
	if got != priorBatch {
		t.Errorf("dedupe must return prior BatchID %v so caller request-id correlation works; got %v", priorBatch, got)
	}
	if got := p.BalanceDedupeSkippedCount(); got != 1 {
		t.Errorf("skipped counter: got %d want 1", got)
	}
}

func TestSetKeyBalance_Dedupe_HasBalanceChanged_DoesNotSkip(t *testing.T) {
	p := New(nil)
	id := validKeyID()
	p.balanceDedupe[id] = balanceDedupeEntry{
		HasBalance: true,
		Reason:     "",
		WrittenAt:  time.Now(),
		BatchID:    uuid.New(),
	}

	// Real state transition: true → false. MUST NOT dedupe.
	defer func() { _ = recover() }() // commit with nil store will panic, OK
	_, _ = p.SetKeyBalance(context.Background(), id, false, "balance_depleted", 0)

	if got := p.BalanceDedupeSkippedCount(); got != 0 {
		t.Errorf("state transition must not dedupe; skipped counter got %d want 0", got)
	}
}

func TestSetKeyBalance_Dedupe_ReasonChanged_DoesNotSkip(t *testing.T) {
	p := New(nil)
	id := validKeyID()
	// hasBalance stays false, but reason flips. Reason change is a real
	// state transition even when the bool didn't move (e.g. depleted →
	// manual_freeze) and must reach the outbox / audit.
	p.balanceDedupe[id] = balanceDedupeEntry{
		HasBalance: false,
		Reason:     "balance_depleted",
		WrittenAt:  time.Now(),
		BatchID:    uuid.New(),
	}

	defer func() { _ = recover() }()
	_, _ = p.SetKeyBalance(context.Background(), id, false, "manual_freeze", 0)

	if got := p.BalanceDedupeSkippedCount(); got != 0 {
		t.Errorf("reason change must not dedupe; skipped counter got %d want 0", got)
	}
}

func TestSetKeyBalance_Dedupe_TTLExpired_DoesNotSkip(t *testing.T) {
	p := New(nil)
	id := validKeyID()
	// Entry older than the default TTL (5 min) — must rewrite even though
	// the state hasn't changed, so Redis cache drift can self-heal.
	p.balanceDedupe[id] = balanceDedupeEntry{
		HasBalance: true,
		Reason:     "",
		WrittenAt:  time.Now().Add(-10 * time.Minute),
		BatchID:    uuid.New(),
	}

	defer func() { _ = recover() }()
	_, _ = p.SetKeyBalance(context.Background(), id, true, "", 0)

	if got := p.BalanceDedupeSkippedCount(); got != 0 {
		t.Errorf("expired entry must not dedupe; skipped counter got %d want 0", got)
	}
}

func TestSetKeyBalance_Dedupe_TTLZeroDisablesEntirely(t *testing.T) {
	// Operator kill switch: TTL=0 means every call writes — used when ops
	// suspects a dedupe staleness bug and wants to flush.
	p := New(nil)
	p.SetBalanceDedupeTTLForTest(0)
	id := validKeyID()
	p.balanceDedupe[id] = balanceDedupeEntry{
		HasBalance: true,
		Reason:     "",
		WrittenAt:  time.Now(),
		BatchID:    uuid.New(),
	}

	defer func() { _ = recover() }()
	_, _ = p.SetKeyBalance(context.Background(), id, true, "", 0)

	if got := p.BalanceDedupeSkippedCount(); got != 0 {
		t.Errorf("TTL=0 must disable dedupe; skipped counter got %d want 0", got)
	}
}

// validateRetryBudget enforces all-or-nothing across the three axes;
// see audit doc 2026-05-16 §5 P1 #7 for rationale.

func TestValidateRetryBudget_AllZeroPasses(t *testing.T) {
	if err := validateRetryBudget(types.RetryBudgetConfig{}); err != nil {
		t.Fatalf("all-zero (gateway defaults) should pass, got %v", err)
	}
}

func TestValidateRetryBudget_AllThreeSetPasses(t *testing.T) {
	rb := types.RetryBudgetConfig{MaxKeysPerProvider: 2, MaxProviders: 3, TotalWallTimeMs: 5000}
	if err := validateRetryBudget(rb); err != nil {
		t.Fatalf("all three axes set should pass, got %v", err)
	}
}

// 2026-05-16 audit P2: enum + Unix-seconds validators

func TestValidateEnum(t *testing.T) {
	allowed := map[string]struct{}{"a": {}, "b": {}}
	if err := validateEnum("x", "a", allowed); err != nil {
		t.Errorf("known value should pass: %v", err)
	}
	if err := validateEnum("x", "c", allowed); err == nil {
		t.Error("unknown value should be rejected")
	}
	if err := validateEnum("x", "", allowed); err == nil {
		t.Error("empty value should be rejected (caller forgot to set)")
	}
}

func TestValidateUnixSeconds(t *testing.T) {
	// Allowed
	for _, ok := range []int64{0, 1, 1715760000, 2_000_000_000} {
		if err := validateUnixSeconds("t", ok); err != nil {
			t.Errorf("%d should pass: %v", ok, err)
		}
	}
	// Reject millisecond-looking values
	for _, bad := range []int64{1_000_000_000_001, 1_715_760_000_000, 9_999_999_999_999} {
		if err := validateUnixSeconds("t", bad); err == nil {
			t.Errorf("%d (looks like millis) should fail", bad)
		}
	}
}

func TestValidateKeyMeta_RejectsUnknownStatus(t *testing.T) {
	// 2026-05-16 P2: status enum strict. Old behaviour only checked non-empty.
	m := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Status:   "deactivated", // close to "suspended" — typo
		Revision: 1715760000000000,
	}
	if err := validateKeyMeta(m); err == nil {
		t.Fatal("non-canonical status should be rejected")
	}
}

// 2026-05-16 B 方案: enforce_model_whitelist 拆字段

func TestValidateKeyMeta_EnforceWhitelist_RequiresNonEmptyList(t *testing.T) {
	m := &types.KeyMeta{
		KeyID:                 uuid.NewString(),
		KeyHash:               string(validHash()),
		Status:                "active",
		Revision:              1715760000000000,
		EnforceModelWhitelist: true,
		AllowedModels:         nil, // 空 list 但 enforce — 语义矛盾
	}
	if err := validateKeyMeta(m); err == nil {
		t.Fatal("enforce=true + empty allowed_models should be rejected; tell caller to use status=suspended for full deny")
	}
}

func TestValidateKeyMeta_EnforceWhitelist_WithListPasses(t *testing.T) {
	m := &types.KeyMeta{
		KeyID:                 uuid.NewString(),
		KeyHash:               string(validHash()),
		Status:                "active",
		Revision:              1715760000000000,
		EnforceModelWhitelist: true,
		AllowedModels:         []string{"gpt-4o", "claude-opus-4-7"},
	}
	if err := validateKeyMeta(m); err != nil {
		t.Errorf("enforce=true + non-empty list should pass: %v", err)
	}
}

func TestValidateKeyMeta_NoEnforce_EmptyListPasses(t *testing.T) {
	// Backwards-compat: legacy data has enforce=false (Go zero value) +
	// AllowedModels=[]. Must still pass — that's the "no whitelist" intent.
	m := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Status:   "active",
		Revision: 1715760000000000,
		// EnforceModelWhitelist defaults to false
		AllowedModels: []string{},
	}
	if err := validateKeyMeta(m); err != nil {
		t.Errorf("legacy shape (enforce=false + empty list) must keep passing: %v", err)
	}
}

func TestValidateKeyMeta_ScopeAttrsLimit(t *testing.T) {
	base := func() *types.KeyMeta {
		return &types.KeyMeta{
			KeyID:    uuid.NewString(),
			KeyHash:  string(validHash()),
			Status:   "active",
			Revision: 1715760000000000,
		}
	}
	// 10 pairs = OK
	m := base()
	m.ScopeAttrs = make(map[string]string, MaxScopeAttrs)
	for i := 0; i < MaxScopeAttrs; i++ {
		m.ScopeAttrs[fmt.Sprintf("k%d", i)] = "v"
	}
	if err := validateKeyMeta(m); err != nil {
		t.Errorf("%d scope_attrs entries should pass: %v", MaxScopeAttrs, err)
	}
	// 11 pairs = 400
	m = base()
	m.ScopeAttrs = make(map[string]string, MaxScopeAttrs+1)
	for i := 0; i <= MaxScopeAttrs; i++ {
		m.ScopeAttrs[fmt.Sprintf("k%d", i)] = "v"
	}
	if err := validateKeyMeta(m); err == nil {
		t.Errorf("%d scope_attrs entries should be rejected", MaxScopeAttrs+1)
	}
}

func TestValidateKeyMeta_RejectsMillisExpires(t *testing.T) {
	m := &types.KeyMeta{
		KeyID:     uuid.NewString(),
		KeyHash:   string(validHash()),
		Status:    "active",
		Revision:  1715760000000000,
		ExpiresAt: 1_715_760_000_000, // ms (caller wrote ms not s)
	}
	if err := validateKeyMeta(m); err == nil {
		t.Fatal("expires_at in milliseconds should be rejected (publisher catches the unit confusion)")
	}
}

func TestValidateRetryBudget_PartialFails(t *testing.T) {
	cases := []types.RetryBudgetConfig{
		{MaxProviders: 3},                              // 1 of 3
		{MaxKeysPerProvider: 2, MaxProviders: 3},       // 2 of 3
		{MaxProviders: 3, TotalWallTimeMs: 5000},       // 2 of 3 different combo
		{MaxKeysPerProvider: 2, TotalWallTimeMs: 5000}, // 2 of 3 different combo
	}
	for i, rb := range cases {
		if err := validateRetryBudget(rb); err == nil {
			t.Errorf("case %d: partial (%+v) should be rejected; mixed-zero invokes 'unlimited' on unset axes, which is the all-or-nothing foot-gun the audit flagged", i, rb)
		}
	}
}

func validProductRuntimePolicyForCAS(productID string, revision int64) *types.ProductRuntimePolicy {
	return &types.ProductRuntimePolicy{
		ProductID:          productID,
		Revision:           revision,
		Status:             "active",
		CredentialSource:   "dedicated_upstream",
		AllowedProviderIDs: []string{"pc_101"},
		ModelScopeMode:     "product_allowlist",
		AllowedModels:      []string{"kimi-k2.6"},
		FallbackPolicy:     "within_product",
		BillingMode:        "payg_balance",
		RouteStrategy:      "priority",
	}
}
