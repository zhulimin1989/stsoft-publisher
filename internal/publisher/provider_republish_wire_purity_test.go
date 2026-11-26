package publisher

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// ---------------------------------------------------------------------------
// G4 P2: republish must rewrite the legacy vendor-protocol shape
// (outbound_protocol=glm_coding + vendor accepts row) into the pure-dialect
// shape (outbound_protocol=openai, pure accepts, compat_api_type kept as the
// adapter-bundle selector) while keeping every other field byte-equal —
// path_prefix, auth_scheme, binding names, priority, capability profiles,
// field_disposition, default_protocol_binding, base_url all unchanged
// (§4 invariant checklist of the G4 design doc; CB scope-key red line D3).
// ---------------------------------------------------------------------------

// legacyGLMCodingProvider mirrors the live GLM coding paid-line provider shape
// (glm-coding-chat native binding + anthropic bridge binding).
func legacyGLMCodingProvider(id string) types.ProviderInfo {
	return types.ProviderInfo{
		ID:              id,
		Name:            id,
		BaseURL:         "https://open.bigmodel.example",
		Status:          "active",
		ProviderProfile: types.ProviderProfileGLMCoding,
		ProtocolBindings: []types.ProtocolBinding{
			{
				Name:             "anthropic-messages-to-openai",
				OutboundProtocol: "openai",
				PathPrefix:       "/api/coding/paas/v4",
				AuthScheme:       "bearer",
				TranslatorKey:    "standard_to_openai",
				Bridge:           "anthropic_to_openai_chat",
				CapabilityProfiles: []string{
					"openai.chat_completions.full",
				},
				Accepts: []types.ProtocolBindingAccept{{
					Protocol:        "anthropic",
					Capabilities:    []string{"chat"},
					RequestContract: "anthropic_messages",
				}},
			},
			{
				Name:             "glm-coding-chat",
				OutboundProtocol: "glm_coding",
				PathPrefix:       "/api/coding/paas/v4",
				AuthScheme:       "bearer",
				TranslatorKey:    "passthrough",
				CompatAPIType:    "glm_coding",
				CapabilityProfiles: []string{
					"openai.chat_completions.full",
				},
				FieldDisposition: &types.FieldDispositionConfig{
					Request: map[string]string{"thinking": "adapt"},
				},
				Accepts: []types.ProtocolBindingAccept{
					{
						Protocol:        "glm_coding",
						Capabilities:    []string{"chat"},
						RequestContract: "chat_completions",
					},
					{
						Protocol:        "openai",
						Capabilities:    []string{"chat"},
						RequestContract: "chat_completions",
					},
				},
			},
		},
		DefaultProtocolBinding: "glm-coding-chat",
	}
}

func TestPlanProviderRepublish_GLMCodingLegacyShapeRewrittenToPureDialect(t *testing.T) {
	before := legacyGLMCodingProvider("zhipu-glm-coding-1234abcd")
	plan, err := PlanProviderRepublish(before.ID, []byte(mustJSON(t, before)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("legacy glm_coding shape must be rewritten: %+v", plan)
	}
	if plan.NeedsManualIntervention {
		t.Fatalf("glm_coding rewrite must not require manual intervention: %+v", plan)
	}
	if plan.ValidationIssue != "" {
		t.Fatalf("rewritten provider failed validation: %s", plan.ValidationIssue)
	}

	after := plan.After
	// Binding names + default unchanged (route/policy/probe references).
	if !reflect.DeepEqual(plan.AfterBindingNames, plan.BeforeBindingNames) {
		t.Fatalf("binding names changed: before=%v after=%v", plan.BeforeBindingNames, plan.AfterBindingNames)
	}
	if after.DefaultProtocolBinding != "glm-coding-chat" {
		t.Fatalf("default_protocol_binding=%q want glm-coding-chat", after.DefaultProtocolBinding)
	}
	if after.BaseURL != before.BaseURL {
		t.Fatalf("base_url changed (CB scope-key red line): %q -> %q", before.BaseURL, after.BaseURL)
	}

	var chat, bridge types.ProtocolBinding
	for _, b := range after.ProtocolBindings {
		switch b.Name {
		case "glm-coding-chat":
			chat = b
		case "anthropic-messages-to-openai":
			bridge = b
		}
	}

	// Native line: outbound purified, vendor accepts row dropped, everything
	// else byte-equal.
	if chat.OutboundProtocol != "openai" {
		t.Fatalf("glm-coding-chat outbound=%q want openai", chat.OutboundProtocol)
	}
	if chat.CompatAPIType != "glm_coding" {
		t.Fatalf("glm-coding-chat compat_api_type=%q want glm_coding (bundle selector must survive, R2)", chat.CompatAPIType)
	}
	wantAccepts := []types.ProtocolBindingAccept{{
		Protocol:        "openai",
		Capabilities:    []string{"chat"},
		RequestContract: "chat_completions",
	}}
	if !reflect.DeepEqual(chat.Accepts, wantAccepts) {
		t.Fatalf("glm-coding-chat accepts=%+v want %+v", chat.Accepts, wantAccepts)
	}
	if chat.PathPrefix != "/api/coding/paas/v4" {
		t.Fatalf("glm-coding-chat path_prefix=%q want /api/coding/paas/v4 (R3)", chat.PathPrefix)
	}
	if chat.AuthScheme != "bearer" || chat.TranslatorKey != "passthrough" {
		t.Fatalf("glm-coding-chat auth/translator drifted: %+v", chat)
	}
	if chat.FieldDisposition == nil || chat.FieldDisposition.Request["thinking"] != "adapt" {
		t.Fatalf("glm-coding-chat field_disposition lost: %+v", chat.FieldDisposition)
	}
	if !reflect.DeepEqual(chat.CapabilityProfiles, []string{"openai.chat_completions.full"}) {
		t.Fatalf("glm-coding-chat capability_profiles drifted: %v", chat.CapabilityProfiles)
	}

	// Bridge binding must be byte-equal to its input (already pure).
	if !reflect.DeepEqual(bridge, before.ProtocolBindings[0]) {
		t.Fatalf("bridge binding must be untouched\n got=%+v\nwant=%+v", bridge, before.ProtocolBindings[0])
	}

	// Diff summary names the rewrite for the operator's canary diff discipline.
	joined := strings.Join(plan.DiffSummary, ";")
	if !strings.Contains(joined, "glm-coding-chat") || !strings.Contains(joined, "openai") {
		t.Fatalf("diff summary should describe the outbound rewrite: %v", plan.DiffSummary)
	}
}

// A provider already in the pure shape is a no-op: republish is idempotent.
func TestPlanProviderRepublish_PureShapeGLMCodingIsIdempotent(t *testing.T) {
	bindings, def, err := types.CompileClientProtocolBindings(types.ProviderProfileGLMCoding, []string{types.ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	info := types.ProviderInfo{
		ID:                     "zhipu-glm-coding-pure",
		Name:                   "zhipu-glm-coding-pure",
		BaseURL:                "https://open.bigmodel.example",
		Status:                 "active",
		ProviderProfile:        types.ProviderProfileGLMCoding,
		ProtocolBindings:       bindings,
		DefaultProtocolBinding: def,
	}
	plan, err := PlanProviderRepublish(info.ID, []byte(mustJSON(t, info)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if plan.Changed {
		t.Fatalf("pure shape must be a republish no-op, diff=%v", plan.DiffSummary)
	}
}

// A legacy vendor binding published without compat_api_type (admin direct
// publish) must get the vendor bundle selector stamped — otherwise the gateway
// would resolve the plain openai bundle and silently drop thinking/tool_stream
// injection (risk R2).
func TestPlanProviderRepublish_StampsMissingCompatAPIType(t *testing.T) {
	info := legacyGLMCodingProvider("zhipu-glm-coding-nocompat")
	for i := range info.ProtocolBindings {
		if info.ProtocolBindings[i].Name == "glm-coding-chat" {
			info.ProtocolBindings[i].CompatAPIType = ""
		}
	}
	plan, err := PlanProviderRepublish(info.ID, []byte(mustJSON(t, info)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("expected rewrite: %+v", plan)
	}
	for _, b := range plan.After.ProtocolBindings {
		if b.Name == "glm-coding-chat" && b.CompatAPIType != "glm_coding" {
			t.Fatalf("compat_api_type=%q want glm_coding stamped (R2 guard)", b.CompatAPIType)
		}
	}
}

// A vendor binding whose only accepts row is the vendor protocol (no parallel
// openai row) has the row rewritten to openai instead of dropped.
func TestPlanProviderRepublish_VendorOnlyAcceptsRowRewrittenNotDropped(t *testing.T) {
	info := legacyGLMCodingProvider("zhipu-glm-coding-vendoronly")
	for i := range info.ProtocolBindings {
		if info.ProtocolBindings[i].Name == "glm-coding-chat" {
			info.ProtocolBindings[i].Accepts = []types.ProtocolBindingAccept{{
				Protocol:        "glm_coding",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}}
		}
	}
	plan, err := PlanProviderRepublish(info.ID, []byte(mustJSON(t, info)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	for _, b := range plan.After.ProtocolBindings {
		if b.Name != "glm-coding-chat" {
			continue
		}
		want := []types.ProtocolBindingAccept{{
			Protocol:        "openai",
			Capabilities:    []string{"chat"},
			RequestContract: "chat_completions",
		}}
		if !reflect.DeepEqual(b.Accepts, want) {
			t.Fatalf("accepts=%+v want %+v", b.Accepts, want)
		}
	}
}

// Doubao / Azure legacy shapes follow the same rewrite; Azure additionally
// gets the api-version query template that used to live on the gateway's
// azure_openai protocol-level query table.
func TestPlanProviderRepublish_DoubaoAndAzureLegacyShapes(t *testing.T) {
	doubao := types.ProviderInfo{
		ID:      "doubao-legacy",
		Name:    "doubao-legacy",
		BaseURL: "https://ark.example",
		Status:  "active",
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "doubao-chat",
			OutboundProtocol: "doubao_ark",
			PathPrefix:       "/api/v3",
			AuthScheme:       "bearer",
			TranslatorKey:    "passthrough",
			CompatAPIType:    "doubao",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []types.ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		}},
		DefaultProtocolBinding: "doubao-chat",
	}
	plan, err := PlanProviderRepublish(doubao.ID, []byte(mustJSON(t, doubao)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish doubao: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("doubao legacy shape must change")
	}
	if got := plan.After.ProtocolBindings[0]; got.OutboundProtocol != "openai" || got.CompatAPIType != "doubao" || got.PathPrefix != "/api/v3" {
		t.Fatalf("doubao rewrite wrong: %+v", got)
	}

	azure := types.ProviderInfo{
		ID:      "azure-legacy",
		Name:    "azure-legacy",
		BaseURL: "https://example.openai.azure.example",
		Status:  "active",
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "azure-openai-chat",
			OutboundProtocol: "azure_openai",
			PathPrefix:       "/openai/deployments/{upstream_model}",
			AuthScheme:       "azure-key",
			TranslatorKey:    "passthrough",
			CompatAPIType:    "azure",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []types.ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		}},
		DefaultProtocolBinding: "azure-openai-chat",
	}
	plan, err = PlanProviderRepublish(azure.ID, []byte(mustJSON(t, azure)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish azure: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("azure legacy shape must change")
	}
	got := plan.After.ProtocolBindings[0]
	if got.OutboundProtocol != "openai" || got.CompatAPIType != "azure" {
		t.Fatalf("azure rewrite wrong: %+v", got)
	}
	if got.QueryTemplate != "api-version={api_version}" {
		t.Fatalf("azure query_template=%q want api-version={api_version}", got.QueryTemplate)
	}
	if got.PathPrefix != "/openai/deployments/{upstream_model}" {
		t.Fatalf("azure path_prefix drifted: %q", got.PathPrefix)
	}
}
