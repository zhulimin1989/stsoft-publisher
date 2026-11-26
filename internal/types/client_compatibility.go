package types

import (
	"fmt"
	"reflect"
	"sort"
)

const (
	ProviderProfileOpenAICompatible = "openai_compatible"
	ProviderProfileOpenAINative     = "openai_native"
	ProviderProfileOpenAIResponses  = "openai_responses_native"
	ProviderProfileAzureOpenAI      = "azure_openai"
	ProviderProfileAnthropicNative  = "anthropic_native"
	ProviderProfileGeminiNative     = "gemini_native"
	ProviderProfileDoubaoArk        = "doubao_ark"
	ProviderProfileGLM              = "glm"
	ProviderProfileGLMCoding        = "glm_coding"
	ProviderProfileCohereV2         = "cohere_v2"

	defaultOpenAICompatiblePathPrefix = "/v1"
	defaultAzureOpenAIPathPrefix      = "/openai/deployments/{upstream_model}"
	defaultAnthropicNativePathPrefix  = "/v1"
	defaultGeminiNativePathPrefix     = "/v1beta"
	defaultDoubaoArkPathPrefix        = "/api/v3"
	defaultGLMPathPrefix              = "/api/paas/v4"
	defaultGLMCodingPathPrefix        = "/api/coding/paas/v4"
	defaultCohereV2PathPrefix         = "/v2"

	TokenCountStrategyNative          = "native"
	TokenCountStrategyGatewayEstimate = "gateway_estimate"
	TokenCountStrategyUnsupported     = "unsupported"

	ClientCompatibilityAll          = "all"
	ClientCompatibilityClaudeCode   = "claude_code"
	ClientCompatibilityCodex        = "codex"
	ClientCompatibilityOpenAISDK    = "openai_sdk"
	ClientCompatibilityOpenClaw     = "openclaw"
	ClientCompatibilityGeminiCLI    = "gemini_cli"
	ClientCompatibilityCursor       = "cursor"
	ClientCompatibilityCline        = "cline"
	ClientCompatibilityCherryStudio = "cherrystudio"
	ClientCompatibilityDify         = "dify"
)

type ClientProtocolBindingOptions struct {
	PathPrefix         string
	TokenCountStrategy string
}

// CompileClientProtocolBindings expands a provider profile + client list into
// explicit protocol_bindings. This keeps common CLI/API support out of operator
// handwritten JSON while preserving the gateway's strict binding contract.
func CompileClientProtocolBindings(profile string, clients []string) ([]ProtocolBinding, string, error) {
	return CompileClientProtocolBindingsWithOptions(profile, clients, ClientProtocolBindingOptions{})
}

func CompileClientProtocolBindingsWithOptions(profile string, clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if profile == "" {
		profile = ProviderProfileOpenAICompatible
	}
	if len(clients) == 0 {
		clients = []string{ClientCompatibilityAll}
	}

	switch profile {
	case ProviderProfileOpenAICompatible:
		return compileOpenAICompatibleBindings(clients, opts)
	case ProviderProfileOpenAINative:
		return compileOpenAICompatibleBindings(clients, opts)
	case ProviderProfileAnthropicNative:
		return compileAnthropicNativeBindings(clients, opts)
	case ProviderProfileOpenAIResponses:
		return compileOpenAIResponsesNativeBindings(clients, opts)
	case ProviderProfileAzureOpenAI, ProviderProfileDoubaoArk, ProviderProfileGLM, ProviderProfileGLMCoding:
		spec, _ := openAIWireSpecForProfile(profile)
		return compileOpenAIWireProfileBindings(spec, clients, opts)
	case ProviderProfileGeminiNative:
		return compileGeminiNativeBindings(clients, opts)
	case ProviderProfileCohereV2:
		return compileCohereV2Bindings(clients, opts)
	default:
		return nil, "", fmt.Errorf("provider_profile unsupported: %s", profile)
	}
}

func compileOpenAICompatibleBindings(clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if err := validateOpenAICompatibleTokenCountStrategy(opts.TokenCountStrategy); err != nil {
		return nil, "", err
	}
	needed, err := requiredOpenAICompatibleBindings(clients)
	if err != nil {
		return nil, "", err
	}
	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
	out := make([]ProtocolBinding, 0, len(needed))
	for _, name := range sortedBindingNames(needed) {
		out = append(out, openAICompatibleBindingByName(name, pathPrefix))
	}
	return out, "openai-chat", nil
}

type openAIWireProfileSpec struct {
	profile           string
	outboundProtocol  string
	compatAPIType     string
	defaultPathPrefix string
	queryTemplate     string
	authScheme        string
	defaultBinding    string
	chatBinding       string
	embeddingsBinding string
	imagesBinding     string
	embeddings        bool
}

// AzureOpenAIQueryTemplate is the api-version query the Azure OpenAI endpoint
// requires. It used to live on the gateway's protocol-level query table for
// outbound_protocol=azure_openai; with the pure-dialect shape it is emitted as
// binding-level query_template (binding wins over protocol level on gateway).
const AzureOpenAIQueryTemplate = "api-version={api_version}"

// openAIWireSpecForProfile is the single source of truth for the four vendor
// profiles that speak the openai chat wire dialect (ADR-0009 / G4 P2). All
// compile paths — the legacy client-App path, the ADR-0012 served-inbound
// path, and the ADR-0016 explicit-paths path — must build vendor bindings
// from this spec so the emitted shape can never drift between paths.
//
// G4 P2 (wire purity): outbound_protocol is the pure dialect ("openai");
// the vendor identity is carried by compat_api_type, which the gateway P0
// AdapterBundle mechanism resolves to the vendor adapter bundle (zhipu
// thinking/tool_stream injection, doubao/azure endpoint semantics). Binding
// names, path_prefix, auth_scheme and translator are unchanged from the
// vendor-protocol era.
func openAIWireSpecForProfile(profile string) (openAIWireProfileSpec, bool) {
	switch profile {
	case ProviderProfileAzureOpenAI:
		return openAIWireProfileSpec{
			profile:           ProviderProfileAzureOpenAI,
			outboundProtocol:  "openai",
			compatAPIType:     "azure",
			defaultPathPrefix: defaultAzureOpenAIPathPrefix,
			queryTemplate:     AzureOpenAIQueryTemplate,
			authScheme:        "azure-key",
			defaultBinding:    "azure-openai-chat",
			chatBinding:       "azure-openai-chat",
			embeddingsBinding: "azure-openai-embeddings",
			imagesBinding:     "azure-openai-images",
			embeddings:        true,
		}, true
	case ProviderProfileDoubaoArk:
		return openAIWireProfileSpec{
			profile:           ProviderProfileDoubaoArk,
			outboundProtocol:  "openai",
			compatAPIType:     "doubao",
			defaultPathPrefix: defaultDoubaoArkPathPrefix,
			authScheme:        "bearer",
			defaultBinding:    "doubao-chat",
			chatBinding:       "doubao-chat",
			embeddingsBinding: "doubao-embeddings",
			imagesBinding:     "doubao-images",
			embeddings:        true,
		}, true
	case ProviderProfileGLM:
		return openAIWireProfileSpec{
			profile:           ProviderProfileGLM,
			outboundProtocol:  "openai",
			compatAPIType:     "glm",
			defaultPathPrefix: defaultGLMPathPrefix,
			authScheme:        "bearer",
			defaultBinding:    "glm-chat",
			chatBinding:       "glm-chat",
			embeddingsBinding: "glm-embeddings",
			imagesBinding:     "glm-images",
			embeddings:        true,
		}, true
	case ProviderProfileGLMCoding:
		return openAIWireProfileSpec{
			profile:           ProviderProfileGLMCoding,
			outboundProtocol:  "openai",
			compatAPIType:     "glm_coding",
			defaultPathPrefix: defaultGLMCodingPathPrefix,
			authScheme:        "bearer",
			defaultBinding:    "glm-coding-chat",
			chatBinding:       "glm-coding-chat",
		}, true
	default:
		return openAIWireProfileSpec{}, false
	}
}

func compileOpenAIWireProfileBindings(spec openAIWireProfileSpec, clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if err := validateNonNativeTokenCountStrategy(spec.profile, opts.TokenCountStrategy); err != nil {
		return nil, "", err
	}
	needed, err := requiredOpenAIWireBindings(spec, clients)
	if err != nil {
		return nil, "", err
	}
	pathPrefix := firstNonEmpty(opts.PathPrefix, spec.defaultPathPrefix)
	out := make([]ProtocolBinding, 0, len(needed))
	for _, name := range sortedBindingNames(needed) {
		out = append(out, openAIWireBindingByName(spec, name, pathPrefix))
	}
	return out, spec.defaultBinding, nil
}

func requiredOpenAIWireBindings(spec openAIWireProfileSpec, clients []string) (map[string]struct{}, error) {
	needed := map[string]struct{}{}
	add := func(names ...string) {
		for _, name := range names {
			needed[name] = struct{}{}
		}
	}
	for _, client := range clients {
		switch client {
		case ClientCompatibilityAll:
			add(spec.chatBinding)
			if spec.embeddings {
				add(spec.embeddingsBinding)
			}
		case ClientCompatibilityOpenAISDK, ClientCompatibilityCursor, ClientCompatibilityCline, ClientCompatibilityCherryStudio:
			add(spec.chatBinding)
		case ClientCompatibilityDify:
			add(spec.chatBinding)
			if spec.embeddings {
				add(spec.embeddingsBinding)
			}
		default:
			return nil, fmt.Errorf("client_compatibility unsupported for provider_profile %s: %s", spec.profile, client)
		}
	}
	return needed, nil
}

func openAIWireBindingByName(spec openAIWireProfileSpec, name, pathPrefix string) ProtocolBinding {
	switch name {
	case spec.chatBinding:
		// G4 P2: pure dialect accepts only — no vendor protocol row. The
		// vendor identity lives in CompatAPIType (adapter-bundle selector).
		return ProtocolBinding{
			Name:             name,
			OutboundProtocol: spec.outboundProtocol,
			PathPrefix:       pathPrefix,
			QueryTemplate:    spec.queryTemplate,
			AuthScheme:       spec.authScheme,
			TranslatorKey:    "passthrough",
			CompatAPIType:    spec.compatAPIType,
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		}
	case spec.embeddingsBinding:
		return ProtocolBinding{
			Name:             name,
			OutboundProtocol: spec.outboundProtocol,
			PathPrefix:       pathPrefix,
			QueryTemplate:    spec.queryTemplate,
			AuthScheme:       spec.authScheme,
			TranslatorKey:    "passthrough",
			CompatAPIType:    spec.compatAPIType,
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"embeddings"},
				RequestContract: "embeddings",
			}},
		}
	case spec.imagesBinding:
		return ProtocolBinding{
			Name:             name,
			OutboundProtocol: spec.outboundProtocol,
			PathPrefix:       pathPrefix,
			QueryTemplate:    spec.queryTemplate,
			AuthScheme:       spec.authScheme,
			TranslatorKey:    "passthrough",
			CompatAPIType:    spec.compatAPIType,
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"image_generation"},
				RequestContract: "image_generations",
			}},
		}
	default:
		panic(fmt.Sprintf("unknown %s binding: %s", spec.profile, name))
	}
}

func compileOpenAIResponsesNativeBindings(clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if err := validateNonNativeTokenCountStrategy(ProviderProfileOpenAIResponses, opts.TokenCountStrategy); err != nil {
		return nil, "", err
	}
	needed := map[string]struct{}{}
	for _, client := range clients {
		switch client {
		case ClientCompatibilityAll, ClientCompatibilityCodex, ClientCompatibilityOpenAISDK:
			needed["openai-responses"] = struct{}{}
		default:
			return nil, "", fmt.Errorf("client_compatibility unsupported for provider_profile %s: %s", ProviderProfileOpenAIResponses, client)
		}
	}
	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
	out := make([]ProtocolBinding, 0, len(needed))
	for range needed {
		out = append(out, openAIResponsesNativeBinding(pathPrefix))
	}
	return out, "openai-responses", nil
}

func openAIResponsesNativeBinding(pathPrefix string) ProtocolBinding {
	return ProtocolBinding{
		Name:             "openai-responses",
		OutboundProtocol: "openai_responses",
		PathPrefix:       pathPrefix,
		AuthScheme:       "bearer",
		TranslatorKey:    "passthrough",
		CompatAPIType:    "openai_responses",
		CapabilityProfiles: []string{
			"openai.responses.base",
		},
		Accepts: []ProtocolBindingAccept{{
			Protocol:        "responses",
			Capabilities:    []string{"chat"},
			RequestContract: "responses",
		}},
	}
}

func compileGeminiNativeBindings(clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if err := validateNonNativeTokenCountStrategy(ProviderProfileGeminiNative, opts.TokenCountStrategy); err != nil {
		return nil, "", err
	}
	needed := map[string]struct{}{}
	for _, client := range clients {
		switch client {
		case ClientCompatibilityAll, ClientCompatibilityGeminiCLI:
			needed["gemini-generate"] = struct{}{}
		default:
			return nil, "", fmt.Errorf("client_compatibility unsupported for provider_profile %s: %s", ProviderProfileGeminiNative, client)
		}
	}
	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultGeminiNativePathPrefix)
	out := make([]ProtocolBinding, 0, len(needed))
	for _, name := range sortedBindingNames(needed) {
		out = append(out, geminiNativeBindingByName(name, pathPrefix))
	}
	return out, "gemini-generate", nil
}

func geminiNativeBindingByName(name, pathPrefix string) ProtocolBinding {
	base := ProtocolBinding{
		Name:             name,
		OutboundProtocol: "gemini",
		PathPrefix:       pathPrefix,
		AuthScheme:       "google-key",
		CompatAPIType:    "gemini",
		CapabilityProfiles: []string{
			"gemini.generate_content.base",
			"gemini.generate_content.tools",
		},
	}
	switch name {
	case "gemini-generate":
		base.TranslatorKey = "passthrough"
		base.Accepts = []ProtocolBindingAccept{{
			Protocol:        "gemini",
			Capabilities:    []string{"chat"},
			RequestContract: "gemini_generate",
		}}
	default:
		panic(fmt.Sprintf("unknown gemini-native binding: %s", name))
	}
	return base
}

func compileCohereV2Bindings(clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if err := validateNonNativeTokenCountStrategy(ProviderProfileCohereV2, opts.TokenCountStrategy); err != nil {
		return nil, "", err
	}
	for _, client := range clients {
		if client != ClientCompatibilityAll && client != ClientCompatibilityDify {
			return nil, "", fmt.Errorf("client_compatibility unsupported for provider_profile %s: %s", ProviderProfileCohereV2, client)
		}
	}
	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultCohereV2PathPrefix)
	return []ProtocolBinding{cohereV2RerankBinding(pathPrefix)}, "cohere-rerank", nil
}

func cohereV2RerankBinding(pathPrefix string) ProtocolBinding {
	return ProtocolBinding{
		Name:             "cohere-rerank",
		OutboundProtocol: "cohere_v2",
		PathPrefix:       pathPrefix,
		AuthScheme:       "bearer",
		TranslatorKey:    "passthrough",
		CompatAPIType:    "cohere_v2",
		Accepts: []ProtocolBindingAccept{{
			Protocol:        "openai",
			Capabilities:    []string{"rerank"},
			RequestContract: "rerank",
		}},
	}
}

func compileAnthropicNativeBindings(clients []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	strategy := firstNonEmpty(opts.TokenCountStrategy, TokenCountStrategyNative)
	if err := validateTokenCountStrategy(strategy, false); err != nil {
		return nil, "", err
	}
	needed := map[string]struct{}{}
	for _, client := range clients {
		switch client {
		case ClientCompatibilityAll, ClientCompatibilityClaudeCode:
			needed["anthropic-messages"] = struct{}{}
			if strategy == TokenCountStrategyNative {
				needed["anthropic-count-tokens"] = struct{}{}
			}
		default:
			return nil, "", fmt.Errorf("client_compatibility unsupported: %s", client)
		}
	}

	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultAnthropicNativePathPrefix)
	out := make([]ProtocolBinding, 0, len(needed))
	for _, name := range sortedBindingNames(needed) {
		out = append(out, anthropicNativeBindingByName(name, pathPrefix))
	}
	return out, "anthropic-messages", nil
}

func requiredOpenAICompatibleBindings(clients []string) (map[string]struct{}, error) {
	needed := map[string]struct{}{}
	add := func(names ...string) {
		for _, name := range names {
			needed[name] = struct{}{}
		}
	}
	for _, client := range clients {
		switch client {
		case ClientCompatibilityAll:
			add("openai-chat", "openai-embeddings", "anthropic-messages-to-openai", "responses-to-openai-chat")
		case ClientCompatibilityClaudeCode:
			add("anthropic-messages-to-openai")
		case ClientCompatibilityCodex:
			add("responses-to-openai-chat")
		case ClientCompatibilityOpenAISDK:
			add("openai-chat", "openai-embeddings")
		case ClientCompatibilityOpenClaw:
			add("openai-chat", "anthropic-messages-to-openai")
		case ClientCompatibilityGeminiCLI:
			return nil, fmt.Errorf("gemini_cli requires a gemini-wire provider")
		case ClientCompatibilityCursor, ClientCompatibilityCline, ClientCompatibilityCherryStudio:
			add("openai-chat")
		case ClientCompatibilityDify:
			add("openai-chat", "openai-embeddings")
		default:
			return nil, fmt.Errorf("client_compatibility unsupported: %s", client)
		}
	}
	return needed, nil
}

func sortedBindingNames(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for name := range values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func openAICompatibleBindingByName(name, pathPrefix string) ProtocolBinding {
	switch name {
	case "anthropic-messages-to-openai":
		return ProtocolBinding{
			Name:             "anthropic-messages-to-openai",
			OutboundProtocol: "openai",
			PathPrefix:       pathPrefix,
			AuthScheme:       "bearer",
			TranslatorKey:    "standard_to_openai",
			Bridge:           "anthropic_to_openai_chat",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "anthropic",
				Capabilities:    []string{"chat"},
				RequestContract: "anthropic_messages",
			}},
		}
	case "openai-embeddings":
		return ProtocolBinding{
			Name:             "openai-embeddings",
			OutboundProtocol: "openai",
			PathPrefix:       pathPrefix,
			AuthScheme:       "bearer",
			TranslatorKey:    "passthrough",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"embeddings"},
				RequestContract: "embeddings",
			}},
		}
	case "openai-images":
		return ProtocolBinding{
			Name:             "openai-images",
			OutboundProtocol: "openai",
			PathPrefix:       pathPrefix,
			AuthScheme:       "bearer",
			TranslatorKey:    "passthrough",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"image_generation"},
				RequestContract: "image_generations",
			}},
		}
	case "responses-to-openai-chat":
		return ProtocolBinding{
			Name:             "responses-to-openai-chat",
			OutboundProtocol: "openai",
			PathPrefix:       pathPrefix,
			AuthScheme:       "bearer",
			TranslatorKey:    "responses_to_chat_synthesis",
			Bridge:           "responses_to_chat",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "responses",
				Capabilities:    []string{"chat"},
				RequestContract: "responses",
			}},
		}
	case "openai-chat":
		return ProtocolBinding{
			Name:             "openai-chat",
			OutboundProtocol: "openai",
			PathPrefix:       pathPrefix,
			AuthScheme:       "bearer",
			TranslatorKey:    "passthrough",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		}
	default:
		panic(fmt.Sprintf("unknown openai-compatible binding: %s", name))
	}
}

func anthropicNativeBindingByName(name, pathPrefix string) ProtocolBinding {
	switch name {
	case "anthropic-count-tokens":
		return ProtocolBinding{
			Name:             "anthropic-count-tokens",
			OutboundProtocol: "anthropic",
			PathPrefix:       pathPrefix,
			AuthScheme:       "x-api-key",
			TranslatorKey:    "passthrough",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "anthropic",
				Capabilities:    []string{"count_tokens"},
				RequestContract: "anthropic_count_tokens",
			}},
		}
	case "anthropic-messages":
		return ProtocolBinding{
			Name:             "anthropic-messages",
			OutboundProtocol: "anthropic",
			PathPrefix:       pathPrefix,
			AuthScheme:       "x-api-key",
			TranslatorKey:    "passthrough",
			CapabilityProfiles: []string{
				"anthropic.messages.base",
				"anthropic.messages.tools",
			},
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "anthropic",
				Capabilities:    []string{"chat"},
				RequestContract: "anthropic_messages",
			}},
		}
	default:
		panic(fmt.Sprintf("unknown anthropic-native binding: %s", name))
	}
}

func validateTokenCountStrategy(strategy string, allowGatewayEstimate bool) error {
	switch strategy {
	case "", TokenCountStrategyNative, TokenCountStrategyUnsupported:
		return nil
	case TokenCountStrategyGatewayEstimate:
		if allowGatewayEstimate {
			return nil
		}
		return fmt.Errorf("token_count_strategy unsupported for provider_profile %s: %s", ProviderProfileAnthropicNative, strategy)
	default:
		return fmt.Errorf("token_count_strategy unsupported: %s", strategy)
	}
}

func validateOpenAICompatibleTokenCountStrategy(strategy string) error {
	switch strategy {
	case "", TokenCountStrategyUnsupported:
		return nil
	case TokenCountStrategyNative, TokenCountStrategyGatewayEstimate:
		return fmt.Errorf("token_count_strategy unsupported for provider_profile %s: %s", ProviderProfileOpenAICompatible, strategy)
	default:
		return fmt.Errorf("token_count_strategy unsupported: %s", strategy)
	}
}

func validateNonNativeTokenCountStrategy(profile, strategy string) error {
	switch strategy {
	case "", TokenCountStrategyUnsupported:
		return nil
	case TokenCountStrategyNative, TokenCountStrategyGatewayEstimate:
		return fmt.Errorf("token_count_strategy unsupported for provider_profile %s: %s", profile, strategy)
	default:
		return fmt.Errorf("token_count_strategy unsupported: %s", strategy)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// ADR-0012 / ADR-0009: explicit inbound-dialect binding compilation.
//
// The legacy path above compiles bindings from a client-App list
// (claude_code/codex/...) by reverse-lookup into a hardcoded table. ADR-0012
// inverts the causality: the compile input becomes the set of inbound Wire
// dialects an endpoint serves; bindings are *composed* from
// (inbound dialect × upstream dialect × bridge registry). Client-App
// compatibility becomes a derived output (Accepts.Protocol union).
//
// This is additive and gated by intent.ClientExposure.ServedInboundProtocols;
// it does NOT replace or delete the legacy App-list path (that is ADR-0012 S4).
// ---------------------------------------------------------------------------

// Inbound Wire dialects (pure dialects only; vendor variants like glm/glm_coding
// are NOT valid inbound dialects per ADR-0009).
const (
	InboundProtocolOpenAI    = "openai"
	InboundProtocolAnthropic = "anthropic"
	InboundProtocolResponses = "responses"
	InboundProtocolGemini    = "gemini"
)

// KnownServedInboundProtocols is the enum for ClientExposure.served_inbound_protocols.
func KnownServedInboundProtocols() []string {
	return []string{
		InboundProtocolAnthropic,
		InboundProtocolGemini,
		InboundProtocolOpenAI,
		InboundProtocolResponses,
	}
}

// ProtocolPath is one explicit path declaration on an upstream endpoint
// (ADR-0016): the inbound Wire dialect it serves plus the channel that carries
// it to the upstream (passthrough or an explicit bridge). The channel value
// domain is shared with the dedicated endpoint protocol_strategy enum
// (KnownDedicatedProtocolChannels, ADR-0014/0015): passthrough /
// responses_to_chat / anthropic_to_chat.
type ProtocolPath struct {
	InboundProtocol string `json:"inbound_protocol"`
	Channel         string `json:"channel"`
}

// upstreamDialectForProfile maps a provider_profile to the pure upstream Wire
// dialect it speaks. Vendor profiles (glm, glm_coding, doubao, azure) all speak
// the openai chat wire dialect; only endpoint/path/auth differ (ADR-0009).
func upstreamDialectForProfile(profile string) (string, error) {
	switch profile {
	case "", ProviderProfileOpenAICompatible, ProviderProfileOpenAINative,
		ProviderProfileAzureOpenAI, ProviderProfileDoubaoArk,
		ProviderProfileGLM, ProviderProfileGLMCoding:
		return InboundProtocolOpenAI, nil
	case ProviderProfileAnthropicNative:
		return InboundProtocolAnthropic, nil
	case ProviderProfileGeminiNative:
		return InboundProtocolGemini, nil
	case ProviderProfileOpenAIResponses:
		return InboundProtocolResponses, nil
	default:
		return "", fmt.Errorf("provider_profile has no inbound-dialect mapping: %s", profile)
	}
}

// CapabilityChat is the chat operation capability. chat bindings are composed
// from the served inbound Wire dialects (passthrough/bridge); all other
// capabilities are orthogonal operations (ADR-0009) that bind same-dialect to
// the upstream (no bridge).
const CapabilityChat = "chat"

// CompileInboundProtocolBindings composes explicit protocol_bindings from a set
// of inbound Wire dialects an endpoint serves plus a set of declared
// capabilities, per ADR-0012 / ADR-0009. capability is an orthogonal axis:
//
//   - chat (or empty capabilities) → for each served inbound dialect, emit the
//     same-dialect passthrough binding, or a registered bridge (inbound →
//     upstream) when the dialects differ. Missing bridge => error.
//   - embeddings / rerank / other non-chat → emit the upstream dialect's
//     capability binding directly (same dialect, NO bridge), reusing the same
//     per-profile builders the legacy App path used. A profile that has no
//     builder for the capability => error (never synthesized).
//
// The final binding set is the union of (served inbound dialect → chat
// passthrough/bridge) ∪ (declared non-chat capability → upstream capability
// binding). For served=[openai]+capabilities=[chat,embeddings] on an
// openai-family profile this yields {openai-chat, openai-embeddings}, identical
// to the legacy client_compatibility=[openai_sdk] output.
//
// It reuses the existing per-profile binding builders so binding shape stays
// identical to the legacy path. Returns the compiled bindings and the default
// binding name (the upstream-passthrough binding when present, else the first
// sorted binding).
func CompileInboundProtocolBindings(profile string, inbound []string, capabilities []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, error) {
	if profile == "" {
		profile = ProviderProfileOpenAICompatible
	}

	bindingsByName := map[string]ProtocolBinding{}
	passthroughDefault := ""
	addBinding := func(binding ProtocolBinding) error {
		if existing, ok := bindingsByName[binding.Name]; ok {
			if !reflect.DeepEqual(existing, binding) {
				return fmt.Errorf("served_inbound_protocols produce conflicting binding %s", binding.Name)
			}
		}
		bindingsByName[binding.Name] = binding
		return nil
	}

	// chat axis: compose from served inbound dialects (passthrough/bridge). The
	// upstream-dialect lookup is only required when there are inbound dialects to
	// compose; capability-only profiles (e.g. cohere_v2 rerank) have no chat
	// dialect mapping and never reach it.
	if capabilitiesIncludeChat(capabilities) && len(inbound) > 0 {
		upstream, err := upstreamDialectForProfile(profile)
		if err != nil {
			return nil, "", err
		}
		for _, dialect := range inbound {
			binding, isPassthrough, err := inboundDialectBinding(profile, upstream, dialect, opts)
			if err != nil {
				return nil, "", err
			}
			if err := addBinding(binding); err != nil {
				return nil, "", err
			}
			if isPassthrough && passthroughDefault == "" {
				passthroughDefault = binding.Name
			}
		}
	}

	// non-chat capability axis: bind same-dialect to upstream (no bridge).
	for _, capability := range capabilities {
		if capability == "" || capability == CapabilityChat {
			continue
		}
		binding, err := upstreamCapabilityBinding(profile, capability, opts)
		if err != nil {
			return nil, "", err
		}
		if err := addBinding(binding); err != nil {
			return nil, "", err
		}
	}

	names := sortedBindingNames(toStructSet(bindingsByName))
	out := make([]ProtocolBinding, 0, len(names))
	for _, name := range names {
		out = append(out, bindingsByName[name])
	}
	defaultBinding := passthroughDefault
	if defaultBinding == "" && len(names) > 0 {
		defaultBinding = names[0]
	}
	return out, defaultBinding, nil
}

// capabilitiesIncludeChat reports whether the chat operation should be composed
// from served inbound dialects. Empty capabilities preserves the prior behavior
// (chat-only, dialect-driven) so existing intents do not regress.
func capabilitiesIncludeChat(capabilities []string) bool {
	if len(capabilities) == 0 {
		return true
	}
	for _, c := range capabilities {
		if c == "" || c == CapabilityChat {
			return true
		}
	}
	return false
}

// upstreamCapabilityBinding returns the same-dialect (upstream) binding that
// serves a single non-chat capability for the given profile, reusing the
// existing per-profile builders. embeddings/image_generation/rerank do NOT go
// through a bridge.
// A profile that does not support the capability returns an error (never a
// synthesized builder).
func upstreamCapabilityBinding(profile, capability string, opts ClientProtocolBindingOptions) (ProtocolBinding, error) {
	switch capability {
	case "embeddings":
		switch profile {
		case ProviderProfileOpenAICompatible, ProviderProfileOpenAINative:
			pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
			return openAICompatibleBindingByName("openai-embeddings", pathPrefix), nil
		case ProviderProfileAzureOpenAI, ProviderProfileDoubaoArk, ProviderProfileGLM:
			spec, _ := openAIWireSpecForProfile(profile)
			return openAIWireProfileEmbeddings(spec, opts), nil
		default:
			return ProtocolBinding{}, fmt.Errorf("provider_profile %s does not support capability %q", profile, capability)
		}
	case "image_generation":
		switch profile {
		case ProviderProfileOpenAICompatible, ProviderProfileOpenAINative:
			pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
			return openAICompatibleBindingByName("openai-images", pathPrefix), nil
		case ProviderProfileAzureOpenAI, ProviderProfileDoubaoArk, ProviderProfileGLM:
			spec, _ := openAIWireSpecForProfile(profile)
			return openAIWireProfileImages(spec, opts), nil
		default:
			return ProtocolBinding{}, fmt.Errorf("provider_profile %s does not support capability %q", profile, capability)
		}
	case "rerank":
		switch profile {
		case ProviderProfileCohereV2:
			pathPrefix := firstNonEmpty(opts.PathPrefix, defaultCohereV2PathPrefix)
			return cohereV2RerankBinding(pathPrefix), nil
		default:
			return ProtocolBinding{}, fmt.Errorf("provider_profile %s does not support capability %q", profile, capability)
		}
	default:
		return ProtocolBinding{}, fmt.Errorf("provider_profile %s does not support capability %q", profile, capability)
	}
}

func openAIWireProfileEmbeddings(spec openAIWireProfileSpec, opts ClientProtocolBindingOptions) ProtocolBinding {
	pathPrefix := firstNonEmpty(opts.PathPrefix, spec.defaultPathPrefix)
	return openAIWireBindingByName(spec, spec.embeddingsBinding, pathPrefix)
}

func openAIWireProfileImages(spec openAIWireProfileSpec, opts ClientProtocolBindingOptions) ProtocolBinding {
	pathPrefix := firstNonEmpty(opts.PathPrefix, spec.defaultPathPrefix)
	return openAIWireBindingByName(spec, spec.imagesBinding, pathPrefix)
}

func toStructSet(m map[string]ProtocolBinding) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for name := range m {
		out[name] = struct{}{}
	}
	return out
}

// inboundDialectBinding returns the single binding that serves one inbound
// dialect for the given profile/upstream dialect, reusing existing builders.
// The bool is true when the binding is an upstream-passthrough (same dialect).
func inboundDialectBinding(profile, upstream, dialect string, opts ClientProtocolBindingOptions) (ProtocolBinding, bool, error) {
	if dialect == upstream {
		b, err := upstreamPassthroughBinding(profile, opts)
		return b, true, err
	}
	// Cross-dialect: require a registered bridge (inbound → upstream).
	switch {
	case dialect == InboundProtocolAnthropic && upstream == InboundProtocolOpenAI:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
		return openAICompatibleBindingByName("anthropic-messages-to-openai", pathPrefix), false, nil
	case dialect == InboundProtocolResponses && upstream == InboundProtocolOpenAI:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
		return openAICompatibleBindingByName("responses-to-openai-chat", pathPrefix), false, nil
	default:
		return ProtocolBinding{}, false, fmt.Errorf(
			"no registered bridge for inbound dialect %q to upstream dialect %q (profile %s)",
			dialect, upstream, profile)
	}
}

// upstreamPassthroughBinding returns the same-dialect passthrough binding for a
// profile, reusing the existing per-profile chat builders.
func upstreamPassthroughBinding(profile string, opts ClientProtocolBindingOptions) (ProtocolBinding, error) {
	switch profile {
	case ProviderProfileOpenAICompatible, ProviderProfileOpenAINative:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
		return openAICompatibleBindingByName("openai-chat", pathPrefix), nil
	case ProviderProfileAzureOpenAI, ProviderProfileDoubaoArk, ProviderProfileGLM, ProviderProfileGLMCoding:
		spec, _ := openAIWireSpecForProfile(profile)
		return openAIWireProfilePassthrough(spec, opts), nil
	case ProviderProfileAnthropicNative:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultAnthropicNativePathPrefix)
		return anthropicNativeBindingByName("anthropic-messages", pathPrefix), nil
	case ProviderProfileGeminiNative:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultGeminiNativePathPrefix)
		return geminiNativeBindingByName("gemini-generate", pathPrefix), nil
	case ProviderProfileOpenAIResponses:
		pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
		return openAIResponsesNativeBinding(pathPrefix), nil
	default:
		return ProtocolBinding{}, fmt.Errorf("provider_profile has no passthrough binding: %s", profile)
	}
}

func openAIWireProfilePassthrough(spec openAIWireProfileSpec, opts ClientProtocolBindingOptions) ProtocolBinding {
	pathPrefix := firstNonEmpty(opts.PathPrefix, spec.defaultPathPrefix)
	return openAIWireBindingByName(spec, spec.chatBinding, pathPrefix)
}

// dialectClientApps maps a pure inbound Wire dialect to the client Apps known to
// speak it. Used only to compute the DERIVED ClientCompatibilities observation
// view (ADR-0012); it is the reverse of the legacy App→binding intent and is
// NOT a compile input.
func dialectClientApps(dialect string) []string {
	switch dialect {
	case InboundProtocolAnthropic:
		return []string{ClientCompatibilityClaudeCode}
	case InboundProtocolOpenAI:
		return []string{
			ClientCompatibilityOpenAISDK,
			ClientCompatibilityCursor,
			ClientCompatibilityCline,
			ClientCompatibilityCherryStudio,
			ClientCompatibilityDify,
			ClientCompatibilityOpenClaw,
		}
	case InboundProtocolResponses:
		return []string{ClientCompatibilityCodex}
	case InboundProtocolGemini:
		return []string{ClientCompatibilityGeminiCLI}
	default:
		return nil
	}
}

// DeriveCompatibilityView computes the inbound-dialect set (union of
// Accepts.Protocol across bindings) and the derived client-App compatibility
// labels. Both outputs are sorted and deduplicated. Observation-only (ADR-0012).
func DeriveCompatibilityView(bindings []ProtocolBinding) (inboundProtocols []string, clientCompatibilities []string) {
	protocolSet := map[string]struct{}{}
	appSet := map[string]struct{}{}
	for _, b := range bindings {
		for _, accept := range b.Accepts {
			if accept.Protocol == "" {
				continue
			}
			protocolSet[accept.Protocol] = struct{}{}
			for _, app := range dialectClientApps(accept.Protocol) {
				appSet[app] = struct{}{}
			}
		}
	}
	return sortedKeys(protocolSet), sortedKeys(appSet)
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
