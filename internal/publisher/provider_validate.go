package publisher

import (
	"fmt"
	"strings"

	"github.com/tokenplatform/publisher/internal/types"
)

var protocolBindingContract = types.EmbeddedProtocolBindingsContract()

var protocolBindingInboundProtocols = stringSet(protocolBindingContract.InboundProtocols)
var protocolBindingCapabilities = stringSet(protocolBindingContract.Capabilities)
var protocolBindingRequestContracts = stringSet(protocolBindingContract.RequestContracts)
var protocolBindingOutboundProtocols = stringSet(protocolBindingContract.OutboundProtocols)
var protocolBindingAuthSchemes = stringSet(protocolBindingContract.AuthSchemes)
var protocolBindingProviderProfiles = stringSet(protocolBindingContract.ProviderProfiles)
var protocolBindingTranslators = stringSet(protocolBindingContract.Translators)
var protocolBindingBridges = stringSet(protocolBindingContract.Bridges)
var protocolBindingCompatAPITypes = stringSet(protocolBindingContract.CompatAPITypes)
var protocolBindingCapabilityProfiles = stringSet(protocolBindingContract.CapabilityProfiles)
var protocolBindingCapabilityFeatures = stringSet(protocolBindingContract.CapabilityFeatures)
var protocolBindingCapabilityParams = stringSet(protocolBindingContract.CapabilityParams)
var protocolBindingParamPolicyCategories = stringSet(protocolBindingContract.ParamPolicyCategories)
var protocolBindingParamPolicyUpstreamBehaviors = stringSet(protocolBindingContract.ParamPolicyUpstreamBehaviors)
var protocolBindingParamPolicyActions = stringSet(protocolBindingContract.ParamPolicyActions)
var protocolBindingFieldDispositionActions = stringSet(protocolBindingContract.FieldDispositionActions)
var protocolBindingRequestCorrelationModes = stringSet(protocolBindingContract.RequestCorrelationModes)
var protocolBindingPathTemplates = endpointSet(protocolBindingContract.RuntimeEndpoints)
var protocolBindingTranslatorMatrix = translatorMap(protocolBindingContract.TranslationMatrix)
var protocolBindingTranslatorForBridge = translatorForBridgeMap(protocolBindingContract.TranslationMatrix)

// validateProviderInfo runs at PublishProvider entry before any I/O. Provider
// routing is protocol_bindings-only; legacy api_type/api_types inputs are
// rejected at the HTTP DTO boundary and are not represented in types.ProviderInfo.
func validateProviderInfo(info *types.ProviderInfo) error {
	if info == nil {
		return fmt.Errorf("%w: provider info required", ErrInvalidArgument)
	}
	if len(info.ProtocolBindings) == 0 {
		return fmt.Errorf("%w: provider.protocol_bindings is required", ErrInvalidArgument)
	}
	if issues := validateProviderProtocolBindings(info); len(issues) > 0 {
		return fmt.Errorf("%w: provider.protocol_bindings invalid: %s", ErrInvalidArgument, strings.Join(issues, "; "))
	}
	if err := validateEnum("provider.status", info.Status, providerStatuses); err != nil {
		return err
	}
	if strings.TrimSpace(info.ProviderProfile) != "" && !known(protocolBindingProviderProfiles, info.ProviderProfile) {
		return fmt.Errorf("%w: provider.provider_profile=%q not in allowed set", ErrInvalidArgument, info.ProviderProfile)
	}
	if info.ProviderAccountRef != "" {
		if err := types.ValidateProviderAccountRef(info.ProviderAccountRef); err != nil {
			return fmt.Errorf("%w: provider.provider_account_ref invalid: %v", ErrInvalidArgument, err)
		}
		if info.ProviderAccountRef == info.ID {
			return fmt.Errorf("%w: provider.provider_account_ref must not reuse provider.id", ErrInvalidArgument)
		}
		if info.ProviderAccountRef == info.BaseURL {
			return fmt.Errorf("%w: provider.provider_account_ref must not reuse provider.base_url", ErrInvalidArgument)
		}
	}
	if info.CredentialSource == types.ProviderCredentialSourceProviderEntitlement && info.ProviderAccountRef == "" {
		return fmt.Errorf("%w: provider.provider_account_ref required for provider_entitlement", ErrInvalidArgument)
	}
	return nil
}

// ValidateProviderInfoForPreview exposes the same provider validation used by
// PublishProvider without writing outbox rows. Admin compile-preview calls use
// this to make the preview contract match production publish behavior.
func ValidateProviderInfoForPreview(info *types.ProviderInfo) error {
	return validateProviderInfo(info)
}

func validateProviderProtocolBindings(info *types.ProviderInfo) []string {
	if info == nil {
		return nil
	}
	var issues []string
	seen := make(map[string]struct{}, len(info.ProtocolBindings))
	for i, b := range info.ProtocolBindings {
		if strings.TrimSpace(b.Name) == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].name required", i))
		} else if _, dup := seen[b.Name]; dup {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].name duplicate: %s", i, b.Name))
		} else {
			seen[b.Name] = struct{}{}
		}
		if len(b.Accepts) == 0 {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts must be non-empty", i))
		}
		for j, a := range b.Accepts {
			protocol := strings.TrimSpace(a.Protocol)
			contract := strings.TrimSpace(a.RequestContract)
			if protocol == "" {
				issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].protocol required", i, j))
			} else if !known(protocolBindingInboundProtocols, protocol) {
				issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].protocol unknown: %s", i, j, protocol))
			}
			if len(a.Capabilities) == 0 {
				issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].capabilities must be non-empty", i, j))
			}
			for k, c := range a.Capabilities {
				capability := strings.TrimSpace(c)
				if capability == "" {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].capabilities[%d] must be non-blank", i, j, k))
					continue
				}
				if !known(protocolBindingCapabilities, capability) {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].capabilities[%d] unknown: %s", i, j, k, capability))
				}
			}
			if contract == "" {
				issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].request_contract required", i, j))
			} else if !known(protocolBindingRequestContracts, contract) {
				issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].request_contract unknown: %s", i, j, contract))
			}
		}
		outbound := strings.TrimSpace(b.OutboundProtocol)
		authScheme := strings.TrimSpace(b.AuthScheme)
		translator := strings.TrimSpace(b.TranslatorKey)
		bridge := strings.TrimSpace(b.Bridge)
		compatAPIType := strings.TrimSpace(b.CompatAPIType)
		if outbound == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].outbound_protocol required", i))
		} else if !known(protocolBindingOutboundProtocols, outbound) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].outbound_protocol unknown: %s", i, outbound))
		}
		if authScheme == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].auth_scheme required", i))
		} else if !known(protocolBindingAuthSchemes, authScheme) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].auth_scheme unknown: %s", i, authScheme))
		}
		if translator != "" && !known(protocolBindingTranslators, translator) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].translator unknown: %s", i, translator))
		}
		if bridge != "" && !known(protocolBindingBridges, bridge) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].bridge: %s: %s", i, crossProtocolBridgeUnknown, bridge))
		}
		if compatAPIType != "" && !known(protocolBindingCompatAPITypes, compatAPIType) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].compat_api_type unknown: %s", i, compatAPIType))
		}
		requestCorrelationMode := strings.TrimSpace(b.RequestCorrelationMode)
		if requestCorrelationMode != "" && !known(protocolBindingRequestCorrelationModes, requestCorrelationMode) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_correlation_mode unknown: %s", i, requestCorrelationMode))
		}
		issues = append(issues, validateBindingRegistrySemantics(i, b)...)
		issues = append(issues, validateBindingExtensions(i, b)...)
	}
	if info.DefaultProtocolBinding != "" {
		if _, ok := seen[info.DefaultProtocolBinding]; !ok {
			issues = append(issues, fmt.Sprintf("default_protocol_binding references unknown binding: %s", info.DefaultProtocolBinding))
		}
	}
	return issues
}

func validateBindingRegistrySemantics(i int, b types.ProtocolBinding) []string {
	var issues []string
	outbound := strings.TrimSpace(b.OutboundProtocol)
	translator := strings.TrimSpace(b.TranslatorKey)
	bridge := strings.TrimSpace(b.Bridge)
	outboundKnown := known(protocolBindingOutboundProtocols, outbound)
	translatorKnown := translator == "" || known(protocolBindingTranslators, translator)
	bridgeKnown := bridge == "" || known(protocolBindingBridges, bridge)

	for j, a := range b.Accepts {
		protocol := strings.TrimSpace(a.Protocol)
		contract := strings.TrimSpace(a.RequestContract)
		protocolKnown := known(protocolBindingInboundProtocols, protocol)
		contractKnown := known(protocolBindingRequestContracts, contract)
		for k, rawCapability := range a.Capabilities {
			capability := strings.TrimSpace(rawCapability)
			capabilityKnown := known(protocolBindingCapabilities, capability)
			if outboundKnown && capabilityKnown && contractKnown {
				if !known(protocolBindingPathTemplates, bindingRegistryKey(outbound, capability, contract)) {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].capabilities[%d]: outbound_protocol %s cannot express capability=%s request_contract=%s", i, j, k, outbound, capability, contract))
				}
			}
			if translatorKnown && translator != "" && translator != "passthrough" && protocolKnown && outboundKnown && capabilityKnown && contractKnown {
				want, ok := protocolBindingTranslatorMatrix[bindingRegistryKey(protocol, outbound, capability, contract)]
				if !ok {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].translator unsupported for %s->%s capability=%s contract=%s", i, protocol, outbound, capability, contract))
				} else if want != translator {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].translator mismatch for %s->%s capability=%s contract=%s: got %s want %s", i, protocol, outbound, capability, contract, translator, want))
				}
			}
			if bridgeKnown && protocolKnown && outboundKnown && capabilityKnown && contractKnown {
				if reason := bridgeRejectReason(bridge, protocol, outbound, capability, contract); reason != "" {
					issues = append(issues, fmt.Sprintf("protocol_bindings[%d].accepts[%d].capabilities[%d]: %s", i, j, k, reason))
				}
			}
		}
	}
	if bridge != "" && bridgeKnown {
		want, ok := protocolBindingTranslatorForBridge[bridge]
		if !ok {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].bridge: %s: %s", i, crossProtocolBridgeUnknown, bridge))
		} else if translator != want {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].translator mismatch for bridge=%s: got %s want %s", i, bridge, translator, want))
		}
	}
	return issues
}

func validateBindingExtensions(i int, b types.ProtocolBinding) []string {
	var issues []string
	issues = appendKnownListIssues(issues, fmt.Sprintf("protocol_bindings[%d].capability_profiles", i), b.CapabilityProfiles, protocolBindingCapabilityProfiles, "profile")
	issues = appendKnownListIssues(issues, fmt.Sprintf("protocol_bindings[%d].supported_features", i), b.SupportedFeatures, protocolBindingCapabilityFeatures, "feature")
	issues = appendKnownListIssues(issues, fmt.Sprintf("protocol_bindings[%d].unsupported_features", i), b.UnsupportedFeatures, protocolBindingCapabilityFeatures, "feature")
	issues = appendKnownListIssues(issues, fmt.Sprintf("protocol_bindings[%d].supported_params", i), b.SupportedParams, protocolBindingCapabilityParams, "param")
	issues = appendKnownListIssues(issues, fmt.Sprintf("protocol_bindings[%d].unsupported_params", i), b.UnsupportedParams, protocolBindingCapabilityParams, "param")
	for field, values := range b.ParamConstraints {
		if strings.TrimSpace(field) == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].param_constraints contains empty field", i))
		} else if !known(protocolBindingCapabilityParams, field) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].param_constraints[%s] unknown", i, field))
		}
		issues = appendNonBlankListIssues(issues, fmt.Sprintf("protocol_bindings[%d].param_constraints[%s]", i, field), values)
	}
	for field, value := range b.FeatureConstraints {
		if strings.TrimSpace(field) == "" || strings.TrimSpace(value) == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].feature_constraints contains empty field/value", i))
		} else if !known(protocolBindingCapabilityFeatures, field) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].feature_constraints[%s] unknown", i, field))
		}
	}
	for j, c := range b.CombinationConstraints {
		if len(c.When) == 0 {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].combination_constraints[%d].when must be non-empty", i, j))
		}
		if len(c.Requires) == 0 && len(c.Conflicts) == 0 {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].combination_constraints[%d] must define requires or conflicts", i, j))
		}
		issues = append(issues, validateCombinationMap(fmt.Sprintf("protocol_bindings[%d].combination_constraints[%d].when", i, j), c.When)...)
		issues = append(issues, validateCombinationMap(fmt.Sprintf("protocol_bindings[%d].combination_constraints[%d].requires", i, j), c.Requires)...)
		issues = append(issues, validateCombinationMap(fmt.Sprintf("protocol_bindings[%d].combination_constraints[%d].conflicts", i, j), c.Conflicts)...)
	}
	for param, policy := range b.RequestParamPolicy {
		if strings.TrimSpace(param) == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy contains empty param", i))
		} else if !known(protocolBindingCapabilityParams, param) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s] unknown param", i, param))
		}
		if !known(protocolBindingParamPolicyCategories, policy.Category) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s].category unknown: %s", i, param, policy.Category))
		}
		if !knownOrEmpty(protocolBindingParamPolicyUpstreamBehaviors, policy.UpstreamBehavior) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s].upstream_behavior unknown: %s", i, param, policy.UpstreamBehavior))
		}
		if !known(protocolBindingParamPolicyActions, policy.OnEqual) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s].on_equal unknown action: %s", i, param, policy.OnEqual))
		}
		if !known(protocolBindingParamPolicyActions, policy.OnMismatch) {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s].on_mismatch unknown action: %s", i, param, policy.OnMismatch))
		}
		if policy.UpstreamBehavior == "fixed" && strings.TrimSpace(policy.FixedValue) == "" {
			issues = append(issues, fmt.Sprintf("protocol_bindings[%d].request_param_policy[%s].fixed_value required for fixed upstream behavior", i, param))
		}
	}
	if b.FieldDisposition != nil {
		issues = append(issues, validateFieldDispositionMap(fmt.Sprintf("protocol_bindings[%d].field_disposition.request", i), b.FieldDisposition.Request)...)
		issues = append(issues, validateFieldDispositionMap(fmt.Sprintf("protocol_bindings[%d].field_disposition.response", i), b.FieldDisposition.Response)...)
	}
	return issues
}

func known(values map[string]struct{}, value string) bool {
	_, ok := values[value]
	return ok
}

func knownOrEmpty(values map[string]struct{}, value string) bool {
	return strings.TrimSpace(value) == "" || known(values, value)
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func endpointSet(values []types.ProtocolRuntimeEndpoint) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[bindingRegistryKey(value.OutboundProtocol, value.Capability, value.RequestContract)] = struct{}{}
	}
	return out
}

func translatorMap(values []types.ProtocolTranslationContract) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[bindingRegistryKey(value.From, value.To, value.Capability, value.RequestContract)] = value.Translator
	}
	return out
}

func translatorForBridgeMap(values []types.ProtocolTranslationContract) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		if value.Bridge != "" {
			out[value.Bridge] = value.Translator
		}
	}
	return out
}

func bindingRegistryKey(parts ...string) string {
	return strings.Join(parts, "|")
}

const (
	capabilityChat                   = "chat"
	wireProfileOpenAI                = "openai"
	wireProfileAnthropic             = "anthropic"
	wireProfileGemini                = "gemini"
	wireProfileResponses             = "responses"
	wireProfileCohereV2              = "cohere_v2"
	wireProfileArk                   = "ark"
	bridgeAnthropicToOpenAIChat      = "anthropic_to_openai_chat"
	bridgeResponsesToChat            = "responses_to_chat"
	crossProtocolBridgeRequired      = "cross_protocol_bridge_required"
	crossProtocolBridgeUnknown       = "cross_protocol_bridge_unknown"
	crossProtocolBridgeKindMismatch  = "cross_protocol_bridge_kind_mismatch"
	requestContractAnthropicMessages = "anthropic_messages"
	requestContractResponses         = "responses"
)

func bridgeRejectReason(bridge, inboundProtocol, outboundProtocol, capability, requestContract string) string {
	if bridge != "" && !known(protocolBindingBridges, bridge) {
		return crossProtocolBridgeUnknown
	}
	if capability != capabilityChat {
		if bridge != "" {
			return crossProtocolBridgeKindMismatch
		}
		return ""
	}
	if sameWireProtocol(inboundProtocol, outboundProtocol) {
		if bridge != "" {
			return crossProtocolBridgeKindMismatch
		}
		return ""
	}
	if bridge == "" {
		return crossProtocolBridgeRequired
	}
	if !bridgeMatchesTuple(bridge, inboundProtocol, outboundProtocol, capability, requestContract) {
		return crossProtocolBridgeKindMismatch
	}
	return ""
}

func bridgeMatchesTuple(bridge, inboundProtocol, outboundProtocol, capability, requestContract string) bool {
	if capability != capabilityChat {
		return false
	}
	switch bridge {
	case bridgeAnthropicToOpenAIChat:
		return inboundProtocol == wireProfileAnthropic &&
			requestContract == requestContractAnthropicMessages &&
			wireProfileForProtocol(outboundProtocol) == wireProfileOpenAI
	case bridgeResponsesToChat:
		return inboundProtocol == wireProfileResponses &&
			requestContract == requestContractResponses &&
			outboundProtocol == wireProfileOpenAI
	default:
		return false
	}
}

func sameWireProtocol(inboundProtocol, outboundProtocol string) bool {
	inboundProfile := wireProfileForProtocol(inboundProtocol)
	if inboundProfile == "" {
		return false
	}
	return inboundProfile == wireProfileForProtocol(outboundProtocol)
}

// wireProfileForProtocol mirrors gateway internal/bindingspec/protocol_behavior.go
// WireProfileForProtocol so publisher validation fails closed before publish.
func wireProfileForProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "glm", "glm_coding", "doubao_ark", "azure_openai":
		return wireProfileOpenAI
	case "anthropic":
		return wireProfileAnthropic
	case "gemini":
		return wireProfileGemini
	case "responses", "openai_responses":
		return wireProfileResponses
	case "cohere_v2":
		return wireProfileCohereV2
	case "ark":
		return wireProfileArk
	default:
		return ""
	}
}

func appendNonBlankListIssues(issues []string, path string, values []string) []string {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			issues = append(issues, fmt.Sprintf("%s[%d] must be non-blank", path, i))
		}
	}
	return issues
}

func appendKnownListIssues(issues []string, path string, values []string, knownValues map[string]struct{}, kind string) []string {
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			issues = append(issues, fmt.Sprintf("%s[%d] must be non-blank", path, i))
			continue
		}
		if !known(knownValues, value) {
			issues = append(issues, fmt.Sprintf("%s[%d] unknown %s: %s", path, i, kind, value))
		}
	}
	return issues
}

func validateCombinationMap(path string, values map[string]string) []string {
	var issues []string
	for field, value := range values {
		if strings.TrimSpace(field) == "" || strings.TrimSpace(value) == "" {
			issues = append(issues, fmt.Sprintf("%s contains empty field/value", path))
			continue
		}
		if !combinationFieldKnown(field) {
			issues = append(issues, fmt.Sprintf("%s[%s] unknown field", path, field))
		}
	}
	return issues
}

func combinationFieldKnown(field string) bool {
	if known(protocolBindingCapabilityParams, field) {
		return true
	}
	if idx := strings.IndexByte(field, '.'); idx > 0 {
		return known(protocolBindingCapabilityParams, field[:idx])
	}
	return false
}

func validateFieldDispositionMap(path string, values map[string]string) []string {
	var issues []string
	for field, action := range values {
		if strings.TrimSpace(field) == "" || strings.TrimSpace(action) == "" {
			issues = append(issues, fmt.Sprintf("%s contains empty field/action", path))
			continue
		}
		if !known(protocolBindingFieldDispositionActions, action) {
			issues = append(issues, fmt.Sprintf("%s[%s] unknown action: %s", path, field, action))
		}
	}
	return issues
}
