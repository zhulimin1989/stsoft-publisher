package types

import (
	_ "embed"
	"encoding/json"
)

const ProtocolBindingsContractVersion = 2

//go:embed testdata/protocol_bindings_contract.json
var protocolBindingsContractJSON []byte

type ProtocolBindingsContract struct {
	Version                      int                           `json:"version"`
	Source                       string                        `json:"source"`
	ProviderProfiles             []string                      `json:"provider_profiles"`
	ClientCompatibilities        []string                      `json:"client_compatibilities"`
	TokenCountStrategies         []string                      `json:"token_count_strategies"`
	InboundProtocols             []string                      `json:"inbound_protocols"`
	Capabilities                 []string                      `json:"capabilities"`
	RequestContracts             []string                      `json:"request_contracts"`
	OutboundProtocols            []string                      `json:"outbound_protocols"`
	AuthSchemes                  []string                      `json:"auth_schemes"`
	Translators                  []string                      `json:"translators"`
	Bridges                      []string                      `json:"bridges"`
	DedicatedProtocolChannels    []string                      `json:"dedicated_protocol_channels"`
	LossyTiers                   []string                      `json:"lossy_tiers"`
	BridgeContracts              []ProtocolBridgeContract      `json:"bridge_contracts"`
	CompatAPITypes               []string                      `json:"compat_api_types"`
	CapabilityProfiles           []string                      `json:"capability_profiles"`
	CapabilityFeatures           []string                      `json:"capability_features"`
	CapabilityParams             []string                      `json:"capability_params"`
	ParamPolicyCategories        []string                      `json:"param_policy_categories"`
	ParamPolicyUpstreamBehaviors []string                      `json:"param_policy_upstream_behaviors"`
	ParamPolicyActions           []string                      `json:"param_policy_actions"`
	FieldDispositionActions      []string                      `json:"field_disposition_actions"`
	RequestCorrelationModes      []string                      `json:"request_correlation_modes"`
	RuntimeEndpoints             []ProtocolRuntimeEndpoint     `json:"runtime_endpoints"`
	TranslationMatrix            []ProtocolTranslationContract `json:"translation_matrix"`
}

type ProtocolRuntimeEndpoint struct {
	OutboundProtocol string `json:"outbound_protocol"`
	Capability       string `json:"capability"`
	RequestContract  string `json:"request_contract"`
	PathTemplate     string `json:"path_template"`
	QueryTemplate    string `json:"query_template,omitempty"`
}

type ProtocolTranslationContract struct {
	From            string `json:"from"`
	To              string `json:"to"`
	Capability      string `json:"capability"`
	RequestContract string `json:"request_contract"`
	Translator      string `json:"translator"`
	Bridge          string `json:"bridge"`
}

type ProtocolBridgeContract struct {
	Bridge         string                       `json:"bridge"`
	LossyTier      string                       `json:"lossy_tier"`
	AdmittedSubset ProtocolBridgeAdmittedSubset `json:"admitted_subset,omitempty"`
}

type ProtocolBridgeAdmittedSubset struct {
	Request   ProtocolBridgeRequestSubset   `json:"request,omitempty"`
	Response  ProtocolBridgeResponseSubset  `json:"response,omitempty"`
	Streaming ProtocolBridgeStreamingSubset `json:"streaming,omitempty"`
}

type ProtocolBridgeRequestSubset struct {
	AllowedItemTypes []string `json:"allowed_item_types,omitempty"`
}

type ProtocolBridgeResponseSubset struct {
	AllowedOutputTypes []string `json:"allowed_output_types,omitempty"`
}

type ProtocolBridgeStreamingSubset struct {
	AllowedEvents []string `json:"allowed_events,omitempty"`
}

var embeddedProtocolBindingsContract = mustLoadProtocolBindingsContract(protocolBindingsContractJSON)

func EmbeddedProtocolBindingsContract() ProtocolBindingsContract {
	return embeddedProtocolBindingsContract
}

func mustLoadProtocolBindingsContract(data []byte) ProtocolBindingsContract {
	var contract ProtocolBindingsContract
	if err := json.Unmarshal(data, &contract); err != nil {
		panic("protocol bindings contract: " + err.Error())
	}
	if contract.Version != ProtocolBindingsContractVersion {
		panic("protocol bindings contract: unsupported version")
	}
	return contract
}

func KnownProviderProfiles() []string {
	return []string{
		ProviderProfileAnthropicNative,
		ProviderProfileAzureOpenAI,
		ProviderProfileCohereV2,
		ProviderProfileDoubaoArk,
		ProviderProfileGeminiNative,
		ProviderProfileGLM,
		ProviderProfileGLMCoding,
		ProviderProfileOpenAICompatible,
		ProviderProfileOpenAINative,
		ProviderProfileOpenAIResponses,
	}
}

func KnownClientCompatibilities() []string {
	return []string{
		ClientCompatibilityAll,
		ClientCompatibilityCherryStudio,
		ClientCompatibilityClaudeCode,
		ClientCompatibilityCline,
		ClientCompatibilityCodex,
		ClientCompatibilityCursor,
		ClientCompatibilityDify,
		ClientCompatibilityGeminiCLI,
		ClientCompatibilityOpenAISDK,
		ClientCompatibilityOpenClaw,
	}
}

func KnownTokenCountStrategies() []string {
	return []string{
		TokenCountStrategyGatewayEstimate,
		TokenCountStrategyNative,
		TokenCountStrategyUnsupported,
	}
}

const (
	ClientProtocolAnthropic = "anthropic"
	ClientProtocolOpenAI    = "openai"
	ClientProtocolResponses = "responses"

	// ADR-0014 G-S2: dedicated endpoint explicit channel selectors. These are
	// the channel values for dedicated_upstream.endpoints[].protocol_strategy.
	DedicatedProtocolChannelPassthrough     = "passthrough"
	DedicatedProtocolChannelResponsesToChat = "responses_to_chat"
	DedicatedProtocolChannelAnthropicToChat = "anthropic_to_chat"

	ProtocolBridgeAnthropicToOpenAIChat = "anthropic_to_openai_chat"
	ProtocolBridgeResponsesToChat       = "responses_to_chat"

	LossyTierLow  = "low"
	LossyTierHigh = "high"
)

func KnownDedicatedClientProtocols() []string {
	return []string{
		ClientProtocolAnthropic,
		ClientProtocolOpenAI,
		ClientProtocolResponses,
	}
}

// KnownDedicatedProtocolChannels is the ADR-0014/0015 enum for dedicated
// endpoint protocol_strategy: the three explicit channels only. The legacy
// transition values (auto/passthrough_only) were retired in ADR-0015 S5a
// after all live dedicated keys migrated to explicit channels (verified in
// production redis 2026-06-30: 72 endpoints, zero legacy values). This is
// independent of the retired §10 product/route loss-strategy enum
// (auto/passthrough_only, removed in ADR-0015 S5b).
func KnownDedicatedProtocolChannels() []string {
	return []string{
		DedicatedProtocolChannelPassthrough,
		DedicatedProtocolChannelResponsesToChat,
		DedicatedProtocolChannelAnthropicToChat,
	}
}

func KnownLossyTiers() []string {
	return []string{
		LossyTierLow,
		LossyTierHigh,
	}
}
