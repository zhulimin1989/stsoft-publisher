package intent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	contracttypes "github.com/tokenplatform/publisher/internal/types"
)

func TestIntentSchemaContracts(t *testing.T) {
	for _, c := range intentSchemaContracts() {
		t.Run(c.file, func(t *testing.T) {
			want := mustMarshalSchema(t, c.schema())
			path := filepath.Join("..", "..", "contracts", "intent", c.file)
			if os.Getenv("PUBLISHER_UPDATE_SCHEMAS") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, want, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read schema %s: %v; run PUBLISHER_UPDATE_SCHEMAS=1 go test ./internal/intent -run TestIntentSchemaContracts", path, err)
			}
			if !reflect.DeepEqual(normalizeJSON(t, got), normalizeJSON(t, want)) {
				t.Fatalf("%s drifted; run PUBLISHER_UPDATE_SCHEMAS=1 go test ./internal/intent -run TestIntentSchemaContracts", path)
			}
		})
	}
}

func TestIntentSchemaVersionsMatchContractMatrix(t *testing.T) {
	for _, c := range intentSchemaContracts() {
		schema := c.schema()
		if schema["version"] != c.version {
			t.Fatalf("%s version=%v want %d", c.file, schema["version"], c.version)
		}
	}
}

func TestProviderEndpointSchemaIncludesProviderIsolationFields(t *testing.T) {
	schema := providerEndpointSchema()
	properties := schema["properties"].(map[string]any)
	required := schema["required"].([]string)
	for _, field := range []string{"provider_account_ref", "visibility", "credential_source", "allowed_route_kinds", "owner_scope"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("provider_endpoint schema missing %s", field)
		}
		if containsString(required, field) {
			t.Fatalf("%s must remain optional on ProviderEndpoint, required=%#v", field, required)
		}
	}
	accountRef := properties["provider_account_ref"].(map[string]any)
	if accountRef["minLength"] != 1 || accountRef["pattern"] != contracttypes.ProviderAccountRefPattern || accountRef["maxLength"] != contracttypes.ProviderAccountRefMaxLength {
		t.Fatalf("provider endpoint provider_account_ref constraints=%#v", accountRef)
	}

	visibility := properties["visibility"].(map[string]any)
	if !reflect.DeepEqual(visibility["enum"], []any{"product_route", "dedicated_only", "internal"}) {
		t.Fatalf("visibility schema = %#v", visibility)
	}
	credentialSource := properties["credential_source"].(map[string]any)
	if !reflect.DeepEqual(credentialSource["enum"], []any{"keypool", "dedicated_upstream", "provider_entitlement"}) {
		t.Fatalf("credential_source schema = %#v", credentialSource)
	}
	routeKinds := properties["allowed_route_kinds"].(map[string]any)
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
	if !providerEntitlementRuleRequiresAccountRef(schema["allOf"].([]any)) {
		t.Fatal("provider_endpoint schema must conditionally require provider_account_ref for provider_entitlement")
	}
}

func providerEntitlementRuleRequiresAccountRef(rules []any) bool {
	for _, rawRule := range rules {
		rule := rawRule.(map[string]any)
		ifSchema := rule["if"].(map[string]any)
		ifProps := ifSchema["properties"].(map[string]any)
		credentialSource, ok := ifProps["credential_source"].(map[string]any)
		if !ok || credentialSource["const"] != contracttypes.ProviderCredentialSourceProviderEntitlement {
			continue
		}
		thenSchema := rule["then"].(map[string]any)
		return containsString(thenSchema["required"].([]string), "provider_account_ref")
	}
	return false
}

func TestProviderCredentialSchemaRequiresProviderAccountRef(t *testing.T) {
	schema := providerCredentialSchema()
	properties := schema["properties"].(map[string]any)
	got, ok := properties["provider_account_ref"].(map[string]any)
	if !ok || got["type"] != "string" {
		t.Fatalf("provider_account_ref schema=%#v want string", properties["provider_account_ref"])
	}
	if got["minLength"] != 1 || got["pattern"] != contracttypes.ProviderAccountRefPattern || got["maxLength"] != contracttypes.ProviderAccountRefMaxLength {
		t.Fatalf("provider_account_ref constraints=%#v", got)
	}
	if required := schema["required"].([]string); !containsString(required, "provider_account_ref") {
		t.Fatalf("provider_account_ref must be required for intent writes, required=%#v", required)
	}
}

type intentSchemaContract struct {
	file    string
	version int
	schema  func() map[string]any
}

func intentSchemaContracts() []intentSchemaContract {
	return []intentSchemaContract{
		{file: "provider_endpoint.schema.json", version: 2, schema: providerEndpointSchema},
		{file: "provider_credential.schema.json", version: 2, schema: providerCredentialSchema},
		{file: "client_exposure.schema.json", version: 1, schema: clientExposureSchema},
		{file: "model_route_intent.schema.json", version: 1, schema: modelRouteIntentSchema},
		{file: "product_route_policy.schema.json", version: 1, schema: productRoutePolicySchema},
		{file: "product_policy_delete.schema.json", version: 1, schema: productPolicyDeleteSchema},
		{file: "runtime_intent.schema.json", version: 2, schema: runtimeIntentSchema},
	}
}

func mustMarshalSchema(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return append(raw, '\n')
}

func normalizeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	return value
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
