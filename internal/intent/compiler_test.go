package intent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestCompilerProviderExposureMatchesExistingBindingCompiler(t *testing.T) {
	in := channelizedRouteGraphIntent()

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Providers) != 2 {
		t.Fatalf("provider count=%d want 2", len(out.Providers))
	}
	got := out.Providers[0]
	if got.OwnedBy != "kimi-main" || got.ProviderProfile != types.ProviderProfileOpenAICompatible {
		t.Fatalf("provider planner facts mismatch: owned_by=%q provider_profile=%q", got.OwnedBy, got.ProviderProfile)
	}

	// ADR-0012 S4: bindings are composed from served_inbound_protocols
	// (anthropic + responses on an openai_compatible upstream).
	wantBindings, wantDefault, err := types.CompileInboundProtocolBindings(
		types.ProviderProfileOpenAICompatible,
		[]string{types.InboundProtocolAnthropic, types.InboundProtocolResponses},
		[]string{"chat"},
		types.ClientProtocolBindingOptions{PathPrefix: "/v1", TokenCountStrategy: types.TokenCountStrategyUnsupported},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	if !reflect.DeepEqual(got.ProtocolBindings, wantBindings) {
		t.Fatalf("protocol_bindings mismatch\n got: %#v\nwant: %#v", got.ProtocolBindings, wantBindings)
	}
	if got.DefaultProtocolBinding != wantDefault {
		t.Fatalf("default binding=%q want %q", got.DefaultProtocolBinding, wantDefault)
	}
	if !containsBridge(got.ProtocolBindings, "anthropic_to_openai_chat") {
		t.Fatalf("compiled bindings lost bridge: %#v", got.ProtocolBindings)
	}

	if len(out.KeyPools) != 2 {
		t.Fatalf("keypool count=%d want 2", len(out.KeyPools))
	}
	if got := out.KeyPools[0].Entries[0].Ciphertext; got != "secret://channels/kimi-main/key-a" {
		t.Fatalf("key_ref not carried into keypool ciphertext reference: %q", got)
	}
	if got := out.KeyPools[0].Entries[0].ProviderAccountRef; got != "pa_550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("provider_account_ref not carried into keypool entry: %q", got)
	}
}

func TestCompilerRejectsMissingProviderAccountRef(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProviderCredentials[0].ProviderAccountRef = ""
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "provider_account_ref required") {
		t.Fatalf("Compile error=%v want missing provider_account_ref rejection", err)
	}
}

func TestCompilerRejectsCredentialDerivedProviderAccountRef(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProviderCredentials[0].ProviderAccountRef = in.ProviderCredentials[0].KeyID
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "provider_account_ref") {
		t.Fatalf("Compile error=%v want provider_account_ref rejection", err)
	}
	in = channelizedRouteGraphIntent()
	in.ProviderCredentials[0].ProviderAccountRef = in.ProviderCredentials[0].KeyRef
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "provider_account_ref") {
		t.Fatalf("Compile KeyRef-derived provider_account_ref error=%v", err)
	}
}

func TestCompilerPassesProviderIsolationFields(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProviderEndpoints[0].Visibility = "dedicated_only"
	in.ProviderEndpoints[0].CredentialSource = "dedicated_upstream"
	in.ProviderEndpoints[0].AllowedRouteKinds = []string{"dedicated_route"}
	in.ProviderEndpoints[0].OwnerScope = &types.OwnerScope{
		TenantID:      "tenant_demo",
		ProductID:     "prod_coding",
		EntitlementID: "entitlement_demo_001",
	}

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Providers) != 2 {
		t.Fatalf("provider count=%d want 2", len(out.Providers))
	}
	got := out.Providers[0]
	if got.Visibility != "dedicated_only" ||
		got.CredentialSource != "dedicated_upstream" ||
		!reflect.DeepEqual(got.AllowedRouteKinds, []string{"dedicated_route"}) ||
		got.OwnerScope == nil ||
		got.OwnerScope.TenantID != "tenant_demo" ||
		got.OwnerScope.ProductID != "prod_coding" ||
		got.OwnerScope.EntitlementID != "entitlement_demo_001" {
		t.Fatalf("provider isolation fields not passed through: %+v", got)
	}
	if out.Providers[1].Visibility != "" ||
		out.Providers[1].CredentialSource != "" ||
		out.Providers[1].AllowedRouteKinds != nil ||
		out.Providers[1].OwnerScope != nil {
		t.Fatalf("omitted provider isolation fields should stay zero-value for omitempty: %+v", out.Providers[1])
	}
}

func TestCompilerProviderEntitlementCarriesOpaqueProviderAccountRef(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProviderEndpoints[0].Visibility = types.ProviderVisibilityDedicatedOnly
	in.ProviderEndpoints[0].CredentialSource = types.ProviderCredentialSourceProviderEntitlement
	in.ProviderEndpoints[0].AllowedRouteKinds = []string{types.ProviderRouteKindDedicated}
	in.ProviderEndpoints[0].ProviderAccountRef = "overseas-prod-1"

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := out.Providers[0].ProviderAccountRef; got != in.ProviderEndpoints[0].ProviderAccountRef {
		t.Fatalf("provider_entitlement provider_account_ref=%q want %q", got, in.ProviderEndpoints[0].ProviderAccountRef)
	}

	in.ProviderEndpoints[0].ProviderAccountRef = ""
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "provider_account_ref required for provider_entitlement") {
		t.Fatalf("Compile missing provider_entitlement provider_account_ref error=%v", err)
	}

	in = channelizedRouteGraphIntent()
	in.ProviderEndpoints[0].Visibility = types.ProviderVisibilityDedicatedOnly
	in.ProviderEndpoints[0].CredentialSource = types.ProviderCredentialSourceProviderEntitlement
	in.ProviderEndpoints[0].AllowedRouteKinds = []string{types.ProviderRouteKindDedicated}
	in.ProviderEndpoints[0].ProviderAccountRef = in.ProviderEndpoints[0].ID
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "must not reuse id") {
		t.Fatalf("Compile provider ID-derived provider_account_ref error=%v", err)
	}
	in.ProviderEndpoints[0].BaseURL = "provider-endpoint-1"
	in.ProviderEndpoints[0].ProviderAccountRef = in.ProviderEndpoints[0].BaseURL
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "must not reuse base_url") {
		t.Fatalf("Compile BaseURL-derived provider_account_ref error=%v", err)
	}
}

func TestCompilerRouteGraphRuntimePassthroughAndInvariants(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProductPolicies[0].Revision = 1700000000000123

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Routes) != 1 {
		t.Fatalf("route count=%d want 1", len(out.Routes))
	}
	route := out.Routes[0]
	if route.ModelID != "kimi-k2.6" || len(route.Providers) != 2 {
		t.Fatalf("unexpected route: %#v", route)
	}
	if route.Providers[0].ProviderID != "pc_101" || route.Providers[0].UpstreamModel != "kimi-k2.6" {
		t.Fatalf("unexpected provider route entry: %#v", route.Providers[0])
	}

	if len(out.ModelCatalog) != 1 {
		t.Fatalf("model catalog count=%d want 1", len(out.ModelCatalog))
	}
	if out.ModelCatalog[0].ModelID != "kimi-k2.6" || !reflect.DeepEqual(out.ModelCatalog[0].Capabilities, []string{"chat"}) {
		t.Fatalf("unexpected model catalog: %#v", out.ModelCatalog[0])
	}

	if len(out.ProductPolicies) != 1 {
		t.Fatalf("product policy count=%d want 1", len(out.ProductPolicies))
	}
	policy := out.ProductPolicies[0]
	if policy.ProductID != "prod_coding" {
		t.Fatalf("product_id=%q want prod_coding", policy.ProductID)
	}
	if policy.Revision != 1700000000000123 {
		t.Fatalf("product policy revision=%d want 1700000000000123", policy.Revision)
	}
	if !reflect.DeepEqual(policy.AllowedProviderIDs, []string{"pc_101", "pc_102"}) {
		t.Fatalf("allowed providers=%v want [pc_101 pc_102]", policy.AllowedProviderIDs)
	}
	if policy.RouteGraph == nil {
		t.Fatal("route_graph missing from ProductRuntimePolicy")
	}
	if !policy.RouteGraphRequired || policy.RouteGraphEnforcement != "enforce" {
		t.Fatalf("route_graph gate not passed through: required=%v enforcement=%q", policy.RouteGraphRequired, policy.RouteGraphEnforcement)
	}
	if !reflect.DeepEqual(policy.RouteGraph, in.ProductPolicies[0].RouteGraph) {
		t.Fatalf("route_graph was not passed through intact\n got: %#v\nwant: %#v", policy.RouteGraph, in.ProductPolicies[0].RouteGraph)
	}
	if policy.RouteGraph.Groups[0].Members[0].MaxRPM != 9999 {
		t.Fatalf("group member max_rpm not preserved in route_graph: %#v", policy.RouteGraph.Groups[0].Members[0])
	}
	if policy.FallbackPolicy != "within_product" || policy.RouteStrategy != "priority" {
		t.Fatalf("unexpected product policy route fields: %#v", policy)
	}

	assertRuntimeInvariant(t, out, "kimi-k2.6")
	assertEffectivePolicy(t, out.EffectivePolicies, "pc_101", "rpm", 500, true)
	assertEffectivePolicy(t, out.EffectivePolicies, "pc_101", "tpm", 100000, true)
	assertEffectivePolicy(t, out.EffectivePolicies, "pc_102", "rpm", 200, true)
	assertEffectivePolicy(t, out.EffectivePolicies, "pc_102", "tpm", 50000, true)
	assertEffectivePolicyMode(t, out.EffectivePolicies, "pc_101", "rpm", "enforce")
	assertEffectivePolicyMode(t, out.EffectivePolicies, "pc_102", "rpm", "shadow")
	for _, policy := range out.EffectivePolicies {
		if policy.LimitValue == 9999 || policy.LimitValue == 888888 {
			t.Fatalf("group member limit compiled unexpectedly: %#v", policy)
		}
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings=%#v want one shared credential warning", out.Warnings)
	}
	warning := out.Warnings[0]
	if warning.Code != "shared_credential_group" || warning.Meta["aggregate_limit_unenforced"] != "true" {
		t.Fatalf("unexpected warning: %#v", warning)
	}
	if !reflect.DeepEqual(warning.Providers, []string{"pc_101", "pc_102"}) {
		t.Fatalf("warning providers=%v want [pc_101 pc_102]", warning.Providers)
	}
}

func TestCompilerProductPolicyDeleteIntent(t *testing.T) {
	in := RuntimeIntent{
		SchemaVersion: CurrentIntentSchemaVersion,
		ProductPolicyDeletes: []types.ProductPolicyDelete{
			{ProductID: "prod_coding", Revision: 1700000000000456},
		},
	}

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.ProductPolicyDeletes) != 1 {
		t.Fatalf("product policy delete count=%d want 1", len(out.ProductPolicyDeletes))
	}
	got := out.ProductPolicyDeletes[0]
	if got.ProductID != "prod_coding" || got.Revision != 1700000000000456 {
		t.Fatalf("unexpected product policy delete: %+v", got)
	}
}

func TestCompilerProductPolicyDeleteRequiresRevision(t *testing.T) {
	in := RuntimeIntent{
		SchemaVersion: CurrentIntentSchemaVersion,
		ProductPolicyDeletes: []types.ProductPolicyDelete{
			{ProductID: "prod_coding"},
		},
	}

	_, err := Compile(in)
	if err == nil || !strings.Contains(err.Error(), "product_policy_deletes[0].revision must be > 0") {
		t.Fatalf("Compile error=%v want revision rejection", err)
	}
}

// ADR-0015 S5b: the product/route protocol_strategy loss switch is retired
// from the intent contract. The ADR-0011 allow_* bridge flags still compile
// through, and the compiled runtime payloads must not carry any
// protocol_strategy field.
func TestCompilerPassesBridgeAllowFlagsWithoutRetiredProtocolStrategy(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProductPolicies[0].AllowHighLossBridgeWhenNoNative = true
	in.ProductPolicies[0].AllowBridgeOnNativeFailure = true

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	policy := out.ProductPolicies[0]
	if !policy.AllowHighLossBridgeWhenNoNative || !policy.AllowBridgeOnNativeFailure {
		t.Fatalf("allow_* bridge flags were not compiled: %+v", policy)
	}

	routeRaw, err := json.Marshal(out.Routes[0])
	if err != nil {
		t.Fatalf("marshal compiled route: %v", err)
	}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal compiled product policy: %v", err)
	}
	if strings.Contains(string(routeRaw), "protocol_strategy") {
		t.Fatalf("retired protocol_strategy leaked into compiled route: %s", routeRaw)
	}
	if strings.Contains(string(policyRaw), `"protocol_strategy"`) {
		t.Fatalf("retired protocol_strategy leaked into compiled product policy: %s", policyRaw)
	}
}

func TestCompilerAllowsClientExposureWithoutCapabilities(t *testing.T) {
	raw := `{
		"schema_version": 2,
		"provider_endpoints": [{
			"id": "openai-prod",
			"provider_profile": "openai_compatible",
			"base_url": "https://api.openai.example",
			"status": "active",
			"path_prefix": "/v1"
		}],
		"provider_credentials": [{
			"provider_id": "openai-prod",
			"key_ref": "secret://providers/openai-prod/key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440020",
			"status": "enabled",
			"allowed_models": ["gpt-4o-mini"]
		}],
		"client_exposures": [{
			"provider_id": "openai-prod",
			"served_inbound_protocols": ["openai"],
			"token_count_strategy": "unsupported"
		}],
		"model_routes": [{
			"logical_model": "gpt-4o-mini",
			"routes": [{
				"provider_id": "openai-prod",
				"upstream_model": "gpt-4o-mini",
				"status": "active"
			}]
		}]
	}`
	var in RuntimeIntent
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal intent: %v", err)
	}
	if len(in.ClientExposures) != 1 || in.ClientExposures[0].Capabilities != nil {
		t.Fatalf("test intent should omit client_exposure capabilities: %#v", in.ClientExposures)
	}

	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Providers) != 1 {
		t.Fatalf("provider count=%d want 1", len(out.Providers))
	}
	provider := out.Providers[0]
	binding, ok := protocolBindingByName(provider.ProtocolBindings, "openai-chat")
	if !ok {
		t.Fatalf("openai-chat binding missing from ProviderInfo: %#v", provider.ProtocolBindings)
	}
	if binding.Bridge != "" || binding.TranslatorKey != "passthrough" {
		t.Fatalf("same-wire chat binding should stay passthrough without a bridge: %#v", binding)
	}
	if !hasCapabilityProfile(binding, "openai.chat_completions.full") {
		t.Fatalf("same-wire chat binding lost full chat-completions profile: %#v", binding)
	}
	if len(binding.Accepts) != 1 ||
		binding.Accepts[0].Protocol != "openai" ||
		binding.Accepts[0].RequestContract != "chat_completions" ||
		!reflect.DeepEqual(binding.Accepts[0].Capabilities, []string{"chat"}) {
		t.Fatalf("chat binding was not derived from client compatibility/request contract: %#v", binding.Accepts)
	}
	if len(out.ModelCatalog) != 1 || !reflect.DeepEqual(out.ModelCatalog[0].Capabilities, []string{"chat"}) {
		t.Fatalf("model_route empty capabilities should retain chat fallback: %#v", out.ModelCatalog)
	}
	if len(out.Routes) != 1 || len(out.Routes[0].Providers) != 1 || len(out.Routes[0].Providers[0].Capabilities) != 0 {
		t.Fatalf("route provider should not inherit client_exposure capabilities: %#v", out.Routes)
	}
}

func TestCompilerFailsClosedForMissingFieldsAndIllegalExposure(t *testing.T) {
	cases := []struct {
		name string
		in   RuntimeIntent
		want string
	}{
		{
			name: "missing provider id",
			in: RuntimeIntent{
				SchemaVersion: CurrentIntentSchemaVersion,
				ProviderEndpoints: []ProviderEndpoint{{
					ProviderProfile: types.ProviderProfileOpenAICompatible,
					BaseURL:         "https://api.openai.example",
					Status:          "active",
				}},
			},
			want: "provider_endpoints[0].id required",
		},
		{
			name: "illegal exposure",
			in: RuntimeIntent{
				SchemaVersion: CurrentIntentSchemaVersion,
				ProviderEndpoints: []ProviderEndpoint{{
					ID:              "openai-prod",
					ProviderProfile: types.ProviderProfileOpenAICompatible,
					BaseURL:         "https://api.openai.example",
					Status:          "active",
				}},
				ClientExposures: []ClientExposure{{
					ProviderID:             "openai-prod",
					ServedInboundProtocols: []string{types.InboundProtocolGemini},
					Capabilities:           []string{"chat"},
					TokenCountStrategy:     types.TokenCountStrategyUnsupported,
					ProtocolPathPrefix:     "/v1",
				}},
			},
			want: "no registered bridge for inbound dialect",
		},
		{
			name: "unknown exposure provider",
			in: RuntimeIntent{
				SchemaVersion: CurrentIntentSchemaVersion,
				ProviderEndpoints: []ProviderEndpoint{{
					ID:              "openai-prod",
					ProviderProfile: types.ProviderProfileOpenAICompatible,
					BaseURL:         "https://api.openai.example",
					Status:          "active",
				}},
				ClientExposures: []ClientExposure{{
					ProviderID:             "missing",
					ServedInboundProtocols: []string{types.InboundProtocolResponses},
					Capabilities:           []string{"chat"},
					TokenCountStrategy:     types.TokenCountStrategyUnsupported,
					ProtocolPathPrefix:     "/v1",
				}},
			},
			want: "client_exposures[0].provider_id unknown",
		},
		{
			name: "incompatible schema version",
			in:   RuntimeIntent{SchemaVersion: CurrentIntentSchemaVersion + 1},
			want: "unsupported intent schema_version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.in)
			if err == nil {
				t.Fatalf("Compile succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCompilerRejectsRouteGraphProviderReferencesFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RuntimeIntent)
		want   string
	}{
		{
			name: "product channel unknown provider",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.ProductChannels[0].ProviderID = "pc_missing"
			},
			want: "route_graph.product_channels[0].provider_id unknown: pc_missing",
		},
		{
			name: "group member unknown provider",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.Groups[0].Members[0].ProviderID = "pc_missing"
			},
			want: "route_graph.groups[0].members[0].provider_id unknown: pc_missing",
		},
		{
			name: "unknown default target",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.DefaultTarget.TargetID = "missing"
			},
			want: "route_graph.default_target unknown",
		},
		{
			name: "assignment add rejected",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.Assignments[0].Mode = "ADD"
			},
			want: "route_graph.assignments[0].mode=ADD not allowed",
		},
		{
			name: "route config missing provider for allowed model",
			mutate: func(in *RuntimeIntent) {
				in.ModelRoutes[0].Routes = in.ModelRoutes[0].Routes[:1]
			},
			want: "route_graph.product_channels[1].provider_id pc_102 missing from model_routes[kimi-k2.6]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := channelizedRouteGraphIntent()
			tc.mutate(&in)
			_, err := Compile(in)
			if err == nil {
				t.Fatalf("Compile succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCompilerRejectsRouteGraphProviderIDNotOneToOne(t *testing.T) {
	in := channelizedRouteGraphIntent()
	in.ProductPolicies[0].RouteGraph.ProductChannels[1].ProviderID = "pc_101"
	in.ProductPolicies[0].RouteGraph.Groups[0].Members[1].ProviderID = "pc_101"

	_, err := Compile(in)
	if err == nil {
		t.Fatal("Compile succeeded, want provider_id duplicate error")
	}
	if !strings.Contains(err.Error(), "route_graph.product_channels[1].provider_id duplicate: pc_101") {
		t.Fatalf("error=%q want provider_id duplicate", err.Error())
	}
}

func TestCompilerRejectsIncompleteRouteGraph(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RuntimeIntent)
		want   string
	}{
		{
			name: "missing schema version",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.SchemaVersion = 0
			},
			want: "route_graph.schema_version required",
		},
		{
			name: "missing default target type",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.DefaultTarget.TargetType = ""
			},
			want: "route_graph.default_target.target_type required",
		},
		{
			name: "missing product channels",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.ProductChannels = nil
			},
			want: "route_graph.product_channels required",
		},
		{
			name: "missing assignment mode",
			mutate: func(in *RuntimeIntent) {
				in.ProductPolicies[0].RouteGraph.Assignments[0].Mode = ""
			},
			want: "route_graph.assignments[0].mode required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := channelizedRouteGraphIntent()
			tc.mutate(&in)
			_, err := Compile(in)
			if err == nil {
				t.Fatalf("Compile succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCompileProductChannelPoliciesDedupesIdenticalScopeMetric(t *testing.T) {
	graph := &types.ProductRouteGraph{
		ProductChannels: []types.ProductRouteChannel{
			{ID: "pc_101_a", ProviderID: "pc_101", ChannelID: "kimi-main", MaxRPM: 500, Status: "active"},
			{ID: "pc_101_b", ProviderID: "pc_101", ChannelID: "kimi-backup", MaxRPM: 500, Status: "active"},
		},
	}

	policies, err := compileProductChannelPolicies("prod_coding", graph, nil, "enforce")
	if err != nil {
		t.Fatalf("compileProductChannelPolicies: %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("policy count=%d want deduped 1: %#v", len(policies), policies)
	}
	if policies[0].ScopeID != "pc_101" || policies[0].Metric != "rpm" || policies[0].LimitValue != 500 {
		t.Fatalf("unexpected deduped policy: %#v", policies[0])
	}
}

func TestCompileProductChannelPoliciesRejectsConflictingScopeMetric(t *testing.T) {
	graph := &types.ProductRouteGraph{
		ProductChannels: []types.ProductRouteChannel{
			{ID: "pc_101_a", ProviderID: "pc_101", ChannelID: "kimi-main", MaxRPM: 500, Status: "active"},
			{ID: "pc_101_b", ProviderID: "pc_101", ChannelID: "kimi-backup", MaxRPM: 600, Status: "active"},
		},
	}

	_, err := compileProductChannelPolicies("prod_coding", graph, nil, "enforce")
	if err == nil {
		t.Fatal("compileProductChannelPolicies succeeded, want conflict")
	}
	if !strings.Contains(err.Error(), "conflicting EffectivePolicy for scope_id=pc_101 metric=rpm") {
		t.Fatalf("error=%q want conflicting EffectivePolicy", err.Error())
	}
}

func TestCompileProductChannelPoliciesNonActiveChannelsAreShadow(t *testing.T) {
	graph := &types.ProductRouteGraph{
		ProductChannels: []types.ProductRouteChannel{
			{ID: "pc_101", ProviderID: "pc_101", ChannelID: "kimi-main", MaxRPM: 500, Status: "active"},
			{ID: "pc_102", ProviderID: "pc_102", ChannelID: "kimi-testing", MaxRPM: 200, Status: "testing"},
			{ID: "pc_103", ProviderID: "pc_103", ChannelID: "kimi-disabled", MaxRPM: 100, Status: "disabled"},
		},
	}

	policies, err := compileProductChannelPolicies("prod_coding", graph, nil, "enforce")
	if err != nil {
		t.Fatalf("compileProductChannelPolicies: %v", err)
	}
	assertEffectivePolicyMode(t, policies, "pc_101", "rpm", "enforce")
	assertEffectivePolicyMode(t, policies, "pc_102", "rpm", "shadow")
	assertEffectivePolicyMode(t, policies, "pc_103", "rpm", "shadow")
}

func TestCompileProductChannelPoliciesRouteGraphEnforcementModes(t *testing.T) {
	cases := []struct {
		name                  string
		routeGraphEnforcement string
		channelStatus         string
		wantMode              string
	}{
		{
			name:                  "empty enforcement active channel defaults shadow",
			routeGraphEnforcement: "",
			channelStatus:         "active",
			wantMode:              "shadow",
		},
		{
			name:                  "enforce active channel enforces",
			routeGraphEnforcement: "enforce",
			channelStatus:         "active",
			wantMode:              "enforce",
		},
		{
			name:                  "enforce testing channel remains shadow",
			routeGraphEnforcement: "enforce",
			channelStatus:         "testing",
			wantMode:              "shadow",
		},
		{
			name:                  "enforce disabled channel remains shadow",
			routeGraphEnforcement: "enforce",
			channelStatus:         "disabled",
			wantMode:              "shadow",
		},
		{
			name:                  "shadow active channel remains shadow",
			routeGraphEnforcement: "shadow",
			channelStatus:         "active",
			wantMode:              "shadow",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := &types.ProductRouteGraph{
				ProductChannels: []types.ProductRouteChannel{
					{ID: "pc_101", ProviderID: "pc_101", ChannelID: "kimi-main", MaxRPM: 500, Status: tc.channelStatus},
				},
			}

			policies, err := compileProductChannelPolicies("prod_coding", graph, nil, tc.routeGraphEnforcement)
			if err != nil {
				t.Fatalf("compileProductChannelPolicies: %v", err)
			}
			t.Logf("route_graph_enforcement=%q channel_status=%q enforcement_mode=%q", tc.routeGraphEnforcement, tc.channelStatus, policies[0].EnforcementMode)
			assertEffectivePolicyMode(t, policies, "pc_101", "rpm", tc.wantMode)
		})
	}
}

func channelizedRouteGraphIntent() RuntimeIntent {
	return RuntimeIntent{
		SchemaVersion: CurrentIntentSchemaVersion,
		ProviderEndpoints: []ProviderEndpoint{
			{
				ID:              "pc_101",
				ProviderProfile: types.ProviderProfileOpenAICompatible,
				BaseURL:         "https://api.moonshot.example",
				Region:          "cn",
				Status:          "active",
				Name:            "Kimi coding channel",
				Code:            "kimi-main",
				OwnedBy:         "kimi-main",
				KeyStrategy:     "priority",
				PathPrefix:      "/v1",
			},
			{
				ID:              "pc_102",
				ProviderProfile: types.ProviderProfileOpenAICompatible,
				BaseURL:         "https://api.moonshot.example",
				Region:          "cn",
				Status:          "active",
				Name:            "Kimi chat channel",
				Code:            "kimi-main",
				OwnedBy:         "kimi-main",
				KeyStrategy:     "priority",
				PathPrefix:      "/v1",
			},
		},
		ProviderCredentials: []ProviderCredential{
			{
				ProviderID:         "pc_101",
				KeyRef:             "secret://channels/kimi-main/key-a",
				KeyID:              "key-a",
				ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440000",
				Status:             "enabled",
				AllowedModels:      []string{"kimi-k2.6"},
				RPMLimit:           500,
				TPMLimit:           100000,
				Priority:           1,
				Weight:             100,
			},
			{
				ProviderID:         "pc_102",
				KeyRef:             "secret://channels/kimi-main/key-a",
				KeyID:              "key-a",
				ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440000",
				Status:             "enabled",
				AllowedModels:      []string{"kimi-k2.6"},
				RPMLimit:           200,
				TPMLimit:           50000,
				Priority:           1,
				Weight:             100,
			},
		},
		ClientExposures: []ClientExposure{
			{
				// ADR-0012 equivalence: claude_code→anthropic, codex→responses.
				ProviderID:             "pc_101",
				ServedInboundProtocols: []string{types.InboundProtocolAnthropic, types.InboundProtocolResponses},
				Capabilities:           []string{"chat"},
				TokenCountStrategy:     types.TokenCountStrategyUnsupported,
				ProtocolPathPrefix:     "/v1",
			},
			{
				// ADR-0012 equivalence: openai_sdk→openai.
				ProviderID:             "pc_102",
				ServedInboundProtocols: []string{types.InboundProtocolOpenAI},
				Capabilities:           []string{"chat"},
				TokenCountStrategy:     types.TokenCountStrategyUnsupported,
				ProtocolPathPrefix:     "/v1",
			},
		},
		ModelRoutes: []ModelRouteIntent{{
			LogicalModel:     "kimi-k2.6",
			Capabilities:     []string{"chat"},
			ContextWindow:    128000,
			MaxOutput:        16384,
			OwnedBy:          "kimi-main",
			ProviderStrategy: "priority",
			Routes: []ModelRouteTarget{
				{
					ProviderID:    "pc_101",
					UpstreamModel: "kimi-k2.6",
					Priority:      1,
					Weight:        100,
					Status:        "active",
				},
				{
					ProviderID:    "pc_102",
					UpstreamModel: "kimi-k2.6",
					Priority:      2,
					Weight:        50,
					Status:        "active",
				},
			},
		}},
		ProductPolicies: []ProductRoutePolicy{{
			ProductID:             "prod_coding",
			Status:                "active",
			CredentialSource:      "dedicated_upstream",
			AllowedModels:         []string{"kimi-k2.6"},
			ModelScopeMode:        "product_allowlist",
			FallbackPolicy:        "within_product",
			BillingMode:           "token_plan_quota",
			RouteStrategy:         "priority",
			AllowedProviderIDs:    []string{"pc_101"},
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
					Strategy: "weighted_failover",
					Members: []types.ProductRouteGroupMember{
						{
							ProductChannelID: "pc_101",
							ChannelID:        "kimi-main",
							ProviderID:       "pc_101",
							Priority:         1,
							Weight:           100,
							Role:             "primary",
							MaxRPM:           9999,
							MaxTPM:           888888,
						},
						{
							ProductChannelID: "pc_102",
							ChannelID:        "kimi-main",
							ProviderID:       "pc_102",
							Priority:         2,
							Weight:           50,
							Role:             "fallback",
						},
					},
				}},
				ProductChannels: []types.ProductRouteChannel{
					{
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
					},
					{
						ID:            "pc_102",
						ChannelID:     "kimi-main",
						ProviderID:    "pc_102",
						AllowedModels: []string{"kimi-k2.6"},
						DeniedModels:  []string{},
						Priority:      2,
						Weight:        50,
						MaxRPM:        200,
						MaxTPM:        50000,
						Status:        "testing",
					},
				},
				Assignments: []types.ProductRouteAssignment{
					{
						ID:                  "asg_vip",
						ProductID:           "prod_coding",
						SubjectType:         "USER_SEGMENT",
						SubjectID:           "seg_vip",
						ModelPattern:        "kimi-*",
						ClientCompatibility: "claude_code",
						TargetType:          "GROUP",
						TargetID:            "primary",
						Mode:                "PREFER",
						Priority:            10,
					},
					{
						ID:          "asg_user",
						ProductID:   "prod_coding",
						SubjectType: "USER",
						SubjectID:   "usr_123",
						TargetType:  "PRODUCT_CHANNEL",
						TargetID:    "pc_101",
						Mode:        "ONLY",
						Priority:    20,
					},
				},
			},
		}},
	}
}

func assertRuntimeInvariant(t *testing.T, out *RuntimeConfig, model string) {
	t.Helper()
	providers := map[string]struct{}{}
	for _, provider := range out.Providers {
		providers[provider.ID] = struct{}{}
	}
	keyPools := map[string]struct{}{}
	for _, pool := range out.KeyPools {
		keyPools[pool.Meta.ProviderID] = struct{}{}
	}
	routeProviders := map[string]struct{}{}
	for _, route := range out.Routes {
		if route.ModelID != model {
			continue
		}
		for _, provider := range route.Providers {
			routeProviders[provider.ProviderID] = struct{}{}
		}
	}
	policyScopes := map[string]struct{}{}
	for _, policy := range out.EffectivePolicies {
		policyScopes[policy.ScopeID] = struct{}{}
	}
	for _, productPolicy := range out.ProductPolicies {
		if productPolicy.RouteGraph == nil {
			continue
		}
		for _, channel := range productPolicy.RouteGraph.ProductChannels {
			providerID := channel.ProviderID
			if _, ok := providers[providerID]; !ok {
				t.Fatalf("route_graph provider %s missing from ProviderInfo", providerID)
			}
			if _, ok := keyPools[providerID]; !ok {
				t.Fatalf("route_graph provider %s missing from KeyPool.meta.provider_id", providerID)
			}
			if _, ok := routeProviders[providerID]; !ok {
				t.Fatalf("route_graph provider %s missing from RouteConfig.providers for %s", providerID, model)
			}
			if _, ok := policyScopes[providerID]; !ok {
				t.Fatalf("route_graph provider %s missing from EffectivePolicy.scope_id", providerID)
			}
		}
	}
}

func assertEffectivePolicy(t *testing.T, policies []types.EffectivePolicy, scopeID, metric string, limit int64, wantSharedMeta bool) {
	t.Helper()
	for _, policy := range policies {
		if policy.ScopeID == scopeID && policy.Metric == metric {
			if policy.Scope != "provider" || policy.LimitValue != limit {
				t.Fatalf("effective policy for %s/%s = %#v", scopeID, metric, policy)
			}
			if wantSharedMeta && policy.Meta["aggregate_limit_unenforced"] != "true" {
				t.Fatalf("shared credential meta missing on policy: %#v", policy)
			}
			return
		}
	}
	t.Fatalf("missing effective policy scope_id=%s metric=%s in %#v", scopeID, metric, policies)
}

func assertEffectivePolicyMode(t *testing.T, policies []types.EffectivePolicy, scopeID, metric, mode string) {
	t.Helper()
	for _, policy := range policies {
		if policy.ScopeID == scopeID && policy.Metric == metric {
			if policy.EnforcementMode != mode {
				t.Fatalf("effective policy mode for %s/%s = %q want %q: %#v", scopeID, metric, policy.EnforcementMode, mode, policy)
			}
			return
		}
	}
	t.Fatalf("missing effective policy scope_id=%s metric=%s in %#v", scopeID, metric, policies)
}

func containsBridge(bindings []types.ProtocolBinding, bridge string) bool {
	for _, binding := range bindings {
		if binding.Bridge == bridge {
			return true
		}
	}
	return false
}

func protocolBindingByName(bindings []types.ProtocolBinding, name string) (types.ProtocolBinding, bool) {
	for _, binding := range bindings {
		if binding.Name == name {
			return binding, true
		}
	}
	return types.ProtocolBinding{}, false
}

func hasCapabilityProfile(binding types.ProtocolBinding, profile string) bool {
	for _, value := range binding.CapabilityProfiles {
		if value == profile {
			return true
		}
	}
	return false
}
