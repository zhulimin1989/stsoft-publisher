package types_test

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

const (
	runtimeSchemaURI    = "https://json-schema.org/draft/2020-12/schema"
	runtimeContractsDir = "../../contracts/runtime"
	goldenContractsDir  = "../../contracts/golden"
	runtimeUpdateEnv    = "PUBLISHER_UPDATE_SCHEMAS"
	jsonSchemaIndent    = "  "
)

func TestRuntimeConfigSchemaContracts(t *testing.T) {
	for _, c := range runtimeSchemaContracts() {
		t.Run(c.title, func(t *testing.T) {
			t.Parallel()

			actual := map[string]any(nil)
			if c.schema != nil {
				actual = c.schema()
			} else {
				actual = structSchema(reflect.TypeOf(c.v).Elem())
			}
			actual["$schema"] = runtimeSchemaURI
			actual["$id"] = c.id
			actual["title"] = c.title
			actual["version"] = c.version

			actualBytes, err := json.MarshalIndent(actual, "", jsonSchemaIndent)
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
			actualBytes = append(actualBytes, '\n')

			path := filepath.Join(runtimeContractsDir, c.file)
			if os.Getenv(runtimeUpdateEnv) == "1" {
				if err := os.MkdirAll(runtimeContractsDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", runtimeContractsDir, err)
				}
				if err := os.WriteFile(path, actualBytes, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				t.Logf("seeded %s", path)
				return
			}

			expected, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v\n\nIf this is an intentional contract change, run:\n  %s=1 go test ./internal/types -run 'TestRuntimeConfigSchemaContracts|TestProtocolBindingsContractManagedUnderContracts'", path, err, runtimeUpdateEnv)
			}
			if !bytes.Equal(actualBytes, expected) {
				t.Errorf("schema drift in %s.\n\nThe Go struct shape no longer matches the pinned runtime contract.\nIf the change is intentional and gateway consumers have been coordinated in lockstep, refresh the contract:\n  %s=1 go test ./internal/types -run 'TestRuntimeConfigSchemaContracts|TestProtocolBindingsContractManagedUnderContracts'\n\n--- expected (%s) ---\n%s\n--- got ---\n%s",
					c.title, runtimeUpdateEnv, path, expected, actualBytes)
			}
		})
	}
}

func TestRuntimeConfigSchemaVersionsMatchPublisherConstants(t *testing.T) {
	for _, c := range runtimeSchemaContracts() {
		t.Run(c.title, func(t *testing.T) {
			path := filepath.Join(runtimeContractsDir, c.file)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("unmarshal %s: %v", path, err)
			}

			got, ok := schema["version"].(float64)
			if !ok {
				t.Fatalf("version metadata missing or non-number: %#v", schema["version"])
			}
			if int(got) != c.version {
				t.Fatalf("version = %d, want %d", int(got), c.version)
			}
		})
	}
}

func TestKeyPoolRuntimeSchemaIncludesOptionalProviderAccountRef(t *testing.T) {
	schema := keyPoolRuntimeSchema()
	entries := schema["properties"].(map[string]any)["entries"].(map[string]any)
	entry := entries["items"].(map[string]any)
	properties := entry["properties"].(map[string]any)
	got, ok := properties["provider_account_ref"].(map[string]any)
	if !ok || got["type"] != "string" {
		t.Fatalf("provider_account_ref schema=%#v want optional string", properties["provider_account_ref"])
	}
	if got["minLength"] != 1 || got["pattern"] != types.ProviderAccountRefPattern || got["maxLength"] != types.ProviderAccountRefMaxLength {
		t.Fatalf("provider_account_ref constraints=%#v", got)
	}
	for _, name := range entry["required"].([]string) {
		if name == "provider_account_ref" {
			t.Fatalf("provider_account_ref must remain optional for legacy keypool entries: %#v", entry["required"])
		}
	}
}

func TestKeyMetaRuntimeSchemaDedicatedEndpointContract(t *testing.T) {
	path := filepath.Join(runtimeContractsDir, "key_meta.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal key_meta schema: %v", err)
	}
	if schema["$id"] != runtimeSchemaID("key_meta", publisher.CurrentKeyMetaSchemaVersion) {
		t.Fatalf("key_meta $id = %v, want v%d id", schema["$id"], publisher.CurrentKeyMetaSchemaVersion)
	}
	if got := int(schema["version"].(float64)); got != 2 {
		t.Fatalf("key_meta schema version = %d, want 2", got)
	}

	properties := schema["properties"].(map[string]any)
	dedicated := properties["dedicated_upstream"].(map[string]any)
	dedicatedProps := dedicated["properties"].(map[string]any)
	accountRef := dedicatedProps["provider_account_ref"].(map[string]any)
	if int(accountRef["minLength"].(float64)) != 1 || accountRef["pattern"] != types.ProviderAccountRefPattern || int(accountRef["maxLength"].(float64)) != types.ProviderAccountRefMaxLength {
		t.Fatalf("dedicated_upstream.provider_account_ref schema = %#v", accountRef)
	}
	if containsAny(dedicated["required"].([]any), "provider_account_ref") {
		t.Fatalf("dedicated_upstream.provider_account_ref must remain optional for historical KeyMeta decode: %#v", dedicated["required"])
	}
	if _, ok := dedicatedProps["provider_id"]; ok {
		t.Fatalf("dedicated_upstream schema must not expose legacy provider_id")
	}
	endpoints := dedicatedProps["endpoints"].(map[string]any)
	items := endpoints["items"].(map[string]any)
	if additional, ok := items["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("endpoints[] additionalProperties = %v, want false", items["additionalProperties"])
	}
	if !reflect.DeepEqual(items["required"], []any{"client_protocol", "provider_id"}) {
		t.Fatalf("endpoints[] required = %#v", items["required"])
	}
	itemProps := items["properties"].(map[string]any)
	if !reflect.DeepEqual(itemProps["client_protocol"].(map[string]any)["enum"], []any{"anthropic", "openai", "responses"}) {
		t.Fatalf("client_protocol enum = %#v", itemProps["client_protocol"])
	}
	// ADR-0015 S5a: legacy transition values (auto/passthrough_only) retired.
	if !reflect.DeepEqual(itemProps["protocol_strategy"].(map[string]any)["enum"], []any{"passthrough", "responses_to_chat", "anthropic_to_chat"}) {
		t.Fatalf("protocol_strategy enum = %#v", itemProps["protocol_strategy"])
	}
	if updatedAt := dedicatedProps["updated_at"].(map[string]any); updatedAt["format"] != "date-time" {
		t.Fatalf("updated_at schema = %#v, want date-time format", updatedAt)
	}
}

// ADR-0015 S5b: the route-level protocol_strategy_override loss switch is
// retired. The runtime schema must no longer expose it (additionalProperties
// is false, so stale payloads carrying it fail schema validation on the
// contract side, while the Go lenient admin decode silently drops it).
func TestRouteConfigRuntimeSchemaRetiredProtocolStrategyOverrideContract(t *testing.T) {
	path := filepath.Join(runtimeContractsDir, "route_config.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal route_config schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	providers := properties["providers"].(map[string]any)
	items := providers["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)

	forbidden := []string{"allow_high_loss_bridge_when_no_native", "allow_bridge_on_native_failure", "protocol_strategy", "protocol_strategy_override"}
	for _, field := range forbidden {
		if _, ok := itemProps[field]; ok {
			t.Fatalf("route providers[] must not expose %s", field)
		}
	}
	if additional, ok := items["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("route providers[] additionalProperties = %v, want false", items["additionalProperties"])
	}
}

func TestProviderInfoRuntimeSchemaIncludesPlannerEndpointFacts(t *testing.T) {
	path := filepath.Join(runtimeContractsDir, "provider_info.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal provider_info schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	accountRef := properties["provider_account_ref"].(map[string]any)
	if int(accountRef["minLength"].(float64)) != 1 || accountRef["pattern"] != types.ProviderAccountRefPattern || int(accountRef["maxLength"].(float64)) != types.ProviderAccountRefMaxLength {
		t.Fatalf("provider_info.provider_account_ref schema = %#v", accountRef)
	}
	if containsAny(schema["required"].([]any), "provider_account_ref") {
		t.Fatalf("provider_info.provider_account_ref must remain optional for additive runtime compatibility: %#v", schema["required"])
	}
	if _, ok := properties["owned_by"]; !ok {
		t.Fatalf("provider_info schema missing owned_by")
	}
	profile, ok := properties["provider_profile"].(map[string]any)
	if !ok {
		t.Fatalf("provider_info schema missing provider_profile")
	}
	if !reflect.DeepEqual(profile["enum"], toAnyStrings(types.KnownProviderProfiles())) {
		t.Fatalf("provider_profile enum = %#v", profile["enum"])
	}
}

func TestProviderInfoRuntimeSchemaProviderIsolationContract(t *testing.T) {
	path := filepath.Join(runtimeContractsDir, "provider_info.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal provider_info schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, field := range []string{"visibility", "credential_source", "allowed_route_kinds", "owner_scope"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("provider_info schema missing %s", field)
		}
		if containsAny(required, field) {
			t.Fatalf("%s must remain optional for back-compat, required=%#v", field, required)
		}
	}

	visibility := properties["visibility"].(map[string]any)
	if !reflect.DeepEqual(visibility["enum"], []any{"product_route", "dedicated_only", "internal"}) {
		t.Fatalf("visibility schema = %#v", visibility)
	}
	if visibility["default"] != "product_route" {
		t.Fatalf("visibility default = %#v, want product_route", visibility["default"])
	}

	credentialSource := properties["credential_source"].(map[string]any)
	if !reflect.DeepEqual(credentialSource["enum"], []any{"keypool", "dedicated_upstream", "provider_entitlement"}) {
		t.Fatalf("credential_source schema = %#v", credentialSource)
	}
	if credentialSource["default"] != "keypool" {
		t.Fatalf("credential_source default = %#v, want keypool", credentialSource["default"])
	}

	routeKinds := properties["allowed_route_kinds"].(map[string]any)
	if got := routeKinds["default"]; !reflect.DeepEqual(got, []any{"model_route", "product_route"}) {
		t.Fatalf("allowed_route_kinds default = %#v, want model/product route", got)
	}
	items := routeKinds["items"].(map[string]any)
	if !reflect.DeepEqual(items["enum"], []any{"dedicated_route", "model_route", "product_route"}) {
		t.Fatalf("allowed_route_kinds items schema = %#v", items)
	}

	ownerScope := properties["owner_scope"].(map[string]any)
	ownerProps := ownerScope["properties"].(map[string]any)
	for _, field := range []string{"tenant_id", "product_id", "entitlement_id"} {
		if _, ok := ownerProps[field]; !ok {
			t.Fatalf("owner_scope missing %s", field)
		}
	}

	allOf, ok := schema["allOf"].([]any)
	if !ok || len(allOf) == 0 {
		t.Fatalf("provider_info schema must encode isolation cross-field rules, allOf=%#v", schema["allOf"])
	}
}

func TestDedicatedUpstreamCiphertextVersionInt64Contract(t *testing.T) {
	field, ok := reflect.TypeOf(types.DedicatedUpstreamCredential{}).FieldByName("CiphertextVersion")
	if !ok {
		t.Fatal("DedicatedUpstreamCredential missing CiphertextVersion")
	}
	if field.Type.Kind() != reflect.Int64 {
		t.Fatalf("CiphertextVersion kind = %s, want int64", field.Type.Kind())
	}

	path := filepath.Join(runtimeContractsDir, "key_meta.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal key_meta schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	dedicated := properties["dedicated_upstream"].(map[string]any)
	dedicatedProps := dedicated["properties"].(map[string]any)
	ciphertextVersion := dedicatedProps["ciphertext_version"].(map[string]any)
	if ciphertextVersion["type"] != "integer" || ciphertextVersion["format"] != "int64" {
		t.Fatalf("ciphertext_version schema = %#v, want integer format int64", ciphertextVersion)
	}
}

// ADR-0015 S5b: the product-level protocol_strategy loss switch is retired.
// The runtime schema must no longer expose it, while the ADR-0011 allow_*
// bridge flags stay intact.
func TestProductRuntimePolicyRetiredProtocolStrategyContract(t *testing.T) {
	path := filepath.Join(runtimeContractsDir, "product_runtime_policy.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal product_runtime_policy schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["protocol_strategy"]; ok {
		t.Fatalf("product_runtime_policy schema must not expose retired protocol_strategy")
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("product_runtime_policy additionalProperties = %v, want false", schema["additionalProperties"])
	}
	for _, field := range []string{"allow_high_loss_bridge_when_no_native", "allow_bridge_on_native_failure"} {
		prop, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("product_runtime_policy schema missing %s", field)
		}
		if prop["type"] != "boolean" || prop["default"] != false {
			t.Fatalf("%s schema = %#v, want boolean default false", field, prop)
		}
	}
}

func TestProtocolBindingsContractBridgeLossyTierAndResponsesSubset(t *testing.T) {
	contract := types.EmbeddedProtocolBindingsContract()
	if !reflect.DeepEqual(contract.LossyTiers, types.KnownLossyTiers()) {
		t.Fatalf("lossy_tiers = %#v, want %#v", contract.LossyTiers, types.KnownLossyTiers())
	}
	// ADR-0015 S5b: the product/route loss-strategy enum is retired; the
	// contract JSON must no longer carry a protocol_strategies array.
	rawContract := map[string]any{}
	if err := json.Unmarshal(mustReadProtocolBindingsContractJSON(t), &rawContract); err != nil {
		t.Fatalf("unmarshal protocol bindings contract JSON: %v", err)
	}
	if _, ok := rawContract["protocol_strategies"]; ok {
		t.Fatal("protocol_bindings_contract.json must not carry retired protocol_strategies")
	}
	if !reflect.DeepEqual(contract.DedicatedProtocolChannels, types.KnownDedicatedProtocolChannels()) {
		t.Fatalf("dedicated_protocol_channels = %#v, want %#v", contract.DedicatedProtocolChannels, types.KnownDedicatedProtocolChannels())
	}

	bridges := map[string]types.ProtocolBridgeContract{}
	for _, bridge := range contract.BridgeContracts {
		bridges[bridge.Bridge] = bridge
	}
	anthropicBridge := bridges[types.ProtocolBridgeAnthropicToOpenAIChat]
	if anthropicBridge.LossyTier != types.LossyTierHigh {
		t.Fatalf("anthropic_to_openai_chat lossy_tier=%q, want high", anthropicBridge.LossyTier)
	}
	responsesBridge := bridges[types.ProtocolBridgeResponsesToChat]
	if responsesBridge.LossyTier != types.LossyTierLow {
		t.Fatalf("responses_to_chat lossy_tier=%q, want low", responsesBridge.LossyTier)
	}
	wantRequestItems := []string{"function_call", "function_call_output", "input_text", "message"}
	if !reflect.DeepEqual(responsesBridge.AdmittedSubset.Request.AllowedItemTypes, wantRequestItems) {
		t.Fatalf("responses request.allowed_item_types=%#v, want %#v", responsesBridge.AdmittedSubset.Request.AllowedItemTypes, wantRequestItems)
	}
	wantOutputTypes := []string{"function_call", "message"}
	if !reflect.DeepEqual(responsesBridge.AdmittedSubset.Response.AllowedOutputTypes, wantOutputTypes) {
		t.Fatalf("responses response.allowed_output_types=%#v, want %#v", responsesBridge.AdmittedSubset.Response.AllowedOutputTypes, wantOutputTypes)
	}
	wantEvents := []string{
		"completed",
		"final_usage",
		"function_call_arguments.delta",
		"function_call_arguments.done",
		"response.output_item.added",
		"response.output_text.delta",
	}
	if !reflect.DeepEqual(responsesBridge.AdmittedSubset.Streaming.AllowedEvents, wantEvents) {
		t.Fatalf("responses streaming.allowed_events=%#v, want %#v", responsesBridge.AdmittedSubset.Streaming.AllowedEvents, wantEvents)
	}
}

func mustReadProtocolBindingsContractJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "protocol_bindings_contract.json"))
	if err != nil {
		t.Fatalf("read protocol bindings contract JSON: %v", err)
	}
	return raw
}

func TestProtocolBindingsContractManagedUnderContracts(t *testing.T) {
	sourcePath := filepath.Join("testdata", "protocol_bindings_contract.json")
	managedPath := filepath.Join(runtimeContractsDir, "protocol_bindings_contract.json")

	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source protocol bindings contract: %v", err)
	}
	if os.Getenv(runtimeUpdateEnv) == "1" {
		if err := os.MkdirAll(runtimeContractsDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", runtimeContractsDir, err)
		}
		if err := os.WriteFile(managedPath, source, 0o644); err != nil {
			t.Fatalf("write %s: %v", managedPath, err)
		}
		t.Logf("seeded %s", managedPath)
		return
	}

	managed, err := os.ReadFile(managedPath)
	if err != nil {
		t.Fatalf("read managed protocol bindings contract: %v\n\nRun:\n  %s=1 go test ./internal/types -run TestProtocolBindingsContractManagedUnderContracts", err, runtimeUpdateEnv)
	}
	if !bytes.Equal(source, managed) {
		t.Fatalf("contracts/runtime/protocol_bindings_contract.json drifted from internal/types/testdata/protocol_bindings_contract.json")
	}
}

func TestRuntimeGoldenFixturesDecodeIntoGoContracts(t *testing.T) {
	entries, err := os.ReadDir(goldenContractsDir)
	if err != nil {
		t.Fatalf("read golden contracts dir: %v", err)
	}

	required := map[string]bool{
		"key_meta":               false,
		"product_route_graph":    false,
		"product_runtime_policy": false,
		"provider_info":          false,
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(goldenContractsDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden fixture %s: %v", entry.Name(), err)
		}

		switch {
		case strings.HasPrefix(entry.Name(), "key_meta."):
			var v types.KeyMeta
			decodeGoldenStrict(t, entry.Name(), raw, &v)
			required["key_meta"] = true
		case strings.HasPrefix(entry.Name(), "key_pool."):
			var v types.KeyPoolConfig
			decodeGoldenStrict(t, entry.Name(), raw, &v)
		case strings.HasPrefix(entry.Name(), "model_catalog."):
			var v types.ModelCatalogEntry
			decodeGoldenStrict(t, entry.Name(), raw, &v)
		case strings.HasPrefix(entry.Name(), "platform_config."):
			var v types.PlatformConfig
			decodeGoldenStrict(t, entry.Name(), raw, &v)
		case strings.HasPrefix(entry.Name(), "product_route_graph."):
			var v types.ProductRouteGraph
			decodeGoldenStrict(t, entry.Name(), raw, &v)
			required["product_route_graph"] = true
		case strings.HasPrefix(entry.Name(), "product_runtime_policy."):
			var v types.ProductRuntimePolicy
			decodeGoldenStrict(t, entry.Name(), raw, &v)
			required["product_runtime_policy"] = true
		case strings.HasPrefix(entry.Name(), "provider_info."):
			var v types.ProviderInfo
			decodeGoldenStrict(t, entry.Name(), raw, &v)
			required["provider_info"] = true
		case strings.HasPrefix(entry.Name(), "route_config."):
			var v types.RouteConfig
			decodeGoldenStrict(t, entry.Name(), raw, &v)
		case strings.HasPrefix(entry.Name(), "runtime_intent."):
			// The intent package compiles RuntimeIntent golden fixtures.
		default:
			t.Fatalf("unclassified golden fixture %s", entry.Name())
		}
	}

	for family, seen := range required {
		if !seen {
			t.Fatalf("contracts/golden missing required %s fixture", family)
		}
	}
}

func TestRuntimeGoldenSchemaVersionsMatchPublisherConstants(t *testing.T) {
	paths := goldenJSONPaths(t, goldenContractsDir)
	for _, path := range paths {
		rel, err := filepath.Rel(goldenContractsDir, path)
		if err != nil {
			t.Fatalf("rel %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(path)

		raw := readGoldenObject(t, path)
		switch {
		case strings.HasPrefix(base, "provider_info."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentProviderInfoSchemaVersion)
		case strings.HasPrefix(base, "route_config."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentRouteConfigSchemaVersion)
		case strings.HasPrefix(base, "key_meta."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentKeyMetaSchemaVersion)
		case strings.HasPrefix(base, "key_pool."):
			meta, ok := raw["meta"].(map[string]any)
			if !ok {
				t.Fatalf("%s $.meta missing or non-object", rel)
			}
			assertGoldenSchemaVersion(t, rel, "$.meta.schema_version", meta["schema_version"], publisher.CurrentKeyPoolMetaSchemaVersion)
			entries, ok := raw["entries"].([]any)
			if !ok {
				t.Fatalf("%s $.entries missing or non-array", rel)
			}
			for i, entry := range entries {
				entryObj, ok := entry.(map[string]any)
				if !ok {
					t.Fatalf("%s $.entries[%d] non-object", rel, i)
				}
				assertGoldenSchemaVersion(t, rel, "$.entries["+strconv.Itoa(i)+"].schema_version", entryObj["schema_version"], publisher.CurrentKeyPoolEntrySchemaVersion)
			}
		case strings.HasPrefix(base, "platform_config."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentPlatformConfigSchemaVersion)
		case strings.HasPrefix(base, "product_route_graph."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentProductRuntimePolicySchemaVersion)
		case strings.HasPrefix(base, "product_runtime_policy."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentProductRuntimePolicySchemaVersion)
			if routeGraph, ok := raw["route_graph"].(map[string]any); ok {
				assertGoldenSchemaVersion(t, rel, "$.route_graph.schema_version", routeGraph["schema_version"], publisher.CurrentProductRuntimePolicySchemaVersion)
			}
		case strings.HasPrefix(base, "model_catalog."):
			assertGoldenSchemaVersion(t, rel, "$.schema_version", raw["schema_version"], publisher.CurrentModelCatalogEntrySchemaVersion)
		case strings.HasPrefix(base, "runtime_intent."):
			continue
		default:
			t.Fatalf("unclassified golden fixture %s", rel)
		}
	}
}

type runtimeSchemaContract struct {
	title   string
	v       any
	file    string
	id      string
	version int
	schema  func() map[string]any
}

func runtimeSchemaContracts() []runtimeSchemaContract {
	keyPoolVersion := publisher.CurrentKeyPoolMetaSchemaVersion
	if keyPoolVersion != publisher.CurrentKeyPoolEntrySchemaVersion {
		panic("key pool meta and entry schema versions diverged; split the runtime contract before regenerating schemas")
	}
	return []runtimeSchemaContract{
		{
			title:   "ProviderInfo",
			v:       &types.ProviderInfo{},
			file:    "provider_info.schema.json",
			id:      runtimeSchemaID("provider_info", publisher.CurrentProviderInfoSchemaVersion),
			version: publisher.CurrentProviderInfoSchemaVersion,
			schema:  providerInfoRuntimeSchema,
		},
		{
			title:   "RouteConfig",
			v:       &types.RouteConfig{},
			file:    "route_config.schema.json",
			id:      runtimeSchemaID("route_config", publisher.CurrentRouteConfigSchemaVersion),
			version: publisher.CurrentRouteConfigSchemaVersion,
			schema:  routeConfigRuntimeSchema,
		},
		{
			title:   "KeyMeta",
			v:       &types.KeyMeta{},
			file:    "key_meta.schema.json",
			id:      runtimeSchemaID("key_meta", publisher.CurrentKeyMetaSchemaVersion),
			version: publisher.CurrentKeyMetaSchemaVersion,
			schema:  keyMetaRuntimeSchema,
		},
		{
			title:   "KeyPool",
			v:       &types.KeyPoolConfig{},
			file:    "key_pool.schema.json",
			id:      runtimeSchemaID("key_pool", keyPoolVersion),
			version: keyPoolVersion,
			schema:  keyPoolRuntimeSchema,
		},
		{
			title:   "PlatformConfig",
			v:       &types.PlatformConfig{},
			file:    "platform_config.schema.json",
			id:      runtimeSchemaID("platform_config", publisher.CurrentPlatformConfigSchemaVersion),
			version: publisher.CurrentPlatformConfigSchemaVersion,
		},
		{
			title:   "ProductRouteGraph",
			file:    "product_route_graph.schema.json",
			id:      runtimeSchemaID("product_route_graph", publisher.CurrentProductRuntimePolicySchemaVersion),
			version: publisher.CurrentProductRuntimePolicySchemaVersion,
			schema:  productRouteGraphRuntimeSchema,
		},
		{
			title:   "ProductRuntimePolicy",
			file:    "product_runtime_policy.schema.json",
			id:      runtimeSchemaID("product_runtime_policy", publisher.CurrentProductRuntimePolicySchemaVersion),
			version: publisher.CurrentProductRuntimePolicySchemaVersion,
			schema:  productRuntimePolicyRuntimeSchema,
		},
	}
}

func providerInfoRuntimeSchema() map[string]any {
	actual := structSchema(reflect.TypeOf(types.ProviderInfo{}))
	props := actual["properties"].(map[string]any)
	props["provider_account_ref"] = map[string]any{
		"type":      "string",
		"minLength": 1,
		"pattern":   types.ProviderAccountRefPattern,
		"maxLength": types.ProviderAccountRefMaxLength,
	}
	props["provider_profile"] = runtimeEnumSchema(types.KnownProviderProfiles()...)
	props["visibility"] = runtimeSchemaWithDefault(runtimeEnumSchema(types.KnownProviderVisibilities()...), types.ProviderVisibilityProductRoute)
	props["credential_source"] = runtimeSchemaWithDefault(runtimeEnumSchema(types.KnownProviderCredentialSources()...), types.ProviderCredentialSourceKeypool)
	routeKinds := props["allowed_route_kinds"].(map[string]any)
	routeKinds["items"] = runtimeEnumSchema(types.KnownProviderRouteKinds()...)
	routeKinds["minItems"] = 1
	routeKinds["uniqueItems"] = true
	routeKinds["default"] = []string{types.ProviderRouteKindModel, types.ProviderRouteKindProduct}
	actual["allOf"] = runtimeProviderIsolationConsistencyRules()
	return actual
}

func keyMetaRuntimeSchema() map[string]any {
	actual := structSchema(reflect.TypeOf(types.KeyMeta{}))
	props := actual["properties"].(map[string]any)
	dedicated := props["dedicated_upstream"].(map[string]any)
	dedicatedProps := dedicated["properties"].(map[string]any)
	dedicatedProps["provider_account_ref"] = map[string]any{
		"type":      "string",
		"minLength": 1,
		"pattern":   types.ProviderAccountRefPattern,
		"maxLength": types.ProviderAccountRefMaxLength,
	}

	updatedAt := dedicatedProps["updated_at"].(map[string]any)
	updatedAt["format"] = "date-time"

	ciphertextVersion := dedicatedProps["ciphertext_version"].(map[string]any)
	ciphertextVersion["format"] = "int64"

	endpoints := dedicatedProps["endpoints"].(map[string]any)
	items := endpoints["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	itemProps["client_protocol"] = runtimeEnumSchema(types.KnownDedicatedClientProtocols()...)
	itemProps["protocol_strategy"] = runtimeEnumSchema(types.KnownDedicatedProtocolChannels()...)
	return actual
}

func routeConfigRuntimeSchema() map[string]any {
	return structSchema(reflect.TypeOf(types.RouteConfig{}))
}

func keyPoolRuntimeSchema() map[string]any {
	actual := structSchema(reflect.TypeOf(types.KeyPoolConfig{}))
	entries := actual["properties"].(map[string]any)["entries"].(map[string]any)
	entry := entries["items"].(map[string]any)
	properties := entry["properties"].(map[string]any)
	properties["provider_account_ref"] = map[string]any{
		"type":      "string",
		"minLength": 1,
		"pattern":   types.ProviderAccountRefPattern,
		"maxLength": types.ProviderAccountRefMaxLength,
	}
	return actual
}

func productRuntimePolicyRuntimeSchema() map[string]any {
	actual := structSchema(reflect.TypeOf(types.ProductRuntimePolicy{}))
	props := actual["properties"].(map[string]any)
	props["route_graph"] = runtimeRefSchema("product_route_graph.schema.json")
	props["allow_high_loss_bridge_when_no_native"].(map[string]any)["default"] = false
	props["allow_bridge_on_native_failure"].(map[string]any)["default"] = false
	return actual
}

func productRouteGraphRuntimeSchema() map[string]any {
	return runtimeObjectSchema([]string{"assignments", "default_target", "groups", "product_channels", "schema_version"}, map[string]any{
		"schema_version": map[string]any{"type": "integer", "const": publisher.CurrentProductRuntimePolicySchemaVersion},
		"default_target": runtimeTargetSchema(),
		"groups": runtimeArraySchema(runtimeObjectSchema([]string{"code", "id", "members", "status", "strategy"}, map[string]any{
			"id":       map[string]any{"type": "string"},
			"code":     map[string]any{"type": "string"},
			"status":   runtimeEnumSchema("active", "disabled"),
			"strategy": runtimeEnumSchema("weighted_failover", "priority_failover", "round_robin", "cost_first", "latency_first"),
			"members": runtimeArraySchema(runtimeObjectSchema([]string{"channel_id", "priority", "product_channel_id", "provider_id", "role", "weight"}, map[string]any{
				"product_channel_id": map[string]any{"type": "string"},
				"channel_id":         map[string]any{"type": "string"},
				"provider_id":        map[string]any{"type": "string"},
				"priority":           map[string]any{"type": "integer"},
				"weight":             map[string]any{"type": "integer"},
				"role":               runtimeEnumSchema("primary", "fallback", "shadow"),
				"max_rpm":            map[string]any{"type": "integer"},
				"max_tpm":            map[string]any{"type": "integer"},
			})),
		})),
		"product_channels": map[string]any{
			"type":     "array",
			"minItems": 1,
			"items": runtimeObjectSchema([]string{"allowed_models", "channel_id", "denied_models", "id", "priority", "provider_id", "status", "weight"}, map[string]any{
				"id":             map[string]any{"type": "string"},
				"channel_id":     map[string]any{"type": "string"},
				"provider_id":    map[string]any{"type": "string"},
				"allowed_models": runtimeArraySchema(map[string]any{"type": "string"}),
				"denied_models":  runtimeArraySchema(map[string]any{"type": "string"}),
				"priority":       map[string]any{"type": "integer"},
				"weight":         map[string]any{"type": "integer"},
				"max_rpm":        map[string]any{"type": "integer"},
				"max_tpm":        map[string]any{"type": "integer"},
				"status":         runtimeEnumSchema("active", "disabled", "testing"),
			}),
		},
		"assignments": runtimeArraySchema(runtimeObjectSchema([]string{"id", "mode", "priority", "product_id", "subject_id", "subject_type", "target_id", "target_type"}, map[string]any{
			"id":                   map[string]any{"type": "string"},
			"product_id":           map[string]any{"type": "string"},
			"subject_type":         runtimeEnumSchema("USER", "USER_SEGMENT"),
			"subject_id":           map[string]any{"type": "string"},
			"model_pattern":        map[string]any{"type": "string"},
			"client_compatibility": map[string]any{"type": "string"},
			"target_type":          runtimeEnumSchema("GROUP", "PRODUCT_CHANNEL"),
			"target_id":            map[string]any{"type": "string"},
			"mode":                 runtimeEnumSchema("ONLY", "PREFER"),
			"priority":             map[string]any{"type": "integer"},
		})),
	})
}

func runtimeTargetSchema() map[string]any {
	return runtimeObjectSchema([]string{"target_id", "target_type"}, map[string]any{
		"target_type": runtimeEnumSchema("GROUP", "PRODUCT_CHANNEL"),
		"target_id":   map[string]any{"type": "string"},
	})
}

func runtimeObjectSchema(required []string, properties map[string]any) map[string]any {
	out := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func runtimeArraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func runtimeEnumSchema(values ...string) map[string]any {
	raw := make([]any, 0, len(values))
	for _, value := range values {
		raw = append(raw, value)
	}
	return map[string]any{"type": "string", "enum": raw}
}

func runtimeSchemaWithDefault(schema map[string]any, defaultValue any) map[string]any {
	schema["default"] = defaultValue
	return schema
}

func runtimeProviderIsolationConsistencyRules() []any {
	return []any{
		map[string]any{
			"if":   runtimeProviderIsolationWhen("visibility", map[string]any{"const": types.ProviderVisibilityDedicatedOnly}),
			"then": runtimeProviderIsolationDedicatedThen(),
		},
		map[string]any{
			"if":   runtimeProviderIsolationWhen("allowed_route_kinds", runtimeDedicatedRouteOnlySchema()),
			"then": runtimeProviderIsolationDedicatedThen(),
		},
		map[string]any{
			"if":   runtimeProviderIsolationWhen("credential_source", runtimeDedicatedCredentialSourceSchema()),
			"then": runtimeProviderIsolationDedicatedThen(),
		},
	}
}

func runtimeProviderIsolationWhen(field string, propertySchema map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"required":   []string{field},
		"properties": map[string]any{field: propertySchema},
	}
}

func runtimeProviderIsolationDedicatedThen() map[string]any {
	// ADR-0003 rev (2026-06-25) / ADR-0009: no BYOK, owner_scope is optional +
	// coarse for dedicated providers; tenant_id is not mandatory. Only
	// visibility / credential_source / allowed_route_kinds stay load-bearing.
	return map[string]any{
		"required": []string{"visibility", "credential_source", "allowed_route_kinds"},
		"properties": map[string]any{
			"visibility":          map[string]any{"const": types.ProviderVisibilityDedicatedOnly},
			"credential_source":   runtimeDedicatedCredentialSourceSchema(),
			"allowed_route_kinds": runtimeDedicatedRouteOnlySchema(),
		},
	}
}

func runtimeDedicatedCredentialSourceSchema() map[string]any {
	return map[string]any{"enum": []string{
		types.ProviderCredentialSourceDedicatedUpstream,
		types.ProviderCredentialSourceProviderEntitlement,
	}}
}

func runtimeDedicatedRouteOnlySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"items":    map[string]any{"const": types.ProviderRouteKindDedicated},
		"minItems": 1,
		"maxItems": 1,
	}
}

func toAnyStrings(values []string) []any {
	raw := make([]any, 0, len(values))
	for _, value := range values {
		raw = append(raw, value)
	}
	return raw
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func runtimeRefSchema(ref string) map[string]any {
	return map[string]any{"$ref": ref}
}

func runtimeSchemaID(name string, version int) string {
	return "https://schemas.tokenplatform.dev/publisher/runtime/" + name + ".v" + strconv.Itoa(version) + ".schema.json"
}

func decodeGoldenStrict(t *testing.T, name string, raw []byte, dst any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("decode %s into Go contract type: %v", name, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("decode %s trailing JSON: %v", name, err)
	}
}

func goldenJSONPaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatalf("walk golden contracts dir: %v", err)
	}
	sort.Strings(paths)
	return paths
}

func readGoldenObject(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden fixture %s: %v", path, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal golden fixture %s: %v", path, err)
	}
	return obj
}

func assertGoldenSchemaVersion(t *testing.T, rel, jsonPath string, got any, want int) {
	t.Helper()
	gotFloat, ok := got.(float64)
	if !ok {
		t.Fatalf("%s %s schema_version missing or non-number: %#v", rel, jsonPath, got)
	}
	if int(gotFloat) != want {
		t.Fatalf("%s %s schema_version = %d, want publisher runtime schema version %d", rel, jsonPath, int(gotFloat), want)
	}
}

func structSchema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return schemaForType(t)
	}

	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, omitempty := parseJSONTag(tag)
		if name == "" {
			continue
		}
		schema := schemaForType(f.Type)
		if f.Type.Kind() == reflect.Ptr && !omitempty {
			schema = nullableSchema(schema)
		}
		props[name] = schema
		if !omitempty && f.Type.Kind() != reflect.Ptr {
			required = append(required, name)
		}
	}
	sort.Strings(required)

	node := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		node["required"] = required
	}
	return node
}

func schemaForType(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaForType(t.Elem())}
	case reflect.Map:
		node := map[string]any{"type": "object"}
		if t.Elem().Kind() != reflect.Interface {
			node["additionalProperties"] = schemaForType(t.Elem())
		}
		return node
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		return structSchema(t)
	}
	return map[string]any{}
}

func nullableSchema(node map[string]any) map[string]any {
	out := make(map[string]any, len(node)+1)
	for k, v := range node {
		out[k] = v
	}
	if typ, ok := out["type"].(string); ok {
		out["type"] = []string{typ, "null"}
		return out
	}
	out["anyOf"] = []any{map[string]any{"type": "null"}}
	return out
}

func parseJSONTag(tag string) (name string, omitempty bool) {
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return
}
