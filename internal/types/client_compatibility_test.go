package types

import "testing"

func TestCompileClientProtocolBindings_OpenAICompatibleAll(t *testing.T) {
	bindings, defaultBinding, err := CompileClientProtocolBindings(ProviderProfileOpenAICompatible, []string{ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	if defaultBinding != "openai-chat" {
		t.Fatalf("default binding=%q", defaultBinding)
	}
	want := map[string]bool{
		"anthropic-messages-to-openai": false,
		"openai-chat":                  false,
		"openai-embeddings":            false,
		"responses-to-openai-chat":     false,
	}
	for _, binding := range bindings {
		if _, ok := want[binding.Name]; !ok {
			t.Fatalf("unexpected binding: %+v", binding)
		}
		if binding.OutboundProtocol != "openai" {
			t.Fatalf("%s outbound_protocol=%q want openai", binding.Name, binding.OutboundProtocol)
		}
		if binding.PathPrefix != "/v1" {
			t.Fatalf("%s path_prefix=%q want /v1", binding.Name, binding.PathPrefix)
		}
		if binding.Name == "openai-embeddings" {
			if len(binding.CapabilityProfiles) != 0 {
				t.Fatalf("%s capability_profiles=%v want empty", binding.Name, binding.CapabilityProfiles)
			}
		} else if !containsString(binding.CapabilityProfiles, "openai.chat_completions.full") {
			t.Fatalf("%s missing openai.chat_completions.full profile: %+v", binding.Name, binding)
		}
		switch binding.Name {
		case "anthropic-messages-to-openai":
			if binding.Bridge != "anthropic_to_openai_chat" {
				t.Fatalf("%s bridge=%q want anthropic_to_openai_chat", binding.Name, binding.Bridge)
			}
		case "responses-to-openai-chat":
			if binding.Bridge != "responses_to_chat" {
				t.Fatalf("%s bridge=%q want responses_to_chat", binding.Name, binding.Bridge)
			}
		default:
			if binding.Bridge != "" {
				t.Fatalf("%s bridge=%q want empty", binding.Name, binding.Bridge)
			}
		}
		want[binding.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing binding %q in %+v", name, bindings)
		}
	}
}

func TestCompileClientProtocolBindings_IndividualClients(t *testing.T) {
	cases := []struct {
		client string
		want   []string
	}{
		{ClientCompatibilityClaudeCode, []string{"anthropic-messages-to-openai"}},
		{ClientCompatibilityCodex, []string{"responses-to-openai-chat"}},
		{ClientCompatibilityOpenAISDK, []string{"openai-chat", "openai-embeddings"}},
		{ClientCompatibilityOpenClaw, []string{"anthropic-messages-to-openai", "openai-chat"}},
		{ClientCompatibilityCursor, []string{"openai-chat"}},
		{ClientCompatibilityCline, []string{"openai-chat"}},
		{ClientCompatibilityCherryStudio, []string{"openai-chat"}},
		{ClientCompatibilityDify, []string{"openai-chat", "openai-embeddings"}},
	}
	for _, tc := range cases {
		t.Run(tc.client, func(t *testing.T) {
			bindings, _, err := CompileClientProtocolBindings(ProviderProfileOpenAICompatible, []string{tc.client})
			if err != nil {
				t.Fatalf("CompileClientProtocolBindings: %v", err)
			}
			if len(bindings) != len(tc.want) {
				t.Fatalf("binding count=%d want %d: %+v", len(bindings), len(tc.want), bindings)
			}
			for i, wantName := range tc.want {
				if bindings[i].Name != wantName {
					t.Fatalf("bindings[%d]=%q want %q; all=%+v", i, bindings[i].Name, wantName, bindings)
				}
			}
		})
	}
}

func TestCompileClientProtocolBindings_OpenAICompatibleRejectsGeminiCLI(t *testing.T) {
	_, _, err := CompileClientProtocolBindings(ProviderProfileOpenAICompatible, []string{ClientCompatibilityGeminiCLI})
	if err == nil {
		t.Fatal("openai_compatible gemini_cli must be rejected")
	}
	if err.Error() != "gemini_cli requires a gemini-wire provider" {
		t.Fatalf("error=%q want gemini_cli requires a gemini-wire provider", err.Error())
	}
}

func TestCompileClientProtocolBindings_RejectsUnknowns(t *testing.T) {
	if _, _, err := CompileClientProtocolBindings("unknown_profile", []string{ClientCompatibilityAll}); err == nil {
		t.Fatal("unknown provider profile must be rejected")
	}
	if _, _, err := CompileClientProtocolBindings(ProviderProfileOpenAICompatible, []string{"unknown_client"}); err == nil {
		t.Fatal("unknown client must be rejected")
	}
}

func TestCompileClientProtocolBindings_AnthropicNativeClaudeCode(t *testing.T) {
	bindings, defaultBinding, err := CompileClientProtocolBindings(ProviderProfileAnthropicNative, []string{ClientCompatibilityClaudeCode})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	if defaultBinding != "anthropic-messages" {
		t.Fatalf("default binding=%q", defaultBinding)
	}
	want := map[string]bool{
		"anthropic-messages":     false,
		"anthropic-count-tokens": false,
	}
	for _, binding := range bindings {
		if _, ok := want[binding.Name]; !ok {
			t.Fatalf("unexpected binding: %+v", binding)
		}
		if binding.OutboundProtocol != "anthropic" {
			t.Fatalf("%s outbound_protocol=%q want anthropic", binding.Name, binding.OutboundProtocol)
		}
		if binding.PathPrefix != "/v1" {
			t.Fatalf("%s path_prefix=%q want /v1", binding.Name, binding.PathPrefix)
		}
		if binding.AuthScheme != "x-api-key" || binding.TranslatorKey != "passthrough" {
			t.Fatalf("%s auth/translator mismatch: %+v", binding.Name, binding)
		}
		want[binding.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing binding %q in %+v", name, bindings)
		}
	}
}

func TestCompileClientProtocolBindings_GeminiNativeAllOnlyGeneratesNativeBinding(t *testing.T) {
	bindings, defaultBinding, err := CompileClientProtocolBindings(ProviderProfileGeminiNative, []string{ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	if defaultBinding != "gemini-generate" {
		t.Fatalf("default binding=%q want gemini-generate", defaultBinding)
	}
	if len(bindings) != 1 || bindings[0].Name != "gemini-generate" {
		t.Fatalf("gemini_native all generated unexpected bindings: %+v", bindings)
	}
}

func TestCompileClientProtocolBindings_GeminiNativeRejectsNonGeminiClients(t *testing.T) {
	for _, client := range []string{
		ClientCompatibilityOpenAISDK,
		ClientCompatibilityCursor,
		ClientCompatibilityCline,
		ClientCompatibilityCherryStudio,
		ClientCompatibilityDify,
		ClientCompatibilityCodex,
	} {
		t.Run(client, func(t *testing.T) {
			_, _, err := CompileClientProtocolBindings(ProviderProfileGeminiNative, []string{client})
			if err == nil {
				t.Fatalf("gemini_native %s must be rejected", client)
			}
			want := "client_compatibility unsupported for provider_profile gemini_native: " + client
			if err.Error() != want {
				t.Fatalf("error=%q want %q", err.Error(), want)
			}
		})
	}
}

func TestCompileClientProtocolBindings_AnthropicNativeUnsupportedCountTokens(t *testing.T) {
	bindings, _, err := CompileClientProtocolBindingsWithOptions(
		ProviderProfileAnthropicNative,
		[]string{ClientCompatibilityClaudeCode},
		ClientProtocolBindingOptions{TokenCountStrategy: TokenCountStrategyUnsupported, PathPrefix: "/anthropic/v1"},
	)
	if err != nil {
		t.Fatalf("CompileClientProtocolBindingsWithOptions: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Name != "anthropic-messages" {
		t.Fatalf("unsupported token_count_strategy must omit count_tokens binding, got %+v", bindings)
	}
	if bindings[0].PathPrefix != "/anthropic/v1" {
		t.Fatalf("path_prefix override not applied: %+v", bindings[0])
	}
}

func TestCompileClientProtocolBindings_OpenAICompatibleRejectsCountTokenStrategies(t *testing.T) {
	if _, _, err := CompileClientProtocolBindingsWithOptions(
		ProviderProfileOpenAICompatible,
		[]string{ClientCompatibilityClaudeCode},
		ClientProtocolBindingOptions{TokenCountStrategy: TokenCountStrategyGatewayEstimate},
	); err == nil {
		t.Fatal("openai_compatible gateway_estimate count_tokens must be rejected until synthetic support exists")
	}
}

func TestCompileClientProtocolBindings_AdditionalCompilerProfiles(t *testing.T) {
	cases := []struct {
		name           string
		profile        string
		clients        []string
		wantDefault    string
		wantOutbound   string
		wantBinding    string
		wantPathPrefix string
	}{
		{
			name:           "openai responses native",
			profile:        ProviderProfileOpenAIResponses,
			clients:        []string{ClientCompatibilityCodex},
			wantDefault:    "openai-responses",
			wantOutbound:   "openai_responses",
			wantBinding:    "openai-responses",
			wantPathPrefix: "/v1",
		},
		{
			name:           "azure openai",
			profile:        ProviderProfileAzureOpenAI,
			clients:        []string{ClientCompatibilityOpenAISDK},
			wantDefault:    "azure-openai-chat",
			wantOutbound:   "openai",
			wantBinding:    "azure-openai-chat",
			wantPathPrefix: "/openai/deployments/{upstream_model}",
		},
		{
			name:           "gemini native",
			profile:        ProviderProfileGeminiNative,
			clients:        []string{ClientCompatibilityGeminiCLI},
			wantDefault:    "gemini-generate",
			wantOutbound:   "gemini",
			wantBinding:    "gemini-generate",
			wantPathPrefix: "/v1beta",
		},
		{
			name:           "glm",
			profile:        ProviderProfileGLM,
			clients:        []string{ClientCompatibilityAll},
			wantDefault:    "glm-chat",
			wantOutbound:   "openai",
			wantBinding:    "glm-chat",
			wantPathPrefix: "/api/paas/v4",
		},
		{
			name:           "cohere v2",
			profile:        ProviderProfileCohereV2,
			clients:        []string{ClientCompatibilityAll},
			wantDefault:    "cohere-rerank",
			wantOutbound:   "cohere_v2",
			wantBinding:    "cohere-rerank",
			wantPathPrefix: "/v2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bindings, defaultBinding, err := CompileClientProtocolBindings(tc.profile, tc.clients)
			if err != nil {
				t.Fatalf("CompileClientProtocolBindings: %v", err)
			}
			if defaultBinding != tc.wantDefault {
				t.Fatalf("default binding=%q want %q", defaultBinding, tc.wantDefault)
			}
			if len(bindings) == 0 {
				t.Fatal("expected at least one binding")
			}
			found := false
			for _, binding := range bindings {
				if binding.Name != tc.wantBinding {
					continue
				}
				found = true
				if binding.OutboundProtocol != tc.wantOutbound {
					t.Fatalf("%s outbound_protocol=%q want %q", binding.Name, binding.OutboundProtocol, tc.wantOutbound)
				}
				if binding.PathPrefix != tc.wantPathPrefix {
					t.Fatalf("%s path_prefix=%q want %q", binding.Name, binding.PathPrefix, tc.wantPathPrefix)
				}
			}
			if !found {
				t.Fatalf("missing binding %q in %+v", tc.wantBinding, bindings)
			}
		})
	}
}
