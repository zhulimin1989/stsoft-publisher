package types

import "encoding/json"

// ============================================================
// KeyMeta — unit 1
// ============================================================

// KeyMeta is the gateway data-plane view of one API key. Published by the
// user-service compiler to gw:keymeta with KeyHash as the hash field. All
// identity-domain attributes (tenant/workspace/app/...) live in ScopeAttrs;
// the legacy top-level TenantID/WorkspaceID/AppID fields were removed in
// publisher V2 §23.2.
type KeyMeta struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	Revision      int64  `json:"revision,omitempty"` // unix microseconds; ordering reference
	KeyID         string `json:"key_id"`
	KeyHash       string `json:"key_hash"`
	Alias         string `json:"alias,omitempty"`

	// ScopeAttrs carries data-driven business identity. Keyed by
	// canonical names like tenant_id / workspace_id / app_id, plus any
	// custom user/ops attrs the control-plane has registered (usr.*,
	// ops.*). Gateway emits these unchanged into UsageEvent.
	ScopeAttrs map[string]string `json:"scope_attrs,omitempty"`

	// Admission
	Status      string   `json:"status"`
	ExpiresAt   int64    `json:"expires_at"`
	IPWhitelist []string `json:"ip_whitelist"`
	// EnforceModelWhitelist toggles whether AllowedModels is consulted.
	//
	// 2026-05-16: introduced to remove the legacy "empty list = allow all"
	// counter-intuitive default. Backwards-compat:
	//   - omitted/false ⇒ no whitelist enforcement (regardless of AllowedModels).
	//     Matches the legacy `len(AllowedModels)==0 → skip` behaviour for old data.
	//   - true          ⇒ AllowedModels must be non-empty (publisher 400s
	//     otherwise) and gateway rejects models not in the list.
	//
	// Publisher enforces enforce=true ⇒ list non-empty at admission time;
	// gateway reads the flag and short-circuits ModelPermissionChecker when
	// enforce=false. See audit doc §5 P0 #4 + docs/publisher-api-reference.md §4.1.
	EnforceModelWhitelist bool     `json:"enforce_model_whitelist,omitempty"`
	AllowedModels         []string `json:"allowed_models"`
	// SSEConcurrency removed 2026-05-16: gateway never consumed the field
	// (no per-key SSE concurrency cap was implemented). T49 decision was
	// "do not land — single-call caps already cover the abuse case".

	// Quota (RPM/TPM/CC)
	Policies []EffectivePolicy `json:"policies,omitempty"`

	// ResourceBindings is the flattened static execution contract the
	// gateway uses to look up runtime state for each scoped quota /
	// reservation policy. Control-plane compiles workspace ancestry and
	// policy ownership into this list so gateway hot paths never need to
	// walk the workspace tree dynamically. JSON tags MUST match
	// gateway model.ResourceBinding exactly — cross-repo regression
	// pinned by gateway internal/configsync/publisher_schema_test.go.
	ResourceBindings []ResourceBinding `json:"resource_bindings,omitempty"`

	// AllowanceBindings is the V1 gateway-side lookup list: each entry
	// tells the AllowanceChecker which `gw:allow:{scope}:{scope_id}:{period}`
	// flag to MGET when the key is presented at admission. Control-plane
	// resolves scope_id and the period instance (e.g. "202605") at compile
	// time so the gateway only does O(N) boolean lookups, no money math.
	//
	// Empty slice = no allowance enforcement (V1 transition state during
	// rollout). Gateway must reject the request when ANY bound flag is
	// "0" / missing — `gw:allow_reason:...` carries the optional reason.
	//
	// Will be retired by gateway T54 Phase 3 (single per-key `gw:allow:{key_id}`
	// boolean flag); publishers may stop emitting AllowanceBindings once
	// control-server cuts the writer over and gateway swaps its wire.
	AllowanceBindings []AllowanceBinding `json:"allowance_bindings,omitempty"`

	// Single-call cap (token-denominated, no money)
	MaxTokensPerCall int64 `json:"max_tokens_per_call,omitempty"`

	// Content guardrails
	Guardrails *GuardrailPolicy `json:"guardrails,omitempty"`

	// DedicatedUpstream carries an optional per-user upstream credential
	// binding. Nil means ordinary users continue through the platform
	// provider keypool path.
	DedicatedUpstream *DedicatedUpstreamCredential `json:"dedicated_upstream,omitempty"`
}

// PublisherKeyMeta is the publisher-to-gateway KeyMeta runtime contract.
type PublisherKeyMeta = KeyMeta

type DedicatedUpstreamCredential struct {
	UpstreamKeyID string `json:"upstream_key_id"`
	// ProviderAccountRef is issued by the upstream account registry and remains
	// stable when this dedicated credential rotates. It is optional on the wire
	// only so historical KeyMeta values remain decodable; all new dedicated
	// writes require a registry-issued opaque stable ID.
	ProviderAccountRef string                      `json:"provider_account_ref,omitempty"`
	Ciphertext         string                      `json:"ciphertext"`
	CiphertextVersion  int64                       `json:"ciphertext_version"`
	CiphertextSHA256   string                      `json:"ciphertext_sha256"`
	KMSKeyID           string                      `json:"kms_key_id"`
	Status             string                      `json:"status"`
	ExpiresAt          int64                       `json:"expires_at"`
	UpdatedAt          string                      `json:"updated_at"`
	FallbackAllowed    bool                        `json:"fallback_allowed"`
	Endpoints          []DedicatedUpstreamEndpoint `json:"endpoints"`
}

type DedicatedUpstreamEndpoint struct {
	ClientProtocol   string `json:"client_protocol"`
	ProviderID       string `json:"provider_id"`
	ProtocolStrategy string `json:"protocol_strategy,omitempty"`
}

// ResourceBinding pins one (scope_type, scope_id, metric) runtime lookup
// the gateway resolves when evaluating a quota / reservation policy.
// Mirrors gateway model.ResourceBinding 1:1 (JSON tags identical) — a
// drift makes the policy evaluator silently miss bindings at admission
// time.
type ResourceBinding struct {
	BindingID       string `json:"binding_id,omitempty"`
	ScopeType       string `json:"scope_type"`
	ScopeID         string `json:"scope_id"`
	Metric          string `json:"metric"`
	UnitKind        string `json:"unit_kind,omitempty"`
	StateSemantic   string `json:"state_semantic,omitempty"`
	ReservationMode string `json:"reservation_mode,omitempty"`
	PeriodKind      string `json:"period_kind,omitempty"`
	SourcePolicyID  string `json:"source_policy_id,omitempty"`
	LineageRole     string `json:"lineage_role,omitempty"`
}

// AllowanceBinding pins one Redis allowance-flag lookup the gateway must
// perform at admission time. JSON tags MUST match gateway
// model.AllowanceBinding exactly — a mismatch makes the AllowanceChecker
// skip enforcement silently. Cross-repo regression pinned by
// gateway internal/configsync/publisher_schema_test.go.
type AllowanceBinding struct {
	Scope   string `json:"scope"`
	ScopeID string `json:"scope_id"`
	Period  string `json:"period"`
}

// GuardrailPolicy bounds per-request payload shape before quota evaluation.
//
// MaxOutputTokens caps the value of the user request's `max_tokens` field
// (i.e. the max completion tokens the model is asked to generate) — gateway
// rejects with `guardrail_max_tokens_exceeded` if the caller asks for more
// than this. Renamed from MaxMaxTokens to remove the double "Max" — wire
// tag is the canonical name; legacy "max_max_tokens" is dropped (gateway
// commit 6a8c64b cut over on 2026-05-14).
type GuardrailPolicy struct {
	MaxMessages     int   `json:"max_messages,omitempty"`
	MaxOutputTokens int   `json:"max_output_tokens,omitempty"`
	AllowTools      *bool `json:"allow_tools,omitempty"`
	AllowVision     *bool `json:"allow_vision,omitempty"`
}

// EffectivePolicy is the runtime form of a rate/concurrency limit produced
// by the control-plane compiler. Gateway reads (scope, scope_id, metric,
// period_*) to derive Redis runtime keys.
type EffectivePolicy struct {
	Version         int               `json:"version"`
	PolicyID        string            `json:"policy_id"`
	PolicyType      string            `json:"policy_type"`                // "rate_limit" / "concurrency"
	ControlSemantic string            `json:"control_semantic,omitempty"` // "hard_cutoff" / "settle_only"
	SubjectSide     string            `json:"subject_side,omitempty"`
	Metric          string            `json:"metric"` // "rpm" / "tpm" / "cc"
	Scope           string            `json:"scope"`  // "key" / "workspace" / "tenant" / ...
	ScopeID         string            `json:"scope_id"`
	PeriodKind      string            `json:"period_kind,omitempty"` // "minute" / "hour" / "day" / "month" / ""
	PeriodID        string            `json:"period_id,omitempty"`
	Window          string            `json:"window,omitempty"`
	LimitValue      int64             `json:"limit_value"`
	Unlimited       bool              `json:"unlimited,omitempty"`
	EnforcementMode string            `json:"enforcement_mode,omitempty"` // "enforce" / "shadow"
	Conditions      PolicyConditions  `json:"conditions,omitempty"`
	RuntimeKeys     PolicyRuntimeKeys `json:"runtime_keys,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
}

type PolicyConditions struct {
	Models        []string `json:"models,omitempty"`
	Providers     []string `json:"providers,omitempty"`
	Protocols     []string `json:"protocols,omitempty"`
	CallTypes     []string `json:"call_types,omitempty"`
	EffectiveFrom string   `json:"effective_from,omitempty"`
	EffectiveTo   string   `json:"effective_to,omitempty"`
}

type PolicyRuntimeKeys struct {
	Family        string `json:"family,omitempty"`
	PendingSuffix string `json:"pending_suffix,omitempty"`
	SettledSuffix string `json:"settled_suffix,omitempty"`
	CountSuffix   string `json:"count_suffix,omitempty"`
	LeaseSuffix   string `json:"lease_suffix,omitempty"`
	MetaSuffix    string `json:"meta_suffix,omitempty"`
}

// ============================================================
// ModelCatalog — unit 5
// ============================================================

type ModelCatalogEntry struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。当前值由
	// publisher fillModelCatalogEntryDefaults 自动填,调用方传 0 即可。
	// 跟 gateway internal/configsync/schema_versions.go 完全同步。
	SchemaVersion      int                           `json:"schema_version,omitempty"`
	Revision           int64                         `json:"revision"`
	ModelID            string                        `json:"model_id"`
	DisplayName        string                        `json:"display_name"`
	Capabilities       []string                      `json:"capabilities"`
	ContextWindow      int                           `json:"context_window"`
	MaxOutput          int                           `json:"max_output"`
	RequestParamPolicy map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
	OwnedBy            string                        `json:"owned_by"`
}

// ============================================================
// ModelRoute — unit 6
// ============================================================

type RouteConfig struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。同步 gateway
	// internal/configsync/schema_versions.go RouteConfigSchemaVersion。
	SchemaVersion int    `json:"schema_version,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	ModelID       string `json:"model_id"`
	// AliasTarget marks this entry as an equivalence pointer: the model is
	// not a routable destination on its own; the gateway treats requests to
	// ModelID as requests to AliasTarget (no fallback metering). Empty when
	// the model has real routes of its own.
	AliasTarget      string            `json:"alias_target,omitempty"`
	Providers        []RouteEntry      `json:"providers"`
	FallbackModel    string            `json:"fallback_model,omitempty"`
	ProviderStrategy string            `json:"provider_strategy,omitempty"`
	DefaultPolicy    RoutePolicyConfig `json:"default_policy,omitempty"`
}

type RouteEntry struct {
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	Priority      int    `json:"priority"`
	Weight        int    `json:"weight"`
	Status        string `json:"status"`
	APIVersion    string `json:"api_version,omitempty"`
	// AnthropicBeta opts the route into Anthropic's beta header contract,
	// e.g. "prompt-caching-2024-07-31".
	AnthropicBeta string `json:"anthropic_beta,omitempty"`
	// Adapter field removed. Per-route bundle override is no longer supported;
	// provider protocol_bindings determine the adapter bundle. Old payloads
	// carrying "adapter" are silently dropped by encoding/json (unknown field
	// tolerance).
	// Tags removed 2026-05-16: tag-based routing was never wired into a
	// selector on the gateway side (T60). Reintroduce when an ABAC
	// routing requirement appears with a concrete consumer.
	// Capabilities optionally narrows/overrides the model_catalog capability
	// set for this provider route. Empty preserves legacy catalog-level caps.
	Capabilities       []string                      `json:"capabilities,omitempty"`
	ComplianceTags     []string                      `json:"compliance_tags,omitempty"`
	Compliance         CompliancePolicy              `json:"compliance,omitempty"`
	RequestParamPolicy map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
}

type RoutePolicyConfig struct {
	PreferredProviders []string `json:"preferred_providers,omitempty"`
	// AllowFallback toggles whether the gateway routing planner expands
	// cross-model fallback (model A failed → try model A's fallback_model
	// chain). nil ⇒ fall back to PlatformConfig.DefaultRoutePolicy; explicit
	// false hard-disables fallback for this route regardless of platform
	// default.
	AllowFallback        *bool            `json:"allow_fallback,omitempty"`
	RequiredCapabilities []string         `json:"required_capabilities,omitempty"`
	RequiredParams       []string         `json:"required_params,omitempty"`
	ComplianceTags       []string         `json:"compliance_tags,omitempty"`
	Compliance           CompliancePolicy `json:"compliance,omitempty"`
}

type CompliancePolicy struct {
	Region            string `json:"region,omitempty"`
	ZeroDataRetention *bool  `json:"zero_data_retention,omitempty"`
	NoTraining        *bool  `json:"no_training,omitempty"`
}

// ============================================================
// KeyPool — unit 7
// ============================================================

// KeyPoolMeta is the pool-level metadata (Strategy + Revision) stored once
// per provider in the gw:keypool_meta Hash. Each individual upstream key
// lives in a separate Hash gw:keypool:{provider_id} keyed by key_id —
// see KeyPoolEntry. This split lets writers add/remove/disable one
// upstream key without rewriting the whole pool.
type KeyPoolMeta struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。同步 gateway
	// internal/configsync/schema_versions.go KeyPoolMetaSchemaVersion。
	SchemaVersion int    `json:"schema_version,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	ProviderID    string `json:"provider_id"`
	Strategy      string `json:"strategy,omitempty"`
}

// KeyPoolEntry is one upstream API key within a provider's pool. Stored as
// one field in gw:keypool:{provider_id} (field=KeyID).
type KeyPoolEntry struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。同步 gateway
	// internal/configsync/schema_versions.go KeyPoolEntrySchemaVersion。
	SchemaVersion int `json:"schema_version,omitempty"`
	// Revision 让网关 admindebug 看到 per-entry 版本号,后续 stale-skip 用。
	// 跟 sibling configsync types 对齐(gateway commit 5f2cf3e)。
	Revision int64  `json:"revision,omitempty"`
	KeyID    string `json:"key_id"`
	// ProviderAccountRef is the stable, non-secret opaque ID issued by the
	// control-plane account registry for reconciliation. Publisher validates
	// only the bounded opaque-ID shape; the caller/registry must verify
	// ownership. It is deliberately independent from KeyID/ciphertext so
	// credential rotation does not change the account identity. Do not
	// populate it with an API key, BaseURL, request ID, or credential ID.
	ProviderAccountRef string   `json:"provider_account_ref,omitempty"`
	Alias              string   `json:"alias,omitempty"`
	AllowedModels      []string `json:"allowed_models,omitempty"`
	Ciphertext         string   `json:"ciphertext"`
	Priority           int      `json:"priority"`
	Weight             int      `json:"weight"`
	RPMLimit           int      `json:"rpm_limit"`
	TPMLimit           int      `json:"tpm_limit"`
	Status             string   `json:"status"`
}

// KeyPoolConfig bundles meta + entries for callers that want to ship a
// whole pool in one shot (initial sync / disaster recovery). Per-key
// updates should use PublishKeyPoolEntry / DeleteKeyPoolEntry instead.
type KeyPoolConfig struct {
	Meta    KeyPoolMeta    `json:"meta"`
	Entries []KeyPoolEntry `json:"entries"`
}

// ============================================================
// Provider — unit 8
// ============================================================

const (
	ProviderVisibilityProductRoute  = "product_route"
	ProviderVisibilityDedicatedOnly = "dedicated_only"
	ProviderVisibilityInternal      = "internal"

	ProviderCredentialSourceKeypool             = "keypool"
	ProviderCredentialSourceDedicatedUpstream   = "dedicated_upstream"
	ProviderCredentialSourceProviderEntitlement = "provider_entitlement"

	ProviderRouteKindDedicated = "dedicated_route"
	ProviderRouteKindModel     = "model_route"
	ProviderRouteKindProduct   = "product_route"
)

func KnownProviderVisibilities() []string {
	return []string{
		ProviderVisibilityProductRoute,
		ProviderVisibilityDedicatedOnly,
		ProviderVisibilityInternal,
	}
}

func KnownProviderCredentialSources() []string {
	return []string{
		ProviderCredentialSourceKeypool,
		ProviderCredentialSourceDedicatedUpstream,
		ProviderCredentialSourceProviderEntitlement,
	}
}

func KnownProviderRouteKinds() []string {
	return []string{
		ProviderRouteKindDedicated,
		ProviderRouteKindModel,
		ProviderRouteKindProduct,
	}
}

type OwnerScope struct {
	TenantID      string `json:"tenant_id,omitempty"`
	ProductID     string `json:"product_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

type ProviderInfo struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。同步 gateway
	// internal/configsync/schema_versions.go ProviderInfoSchemaVersion。
	SchemaVersion int    `json:"schema_version,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	ID            string `json:"id"`
	Code          string `json:"code,omitempty"`
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	Status        string `json:"status"`
	// KeyStrategy is the per-provider key-pool selector strategy. Gateway
	// T58 (2026-05-15) folded the standalone gw:keypool_meta Hash into
	// ProviderInfo so all provider-level config lives in one place.
	//
	// Allowed values: "priority" and "weight". Empty falls back to the
	// planner default key selector.
	KeyStrategy string `json:"key_strategy,omitempty"`
	// ProviderProfile and OwnedBy are endpoint identity facts used by gateway
	// planner decisions. ProtocolBindings remain the routing contract.
	ProviderProfile string `json:"provider_profile,omitempty"`
	OwnedBy         string `json:"owned_by,omitempty"`
	// ProviderAccountRef is an optional account-registry identity propagated for
	// provider-level credential lanes such as provider_entitlement. Ordinary
	// keypool traffic remains authoritative at KeyPoolEntry.ProviderAccountRef.
	// Publisher validates only the bounded opaque-ID shape, never account ownership.
	ProviderAccountRef string `json:"provider_account_ref,omitempty"`

	// Provider isolation metadata is a schema/contract surface. Runtime
	// enforcement is owned by gateway planner follow-up work.
	Visibility        string      `json:"visibility,omitempty"`
	CredentialSource  string      `json:"credential_source,omitempty"`
	AllowedRouteKinds []string    `json:"allowed_route_kinds,omitempty"`
	OwnerScope        *OwnerScope `json:"owner_scope,omitempty"`

	// ProtocolBindings is the explicit provider protocol-routing contract and
	// the sole routing input written to gw:providers. Publisher validates
	// against a gateway enum/registry snapshot before writing.
	ProtocolBindings []ProtocolBinding `json:"protocol_bindings,omitempty"`
	// DefaultProtocolBinding is retained as a named default reference. It must
	// reference an existing ProtocolBindings entry, but gateway routing still
	// requires an explicit accepts tuple for every inbound protocol/contract.
	DefaultProtocolBinding string `json:"default_protocol_binding,omitempty"`

	// InboundProtocols and ClientCompatibilities are DERIVED observation-only
	// views (ADR-0012): the set of inbound Wire dialects this provider serves =
	// union of ProtocolBindings[].Accepts[].Protocol, and the client-App labels
	// that speak those dialects. They are NOT compile inputs and NOT consumed by
	// gateway routing (routing uses Accepts directly); they exist for
	// observability/reporting. Omitted when empty.
	InboundProtocols      []string `json:"inbound_protocols,omitempty"`
	ClientCompatibilities []string `json:"client_compatibilities,omitempty"`
}

// ============================================================
// PlatformConfig — unit 9
// ============================================================

type PlatformConfig struct {
	// SchemaVersion 让 gateway reader 跨版本路由 decoder。同步 gateway
	// internal/configsync/schema_versions.go PlatformConfigSchemaVersion。
	SchemaVersion int   `json:"schema_version,omitempty"`
	Revision      int64 `json:"revision"`
	// PlatformRPM / PlatformCC are NODE-LOCAL caps (gateway T53, 2026-05-15):
	// each gateway pod enforces these in its own process bucket / lock,
	// they are NOT a cluster-wide cap. A 10-pod fleet with platform_rpm=60000
	// admits up to 600000 rpm total. Producers should set the value to the
	// per-pod ceiling, not the cluster ceiling.
	PlatformRPM             int    `json:"platform_rpm"`
	PlatformCC              int    `json:"platform_cc"`
	DefaultProviderStrategy string `json:"default_provider_strategy"`
	// DefaultRoutePolicy is the platform-level fallback when a model's
	// RouteConfig.DefaultPolicy leaves a field nil. Most importantly
	// AllowFallback — null in DefaultPolicy means "use this platform default".
	DefaultRoutePolicy DefaultRoutePolicyConfig `json:"default_route_policy,omitempty"`
	// DefaultKeyPoolStrategy is the platform-level keypool strategy fallback.
	// Gateway currently does not consume it (per-provider ProviderInfo.KeyStrategy
	// wins); kept for future use and back-compat. T49 decision pending.
	DefaultKeyPoolStrategy string           `json:"default_keypool_strategy,omitempty"`
	DefaultTimeoutMs       int              `json:"default_timeout_ms"`
	StreamTimeoutMs        int              `json:"stream_timeout_ms"`
	FirstTokenTimeoutMs    int              `json:"first_token_timeout_ms"`
	AttemptTimeoutMs       int              `json:"attempt_timeout_ms"`
	MaxBodySize            int              `json:"max_body_size"`
	MaxFallbackDepth       int              `json:"max_fallback_depth"`
	DisclosurePolicy       DisclosurePolicy `json:"disclosure_policy,omitempty"`

	// ProviderBudgetPolicies carries published provider/account/model level
	// rate-limit and concurrency policies. Gateway's CombinedProviderGate
	// reads this slice and turns each EffectivePolicy into a runtime
	// check; an empty slice disables all provider-side budget enforcement.
	// Reuses the same EffectivePolicy schema as KeyMeta.Policies — the
	// only difference is which gate evaluates them.
	ProviderBudgetPolicies []EffectivePolicy `json:"provider_budget_policies,omitempty"`

	// Auth is the runtime-tunable subset of gateway pre-key-validation
	// defenses (PreAuth IP rate limit, AbuseDetector thresholds, Idempotency
	// TTL). Hot-applied on the gateway side without restart; zero values
	// keep the gateway's cold-start yaml defaults.
	Auth AuthRuntimeConfig `json:"auth,omitempty"`

	// RetryBudget caps how aggressively the gateway retries a failing
	// request across keys, providers, and wall-clock time. Zero on all
	// three axes lets the gateway fall back to its production-safe
	// in-code defaults; non-zero on any axis treats the rest as
	// "unlimited for that axis" per spec.
	RetryBudget RetryBudgetConfig `json:"retry_budget,omitempty"`

	// ResponsesAffinityEnabled gates the OpenAI Responses API stateful
	// session pinning (gw:responses:owner:{response_id}). nil ⇒ true
	// (default-on). Pointer type lets the caller omit the field without
	// flipping the gate off. Set to a pointer-to-false only for emergency
	// disable / testing; production should normally leave nil.
	ResponsesAffinityEnabled *bool `json:"responses_affinity_enabled,omitempty"`

	// Operational selects which ScopeAttrs key gateway uses for operational
	// scaffolding (idempotency isolation, Kafka event partitioning,
	// full-detail event filtering). Each field defaults to "tenant_id" so
	// existing deployments need no migration; teams running region-sharded
	// or workspace-isolated topologies can re-aim these without code change.
	Operational OperationalAttrs `json:"operational,omitempty"`

	// IngressProductAssertions is the G7 / ADR-0010 first admission layer:
	// per-ingress-gate product assertions consumed by the gateway's
	// ProductContextChecker (gateway SetIngressAssertions, Phase A
	// 4044847). A gate whose pattern is not listed here is UNASSERTED and
	// passes unconditionally — the layer is fail-open by construction, so
	// leaving this field empty (the default) keeps the published payload
	// byte-identical to pre-G7 output and gateway behavior unchanged.
	// Design: tokenplatform-spec design/G7-product-context-admission.md §4.
	IngressProductAssertions []IngressProductAssertion `json:"ingress_product_assertions,omitempty"`
}

// IngressAssertion modes. Empty/absent mode means warn — fail-open by
// default; enforce must always be an explicit, deliberate publish. Values
// mirror gateway model.GateAssertionModeWarn / GateAssertionModeEnforce;
// the gateway additionally treats any unknown mode as warn (safety net for
// version skew or admin-direct Redis writes).
const (
	IngressAssertionModeWarn    = "warn"
	IngressAssertionModeEnforce = "enforce"
)

// IngressProductAssertion asserts "this ingress gate serves these
// products". Pattern is the gateway decoder's STATIC registration pattern
// (e.g. "POST /api/coding/paas/v4/chat/completions", "GET /v1/models" —
// the exact string handed to ServeMux, never a runtime-parsed path).
// AllowedProducts is the SET of product_ids allowed through the gate:
// shared gates like "POST /v1/messages" legitimately carry several
// products — collapsing a shared gate to a single product would 403 whole
// classes of legal traffic once enforced (design risk R2).
type IngressProductAssertion struct {
	Pattern         string   `json:"pattern"`
	AllowedProducts []string `json:"allowed_products"`
	Mode            string   `json:"mode,omitempty"`
}

// OperationalAttrs configures which ScopeAttrs dimension gateway uses as
// the partition / isolation / matching id for each operational subsystem.
// Empty fields fall back to "tenant_id" inside gateway.
type OperationalAttrs struct {
	// IdempotencyAttr is the ScopeAttrs key whose value gateway includes in
	// the idempotency scope prefix. Two requests with identical
	// Idempotency-Key but different values of this attribute are treated as
	// separate requests.
	IdempotencyAttr string `json:"idempotency_attr,omitempty"`

	// KafkaPartitionAttr is the ScopeAttrs key whose value gateway uses as
	// the partition key when publishing events to Kafka.
	KafkaPartitionAttr string `json:"kafka_partition_attr,omitempty"`

	// FullDetailAttr is the ScopeAttrs key gateway matches against its
	// configured full-detail allowlist for verbose process_event emission.
	FullDetailAttr string `json:"full_detail_attr,omitempty"`
}

// DefaultRoutePolicyConfig is the platform-level RoutePolicy fallback.
// Currently exposes only the high-impact AllowFallback flag; other fields
// can be added incrementally without breaking existing payloads.
type DefaultRoutePolicyConfig struct {
	AllowFallback *bool `json:"allow_fallback,omitempty"`
}

// AuthRuntimeConfig mirrors the gateway's auth runtime knobs that the
// control plane can flip without a gateway restart. Field names match the
// gateway yaml schema so a cold-start configuration can mirror this
// payload exactly. Zero-valued fields signal "keep current gateway
// default" — they do NOT zero out a previously-applied value.
type AuthRuntimeConfig struct {
	PreAuthIPRPM       int `json:"pre_auth_ip_rpm,omitempty"`
	PreAuthIPWindowMs  int `json:"pre_auth_ip_window_ms,omitempty"`
	AbuseFailThreshold int `json:"abuse_fail_threshold,omitempty"`
	AbuseWindowSec     int `json:"abuse_window_sec,omitempty"`
	AbuseBanTTLSec     int `json:"abuse_ban_ttl_sec,omitempty"`
	IdempotencyTTLSec  int `json:"idempotency_ttl_sec,omitempty"`
}

// RetryBudgetConfig captures the three independent retry-budget axes the
// gateway executor enforces. Production-typical values:
// max_keys_per_provider=1..2, max_providers=3, total_wall_time_ms=5000.
// All-zero invokes the gateway's in-code defaults.
type RetryBudgetConfig struct {
	MaxKeysPerProvider int   `json:"max_keys_per_provider,omitempty"`
	MaxProviders       int   `json:"max_providers,omitempty"`
	TotalWallTimeMs    int64 `json:"total_wall_time_ms,omitempty"`
}

// ============================================================
// ProductRuntimePolicy — platform-domain runtime boundary
// ============================================================

type ProductRuntimePolicy struct {
	SchemaVersion                   int                `json:"schema_version,omitempty"`
	Revision                        int64              `json:"revision,omitempty"`
	ProductID                       string             `json:"product_id"`
	ProductSlug                     string             `json:"product_slug,omitempty"`
	Status                          string             `json:"status"`
	CredentialSource                string             `json:"credential_source"`
	AllowedProviderIDs              []string           `json:"allowed_provider_ids"`
	AllowedChannelIDs               []string           `json:"allowed_channel_ids"`
	AllowedProviderProfiles         []string           `json:"allowed_provider_profiles"`
	ModelScopeMode                  string             `json:"model_scope_mode"`
	AllowedModels                   []string           `json:"allowed_models"`
	FallbackPolicy                  string             `json:"fallback_policy"`
	BillingMode                     string             `json:"billing_mode"`
	RouteStrategy                   string             `json:"route_strategy,omitempty"`
	AllowHighLossBridgeWhenNoNative bool               `json:"allow_high_loss_bridge_when_no_native,omitempty"`
	AllowBridgeOnNativeFailure      bool               `json:"allow_bridge_on_native_failure,omitempty"`
	RouteGraphRequired              bool               `json:"route_graph_required,omitempty"`
	RouteGraphEnforcement           string             `json:"route_graph_enforcement,omitempty"`
	RouteGraph                      *ProductRouteGraph `json:"route_graph,omitempty"`
	UpdatedAt                       string             `json:"updated_at,omitempty"`
}

type ProductPolicyDelete struct {
	ProductID string `json:"product_id"`
	Revision  int64  `json:"revision"`
}

type ProductRouteGraph struct {
	SchemaVersion   int                      `json:"schema_version"`
	DefaultTarget   ProductRouteTarget       `json:"default_target"`
	Groups          []ProductRouteGroup      `json:"groups"`
	ProductChannels []ProductRouteChannel    `json:"product_channels"`
	Assignments     []ProductRouteAssignment `json:"assignments"`
}

type ProductRouteTarget struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

type ProductRouteGroup struct {
	ID       string                    `json:"id"`
	Code     string                    `json:"code"`
	Status   string                    `json:"status"`
	Strategy string                    `json:"strategy"`
	Members  []ProductRouteGroupMember `json:"members"`
}

type ProductRouteGroupMember struct {
	ProductChannelID string `json:"product_channel_id"`
	ChannelID        string `json:"channel_id"`
	ProviderID       string `json:"provider_id"`
	Priority         int    `json:"priority"`
	Weight           int    `json:"weight"`
	Role             string `json:"role"`
	MaxRPM           int    `json:"max_rpm,omitempty"`
	MaxTPM           int    `json:"max_tpm,omitempty"`
}

type ProductRouteChannel struct {
	ID            string   `json:"id"`
	ChannelID     string   `json:"channel_id"`
	ProviderID    string   `json:"provider_id"`
	AllowedModels []string `json:"allowed_models"`
	DeniedModels  []string `json:"denied_models"`
	Priority      int      `json:"priority"`
	Weight        int      `json:"weight"`
	MaxRPM        int      `json:"max_rpm,omitempty"`
	MaxTPM        int      `json:"max_tpm,omitempty"`
	Status        string   `json:"status"`
}

type ProductRouteAssignment struct {
	ID                  string `json:"id"`
	ProductID           string `json:"product_id"`
	SubjectType         string `json:"subject_type"`
	SubjectID           string `json:"subject_id"`
	ModelPattern        string `json:"model_pattern,omitempty"`
	ClientCompatibility string `json:"client_compatibility,omitempty"`
	TargetType          string `json:"target_type"`
	TargetID            string `json:"target_id"`
	Mode                string `json:"mode"`
	Priority            int    `json:"priority"`
}

type DisclosurePolicy struct {
	Mode      string               `json:"mode,omitempty"`
	Overrides *DisclosureOverrides `json:"overrides,omitempty"`
}

type DisclosureOverrides struct {
	ExposeProviderName           *bool  `json:"expose_provider_name,omitempty"`
	ExposeProviderAccount        *bool  `json:"expose_provider_account,omitempty"`
	ExposeUpstreamStatus         *bool  `json:"expose_upstream_status,omitempty"`
	ExposeUpstreamRawBody        *bool  `json:"expose_upstream_raw_body,omitempty"`
	ExposeActualModel            *bool  `json:"expose_actual_model,omitempty"`
	PassThroughSystemFingerprint *bool  `json:"passthrough_system_fingerprint,omitempty"`
	ModelFieldStrategy           string `json:"model_field_strategy,omitempty"`
}

// ============================================================
// ProviderOverride — unit 10
// ============================================================

type ProviderOverride struct {
	Revision       int64           `json:"revision"`
	Scope          string          `json:"scope"`
	Action         string          `json:"action"`
	Param          json.RawMessage `json:"param,omitempty"`
	EffectiveFrom  int64           `json:"effective_from"`
	EffectiveUntil *int64          `json:"effective_until,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	CreatedBy      string          `json:"created_by,omitempty"`
	CreatedAt      int64           `json:"created_at,omitempty"`
}

// ============================================================
// Bulk item helpers
// ============================================================

type KeyBalanceItem struct {
	KeyID      KeyID `json:"key_id"`
	HasBalance bool  `json:"has_balance"`
	Revision   int64 `json:"revision"`
}

// KeyAdmissionDecision is the ordering identity supplied by Billing for a
// key admission projection. DecisionVersion is a Billing-owned, per-key
// monotonic watermark; it is deliberately independent from Publisher's
// internal revision sequence. Legacy is true only for the compatibility
// surface that accepted revision (or no ordering fields) before this
// contract existed.
type KeyAdmissionDecision struct {
	ID      string
	Version int64
	Legacy  bool
}

// KeyAdmissionItem is one versioned entry in a bulk Billing projection.
type KeyAdmissionItem struct {
	KeyID      KeyID
	HasBalance bool
	Reason     string
	Decision   KeyAdmissionDecision
}

type ModelCatalogItem struct {
	ModelID string             `json:"model_id"`
	Entry   *ModelCatalogEntry `json:"entry"`
}

type ModelRouteItem struct {
	ModelID string       `json:"model_id"`
	Route   *RouteConfig `json:"route"`
}

// KeyPoolItem is the bulk item for whole-pool replacement (init/DR).
type KeyPoolItem struct {
	ProviderID string         `json:"provider_id"`
	Pool       *KeyPoolConfig `json:"pool"`
}

// KeyPoolEntryItem is the bulk item for per-entry upsert across providers.
type KeyPoolEntryItem struct {
	ProviderID string        `json:"provider_id"`
	Entry      *KeyPoolEntry `json:"entry"`
}

// KeyPoolEntryDeleteItem is the bulk item for per-entry delete across providers.
type KeyPoolEntryDeleteItem struct {
	ProviderID string `json:"provider_id"`
	KeyID      string `json:"key_id"`
}

type ProviderOverrideItem struct {
	Scope    string            `json:"scope"`
	Override *ProviderOverride `json:"override"`
}

// MaxBulkItems caps any *bulk* method input array. Matches publisher API
// reference §2.6.
const MaxBulkItems = 100
