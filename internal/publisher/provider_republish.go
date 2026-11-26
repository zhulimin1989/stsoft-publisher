package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/tokenplatform/publisher/internal/types"
)

type ProviderRepublishPlan struct {
	ProviderID               string             `json:"provider_id"`
	Changed                  bool               `json:"changed"`
	NeedsManualIntervention  bool               `json:"needs_manual_intervention,omitempty"`
	ManualInterventionReason string             `json:"manual_intervention_reason,omitempty"`
	ValidationIssue          string             `json:"validation_issue,omitempty"`
	DiffSummary              []string           `json:"diff_summary"`
	BeforeBindingNames       []string           `json:"before_binding_names"`
	AfterBindingNames        []string           `json:"after_binding_names"`
	After                    types.ProviderInfo `json:"-"`
	validationIssueText      string
}

func PlanProviderRepublishAll(rawProviders map[string]string) ([]ProviderRepublishPlan, error) {
	ids := make([]string, 0, len(rawProviders))
	for id := range rawProviders {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	plans := make([]ProviderRepublishPlan, 0, len(ids))
	for _, id := range ids {
		plan, err := PlanProviderRepublish(id, []byte(rawProviders[id]))
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func PlanProviderRepublish(providerID string, raw []byte) (ProviderRepublishPlan, error) {
	var before types.ProviderInfo
	if err := json.Unmarshal(raw, &before); err != nil {
		return ProviderRepublishPlan{}, fmt.Errorf("provider %s: decode provider payload: %w", providerID, err)
	}
	if strings.TrimSpace(before.ID) == "" {
		before.ID = providerID
	}

	after := before
	rewrite, err := recompileProviderBindings(before.ProtocolBindings, before.DefaultProtocolBinding)
	if err != nil {
		return ProviderRepublishPlan{}, fmt.Errorf("provider %s: %w", providerID, err)
	}
	if rewrite.changed {
		after.ProtocolBindings = rewrite.bindings
		after.DefaultProtocolBinding = rewrite.defaultBinding
	}

	plan := ProviderRepublishPlan{
		ProviderID:               providerID,
		Changed:                  rewrite.changed,
		NeedsManualIntervention:  rewrite.needsManualIntervention,
		ManualInterventionReason: rewrite.manualInterventionReason,
		DiffSummary:              rewrite.summary,
		BeforeBindingNames:       bindingNames(before.ProtocolBindings),
		AfterBindingNames:        bindingNames(after.ProtocolBindings),
		After:                    after,
	}
	if rewrite.changed || rewrite.needsManualIntervention {
		if err := validateProviderInfo(&after); err != nil {
			if rewrite.needsManualIntervention {
				plan.ValidationIssue = err.Error()
				plan.validationIssueText = err.Error()
			} else {
				return ProviderRepublishPlan{}, fmt.Errorf("provider %s: republished provider invalid: %w", providerID, err)
			}
		}
	}
	return plan, nil
}

func ApplyProviderRepublishPlan(ctx context.Context, pub Publisher, plan ProviderRepublishPlan) (types.BatchID, error) {
	if !plan.Changed {
		return types.BatchID{}, nil
	}
	if plan.ValidationIssue != "" {
		return types.BatchID{}, fmt.Errorf("provider %s republish plan invalid: %s", plan.ProviderID, plan.ValidationIssue)
	}
	if plan.NeedsManualIntervention {
		return types.BatchID{}, fmt.Errorf("provider %s requires manual intervention: %s", plan.ProviderID, plan.ManualInterventionReason)
	}
	return pub.PublishProvider(ctx, plan.ProviderID, &plan.After)
}

type providerBindingRewrite struct {
	bindings                 []types.ProtocolBinding
	defaultBinding           string
	summary                  []string
	changed                  bool
	needsManualIntervention  bool
	manualInterventionReason string
}

func recompileProviderBindings(bindings []types.ProtocolBinding, defaultBinding string) (providerBindingRewrite, error) {
	if len(bindings) == 0 {
		return providerBindingRewrite{bindings: bindings, defaultBinding: defaultBinding}, nil
	}
	// G4 P2 wire purity: rewrite legacy vendor-protocol bindings
	// (outbound_protocol glm/glm_coding/doubao_ark/azure_openai + vendor
	// accepts rows) to the pure-dialect shape before any set-level recompile.
	// This is a targeted per-binding rewrite — names, path_prefix, auth,
	// priority, capability config and default binding are all untouched
	// (design §4 invariant checklist; CB scope-key red line D3 means base_url
	// is never derived from this code path at all).
	bindings, vendorSummary, vendorChanged := rewriteVendorProtocolBindings(bindings)

	if allBindingNamesKnown(bindings, openAICompatibleBindingNames) {
		rewrite, err := recompileOpenAICompatibleBindings(bindings, defaultBinding)
		if err != nil {
			return providerBindingRewrite{}, err
		}
		rewrite.summary = append(vendorSummary, rewrite.summary...)
		rewrite.changed = rewrite.changed || vendorChanged
		return rewrite, nil
	}
	if allBindingNamesKnown(bindings, geminiNativeBindingNames) {
		rewrite, err := recompileGeminiNativeBindings(bindings, defaultBinding)
		if err != nil {
			return providerBindingRewrite{}, err
		}
		rewrite.summary = append(vendorSummary, rewrite.summary...)
		rewrite.changed = rewrite.changed || vendorChanged
		return rewrite, nil
	}
	nextBindings, nextDefault, summary, changed, err := patchKnownBridgeBindings(bindings, defaultBinding)
	if err != nil {
		return providerBindingRewrite{}, err
	}
	return providerBindingRewrite{
		bindings:       nextBindings,
		defaultBinding: nextDefault,
		summary:        append(vendorSummary, summary...),
		changed:        changed || vendorChanged,
	}, nil
}

// vendorOutboundCompat maps a retired vendor outbound_protocol value to the
// compat_api_type that selects the vendor adapter bundle on the gateway
// (P0 AdapterBundle mechanism). Stamping it when missing is the R2 guard:
// without it an outbound=openai binding would resolve the plain openai bundle
// and silently lose zhipu thinking/tool_stream injection.
var vendorOutboundCompat = map[string]string{
	"glm":          "glm",
	"glm_coding":   "glm_coding",
	"doubao_ark":   "doubao",
	"azure_openai": "azure",
}

// vendorInboundProtocols are the retired vendor accepts[].protocol values.
// They are all openai-wire (gateway WireProfileForProtocol folds them to
// openai), so the pure replacement row is protocol=openai.
var vendorInboundProtocols = map[string]struct{}{
	"glm":          {},
	"glm_coding":   {},
	"doubao_ark":   {},
	"azure_openai": {},
}

// rewriteVendorProtocolBindings rewrites one provider's legacy vendor-protocol
// bindings to the pure-dialect shape (G4 P2):
//
//   - outbound_protocol vendor value → "openai"
//   - compat_api_type stamped with the vendor bundle selector when missing
//   - azure_openai additionally gains the api-version query_template that the
//     gateway used to apply at the protocol level
//   - accepts[] vendor rows are dropped when an equivalent pure row already
//     exists on the same binding, otherwise rewritten to protocol=openai
//
// Runtime behavior of old vs new shape is equivalent under the gateway P1
// read-side normalization (both shapes resolve the same adapter bundle); this
// rewrite is what lets legacy_vendor_protocol_normalized_total reach zero.
func rewriteVendorProtocolBindings(bindings []types.ProtocolBinding) ([]types.ProtocolBinding, []string, bool) {
	out := make([]types.ProtocolBinding, len(bindings))
	copy(out, bindings)
	var summary []string
	changed := false
	for i := range out {
		compat, isVendor := vendorOutboundCompat[strings.TrimSpace(out[i].OutboundProtocol)]
		if isVendor {
			summary = append(summary, fmt.Sprintf("set outbound_protocol %s=openai (was %s)", out[i].Name, out[i].OutboundProtocol))
			out[i].OutboundProtocol = "openai"
			changed = true
			if strings.TrimSpace(out[i].CompatAPIType) == "" {
				summary = append(summary, fmt.Sprintf("set compat_api_type %s=%s (adapter-bundle selector)", out[i].Name, compat))
				out[i].CompatAPIType = compat
			}
			if compat == "azure" && strings.TrimSpace(out[i].QueryTemplate) == "" {
				summary = append(summary, fmt.Sprintf("set query_template %s=%s", out[i].Name, types.AzureOpenAIQueryTemplate))
				out[i].QueryTemplate = types.AzureOpenAIQueryTemplate
			}
		}
		accepts, acceptSummary, acceptsChanged := purifyVendorAccepts(out[i].Name, out[i].Accepts)
		if acceptsChanged {
			out[i].Accepts = accepts
			summary = append(summary, acceptSummary...)
			changed = true
		}
	}
	return out, summary, changed
}

// purifyVendorAccepts drops vendor accepts rows that duplicate an existing
// pure row and rewrites vendor-only rows to protocol=openai.
func purifyVendorAccepts(bindingName string, accepts []types.ProtocolBindingAccept) ([]types.ProtocolBindingAccept, []string, bool) {
	hasPureTwin := func(vendor types.ProtocolBindingAccept) bool {
		for _, other := range accepts {
			if other.Protocol != "openai" {
				continue
			}
			pure := vendor
			pure.Protocol = "openai"
			if reflect.DeepEqual(other, pure) {
				return true
			}
		}
		return false
	}

	out := make([]types.ProtocolBindingAccept, 0, len(accepts))
	var summary []string
	changed := false
	for _, accept := range accepts {
		if _, isVendor := vendorInboundProtocols[strings.TrimSpace(accept.Protocol)]; !isVendor {
			out = append(out, accept)
			continue
		}
		changed = true
		if hasPureTwin(accept) {
			summary = append(summary, fmt.Sprintf("remove accepts protocol=%s from %s (pure openai row already present)", accept.Protocol, bindingName))
			continue
		}
		summary = append(summary, fmt.Sprintf("set accepts protocol %s: %s -> openai", bindingName, accept.Protocol))
		accept.Protocol = "openai"
		out = append(out, accept)
	}
	if !changed {
		return accepts, nil, false
	}
	return out, summary, true
}

var openAICompatibleBindingNames = map[string]struct{}{
	"anthropic-messages-to-openai": {},
	"gemini-generate-to-openai":    {},
	"openai-chat":                  {},
	"openai-embeddings":            {},
	"responses-to-openai-chat":     {},
}

var geminiNativeBindingNames = map[string]struct{}{
	"gemini-generate":       {},
	"openai-chat-to-gemini": {},
	"responses-to-gemini":   {},
}

func recompileOpenAICompatibleBindings(bindings []types.ProtocolBinding, defaultBinding string) (providerBindingRewrite, error) {
	names := bindingNameSet(bindings)
	clients, ok := inferOpenAICompatibleClients(names)
	if !ok {
		nextBindings, nextDefault, summary, changed, err := patchKnownBridgeBindings(bindings, defaultBinding)
		if err != nil {
			return providerBindingRewrite{}, err
		}
		reason := "unable to map openai_compatible binding set: " + strings.Join(sortedMapKeys(names), ",")
		summary = append(summary, "needs manual intervention: "+reason)
		return providerBindingRewrite{
			bindings:                 nextBindings,
			defaultBinding:           nextDefault,
			summary:                  summary,
			changed:                  changed,
			needsManualIntervention:  true,
			manualInterventionReason: reason,
		}, nil
	}
	pathPrefix := firstBindingPathPrefix(bindings)
	next, nextDefault, err := types.CompileClientProtocolBindingsWithOptions(
		types.ProviderProfileOpenAICompatible,
		clients,
		types.ClientProtocolBindingOptions{PathPrefix: pathPrefix},
	)
	if err != nil {
		return providerBindingRewrite{}, err
	}
	if defaultBinding != "" {
		nextDefault = normalizeDefaultBinding(defaultBinding, next, nextDefault)
	}

	summary := diffBindingSet(bindings, next)
	changed := len(summary) > 0 || defaultBinding != nextDefault
	if defaultBinding != nextDefault {
		summary = append(summary, fmt.Sprintf("default_protocol_binding: %s -> %s", defaultBinding, nextDefault))
	}
	return providerBindingRewrite{bindings: next, defaultBinding: nextDefault, summary: summary, changed: changed}, nil
}

func recompileGeminiNativeBindings(bindings []types.ProtocolBinding, defaultBinding string) (providerBindingRewrite, error) {
	pathPrefix := firstBindingPathPrefix(bindings)
	next, nextDefault, err := types.CompileClientProtocolBindingsWithOptions(
		types.ProviderProfileGeminiNative,
		[]string{types.ClientCompatibilityAll},
		types.ClientProtocolBindingOptions{PathPrefix: pathPrefix},
	)
	if err != nil {
		return providerBindingRewrite{}, err
	}
	if defaultBinding != "" {
		nextDefault = normalizeDefaultBinding(defaultBinding, next, nextDefault)
	}
	summary := diffBindingSet(bindings, next)
	changed := len(summary) > 0 || defaultBinding != nextDefault
	if defaultBinding != nextDefault {
		summary = append(summary, fmt.Sprintf("default_protocol_binding: %s -> %s", defaultBinding, nextDefault))
	}
	return providerBindingRewrite{bindings: next, defaultBinding: nextDefault, summary: summary, changed: changed}, nil
}

func patchKnownBridgeBindings(bindings []types.ProtocolBinding, defaultBinding string) ([]types.ProtocolBinding, string, []string, bool, error) {
	out := make([]types.ProtocolBinding, 0, len(bindings))
	var summary []string
	changed := false
	for _, binding := range bindings {
		switch binding.Name {
		case "gemini-generate-to-openai", "openai-chat-to-gemini", "responses-to-gemini":
			summary = append(summary, "remove binding "+binding.Name)
			changed = true
			continue
		case "anthropic-messages-to-openai":
			if binding.Bridge != "anthropic_to_openai_chat" {
				summary = append(summary, "set bridge anthropic-messages-to-openai=anthropic_to_openai_chat")
				binding.Bridge = "anthropic_to_openai_chat"
				changed = true
			}
		case "responses-to-openai-chat":
			if binding.Bridge != "responses_to_chat" {
				summary = append(summary, "set bridge responses-to-openai-chat=responses_to_chat")
				binding.Bridge = "responses_to_chat"
				changed = true
			}
		}
		out = append(out, binding)
	}
	nextDefault := normalizeDefaultBinding(defaultBinding, out, defaultBinding)
	if nextDefault != defaultBinding {
		summary = append(summary, fmt.Sprintf("default_protocol_binding: %s -> %s", defaultBinding, nextDefault))
		changed = true
	}
	return out, nextDefault, summary, changed, nil
}

func inferOpenAICompatibleClients(names map[string]struct{}) ([]string, bool) {
	switch {
	case bindingNameSetEqual(names, "openai-chat", "openai-embeddings", "anthropic-messages-to-openai", "responses-to-openai-chat", "gemini-generate-to-openai"):
		return []string{types.ClientCompatibilityAll}, true
	case bindingNameSetEqual(names, "openai-chat", "openai-embeddings", "anthropic-messages-to-openai", "responses-to-openai-chat"):
		return []string{types.ClientCompatibilityAll}, true
	case bindingNameSetEqual(names, "anthropic-messages-to-openai", "openai-chat"):
		return []string{types.ClientCompatibilityOpenClaw}, true
	case bindingNameSetEqual(names, "anthropic-messages-to-openai"):
		return []string{types.ClientCompatibilityClaudeCode}, true
	case bindingNameSetEqual(names, "responses-to-openai-chat"):
		return []string{types.ClientCompatibilityCodex}, true
	case bindingNameSetEqual(names, "openai-chat", "openai-embeddings"):
		return []string{types.ClientCompatibilityOpenAISDK}, true
	case bindingNameSetEqual(names, "openai-chat"):
		return []string{types.ClientCompatibilityCursor}, true
	default:
		return nil, false
	}
}

func bindingNameSetEqual(names map[string]struct{}, want ...string) bool {
	if len(names) != len(want) {
		return false
	}
	for _, name := range want {
		if !hasBindingName(names, name) {
			return false
		}
	}
	return true
}

func diffBindingSet(before, after []types.ProtocolBinding) []string {
	beforeByName := bindingMap(before)
	afterByName := bindingMap(after)
	var summary []string
	for _, name := range sortedMapKeys(beforeByName) {
		if _, ok := afterByName[name]; !ok {
			summary = append(summary, "remove binding "+name)
		}
	}
	for _, name := range sortedMapKeys(afterByName) {
		beforeBinding, ok := beforeByName[name]
		if !ok {
			summary = append(summary, "add binding "+name)
			continue
		}
		if beforeBinding.Bridge != afterByName[name].Bridge {
			summary = append(summary, fmt.Sprintf("set bridge %s=%s", name, afterByName[name].Bridge))
		}
		if beforeBinding.TranslatorKey != afterByName[name].TranslatorKey {
			summary = append(summary, fmt.Sprintf("set translator %s=%s", name, afterByName[name].TranslatorKey))
		}
	}
	return summary
}

func allBindingNamesKnown(bindings []types.ProtocolBinding, known map[string]struct{}) bool {
	for _, binding := range bindings {
		if _, ok := known[binding.Name]; !ok {
			return false
		}
	}
	return true
}

func bindingNameSet(bindings []types.ProtocolBinding) map[string]struct{} {
	out := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		out[binding.Name] = struct{}{}
	}
	return out
}

func bindingMap(bindings []types.ProtocolBinding) map[string]types.ProtocolBinding {
	out := make(map[string]types.ProtocolBinding, len(bindings))
	for _, binding := range bindings {
		out[binding.Name] = binding
	}
	return out
}

func bindingNames(bindings []types.ProtocolBinding) []string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	sort.Strings(names)
	return names
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func hasBindingName(names map[string]struct{}, name string) bool {
	_, ok := names[name]
	return ok
}

func firstBindingPathPrefix(bindings []types.ProtocolBinding) string {
	for _, binding := range bindings {
		if binding.PathPrefix != "" {
			return binding.PathPrefix
		}
	}
	return ""
}

func normalizeDefaultBinding(current string, bindings []types.ProtocolBinding, fallback string) string {
	if current != "" {
		for _, binding := range bindings {
			if binding.Name == current {
				return current
			}
		}
	}
	if fallback != "" {
		for _, binding := range bindings {
			if binding.Name == fallback {
				return fallback
			}
		}
	}
	if len(bindings) > 0 {
		return bindings[0].Name
	}
	return ""
}
