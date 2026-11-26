package types

import (
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ADR-0016 U2 equivalence regression lock: compiling explicit paths[] must
// produce byte-identical output (bindings + default) to the equivalent
// served_inbound_protocols list, across every live chat-capable profile,
// capability mix, and options variant. paths only changes the input shape,
// never the runtime artifact.
// ---------------------------------------------------------------------------

func TestCompileExplicitPathBindings_EquivalenceLock(t *testing.T) {
	pt := DedicatedProtocolChannelPassthrough
	r2c := DedicatedProtocolChannelResponsesToChat
	a2c := DedicatedProtocolChannelAnthropicToChat

	openAIFamily := []string{
		ProviderProfileOpenAICompatible,
		ProviderProfileOpenAINative,
		ProviderProfileAzureOpenAI,
		ProviderProfileDoubaoArk,
		ProviderProfileGLM,
		ProviderProfileGLMCoding,
	}
	embeddingsCapable := map[string]bool{
		ProviderProfileOpenAICompatible: true,
		ProviderProfileOpenAINative:     true,
		ProviderProfileAzureOpenAI:      true,
		ProviderProfileDoubaoArk:        true,
		ProviderProfileGLM:              true,
	}

	type combo struct {
		paths  []ProtocolPath
		served []string
	}
	openAICombos := []combo{
		{[]ProtocolPath{{InboundProtocolOpenAI, pt}}, []string{InboundProtocolOpenAI}},
		{[]ProtocolPath{{InboundProtocolResponses, r2c}}, []string{InboundProtocolResponses}},
		{[]ProtocolPath{{InboundProtocolAnthropic, a2c}}, []string{InboundProtocolAnthropic}},
		{[]ProtocolPath{{InboundProtocolOpenAI, pt}, {InboundProtocolResponses, r2c}}, []string{InboundProtocolOpenAI, InboundProtocolResponses}},
		{[]ProtocolPath{{InboundProtocolResponses, r2c}, {InboundProtocolOpenAI, pt}}, []string{InboundProtocolResponses, InboundProtocolOpenAI}},
		{[]ProtocolPath{{InboundProtocolOpenAI, pt}, {InboundProtocolResponses, r2c}, {InboundProtocolAnthropic, a2c}}, []string{InboundProtocolOpenAI, InboundProtocolResponses, InboundProtocolAnthropic}},
	}

	type testCase struct {
		name         string
		profile      string
		combo        combo
		capabilities []string
		opts         ClientProtocolBindingOptions
	}
	var cases []testCase
	for _, profile := range openAIFamily {
		for i, c := range openAICombos {
			cases = append(cases,
				testCase{profile + "/combo" + string(rune('A'+i)) + "/chat", profile, c, []string{"chat"}, ClientProtocolBindingOptions{}},
				testCase{profile + "/combo" + string(rune('A'+i)) + "/nilcaps", profile, c, nil, ClientProtocolBindingOptions{}},
				testCase{profile + "/combo" + string(rune('A'+i)) + "/prefix", profile, c, []string{"chat"}, ClientProtocolBindingOptions{PathPrefix: "/custom/v9"}},
			)
			if embeddingsCapable[profile] {
				cases = append(cases, testCase{profile + "/combo" + string(rune('A'+i)) + "/chat+embeddings", profile, c, []string{"chat", "embeddings"}, ClientProtocolBindingOptions{}})
			}
		}
	}
	cases = append(cases,
		testCase{"anthropic_native/passthrough", ProviderProfileAnthropicNative,
			combo{[]ProtocolPath{{InboundProtocolAnthropic, pt}}, []string{InboundProtocolAnthropic}},
			[]string{"chat"}, ClientProtocolBindingOptions{TokenCountStrategy: TokenCountStrategyUnsupported}},
		testCase{"gemini_native/passthrough", ProviderProfileGeminiNative,
			combo{[]ProtocolPath{{InboundProtocolGemini, pt}}, []string{InboundProtocolGemini}},
			[]string{"chat"}, ClientProtocolBindingOptions{}},
		testCase{"openai_responses_native/passthrough", ProviderProfileOpenAIResponses,
			combo{[]ProtocolPath{{InboundProtocolResponses, pt}}, []string{InboundProtocolResponses}},
			[]string{"chat"}, ClientProtocolBindingOptions{}},
		testCase{"default_profile_empty_string", "",
			combo{[]ProtocolPath{{InboundProtocolOpenAI, pt}}, []string{InboundProtocolOpenAI}},
			[]string{"chat"}, ClientProtocolBindingOptions{}},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantBindings, wantDefault, wantErr := CompileInboundProtocolBindings(tc.profile, tc.combo.served, tc.capabilities, tc.opts)
			if wantErr != nil {
				t.Fatalf("served baseline errored: %v", wantErr)
			}
			gotBindings, gotDefault, gotWarnings, gotErr := CompileExplicitPathBindings(tc.profile, tc.combo.paths, tc.capabilities, tc.opts)
			if gotErr != nil {
				t.Fatalf("paths compile errored: %v", gotErr)
			}
			if len(gotWarnings) != 0 {
				t.Fatalf("physically consistent paths must not warn: %#v", gotWarnings)
			}
			if !reflect.DeepEqual(gotBindings, wantBindings) {
				t.Fatalf("bindings drifted from served equivalent\n got=%#v\nwant=%#v", gotBindings, wantBindings)
			}
			if gotDefault != wantDefault {
				t.Fatalf("default binding drifted: got=%q want=%q", gotDefault, wantDefault)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ADR-0016 (2026-06-30 revision): physically impossible combinations warn but
// never block. The declared path is still compiled into a structurally valid
// binding (all mandatory fields populated); runtime is where it fails.
// ---------------------------------------------------------------------------

func TestCompileExplicitPathBindings_InconsistentPathsWarnNotBlock(t *testing.T) {
	pt := DedicatedProtocolChannelPassthrough
	r2c := DedicatedProtocolChannelResponsesToChat
	a2c := DedicatedProtocolChannelAnthropicToChat

	cases := []struct {
		name    string
		profile string
		path    ProtocolPath
	}{
		{"openai_compatible/anthropic-passthrough", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolAnthropic, pt}},
		{"openai_compatible/gemini-passthrough", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolGemini, pt}},
		{"openai_compatible/openai-responses_to_chat", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolOpenAI, r2c}},
		{"openai_compatible/anthropic-responses_to_chat", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolAnthropic, r2c}},
		{"openai_compatible/openai-anthropic_to_chat", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolOpenAI, a2c}},
		{"openai_compatible/responses-anthropic_to_chat", ProviderProfileOpenAICompatible, ProtocolPath{InboundProtocolResponses, a2c}},
		{"anthropic_native/openai-passthrough", ProviderProfileAnthropicNative, ProtocolPath{InboundProtocolOpenAI, pt}},
		{"anthropic_native/responses-responses_to_chat", ProviderProfileAnthropicNative, ProtocolPath{InboundProtocolResponses, r2c}},
		{"anthropic_native/anthropic-anthropic_to_chat", ProviderProfileAnthropicNative, ProtocolPath{InboundProtocolAnthropic, a2c}},
		{"gemini_native/openai-passthrough", ProviderProfileGeminiNative, ProtocolPath{InboundProtocolOpenAI, pt}},
		{"gemini_native/anthropic-anthropic_to_chat", ProviderProfileGeminiNative, ProtocolPath{InboundProtocolAnthropic, a2c}},
		{"openai_responses_native/responses-responses_to_chat", ProviderProfileOpenAIResponses, ProtocolPath{InboundProtocolResponses, r2c}},
		{"openai_responses_native/openai-passthrough", ProviderProfileOpenAIResponses, ProtocolPath{InboundProtocolOpenAI, pt}},
		{"glm_coding/gemini-responses_to_chat", ProviderProfileGLMCoding, ProtocolPath{InboundProtocolGemini, r2c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bindings, defaultBinding, warnings, err := CompileExplicitPathBindings(tc.profile, []ProtocolPath{tc.path}, []string{"chat"}, ClientProtocolBindingOptions{})
			if err != nil {
				t.Fatalf("inconsistent path must warn, not error: %v", err)
			}
			if len(warnings) != 1 {
				t.Fatalf("want exactly 1 warning, got %#v", warnings)
			}
			w := warnings[0]
			if w.Path != tc.path {
				t.Fatalf("warning path=%#v want %#v", w.Path, tc.path)
			}
			// Facts triple: inbound protocol, channel, endpoint profile all named.
			for _, needle := range []string{tc.path.InboundProtocol, tc.path.Channel, tc.profile} {
				if !strings.Contains(w.Message, needle) {
					t.Fatalf("warning message must state %q, got: %s", needle, w.Message)
				}
			}
			// Compiled as declared: one structurally valid binding.
			if len(bindings) != 1 {
				t.Fatalf("declared path must still compile to a binding, got %#v", bindings)
			}
			b := bindings[0]
			if b.Name == "" || b.OutboundProtocol == "" || b.TranslatorKey == "" || b.AuthScheme == "" || b.PathPrefix == "" {
				t.Fatalf("compiled binding missing mandatory fields: %#v", b)
			}
			if len(b.Accepts) == 0 {
				t.Fatalf("compiled binding must accept the declared inbound protocol: %#v", b)
			}
			acceptsDeclared := false
			for _, accept := range b.Accepts {
				if accept.RequestContract == "" || len(accept.Capabilities) == 0 {
					t.Fatalf("accept entry structurally incomplete: %#v", b)
				}
				if accept.Protocol == tc.path.InboundProtocol {
					acceptsDeclared = true
				}
			}
			if !acceptsDeclared {
				t.Fatalf("binding must accept declared inbound %q: %#v", tc.path.InboundProtocol, b)
			}
			if defaultBinding != b.Name {
				t.Fatalf("single-path default=%q want %q", defaultBinding, b.Name)
			}
		})
	}
}

// A bridge channel whose inbound matches the bridge entry dialect but whose
// endpoint dialect mismatches compiles to the canonical bridge binding
// (declaration taken literally) plus a warning.
func TestCompileExplicitPathBindings_BridgeOnWrongUpstreamKeepsCanonicalShape(t *testing.T) {
	bindings, _, warnings, err := CompileExplicitPathBindings(
		ProviderProfileAnthropicNative,
		[]ProtocolPath{{InboundProtocolResponses, DedicatedProtocolChannelResponsesToChat}},
		[]string{"chat"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %#v", warnings)
	}
	want := openAICompatibleBindingByName("responses-to-openai-chat", defaultOpenAICompatiblePathPrefix)
	if len(bindings) != 1 || !reflect.DeepEqual(bindings[0], want) {
		t.Fatalf("binding=%#v want canonical %#v", bindings, want)
	}
}

// Mixed valid + invalid paths: the valid one stays byte-identical to its served
// equivalent, the invalid one compiles as declared with a warning.
func TestCompileExplicitPathBindings_MixedValidAndInvalid(t *testing.T) {
	bindings, defaultBinding, warnings, err := CompileExplicitPathBindings(
		ProviderProfileAnthropicNative,
		[]ProtocolPath{
			{InboundProtocolAnthropic, DedicatedProtocolChannelPassthrough},
			{InboundProtocolOpenAI, DedicatedProtocolChannelPassthrough},
		},
		[]string{"chat"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning for the invalid path only, got %#v", warnings)
	}
	if len(bindings) != 2 {
		t.Fatalf("want 2 bindings, got %#v", bindingNamesOf(bindings))
	}
	servedBindings, servedDefault, servedErr := CompileInboundProtocolBindings(
		ProviderProfileAnthropicNative, []string{InboundProtocolAnthropic}, []string{"chat"}, ClientProtocolBindingOptions{})
	if servedErr != nil {
		t.Fatalf("served baseline: %v", servedErr)
	}
	foundValid := false
	for _, b := range bindings {
		if b.Name == servedBindings[0].Name {
			foundValid = true
			if !reflect.DeepEqual(b, servedBindings[0]) {
				t.Fatalf("valid path drifted from served equivalent: %#v", b)
			}
		}
	}
	if !foundValid {
		t.Fatalf("valid anthropic passthrough binding missing: %v", bindingNamesOf(bindings))
	}
	if defaultBinding != servedDefault {
		t.Fatalf("default=%q want served-equivalent %q", defaultBinding, servedDefault)
	}
}

// Profiles without a chat dialect mapping (cohere_v2) cannot compile any chat
// path: warn + skip, never block; the orthogonal capability axis still works.
func TestCompileExplicitPathBindings_NoDialectProfileWarnsAndSkips(t *testing.T) {
	bindings, defaultBinding, warnings, err := CompileExplicitPathBindings(
		ProviderProfileCohereV2,
		[]ProtocolPath{{InboundProtocolOpenAI, DedicatedProtocolChannelPassthrough}},
		[]string{"rerank"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %#v", warnings)
	}
	if len(bindings) != 1 || bindings[0].Name != "cohere-rerank" {
		t.Fatalf("capability axis must survive: %#v", bindingNamesOf(bindings))
	}
	if defaultBinding != "cohere-rerank" {
		t.Fatalf("default=%q", defaultBinding)
	}
}

// Unknown channel value (should be schema-blocked; defensive at Go level):
// warn + skip, never panic or error.
func TestCompileExplicitPathBindings_UnknownChannelWarnsAndSkips(t *testing.T) {
	bindings, _, warnings, err := CompileExplicitPathBindings(
		ProviderProfileOpenAICompatible,
		[]ProtocolPath{{InboundProtocolOpenAI, "warp_drive"}},
		[]string{"chat"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(warnings) != 1 || len(bindings) != 0 {
		t.Fatalf("want warn+skip, got bindings=%v warnings=%#v", bindingNamesOf(bindings), warnings)
	}
}
