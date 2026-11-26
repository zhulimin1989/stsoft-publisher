package types

import (
	"fmt"
	"reflect"
	"strings"
)

// ---------------------------------------------------------------------------
// ADR-0016 U2: explicit paths[] compilation.
//
// Each declared path = (inbound_protocol, channel). The admin's declaration is
// authoritative: every declared path is compiled into a structurally valid
// binding and published, even when the combination is physically impossible
// (channel entry/exit dialect vs endpoint dialect mismatch). Physically
// impossible combinations only produce warnings (zero-blocking, 2026-06-30
// revision); the path then fails at runtime, visibly, for the admin to fix.
//
// For physically consistent paths the output is byte-identical to the
// served_inbound_protocols compilation (CompileInboundProtocolBindings): the
// same per-profile builders are reused, so ADR-0016 changes the input shape
// only, never the runtime artifact.
// ---------------------------------------------------------------------------

// ProtocolPathWarning is a compile warning attached to one declared path.
type ProtocolPathWarning struct {
	// Code is ProtocolPathWarningInconsistent (compiled as declared, will fail
	// at runtime) or ProtocolPathWarningUncompilable (no structurally valid
	// binding can be constructed; path skipped).
	Code    string
	Path    ProtocolPath
	Message string
}

const (
	ProtocolPathWarningInconsistent = "protocol_path_physically_inconsistent"
	ProtocolPathWarningUncompilable = "protocol_path_uncompilable"
)

// CompileExplicitPathBindings compiles explicit path declarations plus the
// orthogonal capability axis (ADR-0009 S2b, unchanged) into protocol_bindings.
// Returns the bindings, the default binding name (upstream passthrough when a
// consistent one is declared, else first sorted name), and per-path warnings.
func CompileExplicitPathBindings(profile string, paths []ProtocolPath, capabilities []string, opts ClientProtocolBindingOptions) ([]ProtocolBinding, string, []ProtocolPathWarning, error) {
	if profile == "" {
		profile = ProviderProfileOpenAICompatible
	}

	bindingsByName := map[string]ProtocolBinding{}
	var warnings []ProtocolPathWarning
	passthroughDefault := ""
	addBinding := func(binding ProtocolBinding) error {
		if existing, ok := bindingsByName[binding.Name]; ok {
			if !reflect.DeepEqual(existing, binding) {
				return fmt.Errorf("paths produce conflicting binding %s", binding.Name)
			}
		}
		bindingsByName[binding.Name] = binding
		return nil
	}

	// Wire-protocol axis: one binding per declared path. Mirrors the chat-axis
	// gating of CompileInboundProtocolBindings (paths describe chat/wire lines).
	// Unlike the served list — which is silently ignored by capability-only
	// exposures — a declared path that gets dropped is surfaced as a warning:
	// explicit declarations are never silently discarded (ADR-0016).
	if !capabilitiesIncludeChat(capabilities) {
		for _, path := range paths {
			warnings = append(warnings, ProtocolPathWarning{
				Code: ProtocolPathWarningUncompilable,
				Path: path,
				Message: fmt.Sprintf(
					"declared path (inbound=%s, channel=%s) on provider_profile %s: capabilities (%s) exclude chat, so wire paths are not compiled; path skipped",
					path.InboundProtocol, path.Channel, profile, strings.Join(capabilities, ", ")),
			})
		}
	}
	if capabilitiesIncludeChat(capabilities) && len(paths) > 0 {
		upstream, upstreamErr := upstreamDialectForProfile(profile)
		for _, path := range paths {
			if upstreamErr != nil {
				// Profile has no chat wire dialect at all (e.g. cohere_v2): no
				// structurally valid chat binding exists. Warn + skip, never block.
				warnings = append(warnings, ProtocolPathWarning{
					Code: ProtocolPathWarningUncompilable,
					Path: path,
					Message: fmt.Sprintf(
						"declared path (inbound=%s, channel=%s) on provider_profile %s cannot be compiled: %v; path skipped",
						path.InboundProtocol, path.Channel, profile, upstreamErr),
				})
				continue
			}
			binding, isUpstreamPassthrough, warning, ok := explicitPathBinding(profile, upstream, path, opts)
			if warning != nil {
				warnings = append(warnings, *warning)
			}
			if !ok {
				continue
			}
			if err := addBinding(binding); err != nil {
				return nil, "", nil, err
			}
			if isUpstreamPassthrough && passthroughDefault == "" {
				passthroughDefault = binding.Name
			}
		}
	}

	// Non-chat capability axis: identical to CompileInboundProtocolBindings
	// (same-dialect upstream capability builders; unsupported capability stays a
	// hard error — unchanged S2b behavior).
	for _, capability := range capabilities {
		if capability == "" || capability == CapabilityChat {
			continue
		}
		binding, err := upstreamCapabilityBinding(profile, capability, opts)
		if err != nil {
			return nil, "", nil, err
		}
		if err := addBinding(binding); err != nil {
			return nil, "", nil, err
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
	return out, defaultBinding, warnings, nil
}

// explicitPathBinding compiles one declared path. For physically consistent
// declarations it returns the exact builder output the served compilation uses
// (byte-identical). For inconsistent declarations it returns a structurally
// valid binding assembled from the channel's canonical builder with the
// declared inbound dialect stamped on Accepts, plus a warning stating the
// facts triple (inbound protocol × channel entry/exit dialect × endpoint
// dialect). The bool result reports whether a binding was produced.
func explicitPathBinding(profile, upstream string, path ProtocolPath, opts ClientProtocolBindingOptions) (ProtocolBinding, bool, *ProtocolPathWarning, bool) {
	switch path.Channel {
	case DedicatedProtocolChannelPassthrough:
		base, err := upstreamPassthroughBinding(profile, opts)
		if err != nil {
			return ProtocolBinding{}, false, uncompilablePathWarning(path, profile, err), false
		}
		if path.InboundProtocol == upstream {
			return base, true, nil, true
		}
		declared, ok := stampDeclaredInbound(base, path)
		if !ok {
			return ProtocolBinding{}, false, unknownInboundWarning(path, profile), false
		}
		warning := inconsistentPathWarning(path, profile, fmt.Sprintf(
			"channel passthrough does not translate (exit dialect == inbound dialect %s), but the endpoint speaks %s wire",
			path.InboundProtocol, upstream))
		return declared, false, warning, true
	case DedicatedProtocolChannelResponsesToChat:
		return explicitBridgePathBinding(profile, upstream, path, opts, "responses-to-openai-chat", InboundProtocolResponses)
	case DedicatedProtocolChannelAnthropicToChat:
		return explicitBridgePathBinding(profile, upstream, path, opts, "anthropic-messages-to-openai", InboundProtocolAnthropic)
	default:
		return ProtocolBinding{}, false, &ProtocolPathWarning{
			Code: ProtocolPathWarningUncompilable,
			Path: path,
			Message: fmt.Sprintf(
				"declared path (inbound=%s, channel=%s) on provider_profile %s: channel is not a registered channel (known: %s); path skipped",
				path.InboundProtocol, path.Channel, profile, strings.Join(KnownDedicatedProtocolChannels(), ", ")),
		}, false
	}
}

// explicitBridgePathBinding handles the two registered bridge channels. Both
// bridges exit openai-chat wire, so the declaration is physically consistent
// only when the endpoint speaks openai wire and the inbound matches the bridge
// entry dialect. The bridge builder is the canonical openai-family one — the
// exact builder inboundDialectBinding uses on the served path.
func explicitBridgePathBinding(profile, upstream string, path ProtocolPath, opts ClientProtocolBindingOptions, builderName, entryDialect string) (ProtocolBinding, bool, *ProtocolPathWarning, bool) {
	pathPrefix := firstNonEmpty(opts.PathPrefix, defaultOpenAICompatiblePathPrefix)
	base := openAICompatibleBindingByName(builderName, pathPrefix)

	binding := base
	if path.InboundProtocol != entryDialect {
		declared, ok := stampDeclaredInbound(base, path)
		if !ok {
			return ProtocolBinding{}, false, unknownInboundWarning(path, profile), false
		}
		binding = declared
	}
	if path.InboundProtocol == entryDialect && upstream == InboundProtocolOpenAI {
		return binding, false, nil, true
	}

	var facts []string
	if path.InboundProtocol != entryDialect {
		facts = append(facts, fmt.Sprintf("channel %s enters %s wire, but the declared inbound is %s", path.Channel, entryDialect, path.InboundProtocol))
	}
	if upstream != InboundProtocolOpenAI {
		facts = append(facts, fmt.Sprintf("channel %s exits %s wire, but the endpoint speaks %s wire", path.Channel, InboundProtocolOpenAI, upstream))
	}
	return binding, false, inconsistentPathWarning(path, profile, strings.Join(facts, "; ")), true
}

// stampDeclaredInbound rewrites the canonical builder output to accept the
// declared inbound dialect instead, under a deterministic declared-* name so
// it can never collide with a legitimately compiled canonical binding on the
// same provider.
func stampDeclaredInbound(base ProtocolBinding, path ProtocolPath) (ProtocolBinding, bool) {
	contract, ok := chatRequestContractForDialect(path.InboundProtocol)
	if !ok {
		return ProtocolBinding{}, false
	}
	base.Name = fmt.Sprintf("declared-%s-%s", path.InboundProtocol, strings.ReplaceAll(path.Channel, "_", "-"))
	base.Accepts = []ProtocolBindingAccept{{
		Protocol:        path.InboundProtocol,
		Capabilities:    []string{CapabilityChat},
		RequestContract: contract,
	}}
	return base, true
}

// chatRequestContractForDialect maps a pure inbound Wire dialect to its chat
// request contract (the same contracts the canonical builders use).
func chatRequestContractForDialect(dialect string) (string, bool) {
	switch dialect {
	case InboundProtocolOpenAI:
		return "chat_completions", true
	case InboundProtocolAnthropic:
		return "anthropic_messages", true
	case InboundProtocolResponses:
		return "responses", true
	case InboundProtocolGemini:
		return "gemini_generate", true
	default:
		return "", false
	}
}

func inconsistentPathWarning(path ProtocolPath, profile, facts string) *ProtocolPathWarning {
	return &ProtocolPathWarning{
		Code: ProtocolPathWarningInconsistent,
		Path: path,
		Message: fmt.Sprintf(
			"declared path (inbound=%s, channel=%s) on provider_profile %s is physically inconsistent: %s; compiled as declared, expect runtime failures on this path",
			path.InboundProtocol, path.Channel, profile, facts),
	}
}

func uncompilablePathWarning(path ProtocolPath, profile string, err error) *ProtocolPathWarning {
	return &ProtocolPathWarning{
		Code: ProtocolPathWarningUncompilable,
		Path: path,
		Message: fmt.Sprintf(
			"declared path (inbound=%s, channel=%s) on provider_profile %s cannot be compiled: %v; path skipped",
			path.InboundProtocol, path.Channel, profile, err),
	}
}

func unknownInboundWarning(path ProtocolPath, profile string) *ProtocolPathWarning {
	return &ProtocolPathWarning{
		Code: ProtocolPathWarningUncompilable,
		Path: path,
		Message: fmt.Sprintf(
			"declared path (inbound=%s, channel=%s) on provider_profile %s: inbound is not a known Wire dialect (known: %s); path skipped",
			path.InboundProtocol, path.Channel, profile, strings.Join(KnownServedInboundProtocols(), ", ")),
	}
}
