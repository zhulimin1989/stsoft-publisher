package intent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// glmCodingPathsIntent is glmCodingExplicitIntent with every exposure migrated
// to explicit paths[] (served_inbound_protocols kept, per transition contract,
// and expected to be ignored).
func glmCodingPathsIntent() RuntimeIntent {
	in := glmCodingExplicitIntent()
	in.ClientExposures[0].Paths = []types.ProtocolPath{
		{InboundProtocol: types.InboundProtocolOpenAI, Channel: types.DedicatedProtocolChannelPassthrough},
		{InboundProtocol: types.InboundProtocolResponses, Channel: types.DedicatedProtocolChannelResponsesToChat},
	}
	in.ClientExposures[1].Paths = []types.ProtocolPath{
		{InboundProtocol: types.InboundProtocolAnthropic, Channel: types.DedicatedProtocolChannelPassthrough},
	}
	return in
}

// ADR-0016 U2 equivalence lock at the whole-intent level: a paths[] intent
// must compile to a RuntimeConfig deeply equal to its served_inbound_protocols
// equivalent (bindings, defaults, derived views, key pools, routes — all of it).
func TestCompile_PathsIntentEqualsServedIntent(t *testing.T) {
	servedOut, err := Compile(glmCodingExplicitIntent())
	if err != nil {
		t.Fatalf("served compile: %v", err)
	}
	pathsOut, err := Compile(glmCodingPathsIntent())
	if err != nil {
		t.Fatalf("paths compile: %v", err)
	}
	if !reflect.DeepEqual(servedOut, pathsOut) {
		t.Fatalf("paths intent drifted from served intent\nserved=%#v\npaths=%#v", servedOut, pathsOut)
	}
	if len(pathsOut.Warnings) != 0 {
		t.Fatalf("consistent paths must not warn: %#v", pathsOut.Warnings)
	}
}

// paths wins over served_inbound_protocols when both are present: served
// declares protocols that paths does not, and they must NOT be compiled.
func TestCompile_PathsTakePrecedenceOverServed(t *testing.T) {
	in := glmCodingPathsIntent()
	// served on the openai endpoint claims anthropic too; paths does not.
	in.ClientExposures[0].ServedInboundProtocols = []string{
		types.InboundProtocolOpenAI, types.InboundProtocolResponses, types.InboundProtocolAnthropic,
	}
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	p, ok := providerByID(out, "pc_openai")
	if !ok {
		t.Fatalf("pc_openai missing")
	}
	want := []string{"glm-coding-chat", "responses-to-openai-chat"}
	if !reflect.DeepEqual(bindingNamesOf(p.ProtocolBindings), want) {
		t.Fatalf("served must be ignored when paths present: bindings=%v want %v", bindingNamesOf(p.ProtocolBindings), want)
	}
}

// A paths-only exposure (no served_inbound_protocols) compiles: paths is the
// compile input; served is only kept for transition-period producers.
func TestCompile_PathsOnlyExposureCompiles(t *testing.T) {
	in := glmCodingPathsIntent()
	in.ClientExposures[0].ServedInboundProtocols = nil
	in.ClientExposures[1].ServedInboundProtocols = nil
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Providers) != 2 {
		t.Fatalf("providers=%d want 2", len(out.Providers))
	}
}

// Without paths, an empty served_inbound_protocols still errors (unchanged
// ADR-0012 S4 behavior).
func TestCompile_NoPathsStillRequiresServed(t *testing.T) {
	in := glmCodingExplicitIntent()
	in.ClientExposures[0].ServedInboundProtocols = nil
	if _, err := Compile(in); err == nil {
		t.Fatalf("expected error when neither paths nor served_inbound_protocols present")
	}
}

// ADR-0016 zero-blocking: a physically impossible path compiles + publishes,
// surfacing a warning on the compile output (which the publish response and
// logs carry), stating the (inbound, channel, endpoint dialect) facts.
func TestCompile_InconsistentPathWarnsAndStillPublishes(t *testing.T) {
	in := glmCodingPathsIntent()
	// responses_to_chat exits openai wire; the pc_anthropic endpoint speaks
	// anthropic wire. Physically impossible — but declared, so compiled.
	in.ClientExposures[1].Paths = append(in.ClientExposures[1].Paths,
		types.ProtocolPath{InboundProtocol: types.InboundProtocolResponses, Channel: types.DedicatedProtocolChannelResponsesToChat})
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("zero-blocking violated, Compile errored: %v", err)
	}
	var warning *CompileWarning
	for i := range out.Warnings {
		if out.Warnings[i].Code == "protocol_path_physically_inconsistent" {
			warning = &out.Warnings[i]
		}
	}
	if warning == nil {
		t.Fatalf("missing protocol_path_physically_inconsistent warning: %#v", out.Warnings)
	}
	if !reflect.DeepEqual(warning.Providers, []string{"pc_anthropic"}) {
		t.Fatalf("warning providers=%v", warning.Providers)
	}
	for key, want := range map[string]string{
		"inbound_protocol": "responses",
		"channel":          "responses_to_chat",
		"provider_profile": types.ProviderProfileAnthropicNative,
	} {
		if warning.Meta[key] != want {
			t.Fatalf("warning meta[%s]=%q want %q (meta=%#v)", key, warning.Meta[key], want, warning.Meta)
		}
	}
	// Compiled as declared: the endpoint carries both its healthy passthrough
	// and the declared-but-broken bridge binding.
	p, ok := providerByID(out, "pc_anthropic")
	if !ok {
		t.Fatalf("pc_anthropic missing")
	}
	want := []string{"anthropic-messages", "responses-to-openai-chat"}
	if !reflect.DeepEqual(bindingNamesOf(p.ProtocolBindings), want) {
		t.Fatalf("bindings=%v want %v", bindingNamesOf(p.ProtocolBindings), want)
	}
	if p.DefaultProtocolBinding != "anthropic-messages" {
		t.Fatalf("default=%q want anthropic-messages", p.DefaultProtocolBinding)
	}
}

// ADR-0016 explicitly forbids protective rejection: a native anthropic line on
// one endpoint plus an openai endpoint declaring anthropic_to_chat is a legal,
// deliberate admin choice (disaster recovery / migration). No error, no warning.
func TestCompile_NoProtectiveRejectionForParallelAnthropicLines(t *testing.T) {
	in := glmCodingPathsIntent()
	in.ClientExposures[0].Paths = append(in.ClientExposures[0].Paths,
		types.ProtocolPath{InboundProtocol: types.InboundProtocolAnthropic, Channel: types.DedicatedProtocolChannelAnthropicToChat})
	out, err := Compile(in)
	if err != nil {
		t.Fatalf("protective rejection forbidden by ADR-0016, got error: %v", err)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("legal parallel lines must not warn: %#v", out.Warnings)
	}
	p, _ := providerByID(out, "pc_openai")
	want := []string{"anthropic-messages-to-openai", "glm-coding-chat", "responses-to-openai-chat"}
	if !reflect.DeepEqual(bindingNamesOf(p.ProtocolBindings), want) {
		t.Fatalf("bindings=%v want %v", bindingNamesOf(p.ProtocolBindings), want)
	}
}

// ADR-0015 S3 uniqueness stays: two declared paths that resolve to the same
// (protocol, capability, request_contract) key on one provider are a data
// structure integrity violation (non-deterministic resolution) and still fail.
func TestCompile_DuplicateResolutionKeyStillFailsS3(t *testing.T) {
	in := glmCodingPathsIntent()
	in.ClientExposures[0].Paths = []types.ProtocolPath{
		{InboundProtocol: types.InboundProtocolAnthropic, Channel: types.DedicatedProtocolChannelPassthrough},
		{InboundProtocol: types.InboundProtocolAnthropic, Channel: types.DedicatedProtocolChannelAnthropicToChat},
	}
	_, err := Compile(in)
	if err == nil {
		t.Fatalf("expected ADR-0015 S3 uniqueness violation")
	}
	if !strings.Contains(err.Error(), "pc_openai") {
		t.Fatalf("error should name the provider: %v", err)
	}
}
