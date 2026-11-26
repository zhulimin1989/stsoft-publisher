package types

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// G4 P2 (ADR-0009 wire purity): publisher stops emitting vendor protocol
// values. The four vendor profiles (glm / glm_coding / doubao_ark /
// azure_openai) compile to pure-dialect bindings:
//
//   - outbound_protocol = "openai" (pure wire dialect)
//   - accepts[].protocol = pure dialects only (no glm / glm_coding rows)
//   - compat_api_type   = original vendor value (adapter-bundle selector; the
//     gateway P0 mechanism resolves the Zhipu/Doubao/Azure adapter bundle from
//     it, keeping thinking / tool_stream / api-version body+query semantics)
//   - path_prefix / auth_scheme / binding names / translator unchanged
//
// Runtime behavioral equivalence of old vs new shape is locked by the gateway
// P1 four-combination conformance matrix (test branch ac798e0).
// ---------------------------------------------------------------------------

// vendorProtocolValues are the retired wire-protocol enum values that must
// never appear in newly compiled output (ADR-0009 §1.7 target enums).
var vendorProtocolValues = map[string]bool{
	"glm":          true,
	"glm_coding":   true,
	"doubao_ark":   true,
	"azure_openai": true,
	"zhipu":        true,
	"doubao":       true,
	"azure":        true,
}

func assertPureDialectBindings(t *testing.T, bindings []ProtocolBinding, wantCompat string) {
	t.Helper()
	for _, b := range bindings {
		if vendorProtocolValues[b.OutboundProtocol] {
			t.Fatalf("%s outbound_protocol=%q is a vendor value; want pure dialect", b.Name, b.OutboundProtocol)
		}
		if b.OutboundProtocol != "openai" {
			t.Fatalf("%s outbound_protocol=%q want openai", b.Name, b.OutboundProtocol)
		}
		if b.CompatAPIType != wantCompat {
			t.Fatalf("%s compat_api_type=%q want %q (adapter-bundle selector must survive)", b.Name, b.CompatAPIType, wantCompat)
		}
		for _, a := range b.Accepts {
			if vendorProtocolValues[a.Protocol] {
				t.Fatalf("%s accepts vendor protocol %q; want pure dialects only", b.Name, a.Protocol)
			}
		}
	}
}

func TestCompileClientProtocolBindings_VendorProfilesEmitPureDialect(t *testing.T) {
	cases := []struct {
		profile        string
		clients        []string
		wantCompat     string
		wantDefault    string
		wantPathPrefix string
		wantQuery      string
		wantNames      []string
	}{
		{
			profile:        ProviderProfileGLM,
			clients:        []string{ClientCompatibilityAll},
			wantCompat:     "glm",
			wantDefault:    "glm-chat",
			wantPathPrefix: defaultGLMPathPrefix,
			wantNames:      []string{"glm-chat", "glm-embeddings"},
		},
		{
			profile:        ProviderProfileGLMCoding,
			clients:        []string{ClientCompatibilityAll},
			wantCompat:     "glm_coding",
			wantDefault:    "glm-coding-chat",
			wantPathPrefix: defaultGLMCodingPathPrefix,
			wantNames:      []string{"glm-coding-chat"},
		},
		{
			profile:        ProviderProfileDoubaoArk,
			clients:        []string{ClientCompatibilityAll},
			wantCompat:     "doubao",
			wantDefault:    "doubao-chat",
			wantPathPrefix: defaultDoubaoArkPathPrefix,
			wantNames:      []string{"doubao-chat", "doubao-embeddings"},
		},
		{
			profile:        ProviderProfileAzureOpenAI,
			clients:        []string{ClientCompatibilityAll},
			wantCompat:     "azure",
			wantDefault:    "azure-openai-chat",
			wantPathPrefix: defaultAzureOpenAIPathPrefix,
			wantQuery:      "api-version={api_version}",
			wantNames:      []string{"azure-openai-chat", "azure-openai-embeddings"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			bindings, def, err := CompileClientProtocolBindings(tc.profile, tc.clients)
			if err != nil {
				t.Fatalf("CompileClientProtocolBindings: %v", err)
			}
			if !reflect.DeepEqual(bindingNamesOf(bindings), tc.wantNames) {
				t.Fatalf("binding names=%v want %v (names must not change in G4 P2)", bindingNamesOf(bindings), tc.wantNames)
			}
			if def != tc.wantDefault {
				t.Fatalf("default=%q want %q", def, tc.wantDefault)
			}
			assertPureDialectBindings(t, bindings, tc.wantCompat)
			for _, b := range bindings {
				if b.PathPrefix != tc.wantPathPrefix {
					t.Fatalf("%s path_prefix=%q want %q (path_prefix stays on binding, never folds into base_url)", b.Name, b.PathPrefix, tc.wantPathPrefix)
				}
				if b.QueryTemplate != tc.wantQuery {
					t.Fatalf("%s query_template=%q want %q", b.Name, b.QueryTemplate, tc.wantQuery)
				}
			}
			if err := ValidateBindingUniqueness(bindings); err != nil {
				t.Fatalf("S3 uniqueness violated by pure-dialect shape: %v", err)
			}
		})
	}
}

// The GLM coding chat binding is the live paid line: pin its full new shape.
func TestCompileClientProtocolBindings_GLMCodingChatNewShape(t *testing.T) {
	bindings, _, err := CompileClientProtocolBindings(ProviderProfileGLMCoding, []string{ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	b, ok := findBinding(bindings, "glm-coding-chat")
	if !ok {
		t.Fatalf("glm-coding-chat missing: %v", bindingNamesOf(bindings))
	}
	want := ProtocolBinding{
		Name:             "glm-coding-chat",
		OutboundProtocol: "openai",
		PathPrefix:       "/api/coding/paas/v4",
		AuthScheme:       "bearer",
		TranslatorKey:    "passthrough",
		CompatAPIType:    "glm_coding",
		CapabilityProfiles: []string{
			"openai.chat_completions.full",
		},
		Accepts: []ProtocolBindingAccept{{
			Protocol:        "openai",
			Capabilities:    []string{"chat"},
			RequestContract: "chat_completions",
		}},
	}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("glm-coding-chat shape drifted\n got=%+v\nwant=%+v", b, want)
	}
}

// The ADR-0012 served path and the ADR-0016 explicit-paths path reuse the same
// builders — both must emit the pure shape too.
func TestCompileInboundProtocolBindings_VendorProfilesEmitPureDialect(t *testing.T) {
	bindings, def, err := CompileInboundProtocolBindings(
		ProviderProfileGLMCoding,
		[]string{InboundProtocolOpenAI, InboundProtocolResponses},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	if def != "glm-coding-chat" {
		t.Fatalf("default=%q want glm-coding-chat", def)
	}
	pt, _ := findBinding(bindings, "glm-coding-chat")
	if pt.OutboundProtocol != "openai" || pt.CompatAPIType != "glm_coding" {
		t.Fatalf("glm-coding-chat outbound=%q compat=%q want openai/glm_coding", pt.OutboundProtocol, pt.CompatAPIType)
	}
	wantAccepts := []ProtocolBindingAccept{{
		Protocol:        "openai",
		Capabilities:    []string{"chat"},
		RequestContract: "chat_completions",
	}}
	if !reflect.DeepEqual(pt.Accepts, wantAccepts) {
		t.Fatalf("glm-coding-chat accepts=%+v want %+v (vendor accepts row must be gone)", pt.Accepts, wantAccepts)
	}
	if err := ValidateBindingUniqueness(bindings); err != nil {
		t.Fatalf("S3 uniqueness: %v", err)
	}

	// GLM embeddings via capability axis stays pure as well.
	embBindings, _, err := CompileInboundProtocolBindings(
		ProviderProfileGLM,
		[]string{InboundProtocolOpenAI},
		[]string{CapabilityChat, "embeddings"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("glm chat+embeddings compile: %v", err)
	}
	assertPureDialectBindings(t, embBindings, "glm")
	if err := ValidateBindingUniqueness(embBindings); err != nil {
		t.Fatalf("S3 uniqueness (glm chat+embeddings): %v", err)
	}
}

func TestCompileExplicitPathBindings_VendorProfilesEmitPureDialect(t *testing.T) {
	bindings, def, warnings, err := CompileExplicitPathBindings(
		ProviderProfileGLMCoding,
		[]ProtocolPath{
			{InboundProtocol: InboundProtocolOpenAI, Channel: DedicatedProtocolChannelPassthrough},
			{InboundProtocol: InboundProtocolResponses, Channel: DedicatedProtocolChannelResponsesToChat},
		},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileExplicitPathBindings: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("consistent paths must not warn: %#v", warnings)
	}
	if def != "glm-coding-chat" {
		t.Fatalf("default=%q want glm-coding-chat", def)
	}
	pt, _ := findBinding(bindings, "glm-coding-chat")
	if pt.OutboundProtocol != "openai" || pt.CompatAPIType != "glm_coding" {
		t.Fatalf("glm-coding-chat outbound=%q compat=%q want openai/glm_coding", pt.OutboundProtocol, pt.CompatAPIType)
	}
	for _, a := range pt.Accepts {
		if vendorProtocolValues[a.Protocol] {
			t.Fatalf("explicit-paths output still carries vendor accepts row: %+v", pt.Accepts)
		}
	}
}

// Derived observation views must reflect pure dialects only once vendor accepts
// rows stop being emitted.
func TestDeriveCompatibilityView_PureDialectsForVendorProfiles(t *testing.T) {
	bindings, _, err := CompileInboundProtocolBindings(
		ProviderProfileGLMCoding,
		[]string{InboundProtocolOpenAI, InboundProtocolResponses},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	inbound, _ := DeriveCompatibilityView(bindings)
	if !reflect.DeepEqual(inbound, []string{"openai", "responses"}) {
		t.Fatalf("derived inbound=%v want [openai responses] (no glm_coding)", inbound)
	}
}
