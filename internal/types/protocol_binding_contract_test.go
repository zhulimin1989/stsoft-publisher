package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestEmbeddedProtocolBindingsContract(t *testing.T) {
	contract := EmbeddedProtocolBindingsContract()
	if contract.Version != ProtocolBindingsContractVersion {
		t.Fatalf("version=%d, want %d", contract.Version, ProtocolBindingsContractVersion)
	}
	if contract.Source != "tokenplatform-gateway/internal/bindingspec+capabilityspec+capabilityprofile" {
		t.Fatalf("source=%q", contract.Source)
	}
	if !containsString(contract.InboundProtocols, "responses") {
		t.Fatal("responses inbound protocol missing")
	}
	if !containsString(contract.OutboundProtocols, "openai_responses") {
		t.Fatal("openai_responses outbound protocol missing")
	}
	if !hasRuntimeEndpoint(contract.RuntimeEndpoints, "azure_openai", "chat", "chat_completions", "api-version={api_version}") {
		t.Fatal("azure_openai chat endpoint query_template missing")
	}
	if !hasTranslation(contract.TranslationMatrix, "anthropic", "openai", "chat", "anthropic_messages", "standard_to_openai") {
		t.Fatal("anthropic->openai translator contract missing")
	}
	if !containsString(contract.Bridges, "anthropic_to_openai_chat") {
		t.Fatal("anthropic_to_openai_chat bridge contract missing")
	}
	if !hasBridgeTranslation(contract.TranslationMatrix, "anthropic", "openai", "chat", "anthropic_messages", "standard_to_openai", "anthropic_to_openai_chat") {
		t.Fatal("anthropic->openai bridge contract missing")
	}
	if !containsString(contract.CapabilityProfiles, "openai.chat_completions.base") {
		t.Fatal("capability profile contract missing")
	}
	if !containsString(contract.CapabilityParams, "temperature") {
		t.Fatal("capability param contract missing")
	}
	if !containsString(contract.ParamPolicyActions, "omit_with_warning") {
		t.Fatal("param policy action contract missing")
	}
	if !containsString(contract.RequestCorrelationModes, "gateway_header_and_user_agent") {
		t.Fatal("request correlation mode contract missing")
	}
	if !containsString(contract.ProviderProfiles, ProviderProfileOpenAICompatible) {
		t.Fatal("provider profile enum contract missing")
	}
	if !containsString(contract.ClientCompatibilities, ClientCompatibilityClaudeCode) {
		t.Fatal("client compatibility enum contract missing")
	}
	if !containsString(contract.TokenCountStrategies, TokenCountStrategyUnsupported) {
		t.Fatal("token count strategy enum contract missing")
	}
}

// TestEmbeddedProtocolBindingsContractImageGeneration locks the
// image_generation capability lockstep: gateway bindingspec (logical source)
// already carries capability=image_generation, request_contract=
// image_generations, and the {openai,image_generation,image_generations} ->
// /images/generations endpoint; the materialized contract must expose the
// same three entries so publisher pre-publish validation admits image
// bindings (ADR-0013 section 3: new orthogonal capability value, not a new
// admission axis).
func TestEmbeddedProtocolBindingsContractImageGeneration(t *testing.T) {
	contract := EmbeddedProtocolBindingsContract()
	if !containsString(contract.Capabilities, "image_generation") {
		t.Fatal("image_generation capability missing")
	}
	if !containsString(contract.RequestContracts, "image_generations") {
		t.Fatal("image_generations request contract missing")
	}
	if !hasRuntimeEndpointPath(contract.RuntimeEndpoints, "openai", "image_generation", "image_generations", "/images/generations") {
		t.Fatal("openai image_generation endpoint /images/generations missing")
	}
}

func TestProtocolBindingsContractIntentEnumsMatchPublisherClosedSets(t *testing.T) {
	contract := EmbeddedProtocolBindingsContract()
	assertSameStrings(t, "provider_profiles", contract.ProviderProfiles, KnownProviderProfiles())
	assertSameStrings(t, "client_compatibilities", contract.ClientCompatibilities, KnownClientCompatibilities())
	assertSameStrings(t, "token_count_strategies", contract.TokenCountStrategies, KnownTokenCountStrategies())
}

func TestProtocolBindingsCapabilityAxesFrozen(t *testing.T) {
	contract := loadProtocolBindingsContractFile(t, filepath.Join("..", "..", "contracts", "runtime", "protocol_bindings_contract.json"))
	baseline := loadCapabilityAxesFreezeBaseline(t, filepath.Join("..", "..", "contracts", "freeze", "capability_axes_baseline.json"))

	assertCapabilityAxesReviewMarker(t, baseline)
	assertSameStrings(t, "frozen capability_profiles", contract.CapabilityProfiles, baseline.CapabilityProfiles)
	assertSameStrings(t, "frozen capability_features", contract.CapabilityFeatures, baseline.CapabilityFeatures)
	assertSameStrings(t, "frozen capability_params", contract.CapabilityParams, baseline.CapabilityParams)
}

func TestProtocolBindingsContractMatchesGatewayFixtureWhenPresent(t *testing.T) {
	localPath := filepath.Join("testdata", "protocol_bindings_contract.json")
	gatewayPath := filepath.Join("..", "..", "..", "tokenplatform-gateway", "internal", "configsync", "testdata", "protocol_bindings_contract.json")

	local, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local fixture: %v", err)
	}
	gateway, err := os.ReadFile(gatewayPath)
	if os.IsNotExist(err) {
		t.Skip("adjacent tokenplatform-gateway checkout not present")
	}
	if err != nil {
		t.Fatalf("read gateway fixture: %v", err)
	}
	assertGatewayOwnedProtocolContractMatches(t, local, gateway)
}

func TestProtocolBindingsContractMatchesGatewayOriginTestFixtureWhenPresent(t *testing.T) {
	localPath := filepath.Join("testdata", "protocol_bindings_contract.json")
	gatewayRepo := filepath.Join("..", "..", "..", "tokenplatform-gateway")
	local, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local fixture: %v", err)
	}
	gatewayTest, err := exec.Command("git", "-C", gatewayRepo, "show", "origin/test:internal/configsync/testdata/protocol_bindings_contract.json").Output()
	if err != nil {
		t.Skipf("adjacent tokenplatform-gateway origin/test fixture not present: %v", err)
	}
	assertGatewayOwnedProtocolContractMatches(t, local, gatewayTest)
}

func assertGatewayOwnedProtocolContractMatches(t *testing.T, local, gateway []byte) {
	t.Helper()
	localContract := map[string]any{}
	if err := json.Unmarshal(local, &localContract); err != nil {
		t.Fatalf("unmarshal local protocol bindings contract: %v", err)
	}
	stripPublisherOwnedProtocolContractFields(localContract)

	gatewayContract := map[string]any{}
	if err := json.Unmarshal(gateway, &gatewayContract); err != nil {
		t.Fatalf("unmarshal gateway protocol bindings contract: %v", err)
	}
	stripPublisherOwnedProtocolContractFields(gatewayContract)
	if !reflect.DeepEqual(localContract, gatewayContract) {
		t.Fatalf("gateway-owned protocol_bindings_contract.json fields are out of sync with gateway test fixture")
	}
}

func stripPublisherOwnedProtocolContractFields(contract map[string]any) {
	delete(contract, "provider_profiles")
	delete(contract, "client_compatibilities")
	delete(contract, "token_count_strategies")
	delete(contract, "protocol_strategies")
	delete(contract, "dedicated_protocol_channels")
	delete(contract, "lossy_tiers")
	delete(contract, "bridge_contracts")
}

type capabilityAxesFreezeBaseline struct {
	SchemaVersion      int      `json:"schema_version"`
	Source             string   `json:"source"`
	ArchitectureReview string   `json:"architecture_review"`
	CapabilityProfiles []string `json:"capability_profiles"`
	CapabilityFeatures []string `json:"capability_features"`
	CapabilityParams   []string `json:"capability_params"`
}

type frozenCapabilityAxes struct {
	CapabilityFeatures []string `json:"capability_features"`
	CapabilityParams   []string `json:"capability_params"`
	CapabilityProfiles []string `json:"capability_profiles"`
}

func loadCapabilityAxesFreezeBaseline(t *testing.T, path string) capabilityAxesFreezeBaseline {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capability axes freeze baseline %s: %v", path, err)
	}
	var baseline capabilityAxesFreezeBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatalf("unmarshal capability axes freeze baseline %s: %v", path, err)
	}
	if baseline.SchemaVersion != 1 {
		t.Fatalf("capability axes freeze baseline schema_version=%d, want 1", baseline.SchemaVersion)
	}
	return baseline
}

func loadProtocolBindingsContractFile(t *testing.T, path string) ProtocolBindingsContract {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read protocol bindings contract %s: %v", path, err)
	}
	var contract ProtocolBindingsContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("unmarshal protocol bindings contract %s: %v", path, err)
	}
	return contract
}

func assertCapabilityAxesReviewMarker(t *testing.T, baseline capabilityAxesFreezeBaseline) {
	t.Helper()
	if !strings.Contains(baseline.ArchitectureReview, "ARCH-REVIEW-APPROVED") {
		t.Fatalf("capability axes baseline must carry ARCH-REVIEW-APPROVED marker; got %q", baseline.ArchitectureReview)
	}
	wantHash := capabilityAxesHash(t, baseline)
	if !strings.Contains(baseline.ArchitectureReview, "sha256:"+wantHash) {
		t.Fatalf("capability axes baseline review marker must include sha256:%s; got %q", wantHash, baseline.ArchitectureReview)
	}
}

func capabilityAxesHash(t *testing.T, baseline capabilityAxesFreezeBaseline) string {
	t.Helper()
	payload, err := json.Marshal(frozenCapabilityAxes{
		CapabilityFeatures: sortedStringCopy(baseline.CapabilityFeatures),
		CapabilityParams:   sortedStringCopy(baseline.CapabilityParams),
		CapabilityProfiles: sortedStringCopy(baseline.CapabilityProfiles),
	})
	if err != nil {
		t.Fatalf("marshal capability axes baseline for hash: %v", err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)
}

func assertSameStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	got = sortedStringCopy(got)
	want = sortedStringCopy(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch:\n got=%v\nwant=%v", name, got, want)
	}
}

func sortedStringCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasRuntimeEndpoint(values []ProtocolRuntimeEndpoint, outbound, capability, contract, query string) bool {
	for _, value := range values {
		if value.OutboundProtocol == outbound &&
			value.Capability == capability &&
			value.RequestContract == contract &&
			value.QueryTemplate == query {
			return true
		}
	}
	return false
}

func hasRuntimeEndpointPath(values []ProtocolRuntimeEndpoint, outbound, capability, contract, path string) bool {
	for _, value := range values {
		if value.OutboundProtocol == outbound &&
			value.Capability == capability &&
			value.RequestContract == contract &&
			value.PathTemplate == path {
			return true
		}
	}
	return false
}

func hasTranslation(values []ProtocolTranslationContract, from, to, capability, contract, translator string) bool {
	for _, value := range values {
		if value.From == from &&
			value.To == to &&
			value.Capability == capability &&
			value.RequestContract == contract &&
			value.Translator == translator {
			return true
		}
	}
	return false
}

func hasBridgeTranslation(values []ProtocolTranslationContract, from, to, capability, contract, translator, bridge string) bool {
	for _, value := range values {
		if value.From == from &&
			value.To == to &&
			value.Capability == capability &&
			value.RequestContract == contract &&
			value.Translator == translator &&
			value.Bridge == bridge {
			return true
		}
	}
	return false
}
