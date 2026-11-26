package intent

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/tokenplatform/publisher/internal/routegraph"
	"github.com/tokenplatform/publisher/internal/types"
)

func Compile(in RuntimeIntent) (*RuntimeConfig, error) {
	if in.SchemaVersion != CurrentIntentSchemaVersion {
		return nil, fmt.Errorf("unsupported intent schema_version %d; current=%d", in.SchemaVersion, CurrentIntentSchemaVersion)
	}

	endpoints := make(map[string]ProviderEndpoint, len(in.ProviderEndpoints))
	for i, endpoint := range in.ProviderEndpoints {
		id := strings.TrimSpace(endpoint.ID)
		if id == "" {
			return nil, fmt.Errorf("provider_endpoints[%d].id required", i)
		}
		if strings.TrimSpace(endpoint.ProviderProfile) == "" {
			return nil, fmt.Errorf("provider_endpoints[%d].provider_profile required", i)
		}
		if strings.TrimSpace(endpoint.BaseURL) == "" {
			return nil, fmt.Errorf("provider_endpoints[%d].base_url required", i)
		}
		if strings.TrimSpace(endpoint.Status) == "" {
			return nil, fmt.Errorf("provider_endpoints[%d].status required", i)
		}
		if endpoint.ProviderAccountRef != "" {
			if err := types.ValidateProviderAccountRef(endpoint.ProviderAccountRef); err != nil {
				return nil, fmt.Errorf("provider_endpoints[%d].provider_account_ref invalid: %v", i, err)
			}
			if endpoint.ProviderAccountRef == endpoint.ID {
				return nil, fmt.Errorf("provider_endpoints[%d].provider_account_ref must not reuse id", i)
			}
			if endpoint.ProviderAccountRef == endpoint.BaseURL {
				return nil, fmt.Errorf("provider_endpoints[%d].provider_account_ref must not reuse base_url", i)
			}
		}
		if endpoint.CredentialSource == types.ProviderCredentialSourceProviderEntitlement && endpoint.ProviderAccountRef == "" {
			return nil, fmt.Errorf("provider_endpoints[%d].provider_account_ref required for provider_entitlement", i)
		}
		if _, exists := endpoints[id]; exists {
			return nil, fmt.Errorf("provider_endpoints[%d].id duplicate: %s", i, id)
		}
		endpoint.ID = id
		endpoints[id] = endpoint
	}

	exposuresByProvider, err := groupExposures(in.ClientExposures, endpoints)
	if err != nil {
		return nil, err
	}
	credentialsByProvider, err := groupCredentials(in.ProviderCredentials, endpoints)
	if err != nil {
		return nil, err
	}
	warnings, sharedCredentialGroups := detectSharedCredentials(in.ProviderCredentials)

	out := &RuntimeConfig{Warnings: warnings}
	for _, endpoint := range sortedEndpoints(endpoints) {
		exposures := exposuresByProvider[endpoint.ID]
		if len(exposures) == 0 {
			return nil, fmt.Errorf("provider_endpoints[%s] requires at least one client_exposure", endpoint.ID)
		}
		provider, providerWarnings, err := compileProvider(endpoint, exposures)
		if err != nil {
			return nil, err
		}
		out.Providers = append(out.Providers, provider)
		out.Warnings = append(out.Warnings, providerWarnings...)

		pool := compileKeyPool(endpoint, credentialsByProvider[endpoint.ID])
		if len(pool.Entries) > 0 {
			out.KeyPools = append(out.KeyPools, pool)
		}
	}

	routeProvidersByModel := make(map[string]map[string]struct{})
	for i, routeIntent := range in.ModelRoutes {
		catalog, route, err := compileModelRoute(i, routeIntent, endpoints)
		if err != nil {
			return nil, err
		}
		out.ModelCatalog = append(out.ModelCatalog, catalog)
		out.Routes = append(out.Routes, route)
		providers := routeProvidersByModel[route.ModelID]
		if providers == nil {
			providers = make(map[string]struct{})
			routeProvidersByModel[route.ModelID] = providers
		}
		for _, provider := range route.Providers {
			providers[provider.ProviderID] = struct{}{}
		}
	}

	for i, policyIntent := range in.ProductPolicies {
		policy, effectivePolicies, err := compileProductPolicy(i, policyIntent, endpoints, credentialsByProvider, routeProvidersByModel, sharedCredentialGroups)
		if err != nil {
			return nil, err
		}
		out.ProductPolicies = append(out.ProductPolicies, policy)
		out.EffectivePolicies = append(out.EffectivePolicies, effectivePolicies...)
	}
	deleteProductIDs := make(map[string]struct{}, len(in.ProductPolicyDeletes))
	for i, deleteIntent := range in.ProductPolicyDeletes {
		deleted, err := compileProductPolicyDelete(i, deleteIntent)
		if err != nil {
			return nil, err
		}
		if _, exists := deleteProductIDs[deleted.ProductID]; exists {
			return nil, fmt.Errorf("product_policy_deletes[%d].product_id duplicate: %s", i, deleted.ProductID)
		}
		deleteProductIDs[deleted.ProductID] = struct{}{}
		for j, policy := range out.ProductPolicies {
			if policy.ProductID == deleted.ProductID {
				return nil, fmt.Errorf("product_policy_deletes[%d].product_id also appears in product_policies[%d]: %s", i, j, deleted.ProductID)
			}
		}
		out.ProductPolicyDeletes = append(out.ProductPolicyDeletes, deleted)
	}

	return out, nil
}

func groupExposures(values []ClientExposure, endpoints map[string]ProviderEndpoint) (map[string][]ClientExposure, error) {
	out := make(map[string][]ClientExposure)
	for i, exposure := range values {
		providerID := strings.TrimSpace(exposure.ProviderID)
		if providerID == "" {
			return nil, fmt.Errorf("client_exposures[%d].provider_id required", i)
		}
		if _, ok := endpoints[providerID]; !ok {
			return nil, fmt.Errorf("client_exposures[%d].provider_id unknown: %s", i, providerID)
		}
		exposure.ProviderID = providerID
		out[providerID] = append(out[providerID], exposure)
	}
	return out, nil
}

func detectSharedCredentials(values []ProviderCredential) ([]CompileWarning, map[string]string) {
	providersByIdentity := make(map[string]map[string]struct{})
	for _, credential := range values {
		identity := credentialIdentity(credential)
		providerID := strings.TrimSpace(credential.ProviderID)
		if identity == "" || providerID == "" {
			continue
		}
		providers := providersByIdentity[identity]
		if providers == nil {
			providers = make(map[string]struct{})
			providersByIdentity[identity] = providers
		}
		providers[providerID] = struct{}{}
	}

	identities := make([]string, 0, len(providersByIdentity))
	for identity := range providersByIdentity {
		identities = append(identities, identity)
	}
	sort.Strings(identities)

	var warnings []CompileWarning
	groupsByProvider := make(map[string]string)
	for _, identity := range identities {
		providers := sortedSet(providersByIdentity[identity])
		if len(providers) <= 1 {
			continue
		}
		groupID := sharedCredentialGroupID(identity)
		for _, providerID := range providers {
			groupsByProvider[providerID] = groupID
		}
		warnings = append(warnings, CompileWarning{
			Code:       "shared_credential_group",
			Message:    "credential identity is used by multiple channelized providers; aggregate key limit is not enforced by provider-level runtime policies",
			Providers:  providers,
			Credential: groupID,
			Meta: map[string]string{
				"shared_credential_group":    groupID,
				"aggregate_limit_unenforced": "true",
			},
		})
	}
	return warnings, groupsByProvider
}

func credentialIdentity(credential ProviderCredential) string {
	if value := strings.TrimSpace(credential.KeyRef); value != "" {
		return "key_ref:" + value
	}
	if value := strings.TrimSpace(credential.KeyID); value != "" {
		return "key_id:" + value
	}
	return ""
}

func sharedCredentialGroupID(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("shared_credential:%x", sum[:6])
}

func groupCredentials(values []ProviderCredential, endpoints map[string]ProviderEndpoint) (map[string][]ProviderCredential, error) {
	out := make(map[string][]ProviderCredential)
	for i, credential := range values {
		providerID := strings.TrimSpace(credential.ProviderID)
		if providerID == "" {
			return nil, fmt.Errorf("provider_credentials[%d].provider_id required", i)
		}
		if _, ok := endpoints[providerID]; !ok {
			return nil, fmt.Errorf("provider_credentials[%d].provider_id unknown: %s", i, providerID)
		}
		if strings.TrimSpace(credential.KeyRef) == "" {
			return nil, fmt.Errorf("provider_credentials[%d].key_ref required", i)
		}
		if strings.TrimSpace(credential.Status) == "" {
			return nil, fmt.Errorf("provider_credentials[%d].status required", i)
		}
		if credential.ProviderAccountRef == "" {
			return nil, fmt.Errorf("provider_credentials[%d].provider_account_ref required for new writes", i)
		}
		if err := types.ValidateProviderAccountRef(credential.ProviderAccountRef); err != nil {
			return nil, fmt.Errorf("provider_credentials[%d].provider_account_ref invalid: %v", i, err)
		}
		keyID := firstNonEmpty(credential.KeyID, keyIDFromRef(credential.KeyRef))
		if credential.ProviderAccountRef == keyID {
			return nil, fmt.Errorf("provider_credentials[%d].provider_account_ref must not reuse key_id", i)
		}
		if credential.ProviderAccountRef == credential.KeyRef {
			return nil, fmt.Errorf("provider_credentials[%d].provider_account_ref must not reuse key_ref", i)
		}
		credential.ProviderID = providerID
		out[providerID] = append(out[providerID], credential)
	}
	return out, nil
}

func sortedEndpoints(values map[string]ProviderEndpoint) []ProviderEndpoint {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ProviderEndpoint, 0, len(ids))
	for _, id := range ids {
		out = append(out, values[id])
	}
	return out
}

func compileProvider(endpoint ProviderEndpoint, exposures []ClientExposure) (types.ProviderInfo, []CompileWarning, error) {
	bindingsByName := make(map[string]types.ProtocolBinding)
	bindingNames := make([]string, 0)
	defaultBinding := ""
	var warnings []CompileWarning

	for i, exposure := range exposures {
		if strings.TrimSpace(exposure.ProviderID) == "" {
			return types.ProviderInfo{}, nil, fmt.Errorf("client_exposures[%d].provider_id required", i)
		}
		opts := types.ClientProtocolBindingOptions{
			PathPrefix:         firstNonEmpty(exposure.ProtocolPathPrefix, endpoint.PathPrefix),
			TokenCountStrategy: exposure.TokenCountStrategy,
		}
		var compiled []types.ProtocolBinding
		var compiledDefault string
		var err error
		if len(exposure.Paths) > 0 {
			// ADR-0016 U2: explicit paths[] is the compile input when present;
			// served_inbound_protocols is ignored (transition contract). Physically
			// impossible declarations warn but never block — they compile as
			// declared and fail at runtime, visibly (zero-blocking).
			var pathWarnings []types.ProtocolPathWarning
			compiled, compiledDefault, pathWarnings, err = types.CompileExplicitPathBindings(endpoint.ProviderProfile, exposure.Paths, exposure.Capabilities, opts)
			for _, pathWarning := range pathWarnings {
				warnings = append(warnings, CompileWarning{
					Code:      pathWarning.Code,
					Message:   pathWarning.Message,
					Providers: []string{endpoint.ID},
					Meta: map[string]string{
						"provider_profile": endpoint.ProviderProfile,
						"inbound_protocol": pathWarning.Path.InboundProtocol,
						"channel":          pathWarning.Path.Channel,
					},
				})
			}
		} else {
			// ADR-0012 S4: without paths, served_inbound_protocols remains the
			// required compile input — compose bindings from inbound Wire dialects
			// (passthrough + explicit bridges) plus the capability axis (S2b:
			// embeddings/rerank same-dialect). Unchanged behavior.
			if len(exposure.ServedInboundProtocols) == 0 {
				return types.ProviderInfo{}, nil, fmt.Errorf("client_exposures[%s] requires served_inbound_protocols or paths", endpoint.ID)
			}
			compiled, compiledDefault, err = types.CompileInboundProtocolBindings(endpoint.ProviderProfile, exposure.ServedInboundProtocols, exposure.Capabilities, opts)
		}
		if err != nil {
			return types.ProviderInfo{}, nil, fmt.Errorf("client_exposures[%s]: %w", endpoint.ID, err)
		}
		if defaultBinding == "" {
			defaultBinding = compiledDefault
		}
		for _, binding := range compiled {
			existing, exists := bindingsByName[binding.Name]
			if exists {
				if !reflect.DeepEqual(existing, binding) {
					return types.ProviderInfo{}, nil, fmt.Errorf("client_exposures[%s] produce conflicting binding %s", endpoint.ID, binding.Name)
				}
				continue
			}
			bindingsByName[binding.Name] = binding
			bindingNames = append(bindingNames, binding.Name)
		}
	}

	sort.Strings(bindingNames)
	bindings := make([]types.ProtocolBinding, 0, len(bindingNames))
	for _, name := range bindingNames {
		bindings = append(bindings, bindingsByName[name])
	}
	// ADR-0015 S3 (retained by ADR-0016): provider-internal resolution-key
	// uniqueness is runtime data-structure integrity, not a protective check.
	if err := types.ValidateBindingUniqueness(bindings); err != nil {
		return types.ProviderInfo{}, nil, fmt.Errorf("client_exposures[%s] protocol_bindings: %w", endpoint.ID, err)
	}

	// ADR-0012: derive observation-only compatibility views from the compiled
	// bindings' Accepts.Protocol union. Not a compile input; not consumed by
	// gateway routing.
	inboundProtocols, clientCompatibilities := types.DeriveCompatibilityView(bindings)

	provider := types.ProviderInfo{
		ID:                     endpoint.ID,
		ProviderAccountRef:     endpoint.ProviderAccountRef,
		Code:                   firstNonEmpty(endpoint.Code, endpoint.ID),
		Name:                   firstNonEmpty(endpoint.Name, endpoint.ID),
		BaseURL:                endpoint.BaseURL,
		Status:                 endpoint.Status,
		KeyStrategy:            endpoint.KeyStrategy,
		ProviderProfile:        endpoint.ProviderProfile,
		OwnedBy:                endpoint.OwnedBy,
		Visibility:             endpoint.Visibility,
		CredentialSource:       endpoint.CredentialSource,
		AllowedRouteKinds:      copyStrings(endpoint.AllowedRouteKinds),
		OwnerScope:             cloneOwnerScope(endpoint.OwnerScope),
		ProtocolBindings:       bindings,
		DefaultProtocolBinding: defaultBinding,
		InboundProtocols:       inboundProtocols,
		ClientCompatibilities:  clientCompatibilities,
	}
	return provider, warnings, nil
}

func cloneOwnerScope(in *types.OwnerScope) *types.OwnerScope {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func compileKeyPool(endpoint ProviderEndpoint, credentials []ProviderCredential) types.KeyPoolConfig {
	entries := make([]types.KeyPoolEntry, 0, len(credentials))
	for _, credential := range credentials {
		keyID := firstNonEmpty(credential.KeyID, keyIDFromRef(credential.KeyRef))
		entries = append(entries, types.KeyPoolEntry{
			KeyID:              keyID,
			ProviderAccountRef: credential.ProviderAccountRef,
			Alias:              credential.Alias,
			AllowedModels:      copyStrings(credential.AllowedModels),
			Ciphertext:         credential.KeyRef,
			Priority:           credential.Priority,
			Weight:             credential.Weight,
			RPMLimit:           credential.RPMLimit,
			TPMLimit:           credential.TPMLimit,
			Status:             credential.Status,
		})
	}
	return types.KeyPoolConfig{
		Meta: types.KeyPoolMeta{
			ProviderID: endpoint.ID,
			Strategy:   firstNonEmpty(endpoint.KeyStrategy, "priority"),
		},
		Entries: entries,
	}
}

func keyIDFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	parts := strings.FieldsFunc(ref, func(r rune) bool {
		return r == '/' || r == ':' || r == '#'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		if part := strings.TrimSpace(parts[i]); part != "" {
			return part
		}
	}
	return ref
}

func compileModelRoute(index int, in ModelRouteIntent, endpoints map[string]ProviderEndpoint) (types.ModelCatalogEntry, types.RouteConfig, error) {
	modelID := strings.TrimSpace(in.LogicalModel)
	if modelID == "" {
		return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].logical_model required", index)
	}
	if len(in.Routes) == 0 {
		return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].routes required", index)
	}

	entries := make([]types.RouteEntry, 0, len(in.Routes))
	for i, target := range in.Routes {
		providerID := strings.TrimSpace(target.ProviderID)
		if providerID == "" {
			return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].routes[%d].provider_id required", index, i)
		}
		if _, ok := endpoints[providerID]; !ok {
			return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].routes[%d].provider_id unknown: %s", index, i, providerID)
		}
		if strings.TrimSpace(target.UpstreamModel) == "" {
			return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].routes[%d].upstream_model required", index, i)
		}
		if strings.TrimSpace(target.Status) == "" {
			return types.ModelCatalogEntry{}, types.RouteConfig{}, fmt.Errorf("model_routes[%d].routes[%d].status required", index, i)
		}
		entries = append(entries, types.RouteEntry{
			ProviderID:    providerID,
			UpstreamModel: target.UpstreamModel,
			Priority:      target.Priority,
			Weight:        target.Weight,
			Status:        target.Status,
			Capabilities:  copyStrings(target.Capabilities),
		})
	}

	capabilities := copyStrings(in.Capabilities)
	if len(capabilities) == 0 {
		capabilities = []string{"chat"}
	}
	catalog := types.ModelCatalogEntry{
		ModelID:       modelID,
		DisplayName:   firstNonEmpty(in.DisplayName, modelID),
		Capabilities:  capabilities,
		ContextWindow: in.ContextWindow,
		MaxOutput:     in.MaxOutput,
		OwnedBy:       in.OwnedBy,
	}
	route := types.RouteConfig{
		ModelID:          modelID,
		Providers:        entries,
		FallbackModel:    in.FallbackModel,
		ProviderStrategy: in.ProviderStrategy,
	}
	return catalog, route, nil
}

func compileProductPolicy(index int, in ProductRoutePolicy, endpoints map[string]ProviderEndpoint, credentialsByProvider map[string][]ProviderCredential, routeProvidersByModel map[string]map[string]struct{}, sharedCredentialGroups map[string]string) (types.ProductRuntimePolicy, []types.EffectivePolicy, error) {
	productID := strings.TrimSpace(in.ProductID)
	if productID == "" {
		return types.ProductRuntimePolicy{}, nil, fmt.Errorf("product_policies[%d].product_id required", index)
	}
	if in.RouteGraphRequired && in.RouteGraph == nil {
		return types.ProductRuntimePolicy{}, nil, fmt.Errorf("product_policies[%d].route_graph_required=true requires route_graph", index)
	}
	if in.RouteGraphEnforcement != "" && in.RouteGraphEnforcement != "shadow" && in.RouteGraphEnforcement != "enforce" {
		return types.ProductRuntimePolicy{}, nil, fmt.Errorf("product_policies[%d].route_graph_enforcement=%s not allowed", index, in.RouteGraphEnforcement)
	}
	if in.RouteGraphEnforcement != "" && in.RouteGraph == nil {
		return types.ProductRuntimePolicy{}, nil, fmt.Errorf("product_policies[%d].route_graph_enforcement requires route_graph", index)
	}
	allowedProviders, allowedChannels, allowedModels, err := compileRouteGraph(index, in, endpoints, credentialsByProvider, routeProvidersByModel)
	if err != nil {
		return types.ProductRuntimePolicy{}, nil, err
	}
	effectivePolicies, err := compileProductChannelPolicies(productID, in.RouteGraph, sharedCredentialGroups, in.RouteGraphEnforcement)
	if err != nil {
		return types.ProductRuntimePolicy{}, nil, err
	}
	return types.ProductRuntimePolicy{
		ProductID:                       productID,
		Revision:                        in.Revision,
		ProductSlug:                     in.ProductSlug,
		Status:                          in.Status,
		CredentialSource:                in.CredentialSource,
		AllowedProviderIDs:              allowedProviders,
		AllowedChannelIDs:               allowedChannels,
		ModelScopeMode:                  in.ModelScopeMode,
		AllowedModels:                   allowedModels,
		FallbackPolicy:                  in.FallbackPolicy,
		BillingMode:                     in.BillingMode,
		RouteStrategy:                   in.RouteStrategy,
		AllowHighLossBridgeWhenNoNative: in.AllowHighLossBridgeWhenNoNative,
		AllowBridgeOnNativeFailure:      in.AllowBridgeOnNativeFailure,
		RouteGraphRequired:              in.RouteGraphRequired,
		RouteGraphEnforcement:           in.RouteGraphEnforcement,
		RouteGraph:                      in.RouteGraph,
	}, effectivePolicies, nil
}

func compileProductPolicyDelete(index int, in types.ProductPolicyDelete) (types.ProductPolicyDelete, error) {
	productID := strings.TrimSpace(in.ProductID)
	if productID == "" {
		return types.ProductPolicyDelete{}, fmt.Errorf("product_policy_deletes[%d].product_id required", index)
	}
	if in.Revision <= 0 {
		return types.ProductPolicyDelete{}, fmt.Errorf("product_policy_deletes[%d].revision must be > 0", index)
	}
	in.ProductID = productID
	return in, nil
}

func compileRouteGraph(index int, in ProductRoutePolicy, endpoints map[string]ProviderEndpoint, credentialsByProvider map[string][]ProviderCredential, routeProvidersByModel map[string]map[string]struct{}) ([]string, []string, []string, error) {
	allowedProviders := copyStrings(in.AllowedProviderIDs)
	allowedChannels := copyStrings(in.AllowedChannelIDs)
	allowedModels := copyStrings(in.AllowedModels)

	if in.RouteGraph == nil {
		return uniqueSorted(allowedProviders), uniqueSorted(allowedChannels), uniqueSorted(allowedModels), nil
	}
	if err := validateRouteGraph(index, strings.TrimSpace(in.ProductID), in.RouteGraph, endpoints, credentialsByProvider, routeProvidersByModel); err != nil {
		return nil, nil, nil, err
	}
	for _, channel := range in.RouteGraph.ProductChannels {
		allowedProviders = append(allowedProviders, channel.ProviderID)
		allowedChannels = append(allowedChannels, channel.ChannelID)
		allowedModels = append(allowedModels, channel.AllowedModels...)
	}
	return uniqueSorted(allowedProviders), uniqueSorted(allowedChannels), uniqueSorted(allowedModels), nil
}

func validateRouteGraph(index int, productID string, graph *types.ProductRouteGraph, endpoints map[string]ProviderEndpoint, credentialsByProvider map[string][]ProviderCredential, routeProvidersByModel map[string]map[string]struct{}) error {
	prefix := fmt.Sprintf("product_policies[%d].route_graph", index)
	knownProviders := make(map[string]struct{}, len(endpoints))
	for providerID := range endpoints {
		knownProviders[providerID] = struct{}{}
	}
	providersWithCredentials := make(map[string]struct{}, len(credentialsByProvider))
	for providerID, credentials := range credentialsByProvider {
		if len(credentials) > 0 {
			providersWithCredentials[providerID] = struct{}{}
		}
	}
	return routegraph.Validate(graph, routegraph.ValidationOptions{
		Prefix:                   prefix,
		ProductID:                productID,
		KnownProviderIDs:         knownProviders,
		ProvidersWithCredentials: providersWithCredentials,
		RouteProvidersByModel:    routeProvidersByModel,
	})
}

func compileProductChannelPolicies(productID string, graph *types.ProductRouteGraph, sharedCredentialGroups map[string]string, routeGraphEnforcement string) ([]types.EffectivePolicy, error) {
	if graph == nil {
		return nil, nil
	}
	var policies []types.EffectivePolicy
	seen := make(map[string]types.EffectivePolicy)
	for _, channel := range graph.ProductChannels {
		if channel.MaxRPM > 0 {
			policy := productChannelRatePolicy(productID, channel, "rpm", int64(channel.MaxRPM), sharedCredentialGroups[channel.ProviderID], routeGraphEnforcement)
			var err error
			policies, err = appendEffectivePolicyOnce(policies, seen, policy)
			if err != nil {
				return nil, err
			}
		}
		if channel.MaxTPM > 0 {
			policy := productChannelRatePolicy(productID, channel, "tpm", int64(channel.MaxTPM), sharedCredentialGroups[channel.ProviderID], routeGraphEnforcement)
			var err error
			policies, err = appendEffectivePolicyOnce(policies, seen, policy)
			if err != nil {
				return nil, err
			}
		}
	}
	return policies, nil
}

func appendEffectivePolicyOnce(policies []types.EffectivePolicy, seen map[string]types.EffectivePolicy, policy types.EffectivePolicy) ([]types.EffectivePolicy, error) {
	key := policy.Scope + "\x00" + policy.ScopeID + "\x00" + policy.Metric
	existing, ok := seen[key]
	if !ok {
		seen[key] = policy
		return append(policies, policy), nil
	}
	if existing.LimitValue == policy.LimitValue && existing.EnforcementMode == policy.EnforcementMode && existing.PeriodKind == policy.PeriodKind && existing.Window == policy.Window {
		return policies, nil
	}
	return nil, fmt.Errorf("conflicting EffectivePolicy for scope_id=%s metric=%s", policy.ScopeID, policy.Metric)
}

func productChannelRatePolicy(productID string, channel types.ProductRouteChannel, metric string, limit int64, sharedCredentialGroup, routeGraphEnforcement string) types.EffectivePolicy {
	meta := map[string]string{
		"product_id":         productID,
		"product_channel_id": channel.ID,
		"channel_id":         channel.ChannelID,
	}
	if sharedCredentialGroup != "" {
		meta["shared_credential_group"] = sharedCredentialGroup
		meta["aggregate_limit_unenforced"] = "true"
	}
	enforcementMode := "shadow"
	if routeGraphEnforcement == "enforce" && channel.Status == "active" {
		enforcementMode = "enforce"
	}
	return types.EffectivePolicy{
		Version:         1,
		PolicyID:        fmt.Sprintf("product_channel:%s:%s:%s", productID, channel.ID, metric),
		PolicyType:      "rate_limit",
		ControlSemantic: "hard_cutoff",
		SubjectSide:     "provider",
		Metric:          metric,
		Scope:           "provider",
		ScopeID:         channel.ProviderID,
		PeriodKind:      "minute",
		Window:          "1m",
		LimitValue:      limit,
		EnforcementMode: enforcementMode,
		Meta:            meta,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := append([]string(nil), values...)
	return out
}

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
