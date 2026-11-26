package intent

import (
	"reflect"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// glmCodingExplicitIntent models the GLM coding deployment as 2 upstream
// endpoints driven by explicit served_inbound_protocols (ADR-0012):
//   - openai endpoint (glm_coding): serves openai + responses
//   - anthropic endpoint (anthropic_native): serves anthropic
//
// Expected: 3 explicit channels total (openai passthrough + responses bridge +
// anthropic passthrough), no anthropic→chat bridge, no auto.
func glmCodingExplicitIntent() RuntimeIntent {
	return RuntimeIntent{
		SchemaVersion: CurrentIntentSchemaVersion,
		ProviderEndpoints: []ProviderEndpoint{
			{
				ID:              "pc_openai",
				ProviderProfile: types.ProviderProfileGLMCoding,
				BaseURL:         "https://open.bigmodel.cn/api/coding/paas/v4",
				Status:          "active",
				Name:            "GLM coding openai",
				Code:            "glm-openai",
				OwnedBy:         "glm",
				KeyStrategy:     "priority",
			},
			{
				ID:              "pc_anthropic",
				ProviderProfile: types.ProviderProfileAnthropicNative,
				BaseURL:         "https://open.bigmodel.cn/api/anthropic/v1",
				Status:          "active",
				Name:            "GLM coding anthropic",
				Code:            "glm-anthropic",
				OwnedBy:         "glm",
				KeyStrategy:     "priority",
			},
		},
		ProviderCredentials: []ProviderCredential{
			{ProviderID: "pc_openai", KeyRef: "secret://glm/key-a", KeyID: "key-a", ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440010", Status: "enabled", AllowedModels: []string{"glm-4.6"}, Priority: 1, Weight: 100},
			{ProviderID: "pc_anthropic", KeyRef: "secret://glm/key-b", KeyID: "key-b", ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440010", Status: "enabled", AllowedModels: []string{"glm-4.6"}, Priority: 1, Weight: 100},
		},
		ClientExposures: []ClientExposure{
			{
				ProviderID:             "pc_openai",
				ServedInboundProtocols: []string{types.InboundProtocolOpenAI, types.InboundProtocolResponses},
				Capabilities:           []string{"chat"},
			},
			{
				ProviderID:             "pc_anthropic",
				ServedInboundProtocols: []string{types.InboundProtocolAnthropic},
				Capabilities:           []string{"chat"},
				TokenCountStrategy:     types.TokenCountStrategyUnsupported,
			},
		},
		ModelRoutes: []ModelRouteIntent{{
			LogicalModel:     "glm-4.6",
			Capabilities:     []string{"chat"},
			OwnedBy:          "glm",
			ProviderStrategy: "priority",
			Routes: []ModelRouteTarget{
				{ProviderID: "pc_openai", UpstreamModel: "glm-4.6", Priority: 1, Weight: 100, Status: "active"},
				{ProviderID: "pc_anthropic", UpstreamModel: "glm-4.6", Priority: 2, Weight: 100, Status: "active"},
			},
		}},
	}
}

func providerByID(out *RuntimeConfig, id string) (types.ProviderInfo, bool) {
	for _, p := range out.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return types.ProviderInfo{}, false
}

func bindingNamesOf(bindings []types.ProtocolBinding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Name)
	}
	return out
}

func TestCompile_GLMCoding_ThreeChannelsFromInboundComposition(t *testing.T) {
	out, err := Compile(glmCodingExplicitIntent())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	openai, ok := providerByID(out, "pc_openai")
	if !ok {
		t.Fatalf("pc_openai missing")
	}
	wantOpenAI := []string{"glm-coding-chat", "responses-to-openai-chat"}
	if !reflect.DeepEqual(bindingNamesOf(openai.ProtocolBindings), wantOpenAI) {
		t.Fatalf("pc_openai bindings=%v want %v", bindingNamesOf(openai.ProtocolBindings), wantOpenAI)
	}
	for _, b := range openai.ProtocolBindings {
		if b.Bridge == "anthropic_to_openai_chat" {
			t.Fatalf("pc_openai must NOT contain anthropic→openai bridge: %+v", b)
		}
		// G4 P2: publisher emits pure dialects only; the vendor identity moves
		// to compat_api_type (adapter-bundle selector).
		if b.OutboundProtocol != "openai" {
			t.Fatalf("pc_openai binding %s outbound=%q want openai (wire purity)", b.Name, b.OutboundProtocol)
		}
		for _, a := range b.Accepts {
			if a.Protocol == "glm_coding" || a.Protocol == "glm" {
				t.Fatalf("pc_openai binding %s still accepts vendor protocol %q", b.Name, a.Protocol)
			}
		}
		if b.Name == "glm-coding-chat" && b.CompatAPIType != "glm_coding" {
			t.Fatalf("glm-coding-chat compat_api_type=%q want glm_coding (bundle selector)", b.CompatAPIType)
		}
		t.Logf("pc_openai binding name=%s bridge=%q translator=%q accepts=%+v outbound=%s",
			b.Name, b.Bridge, b.TranslatorKey, b.Accepts, b.OutboundProtocol)
	}

	anthropic, ok := providerByID(out, "pc_anthropic")
	if !ok {
		t.Fatalf("pc_anthropic missing")
	}
	if !reflect.DeepEqual(bindingNamesOf(anthropic.ProtocolBindings), []string{"anthropic-messages"}) {
		t.Fatalf("pc_anthropic bindings=%v want [anthropic-messages]", bindingNamesOf(anthropic.ProtocolBindings))
	}
	for _, b := range anthropic.ProtocolBindings {
		t.Logf("pc_anthropic binding name=%s bridge=%q translator=%q accepts=%+v outbound=%s",
			b.Name, b.Bridge, b.TranslatorKey, b.Accepts, b.OutboundProtocol)
	}

	// 3 explicit channels across 2 endpoints, no anthropic_to_chat anywhere.
	total := len(openai.ProtocolBindings) + len(anthropic.ProtocolBindings)
	if total != 3 {
		t.Fatalf("total channels=%d want 3", total)
	}

	// Derived compatibility view on the openai endpoint (G4 P2: pure dialects
	// only — the glm_coding accepts row is no longer emitted).
	if !reflect.DeepEqual(openai.InboundProtocols, []string{"openai", "responses"}) {
		t.Fatalf("pc_openai inbound_protocols=%v want [openai responses]", openai.InboundProtocols)
	}
	if !reflect.DeepEqual(anthropic.InboundProtocols, []string{"anthropic"}) {
		t.Fatalf("pc_anthropic inbound_protocols=%v", anthropic.InboundProtocols)
	}
	if !reflect.DeepEqual(anthropic.ClientCompatibilities, []string{"claude_code"}) {
		t.Fatalf("pc_anthropic client_compatibilities=%v want [claude_code]", anthropic.ClientCompatibilities)
	}
}

// ADR-0012 S4: served_inbound_protocols is the single compile input. The
// equivalent of the retired App list [claude_code, codex] on an
// openai_compatible upstream is [anthropic, responses]; this must compile to
// the anthropic→openai and responses→openai bridge bindings.
func TestCompile_ServedInboundProtocolsEquivalence(t *testing.T) {
	in := channelizedRouteGraphIntent()
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, ok := providerByID(out, "pc_101")
	if !ok {
		t.Fatalf("pc_101 missing")
	}
	wantBindings, wantDefault, err := types.CompileInboundProtocolBindings(
		types.ProviderProfileOpenAICompatible,
		[]string{types.InboundProtocolAnthropic, types.InboundProtocolResponses},
		[]string{"chat"},
		types.ClientProtocolBindingOptions{PathPrefix: "/v1", TokenCountStrategy: types.TokenCountStrategyUnsupported},
	)
	if err != nil {
		t.Fatalf("inbound compile: %v", err)
	}
	if !reflect.DeepEqual(got.ProtocolBindings, wantBindings) {
		t.Fatalf("bindings drifted\n got=%v\nwant=%v", bindingNamesOf(got.ProtocolBindings), bindingNamesOf(wantBindings))
	}
	if got.DefaultProtocolBinding != wantDefault {
		t.Fatalf("default=%q want %q", got.DefaultProtocolBinding, wantDefault)
	}
	// Derived compatibility view still populated (observation-only).
	if len(got.InboundProtocols) == 0 {
		t.Fatalf("compile should derive inbound_protocols")
	}
}

// served_inbound_protocols is required.
func TestCompile_RejectsMissingServedInboundProtocols(t *testing.T) {
	in := glmCodingExplicitIntent()
	in.ClientExposures[0].ServedInboundProtocols = nil
	if _, err := Compile(in); err == nil {
		t.Fatalf("expected error when served_inbound_protocols is empty")
	}
}

// ADR-0012 / ADR-0009 capability axis end-to-end: an openai endpoint declaring
// served=[openai] + capabilities=[chat, embeddings] must compile bindings
// {openai-chat, openai-embeddings}, equivalent to legacy openai_sdk. Proves
// exposure.Capabilities is threaded through compiler.go into the composition.
func TestCompile_OpenAIChatPlusEmbeddings_FromCapabilityAxis(t *testing.T) {
	in := RuntimeIntent{
		SchemaVersion: CurrentIntentSchemaVersion,
		ProviderEndpoints: []ProviderEndpoint{{
			ID:              "pc_emb",
			ProviderProfile: types.ProviderProfileOpenAICompatible,
			BaseURL:         "https://api.example.com",
			Status:          "active",
			Name:            "openai compat",
			Code:            "oai",
			OwnedBy:         "acme",
			KeyStrategy:     "priority",
		}},
		ProviderCredentials: []ProviderCredential{
			{ProviderID: "pc_emb", KeyRef: "secret://acme/key", KeyID: "key", ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440020", Status: "enabled", AllowedModels: []string{"text-embedding-3-small"}, Priority: 1, Weight: 100},
		},
		ClientExposures: []ClientExposure{{
			ProviderID:             "pc_emb",
			ServedInboundProtocols: []string{types.InboundProtocolOpenAI},
			Capabilities:           []string{"chat", "embeddings"},
		}},
		ModelRoutes: []ModelRouteIntent{{
			LogicalModel:     "text-embedding-3-small",
			Capabilities:     []string{"embeddings"},
			OwnedBy:          "acme",
			ProviderStrategy: "priority",
			Routes: []ModelRouteTarget{
				{ProviderID: "pc_emb", UpstreamModel: "text-embedding-3-small", Priority: 1, Weight: 100, Status: "active"},
			},
		}},
	}
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	p, ok := providerByID(out, "pc_emb")
	if !ok {
		t.Fatalf("pc_emb missing")
	}
	want := []string{"openai-chat", "openai-embeddings"}
	if !reflect.DeepEqual(bindingNamesOf(p.ProtocolBindings), want) {
		t.Fatalf("pc_emb bindings=%v want %v", bindingNamesOf(p.ProtocolBindings), want)
	}

	// Equivalent to legacy openai_sdk binding set.
	legacy, _, err := types.CompileClientProtocolBindingsWithOptions(
		types.ProviderProfileOpenAICompatible,
		[]string{types.ClientCompatibilityOpenAISDK},
		types.ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("legacy compile: %v", err)
	}
	if !reflect.DeepEqual(p.ProtocolBindings, legacy) {
		t.Fatalf("compiled bindings differ from legacy openai_sdk\n got=%+v\nwant=%+v", p.ProtocolBindings, legacy)
	}
	for _, b := range p.ProtocolBindings {
		t.Logf("pc_emb binding name=%s accepts=%+v outbound=%s translator=%q",
			b.Name, b.Accepts, b.OutboundProtocol, b.TranslatorKey)
	}
}

// Missing bridge surfaces as compile error (anthropic endpoint asked to serve openai).
func TestCompile_MissingBridgeErrors(t *testing.T) {
	in := glmCodingExplicitIntent()
	in.ClientExposures[1].ServedInboundProtocols = []string{types.InboundProtocolOpenAI}
	if _, err := Compile(in); err == nil {
		t.Fatalf("expected error for missing openai→anthropic bridge")
	}
}
