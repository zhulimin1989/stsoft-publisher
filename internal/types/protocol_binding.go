package types

// ProtocolBinding is one explicit provider protocol-routing contract entry
// (design doc 2026-05-27-binding-based-routing-redesign §7.1 / §21.4). It
// declares which inbound (protocol, capability, request_contract, logical
// model?) requests this provider accepts and how the upstream call is wired
// (outbound_protocol / path_prefix / auth_scheme / translator).
//
// This replaces the coarse legacy api_types tag list. protocol_bindings is the
// only provider routing input (§16 / §21.3 schema v2).
//
// JSON tags MUST match gateway configsync.ProtocolBinding exactly — cross-repo
// drift makes the gateway BindingResolver silently miss bindings. Enum,
// endpoint, and translator parity is pinned by the synced
// protocol_bindings_contract.json fixture.
type ProtocolBinding struct {
	// Name is provider-unique and stable. Used only for trace / metrics /
	// default_protocol_binding references — code must NOT parse semantics
	// out of the name string.
	Name string `json:"name"`
	// Priority disambiguates overlapping bindings; larger = more preferred.
	// Omitted when zero (zero = lowest / unset).
	Priority int `json:"priority,omitempty"`
	// Accepts lists the inbound (protocol, capabilities, contract, models?)
	// tuples this binding serves. Non-empty.
	Accepts []ProtocolBindingAccept `json:"accepts,omitempty"`
	// OutboundProtocol is the upstream wire protocol key gateway uses to look
	// up ProtocolRegistry / TranslationMatrix.
	OutboundProtocol string `json:"outbound_protocol,omitempty"`
	// PathPrefix is the provider/binding-level path root; may be empty.
	// Final path = base_url + path_prefix + protocol.path_template.
	PathPrefix string `json:"path_prefix,omitempty"`
	// QueryTemplate is the binding-level query string template appended by the
	// gateway when the outbound protocol requires query parameters.
	QueryTemplate string `json:"query_template,omitempty"`
	// AuthScheme is the gateway AuthSchemes key. Required by gateway and
	// publisher validation.
	AuthScheme string `json:"auth_scheme,omitempty"`
	// TranslatorKey is the gateway TranslationMatrix key used for cross-protocol
	// body conversion. Empty means gateway decides same-protocol passthrough;
	// cross-protocol bindings should set it explicitly.
	TranslatorKey string `json:"translator,omitempty"`
	// Bridge is the explicit cross-protocol bridge whitelist entry for this
	// binding. Supported values are exported by ProtocolBindingsContract.bridges.
	Bridge string `json:"bridge,omitempty"`
	// CompatAPIType carries the vendor identity now that outbound_protocol is
	// a pure wire dialect (G4 P2 / ADR-0009). The gateway resolves the
	// adapter bundle from it (P0 AdapterBundle stamping:
	// providerprofile.ResolveAPITypeSpec(CompatAPIType).BundleKey), which is
	// what keeps vendor body/query semantics — zhipu thinking/tool_stream
	// injection, azure api-version — alive on outbound_protocol=openai
	// bindings. Retired only when the explicit adapter_bundle field lands
	// (G4 P4-A).
	CompatAPIType string `json:"compat_api_type,omitempty"`

	// CapabilityProfiles names reusable gateway-side capability fragments.
	CapabilityProfiles []string `json:"capability_profiles,omitempty"`
	// Supported/Unsupported entries form binding-local capability overrides.
	SupportedFeatures   []string            `json:"supported_features,omitempty"`
	UnsupportedFeatures []string            `json:"unsupported_features,omitempty"`
	SupportedParams     []string            `json:"supported_params,omitempty"`
	UnsupportedParams   []string            `json:"unsupported_params,omitempty"`
	ParamConstraints    map[string][]string `json:"param_constraints,omitempty"`
	FeatureConstraints  map[string]string   `json:"feature_constraints,omitempty"`
	// CombinationConstraints captures cross-param rules such as
	// stream_options.include_usage requiring stream=true.
	CombinationConstraints []CombinationConstraint `json:"combination_constraints,omitempty"`
	// RequestParamPolicy describes model/binding-scoped client parameter
	// compatibility rules, e.g. fixed sampling values for a model family.
	RequestParamPolicy map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
	CompatibilityMode  *CompatibilityMode            `json:"compatibility_mode,omitempty"`
	FieldDisposition   *FieldDispositionConfig       `json:"field_disposition,omitempty"`
	// RequestCorrelationMode selects a gateway-owned, provider-audit
	// correlation mechanism. It is an enum from the vendored gateway contract;
	// publisher must not infer it from provider id or base URL.
	RequestCorrelationMode string `json:"request_correlation_mode,omitempty"`
}

// ProtocolBindingAccept is one inbound acceptance tuple of a ProtocolBinding.
type ProtocolBindingAccept struct {
	// Protocol is the inbound protocol the gateway ingress decoder produced
	// (openai / anthropic / gemini / responses). Pure wire dialects only —
	// vendor values (glm / glm_coding / ...) are no longer emitted (G4 P2)
	// and are read-side normalized by the gateway during the alias window.
	// Controlled enum, gateway-owned.
	Protocol string `json:"protocol,omitempty"`
	// Capabilities is the non-empty set of capabilities this entry accepts
	// (chat / embed / rerank / count_tokens / ...).
	Capabilities []string `json:"capabilities,omitempty"`
	// RequestContract distinguishes same-(protocol,capability) API semantics
	// (chat_completions vs responses vs anthropic_messages ...). Required;
	// gateway-owned enum.
	RequestContract string `json:"request_contract,omitempty"`
	// Models is an optional logical-model allowlist. Empty ⇒ entry applies to
	// every model under this provider; non-empty ⇒ per-(provider×model) binding.
	Models []string `json:"models,omitempty"`
}

// CombinationConstraint describes cross-param rules that cannot be expressed
// by a single param allowlist or value enum.
type CombinationConstraint struct {
	When      map[string]string `json:"when,omitempty"`
	Requires  map[string]string `json:"requires,omitempty"`
	Conflicts map[string]string `json:"conflicts,omitempty"`
}

// RequestParamPolicy is the publisher-side wire twin of the gateway
// capabilityprofile.RequestParamPolicy.
type RequestParamPolicy struct {
	Category         string   `json:"category,omitempty"`
	UpstreamBehavior string   `json:"upstream_behavior,omitempty"`
	FixedValue       string   `json:"fixed_value,omitempty"`
	AllowedValues    []string `json:"allowed_values,omitempty"`
	OnEqual          string   `json:"on_equal,omitempty"`
	OnMismatch       string   `json:"on_mismatch,omitempty"`
}

// CompatibilityMode controls whether gateway may apply client-compatibility
// actions such as omit_with_warning / normalize_with_warning.
type CompatibilityMode struct {
	OpenAIClientDefaults         *bool `json:"openai_client_defaults,omitempty"`
	DropUnsupportedDefaultParams *bool `json:"drop_unsupported_default_params,omitempty"`
	RejectSemanticParams         *bool `json:"reject_semantic_params,omitempty"`
}

// FieldDispositionConfig records request/response field handling evidence.
type FieldDispositionConfig struct {
	Request  map[string]string `json:"request,omitempty"`
	Response map[string]string `json:"response,omitempty"`
}
