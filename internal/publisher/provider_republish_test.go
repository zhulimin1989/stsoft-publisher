package publisher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestPlanProviderRepublishAll_DryRunListsChangedProviders(t *testing.T) {
	stale := staleOpenAICompatibleAllProvider(t, "stale-openai")
	current := currentOpenAICompatibleAllProvider(t, "current-openai")
	raw := map[string]string{
		"stale-openai":   mustJSON(t, stale),
		"current-openai": mustJSON(t, current),
	}

	plans, err := PlanProviderRepublishAll(raw)
	if err != nil {
		t.Fatalf("PlanProviderRepublishAll: %v", err)
	}

	var changed []string
	for _, plan := range plans {
		if plan.Changed {
			changed = append(changed, plan.ProviderID)
		}
	}
	if len(changed) != 1 || changed[0] != "stale-openai" {
		t.Fatalf("changed providers=%v want [stale-openai]; plans=%+v", changed, plans)
	}
	if !strings.Contains(strings.Join(plans[1].DiffSummary, ";"), "gemini-generate-to-openai") {
		t.Fatalf("stale diff summary missing removed binding: %+v", plans)
	}
}

func TestPlanProviderRepublish_BuildsPayloadWithBridge(t *testing.T) {
	stale := staleOpenAICompatibleAllProvider(t, "stale-openai")
	plan, err := PlanProviderRepublish("stale-openai", []byte(mustJSON(t, stale)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("expected stale provider to change: %+v", plan)
	}

	rows := buildPublishProviderRows(plan.ProviderID, &plan.After)
	if len(rows) == 0 {
		t.Fatal("expected provider rows")
	}
	payload := string(rows[0].Payload)
	if !strings.Contains(payload, `"bridge":"anthropic_to_openai_chat"`) {
		t.Fatalf("republished payload missing anthropic bridge: %s", payload)
	}
	if !strings.Contains(payload, `"bridge":"responses_to_chat"`) {
		t.Fatalf("republished payload missing responses bridge: %s", payload)
	}
	if strings.Contains(payload, "gemini-generate-to-openai") {
		t.Fatalf("republished payload retained removed Gemini bridge: %s", payload)
	}
}

func TestPlanProviderRepublish_OpenAICompatibleGeminiOnlyDoesNotExpandToAll(t *testing.T) {
	geminiOnly := openAICompatibleGeminiOnlyProvider("gemini-only")
	plan, err := PlanProviderRepublish("gemini-only", []byte(mustJSON(t, geminiOnly)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.NeedsManualIntervention {
		t.Fatalf("gemini-only migration must require manual intervention: %+v", plan)
	}
	if !strings.Contains(plan.ManualInterventionReason, "gemini-generate-to-openai") {
		t.Fatalf("manual reason must list unmapped binding names, got %q", plan.ManualInterventionReason)
	}
	joined := strings.Join(plan.DiffSummary, ";")
	for _, forbidden := range []string{"add binding openai-chat", "add binding openai-embeddings", "add binding anthropic-messages-to-openai", "add binding responses-to-openai-chat"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("gemini-only provider must not expand to all; diff=%v", plan.DiffSummary)
		}
	}
	if !strings.Contains(joined, "remove binding gemini-generate-to-openai") {
		t.Fatalf("gemini-only provider should only be conservatively patched, diff=%v", plan.DiffSummary)
	}
}

func TestPlanProviderRepublish_OpenAICompatibleOldAllRemovesGeminiAndAddsBridges(t *testing.T) {
	stale := staleOpenAICompatibleAllProvider(t, "stale-openai")
	plan, err := PlanProviderRepublish("stale-openai", []byte(mustJSON(t, stale)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if plan.NeedsManualIntervention {
		t.Fatalf("old all should be exactly recompiled, not manual: %+v", plan)
	}
	joined := strings.Join(plan.DiffSummary, ";")
	for _, want := range []string{
		"remove binding gemini-generate-to-openai",
		"set bridge anthropic-messages-to-openai=anthropic_to_openai_chat",
		"set bridge responses-to-openai-chat=responses_to_chat",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diff missing %q: %v", want, plan.DiffSummary)
		}
	}
	if containsString(plan.AfterBindingNames, "gemini-generate-to-openai") {
		t.Fatalf("old Gemini binding must be removed: %v", plan.AfterBindingNames)
	}
}

func TestPlanProviderRepublish_UnrecognizedComboFailsClosedToManualIntervention(t *testing.T) {
	info := openAICompatibleAnthropicAndEmbeddingsProvider(t, "manual-openai")
	plan, err := PlanProviderRepublish("manual-openai", []byte(mustJSON(t, info)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.NeedsManualIntervention {
		t.Fatalf("unrecognized combo must require manual intervention: %+v", plan)
	}
	for _, want := range []string{"anthropic-messages-to-openai", "openai-embeddings"} {
		if !strings.Contains(plan.ManualInterventionReason, want) {
			t.Fatalf("manual reason %q missing %s", plan.ManualInterventionReason, want)
		}
	}
	joined := strings.Join(plan.DiffSummary, ";")
	for _, forbidden := range []string{"add binding openai-chat", "add binding responses-to-openai-chat"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unrecognized combo must not expand to all; diff=%v", plan.DiffSummary)
		}
	}
	if !strings.Contains(joined, "set bridge anthropic-messages-to-openai=anthropic_to_openai_chat") {
		t.Fatalf("manual combo should still get conservative bridge patch, diff=%v", plan.DiffSummary)
	}
}

func TestPlanProviderRepublish_ManualPatchedProviderStillValidatedBeforePublish(t *testing.T) {
	geminiOnly := openAICompatibleGeminiOnlyProvider("gemini-only")
	plan, err := PlanProviderRepublish("gemini-only", []byte(mustJSON(t, geminiOnly)))
	if err != nil {
		t.Fatalf("PlanProviderRepublish: %v", err)
	}
	if !plan.NeedsManualIntervention {
		t.Fatalf("manual flag missing: %+v", plan)
	}
	if plan.ValidationIssue == "" {
		t.Fatalf("manual patched provider must record validation issue before publish: %+v", plan)
	}
	if !strings.Contains(plan.ValidationIssue, "protocol_bindings") {
		t.Fatalf("validation issue should explain invalid patched provider, got %q", plan.ValidationIssue)
	}
}

func staleOpenAICompatibleAllProvider(t *testing.T, id string) types.ProviderInfo {
	t.Helper()
	info := currentOpenAICompatibleAllProvider(t, id)
	info.ProtocolBindings = append(info.ProtocolBindings, types.ProtocolBinding{
		Name:             "gemini-generate-to-openai",
		OutboundProtocol: "openai",
		PathPrefix:       "/v1",
		AuthScheme:       "bearer",
		TranslatorKey:    "standard_to_openai",
		CapabilityProfiles: []string{
			"openai.chat_completions.full",
		},
		Accepts: []types.ProtocolBindingAccept{{
			Protocol:        "gemini",
			Capabilities:    []string{"chat"},
			RequestContract: "gemini_generate",
		}},
	})
	for i := range info.ProtocolBindings {
		info.ProtocolBindings[i].Bridge = ""
	}
	return info
}

func openAICompatibleGeminiOnlyProvider(id string) types.ProviderInfo {
	return types.ProviderInfo{
		ID:      id,
		Name:    id,
		BaseURL: "https://api.openai.example",
		Status:  "active",
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "gemini-generate-to-openai",
			OutboundProtocol: "openai",
			PathPrefix:       "/v1",
			AuthScheme:       "bearer",
			TranslatorKey:    "standard_to_openai",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			Accepts: []types.ProtocolBindingAccept{{
				Protocol:        "gemini",
				Capabilities:    []string{"chat"},
				RequestContract: "gemini_generate",
			}},
		}},
		DefaultProtocolBinding: "gemini-generate-to-openai",
	}
}

func openAICompatibleAnthropicAndEmbeddingsProvider(t *testing.T, id string) types.ProviderInfo {
	t.Helper()
	all := currentOpenAICompatibleAllProvider(t, id)
	var bindings []types.ProtocolBinding
	for _, binding := range all.ProtocolBindings {
		switch binding.Name {
		case "anthropic-messages-to-openai":
			binding.Bridge = ""
			bindings = append(bindings, binding)
		case "openai-embeddings":
			bindings = append(bindings, binding)
		}
	}
	return types.ProviderInfo{
		ID:                     id,
		Name:                   id,
		BaseURL:                "https://api.openai.example",
		Status:                 "active",
		ProtocolBindings:       bindings,
		DefaultProtocolBinding: "anthropic-messages-to-openai",
	}
}

func currentOpenAICompatibleAllProvider(t *testing.T, id string) types.ProviderInfo {
	t.Helper()
	bindings, defaultBinding, err := types.CompileClientProtocolBindings(types.ProviderProfileOpenAICompatible, []string{types.ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	return types.ProviderInfo{
		ID:                     id,
		Name:                   id,
		BaseURL:                "https://api.openai.example",
		Status:                 "active",
		ProtocolBindings:       bindings,
		DefaultProtocolBinding: defaultBinding,
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
