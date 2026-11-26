package types

import (
	"reflect"
	"testing"
)

// ADR-0012: explicit inbound-dialect binding composition.

func bindingNamesOf(bindings []ProtocolBinding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Name)
	}
	return out
}

func findBinding(bindings []ProtocolBinding, name string) (ProtocolBinding, bool) {
	for _, b := range bindings {
		if b.Name == name {
			return b, true
		}
	}
	return ProtocolBinding{}, false
}

// GLM coding openai endpoint: serving openai + responses must compose exactly
// {openai passthrough, responses_to_chat bridge} and must NOT contain an
// anthropic→openai bridge.
func TestCompileInboundProtocolBindings_OpenAIEndpointOpenAIPlusResponses(t *testing.T) {
	bindings, def, err := CompileInboundProtocolBindings(
		ProviderProfileGLMCoding,
		[]string{InboundProtocolOpenAI, InboundProtocolResponses},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	got := bindingNamesOf(bindings)
	want := []string{"glm-coding-chat", "responses-to-openai-chat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("binding names=%v want %v", got, want)
	}
	if def != "glm-coding-chat" {
		t.Fatalf("default binding=%q want glm-coding-chat (passthrough)", def)
	}
	// Must NOT contain anthropic→openai bridge.
	if _, ok := findBinding(bindings, "anthropic-messages-to-openai"); ok {
		t.Fatalf("unexpected anthropic→openai bridge: %v", got)
	}
	// passthrough binding accepts openai chat
	pt, _ := findBinding(bindings, "glm-coding-chat")
	if pt.TranslatorKey != "passthrough" || pt.Bridge != "" {
		t.Fatalf("passthrough binding shape wrong: %+v", pt)
	}
	// responses bridge
	br, _ := findBinding(bindings, "responses-to-openai-chat")
	if br.Bridge != "responses_to_chat" {
		t.Fatalf("responses bridge=%q want responses_to_chat", br.Bridge)
	}
	if len(br.Accepts) != 1 || br.Accepts[0].Protocol != "responses" {
		t.Fatalf("responses bridge accepts wrong: %+v", br.Accepts)
	}
}

// Plain openai_compatible endpoint serving openai → single passthrough using
// generic openai-chat builder.
func TestCompileInboundProtocolBindings_OpenAICompatiblePassthrough(t *testing.T) {
	bindings, def, err := CompileInboundProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{InboundProtocolOpenAI},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	if !reflect.DeepEqual(bindingNamesOf(bindings), []string{"openai-chat"}) {
		t.Fatalf("names=%v", bindingNamesOf(bindings))
	}
	if def != "openai-chat" {
		t.Fatalf("default=%q", def)
	}
}

// GLM coding anthropic endpoint: serving anthropic → exactly anthropic
// passthrough (1 binding), no bridge.
func TestCompileInboundProtocolBindings_AnthropicEndpointPassthrough(t *testing.T) {
	bindings, def, err := CompileInboundProtocolBindings(
		ProviderProfileAnthropicNative,
		[]string{InboundProtocolAnthropic},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	if !reflect.DeepEqual(bindingNamesOf(bindings), []string{"anthropic-messages"}) {
		t.Fatalf("names=%v want [anthropic-messages]", bindingNamesOf(bindings))
	}
	if def != "anthropic-messages" {
		t.Fatalf("default=%q want anthropic-messages", def)
	}
	b := bindings[0]
	if b.Bridge != "" || b.TranslatorKey != "passthrough" {
		t.Fatalf("anthropic passthrough shape wrong: %+v", b)
	}
	if b.Accepts[0].Protocol != "anthropic" {
		t.Fatalf("accepts protocol=%q want anthropic", b.Accepts[0].Protocol)
	}
}

// Missing bridge in registry must error, not silently synthesize.
func TestCompileInboundProtocolBindings_MissingBridgeErrors(t *testing.T) {
	// anthropic endpoint asked to serve openai inbound → no openai→anthropic bridge.
	_, _, err := CompileInboundProtocolBindings(
		ProviderProfileAnthropicNative,
		[]string{InboundProtocolOpenAI},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err == nil {
		t.Fatalf("expected error for missing openai→anthropic bridge")
	}
	// gemini endpoint asked to serve anthropic → no bridge.
	if _, _, err := CompileInboundProtocolBindings(
		ProviderProfileGeminiNative,
		[]string{InboundProtocolAnthropic},
		nil,
		ClientProtocolBindingOptions{},
	); err == nil {
		t.Fatalf("expected error for missing anthropic→gemini bridge")
	}
}

// ADR-0012 / ADR-0009 capability axis: served=[openai]+caps=[chat,embeddings]
// must compose {openai-chat passthrough, openai-embeddings}, IDENTICAL to the
// legacy client_compatibility=[openai_sdk] output (binding set equivalence).
func TestCompileInboundProtocolBindings_OpenAIChatPlusEmbeddings_EquivalentToLegacyOpenAISDK(t *testing.T) {
	got, gotDef, err := CompileInboundProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{InboundProtocolOpenAI},
		[]string{CapabilityChat, "embeddings"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("CompileInboundProtocolBindings: %v", err)
	}
	if !reflect.DeepEqual(bindingNamesOf(got), []string{"openai-chat", "openai-embeddings"}) {
		t.Fatalf("names=%v want [openai-chat openai-embeddings]", bindingNamesOf(got))
	}

	// Legacy openai_sdk path must produce the same binding set.
	legacy, legacyDef, err := CompileClientProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{ClientCompatibilityOpenAISDK},
	)
	if err != nil {
		t.Fatalf("legacy compile: %v", err)
	}
	if !reflect.DeepEqual(got, legacy) {
		t.Fatalf("binding set drifted from legacy openai_sdk\n got=%+v\nwant=%+v", got, legacy)
	}
	if gotDef != legacyDef {
		t.Fatalf("default=%q want %q (legacy openai_sdk)", gotDef, legacyDef)
	}
	t.Logf("embeddings-equivalence: %d bindings, default=%q", len(got), gotDef)
	for _, b := range got {
		t.Logf("binding name=%s accepts=%+v", b.Name, b.Accepts)
	}
}

// chat-only / empty capabilities preserves prior dialect-only behavior.
func TestCompileInboundProtocolBindings_ChatOnlyNoRegression(t *testing.T) {
	chat, _, err := CompileInboundProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{InboundProtocolOpenAI},
		[]string{CapabilityChat},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("chat compile: %v", err)
	}
	empty, _, err := CompileInboundProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{InboundProtocolOpenAI},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("empty compile: %v", err)
	}
	if !reflect.DeepEqual(chat, empty) {
		t.Fatalf("chat-only and empty capabilities diverged: %+v vs %+v", chat, empty)
	}
	if !reflect.DeepEqual(bindingNamesOf(chat), []string{"openai-chat"}) {
		t.Fatalf("chat-only names=%v want [openai-chat]", bindingNamesOf(chat))
	}
}

// rerank capability on cohere_v2 binds same-dialect (no bridge).
func TestCompileInboundProtocolBindings_RerankCohere(t *testing.T) {
	got, _, err := CompileInboundProtocolBindings(
		ProviderProfileCohereV2,
		nil,
		[]string{"rerank"},
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("rerank compile: %v", err)
	}
	if !reflect.DeepEqual(bindingNamesOf(got), []string{"cohere-rerank"}) {
		t.Fatalf("names=%v want [cohere-rerank]", bindingNamesOf(got))
	}
}

// Stop rule: a profile without an embeddings builder must error, not synthesize.
func TestCompileInboundProtocolBindings_UnsupportedCapabilityErrors(t *testing.T) {
	// glm_coding has no embeddings builder.
	if _, _, err := CompileInboundProtocolBindings(
		ProviderProfileGLMCoding,
		[]string{InboundProtocolOpenAI},
		[]string{CapabilityChat, "embeddings"},
		ClientProtocolBindingOptions{},
	); err == nil {
		t.Fatalf("expected error: glm_coding has no embeddings builder")
	}
	// anthropic_native has no embeddings builder.
	if _, _, err := CompileInboundProtocolBindings(
		ProviderProfileAnthropicNative,
		[]string{InboundProtocolAnthropic},
		[]string{CapabilityChat, "embeddings"},
		ClientProtocolBindingOptions{},
	); err == nil {
		t.Fatalf("expected error: anthropic_native has no embeddings builder")
	}
}

func TestDeriveCompatibilityView(t *testing.T) {
	bindings, _, err := CompileInboundProtocolBindings(
		ProviderProfileOpenAICompatible,
		[]string{InboundProtocolOpenAI, InboundProtocolResponses},
		nil,
		ClientProtocolBindingOptions{},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	inbound, compat := DeriveCompatibilityView(bindings)
	if !reflect.DeepEqual(inbound, []string{"openai", "responses"}) {
		t.Fatalf("inbound=%v want [openai responses]", inbound)
	}
	// openai → openai_sdk/cursor/cline/cherrystudio/dify/openclaw ; responses → codex
	want := []string{"cherrystudio", "cline", "codex", "cursor", "dify", "openai_sdk", "openclaw"}
	if !reflect.DeepEqual(compat, want) {
		t.Fatalf("compat=%v want %v", compat, want)
	}
}
