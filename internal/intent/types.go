package intent

import "github.com/tokenplatform/publisher/internal/types"

const CurrentIntentSchemaVersion = 2

type RuntimeIntent struct {
	SchemaVersion        int                         `json:"schema_version"`
	ProviderEndpoints    []ProviderEndpoint          `json:"provider_endpoints,omitempty"`
	ProviderCredentials  []ProviderCredential        `json:"provider_credentials,omitempty"`
	ClientExposures      []ClientExposure            `json:"client_exposures,omitempty"`
	ModelRoutes          []ModelRouteIntent          `json:"model_routes,omitempty"`
	ProductPolicies      []ProductRoutePolicy        `json:"product_policies,omitempty"`
	ProductPolicyDeletes []types.ProductPolicyDelete `json:"product_policy_deletes,omitempty"`
}

type ProviderEndpoint struct {
	ID                 string            `json:"id"`
	ProviderAccountRef string            `json:"provider_account_ref,omitempty"`
	ProviderProfile    string            `json:"provider_profile"`
	BaseURL            string            `json:"base_url"`
	Region             string            `json:"region,omitempty"`
	Status             string            `json:"status"`
	Name               string            `json:"name,omitempty"`
	Code               string            `json:"code,omitempty"`
	OwnedBy            string            `json:"owned_by,omitempty"`
	KeyStrategy        string            `json:"key_strategy,omitempty"`
	PathPrefix         string            `json:"path_prefix,omitempty"`
	Visibility         string            `json:"visibility,omitempty"`
	CredentialSource   string            `json:"credential_source,omitempty"`
	AllowedRouteKinds  []string          `json:"allowed_route_kinds,omitempty"`
	OwnerScope         *types.OwnerScope `json:"owner_scope,omitempty"`
}

type ProviderCredential struct {
	ProviderID         string   `json:"provider_id"`
	KeyRef             string   `json:"key_ref"`
	KeyID              string   `json:"key_id,omitempty"`
	ProviderAccountRef string   `json:"provider_account_ref"`
	Alias              string   `json:"alias,omitempty"`
	RPMLimit           int      `json:"rpm_limit,omitempty"`
	TPMLimit           int      `json:"tpm_limit,omitempty"`
	Status             string   `json:"status"`
	AllowedModels      []string `json:"allowed_models,omitempty"`
	Priority           int      `json:"priority,omitempty"`
	Weight             int      `json:"weight,omitempty"`
}

type ClientExposure struct {
	ProviderID string `json:"provider_id"`
	// ServedInboundProtocols is the explicit set of inbound Wire dialects
	// (openai/anthropic/responses/gemini) this endpoint serves (ADR-0012).
	// Deprecated as an input (ADR-0016): superseded by Paths. Still legal
	// during the transition; ignored when Paths is present.
	ServedInboundProtocols []string `json:"served_inbound_protocols"`
	// Paths is the explicit path declaration list (ADR-0016): each entry =
	// (inbound_protocol, channel). When present it is the compile input for the
	// chat/wire axis and ServedInboundProtocols is ignored. Capabilities remain
	// an orthogonal axis (ADR-0009 S2b).
	Paths              []types.ProtocolPath `json:"paths,omitempty"`
	Capabilities       []string             `json:"capabilities,omitempty"`
	TokenCountStrategy string               `json:"token_count_strategy,omitempty"`
	ProtocolPathPrefix string               `json:"protocol_path_prefix,omitempty"`
}

type ModelRouteIntent struct {
	LogicalModel     string             `json:"logical_model"`
	DisplayName      string             `json:"display_name,omitempty"`
	Capabilities     []string           `json:"capabilities,omitempty"`
	ContextWindow    int                `json:"context_window,omitempty"`
	MaxOutput        int                `json:"max_output,omitempty"`
	OwnedBy          string             `json:"owned_by,omitempty"`
	ProviderStrategy string             `json:"provider_strategy,omitempty"`
	FallbackModel    string             `json:"fallback_model,omitempty"`
	Routes           []ModelRouteTarget `json:"routes"`
}

type ModelRouteTarget struct {
	ProviderID    string   `json:"provider_id"`
	UpstreamModel string   `json:"upstream_model"`
	Priority      int      `json:"priority,omitempty"`
	Weight        int      `json:"weight,omitempty"`
	Status        string   `json:"status"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

type ProductRoutePolicy struct {
	ProductID                       string                   `json:"product_id"`
	Revision                        int64                    `json:"revision,omitempty"`
	ProductSlug                     string                   `json:"product_slug,omitempty"`
	Status                          string                   `json:"status"`
	CredentialSource                string                   `json:"credential_source"`
	AllowedProviderIDs              []string                 `json:"allowed_provider_ids,omitempty"`
	AllowedChannelIDs               []string                 `json:"allowed_channel_ids,omitempty"`
	AllowedModels                   []string                 `json:"allowed_models,omitempty"`
	ModelScopeMode                  string                   `json:"model_scope_mode"`
	FallbackPolicy                  string                   `json:"fallback_policy"`
	BillingMode                     string                   `json:"billing_mode"`
	RouteStrategy                   string                   `json:"route_strategy,omitempty"`
	AllowHighLossBridgeWhenNoNative bool                     `json:"allow_high_loss_bridge_when_no_native,omitempty"`
	AllowBridgeOnNativeFailure      bool                     `json:"allow_bridge_on_native_failure,omitempty"`
	RouteGraphRequired              bool                     `json:"route_graph_required,omitempty"`
	RouteGraphEnforcement           string                   `json:"route_graph_enforcement,omitempty"`
	RouteGraph                      *types.ProductRouteGraph `json:"route_graph,omitempty"`
}

type RuntimeConfig struct {
	Providers            []types.ProviderInfo         `json:"providers,omitempty"`
	KeyPools             []types.KeyPoolConfig        `json:"key_pools,omitempty"`
	ModelCatalog         []types.ModelCatalogEntry    `json:"model_catalog,omitempty"`
	Routes               []types.RouteConfig          `json:"routes,omitempty"`
	ProductPolicies      []types.ProductRuntimePolicy `json:"product_policies,omitempty"`
	ProductPolicyDeletes []types.ProductPolicyDelete  `json:"product_policy_deletes,omitempty"`
	EffectivePolicies    []types.EffectivePolicy      `json:"effective_policies,omitempty"`
	Warnings             []CompileWarning             `json:"warnings,omitempty"`
}

type CompileWarning struct {
	Code       string            `json:"code"`
	Message    string            `json:"message"`
	Providers  []string          `json:"providers,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}
