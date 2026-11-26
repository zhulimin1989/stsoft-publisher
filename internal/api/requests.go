package api

import (
	"encoding/json"
	"fmt"

	"github.com/tokenplatform/publisher/internal/types"
)

// ============================================================
// Request DTOs — caller-facing API parameter entities
// ============================================================
//
// These types mirror the wire types in internal/types/dataplane.go but
// deliberately omit publisher-internal fields:
//
//   - `revision`        ⇒ publisher fills with time.Now().UnixMicro()
//   - `schema_version`  ⇒ publisher fills with publisher.CurrentKeyMetaSchemaVersion
//   - `created_at`      ⇒ publisher fills with server clock (ProviderOverride)
//
// 2026-05-16 directive: API parameter entities must be separated from
// business / wire entities. The HTTP surface stays stable even if the
// publisher needs to add another internal field to a wire struct, and
// callers can't accidentally feed a stale revision through the API.
//
// Internal callers that need to construct a wire type directly (DR
// replay, unit tests) still use the types package — only the HTTP
// surface goes through these request DTOs.

// ----------------- KeyMeta (user) -----------------

// KeyMetaRequest is the body shape for POST /user/api/v1/keys
// (single + bulk). The corresponding wire type is types.KeyMeta.
type KeyMetaRequest struct {
	KeyID                 string                             `json:"key_id"`
	KeyHash               string                             `json:"key_hash"`
	Alias                 string                             `json:"alias,omitempty"`
	ScopeAttrs            map[string]string                  `json:"scope_attrs,omitempty"`
	Status                string                             `json:"status"`
	ExpiresAt             int64                              `json:"expires_at"`
	IPWhitelist           []string                           `json:"ip_whitelist"`
	EnforceModelWhitelist bool                               `json:"enforce_model_whitelist,omitempty"`
	AllowedModels         []string                           `json:"allowed_models"`
	Policies              []types.EffectivePolicy            `json:"policies,omitempty"`
	ResourceBindings      []types.ResourceBinding            `json:"resource_bindings,omitempty"`
	AllowanceBindings     []types.AllowanceBinding           `json:"allowance_bindings,omitempty"`
	MaxTokensPerCall      int64                              `json:"max_tokens_per_call,omitempty"`
	Guardrails            *types.GuardrailPolicy             `json:"guardrails,omitempty"`
	DedicatedUpstream     *types.DedicatedUpstreamCredential `json:"dedicated_upstream,omitempty"`
}

func (r *KeyMetaRequest) ToKeyMeta() *types.KeyMeta {
	if r == nil {
		return nil
	}
	return &types.KeyMeta{
		KeyID:                 r.KeyID,
		KeyHash:               r.KeyHash,
		Alias:                 r.Alias,
		ScopeAttrs:            r.ScopeAttrs,
		Status:                r.Status,
		ExpiresAt:             r.ExpiresAt,
		IPWhitelist:           r.IPWhitelist,
		EnforceModelWhitelist: r.EnforceModelWhitelist,
		AllowedModels:         r.AllowedModels,
		Policies:              r.Policies,
		ResourceBindings:      r.ResourceBindings,
		AllowanceBindings:     r.AllowanceBindings,
		MaxTokensPerCall:      r.MaxTokensPerCall,
		Guardrails:            r.Guardrails,
		DedicatedUpstream:     r.DedicatedUpstream,
	}
}

// ----------------- ModelCatalog (admin) -----------------

type ModelCatalogEntryRequest struct {
	ModelID            string                              `json:"model_id"`
	DisplayName        string                              `json:"display_name"`
	Capabilities       []string                            `json:"capabilities"`
	ContextWindow      int                                 `json:"context_window"`
	MaxOutput          int                                 `json:"max_output"`
	RequestParamPolicy map[string]types.RequestParamPolicy `json:"request_param_policy,omitempty"`
	OwnedBy            string                              `json:"owned_by"`
}

func (r *ModelCatalogEntryRequest) ToModelCatalogEntry() *types.ModelCatalogEntry {
	if r == nil {
		return nil
	}
	return &types.ModelCatalogEntry{
		ModelID:            r.ModelID,
		DisplayName:        r.DisplayName,
		Capabilities:       r.Capabilities,
		ContextWindow:      r.ContextWindow,
		MaxOutput:          r.MaxOutput,
		RequestParamPolicy: r.RequestParamPolicy,
		OwnedBy:            r.OwnedBy,
	}
}

type ModelCatalogItemRequest struct {
	ModelID string                    `json:"model_id"`
	Entry   *ModelCatalogEntryRequest `json:"entry"`
}

func (i ModelCatalogItemRequest) ToTypes() types.ModelCatalogItem {
	return types.ModelCatalogItem{ModelID: i.ModelID, Entry: i.Entry.ToModelCatalogEntry()}
}

// ----------------- ModelRoute (admin) -----------------

type RouteConfigRequest struct {
	ModelID          string                  `json:"model_id"`
	AliasTarget      string                  `json:"alias_target,omitempty"`
	Providers        []RouteEntryRequest     `json:"providers"`
	FallbackModel    string                  `json:"fallback_model,omitempty"`
	ProviderStrategy string                  `json:"provider_strategy,omitempty"`
	DefaultPolicy    types.RoutePolicyConfig `json:"default_policy,omitempty"`
}

func (r *RouteConfigRequest) ToRouteConfig() *types.RouteConfig {
	if r == nil {
		return nil
	}
	return &types.RouteConfig{
		ModelID:          r.ModelID,
		AliasTarget:      r.AliasTarget,
		Providers:        routeEntryRequestsToTypes(r.Providers),
		FallbackModel:    r.FallbackModel,
		ProviderStrategy: r.ProviderStrategy,
		DefaultPolicy:    r.DefaultPolicy,
	}
}

type RouteEntryRequest struct {
	ProviderID         string                              `json:"provider_id"`
	UpstreamModel      string                              `json:"upstream_model"`
	Priority           int                                 `json:"priority"`
	Weight             int                                 `json:"weight"`
	Status             string                              `json:"status"`
	APIVersion         string                              `json:"api_version,omitempty"`
	AnthropicBeta      string                              `json:"anthropic_beta,omitempty"`
	Capabilities       []string                            `json:"capabilities,omitempty"`
	ComplianceTags     []string                            `json:"compliance_tags,omitempty"`
	Compliance         types.CompliancePolicy              `json:"compliance,omitempty"`
	RequestParamPolicy map[string]types.RequestParamPolicy `json:"request_param_policy,omitempty"`
}

func (r RouteEntryRequest) ToRouteEntry() types.RouteEntry {
	return types.RouteEntry{
		ProviderID:         r.ProviderID,
		UpstreamModel:      r.UpstreamModel,
		Priority:           r.Priority,
		Weight:             r.Weight,
		Status:             r.Status,
		APIVersion:         r.APIVersion,
		AnthropicBeta:      r.AnthropicBeta,
		Capabilities:       r.Capabilities,
		ComplianceTags:     r.ComplianceTags,
		Compliance:         r.Compliance,
		RequestParamPolicy: r.RequestParamPolicy,
	}
}

func routeEntryRequestsToTypes(entries []RouteEntryRequest) []types.RouteEntry {
	if entries == nil {
		return nil
	}
	out := make([]types.RouteEntry, len(entries))
	for i, entry := range entries {
		out[i] = entry.ToRouteEntry()
	}
	return out
}

type ModelRouteItemRequest struct {
	ModelID string              `json:"model_id"`
	Route   *RouteConfigRequest `json:"route"`
}

func (i ModelRouteItemRequest) ToTypes() types.ModelRouteItem {
	return types.ModelRouteItem{ModelID: i.ModelID, Route: i.Route.ToRouteConfig()}
}

// ----------------- KeyPool (admin) -----------------

// KeyPoolMetaRequest mirrors types.KeyPoolMeta without revision.
type KeyPoolMetaRequest struct {
	ProviderID string `json:"provider_id"`
	Strategy   string `json:"strategy,omitempty"`
}

type KeyPoolConfigRequest struct {
	Meta    KeyPoolMetaRequest   `json:"meta"`
	Entries []types.KeyPoolEntry `json:"entries"`
}

func (r *KeyPoolConfigRequest) ToKeyPoolConfig() *types.KeyPoolConfig {
	if r == nil {
		return nil
	}
	return &types.KeyPoolConfig{
		Meta: types.KeyPoolMeta{
			ProviderID: r.Meta.ProviderID,
			Strategy:   r.Meta.Strategy,
		},
		Entries: r.Entries,
	}
}

type KeyPoolItemRequest struct {
	ProviderID string                `json:"provider_id"`
	Pool       *KeyPoolConfigRequest `json:"pool"`
}

func (i KeyPoolItemRequest) ToTypes() types.KeyPoolItem {
	return types.KeyPoolItem{ProviderID: i.ProviderID, Pool: i.Pool.ToKeyPoolConfig()}
}

// ----------------- Provider (admin) -----------------

// ProviderInfoRequest is the deprecated admin HTTP wire shape for finished
// provider config. New producers should send runtime intent to
// POST /admin/api/v1/providers:from-intent; this endpoint remains temporarily
// for compatibility until billing switches away from finished-config writes.
// Provider routing is protocol_bindings-only: legacy api_type/api_types fields
// are rejected during JSON decode instead of being promoted or silently ignored.
type ProviderInfoRequest struct {
	ID                 string            `json:"id"`
	ProviderAccountRef string            `json:"provider_account_ref,omitempty"`
	Code               string            `json:"code,omitempty"`
	Name               string            `json:"name"`
	BaseURL            string            `json:"base_url"`
	Status             string            `json:"status"`
	KeyStrategy        string            `json:"key_strategy,omitempty"`
	OwnedBy            string            `json:"owned_by,omitempty"`
	Visibility         string            `json:"visibility,omitempty"`
	CredentialSource   string            `json:"credential_source,omitempty"`
	AllowedRouteKinds  []string          `json:"allowed_route_kinds,omitempty"`
	OwnerScope         *types.OwnerScope `json:"owner_scope,omitempty"`

	ProviderProfile        string                  `json:"provider_profile,omitempty"`
	ClientCompatibility    []string                `json:"client_compatibility,omitempty"`
	TokenCountStrategy     string                  `json:"token_count_strategy,omitempty"`
	PathPrefix             string                  `json:"path_prefix,omitempty"`
	ProtocolBindings       []types.ProtocolBinding `json:"protocol_bindings,omitempty"`
	DefaultProtocolBinding string                  `json:"default_protocol_binding,omitempty"`
}

func (r *ProviderInfoRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["api_type"]; ok {
		return fmt.Errorf("provider api_type is not accepted; use protocol_bindings")
	}
	if _, ok := raw["api_types"]; ok {
		return fmt.Errorf("provider api_types is not accepted; use protocol_bindings")
	}
	type alias ProviderInfoRequest
	return json.Unmarshal(data, (*alias)(r))
}

func (r *ProviderInfoRequest) ToProviderInfo() (*types.ProviderInfo, error) {
	if r == nil {
		return nil, nil
	}
	bindings := r.ProtocolBindings
	defaultBinding := r.DefaultProtocolBinding
	if len(bindings) == 0 && (r.ProviderProfile != "" || len(r.ClientCompatibility) > 0) {
		compiled, compiledDefault, err := types.CompileClientProtocolBindingsWithOptions(
			r.ProviderProfile,
			r.ClientCompatibility,
			types.ClientProtocolBindingOptions{
				PathPrefix:         r.PathPrefix,
				TokenCountStrategy: r.TokenCountStrategy,
			},
		)
		if err != nil {
			return nil, err
		}
		bindings = compiled
		if defaultBinding == "" {
			defaultBinding = compiledDefault
		}
	}
	if err := types.ValidateBindingUniqueness(bindings); err != nil {
		return nil, fmt.Errorf("protocol_bindings: %w", err)
	}
	// ADR-0012: derive observation-only compatibility views from the compiled
	// bindings, mirroring intent.Compile, so the legacy publish path produces
	// artifacts equivalent to the intent path.
	inboundProtocols, clientCompatibilities := types.DeriveCompatibilityView(bindings)
	return &types.ProviderInfo{
		ID:                     r.ID,
		ProviderAccountRef:     r.ProviderAccountRef,
		Code:                   r.Code,
		Name:                   r.Name,
		BaseURL:                r.BaseURL,
		Status:                 r.Status,
		KeyStrategy:            r.KeyStrategy,
		ProviderProfile:        r.ProviderProfile,
		OwnedBy:                r.OwnedBy,
		Visibility:             r.Visibility,
		CredentialSource:       r.CredentialSource,
		AllowedRouteKinds:      r.AllowedRouteKinds,
		OwnerScope:             r.OwnerScope,
		ProtocolBindings:       bindings,
		DefaultProtocolBinding: defaultBinding,
		InboundProtocols:       inboundProtocols,
		ClientCompatibilities:  clientCompatibilities,
	}, nil
}

// ----------------- PlatformConfig (admin) -----------------

type PlatformConfigRequest struct {
	PlatformRPM              int                            `json:"platform_rpm"`
	PlatformCC               int                            `json:"platform_cc"`
	DefaultProviderStrategy  string                         `json:"default_provider_strategy"`
	DefaultRoutePolicy       types.DefaultRoutePolicyConfig `json:"default_route_policy,omitempty"`
	DefaultKeyPoolStrategy   string                         `json:"default_keypool_strategy,omitempty"`
	DefaultTimeoutMs         int                            `json:"default_timeout_ms"`
	StreamTimeoutMs          int                            `json:"stream_timeout_ms"`
	FirstTokenTimeoutMs      int                            `json:"first_token_timeout_ms"`
	AttemptTimeoutMs         int                            `json:"attempt_timeout_ms"`
	MaxBodySize              int                            `json:"max_body_size"`
	MaxFallbackDepth         int                            `json:"max_fallback_depth"`
	DisclosurePolicy         types.DisclosurePolicy         `json:"disclosure_policy,omitempty"`
	ProviderBudgetPolicies   []types.EffectivePolicy        `json:"provider_budget_policies,omitempty"`
	Auth                     types.AuthRuntimeConfig        `json:"auth,omitempty"`
	RetryBudget              types.RetryBudgetConfig        `json:"retry_budget,omitempty"`
	ResponsesAffinityEnabled *bool                          `json:"responses_affinity_enabled,omitempty"`
	Operational              types.OperationalAttrs         `json:"operational,omitempty"`
	// IngressProductAssertions — G7 first admission layer (per-gate
	// product assertions). Optional; omitted = no gate asserted =
	// gateway behavior unchanged. Structural validation happens in
	// publisher.PublishPlatformConfig.
	IngressProductAssertions []types.IngressProductAssertion `json:"ingress_product_assertions,omitempty"`
}

func (r *PlatformConfigRequest) ToPlatformConfig() *types.PlatformConfig {
	if r == nil {
		return nil
	}
	return &types.PlatformConfig{
		PlatformRPM:              r.PlatformRPM,
		PlatformCC:               r.PlatformCC,
		DefaultProviderStrategy:  r.DefaultProviderStrategy,
		DefaultRoutePolicy:       r.DefaultRoutePolicy,
		DefaultKeyPoolStrategy:   r.DefaultKeyPoolStrategy,
		DefaultTimeoutMs:         r.DefaultTimeoutMs,
		StreamTimeoutMs:          r.StreamTimeoutMs,
		FirstTokenTimeoutMs:      r.FirstTokenTimeoutMs,
		AttemptTimeoutMs:         r.AttemptTimeoutMs,
		MaxBodySize:              r.MaxBodySize,
		MaxFallbackDepth:         r.MaxFallbackDepth,
		DisclosurePolicy:         r.DisclosurePolicy,
		ProviderBudgetPolicies:   r.ProviderBudgetPolicies,
		Auth:                     r.Auth,
		RetryBudget:              r.RetryBudget,
		ResponsesAffinityEnabled: r.ResponsesAffinityEnabled,
		Operational:              r.Operational,
		IngressProductAssertions: r.IngressProductAssertions,
	}
}

// ----------------- ProductRuntimePolicy (admin) -----------------

type ProductRuntimePolicyRequest struct {
	SchemaVersion                   int                      `json:"schema_version,omitempty"`
	Revision                        int64                    `json:"revision,omitempty"`
	ProductID                       string                   `json:"product_id"`
	ProductSlug                     string                   `json:"product_slug,omitempty"`
	Status                          string                   `json:"status"`
	CredentialSource                string                   `json:"credential_source"`
	AllowedProviderIDs              []string                 `json:"allowed_provider_ids"`
	AllowedChannelIDs               []string                 `json:"allowed_channel_ids"`
	AllowedProviderProfiles         []string                 `json:"allowed_provider_profiles"`
	ModelScopeMode                  string                   `json:"model_scope_mode"`
	AllowedModels                   []string                 `json:"allowed_models"`
	FallbackPolicy                  string                   `json:"fallback_policy"`
	BillingMode                     string                   `json:"billing_mode"`
	RouteStrategy                   string                   `json:"route_strategy,omitempty"`
	AllowHighLossBridgeWhenNoNative bool                     `json:"allow_high_loss_bridge_when_no_native,omitempty"`
	AllowBridgeOnNativeFailure      bool                     `json:"allow_bridge_on_native_failure,omitempty"`
	RouteGraphRequired              bool                     `json:"route_graph_required,omitempty"`
	RouteGraphEnforcement           string                   `json:"route_graph_enforcement,omitempty"`
	RouteGraph                      *types.ProductRouteGraph `json:"route_graph,omitempty"`
	UpdatedAt                       string                   `json:"updated_at,omitempty"`
}

func (r *ProductRuntimePolicyRequest) ToProductRuntimePolicy() *types.ProductRuntimePolicy {
	if r == nil {
		return nil
	}
	return &types.ProductRuntimePolicy{
		SchemaVersion:                   r.SchemaVersion,
		Revision:                        r.Revision,
		ProductID:                       r.ProductID,
		ProductSlug:                     r.ProductSlug,
		Status:                          r.Status,
		CredentialSource:                r.CredentialSource,
		AllowedProviderIDs:              r.AllowedProviderIDs,
		AllowedChannelIDs:               r.AllowedChannelIDs,
		AllowedProviderProfiles:         r.AllowedProviderProfiles,
		ModelScopeMode:                  r.ModelScopeMode,
		AllowedModels:                   r.AllowedModels,
		FallbackPolicy:                  r.FallbackPolicy,
		BillingMode:                     r.BillingMode,
		RouteStrategy:                   r.RouteStrategy,
		AllowHighLossBridgeWhenNoNative: r.AllowHighLossBridgeWhenNoNative,
		AllowBridgeOnNativeFailure:      r.AllowBridgeOnNativeFailure,
		RouteGraphRequired:              r.RouteGraphRequired,
		RouteGraphEnforcement:           r.RouteGraphEnforcement,
		RouteGraph:                      r.RouteGraph,
		UpdatedAt:                       r.UpdatedAt,
	}
}

// ----------------- ProviderOverride (sre) -----------------

// ProviderOverrideRequest mirrors types.ProviderOverride without
// publisher-internal fields:
//   - revision   ⇒ publisher fills
//   - created_at ⇒ publisher fills (server clock)
type ProviderOverrideRequest struct {
	Scope          string          `json:"scope"`
	Action         string          `json:"action"`
	Param          json.RawMessage `json:"param,omitempty"`
	EffectiveFrom  int64           `json:"effective_from"`
	EffectiveUntil *int64          `json:"effective_until,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	CreatedBy      string          `json:"created_by,omitempty"`
}

func (r *ProviderOverrideRequest) ToProviderOverride() *types.ProviderOverride {
	if r == nil {
		return nil
	}
	return &types.ProviderOverride{
		Scope:          r.Scope,
		Action:         r.Action,
		Param:          r.Param,
		EffectiveFrom:  r.EffectiveFrom,
		EffectiveUntil: r.EffectiveUntil,
		Reason:         r.Reason,
		CreatedBy:      r.CreatedBy,
	}
}

type ProviderOverrideItemRequest struct {
	Scope    string                   `json:"scope"`
	Override *ProviderOverrideRequest `json:"override"`
}

func (i ProviderOverrideItemRequest) ToTypes() types.ProviderOverrideItem {
	return types.ProviderOverrideItem{Scope: i.Scope, Override: i.Override.ToProviderOverride()}
}

// ----------------- KeyBalance (billing) -----------------

type AdmissionDecisionRequest struct {
	DecisionID      string `json:"decision_id,omitempty"`
	DecisionVersion *int64 `json:"decision_version,omitempty"`
	// Revision is the legacy alias previously emitted by Billing. The HTTP
	// adapter maps it explicitly; it must never be silently discarded.
	Revision *int64 `json:"revision,omitempty"`
}

type KeyBalanceItemRequest struct {
	KeyID      types.KeyID `json:"key_id"`
	HasBalance *bool       `json:"has_balance"`
	Reason     string      `json:"reason,omitempty"`
	AdmissionDecisionRequest
}

func (i KeyBalanceItemRequest) ToTypes(hasBalance bool, reason string, decision types.KeyAdmissionDecision) types.KeyAdmissionItem {
	if i.Reason != "" {
		reason = i.Reason
	} else if hasBalance {
		// A bulk-level reason applies only to denied items. Preserve an
		// item-level non-empty reason above so the Publisher validator can
		// reject it for allowed=true rather than silently dropping a typo.
		reason = ""
	}
	return types.KeyAdmissionItem{KeyID: i.KeyID, HasBalance: hasBalance, Reason: reason, Decision: decision}
}
