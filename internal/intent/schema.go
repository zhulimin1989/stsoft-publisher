package intent

import contracttypes "github.com/tokenplatform/publisher/internal/types"

func providerEndpointSchema() map[string]any {
	schema := objectSchemaVersion("ProviderEndpoint", "https://tokenplatform.local/contracts/intent/provider_endpoint.schema.json", CurrentIntentSchemaVersion, []string{"id", "provider_profile", "base_url", "status"}, map[string]any{
		"id":                   stringSchema(),
		"provider_account_ref": providerAccountRefSchema(),
		"provider_profile":     enumStringSchema(contracttypes.KnownProviderProfiles()),
		"base_url":             stringSchema(),
		"region":               stringSchema(),
		"status":               stringSchema(),
		"name":                 stringSchema(),
		"code":                 stringSchema(),
		"owned_by":             stringSchema(),
		"key_strategy":         stringSchema(),
		"path_prefix":          stringSchema(),
		"visibility":           providerVisibilitySchema(),
		"credential_source":    providerCredentialSourceSchema(),
		"allowed_route_kinds":  providerRouteKindsSchema(),
		"owner_scope":          ownerScopeSchema(),
	})
	schema["allOf"] = providerIsolationConsistencyRules()
	return schema
}

func providerCredentialSchema() map[string]any {
	return objectSchemaVersion("ProviderCredential", "https://tokenplatform.local/contracts/intent/provider_credential.schema.json", CurrentIntentSchemaVersion, []string{"provider_id", "key_ref", "provider_account_ref", "status"}, map[string]any{
		"provider_id":          stringSchema(),
		"key_ref":              stringSchema(),
		"key_id":               stringSchema(),
		"provider_account_ref": providerAccountRefSchema(),
		"alias":                stringSchema(),
		"rpm_limit":            integerSchema(),
		"tpm_limit":            integerSchema(),
		"status":               stringSchema(),
		"allowed_models":       stringArraySchema(),
		"priority":             integerSchema(),
		"weight":               integerSchema(),
	})
}

func providerAccountRefSchema() map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 1,
		"pattern":   contracttypes.ProviderAccountRefPattern,
		"maxLength": contracttypes.ProviderAccountRefMaxLength,
	}
}

func clientExposureSchema() map[string]any {
	// ADR-0012 S4: served_inbound_protocols (explicit inbound Wire dialects) was
	// the single required compile input. The legacy client_compatibility
	// (App-list) input is removed; client compatibility is a derived output.
	//
	// ADR-0016 U1: paths[] (explicit path declarations: inbound_protocol +
	// channel) supersedes served_inbound_protocols as the compile input. During
	// the transition both are legal; when paths is present it wins and
	// served_inbound_protocols is ignored. served_inbound_protocols stays
	// required until U4 retires it (rollback safety: producers keep emitting it).
	return objectSchema("ClientExposure", "https://tokenplatform.local/contracts/intent/client_exposure.schema.json", []string{"provider_id", "served_inbound_protocols"}, map[string]any{
		"provider_id":              stringSchema(),
		"served_inbound_protocols": deprecatedSchema(enumStringArraySchema(contracttypes.KnownServedInboundProtocols()), "Deprecated input (ADR-0016): superseded by paths[]. Still legal during the transition; ignored when paths is present."),
		"paths":                    describeSchema(arraySchema(protocolPathSchema()), "ADR-0016: explicit path declarations, each = (inbound_protocol, channel). When present this is the compile input for the wire-protocol axis and served_inbound_protocols is ignored. Capabilities remain an orthogonal axis."),
		"capabilities":             enumStringArraySchema(contracttypes.EmbeddedProtocolBindingsContract().Capabilities),
		"token_count_strategy":     enumStringSchema(contracttypes.KnownTokenCountStrategies()),
		"protocol_path_prefix":     stringSchema(),
	})
}

func protocolPathSchema() map[string]any {
	return objectSchema("ProtocolPath", "", []string{"inbound_protocol", "channel"}, map[string]any{
		"inbound_protocol": enumStringSchema(contracttypes.KnownServedInboundProtocols()),
		"channel":          enumStringSchema(contracttypes.KnownDedicatedProtocolChannels()),
	})
}

func modelRouteIntentSchema() map[string]any {
	return objectSchema("ModelRouteIntent", "https://tokenplatform.local/contracts/intent/model_route_intent.schema.json", []string{"logical_model", "routes"}, map[string]any{
		"logical_model":     stringSchema(),
		"display_name":      stringSchema(),
		"capabilities":      enumStringArraySchema(contracttypes.EmbeddedProtocolBindingsContract().Capabilities),
		"context_window":    integerSchema(),
		"max_output":        integerSchema(),
		"owned_by":          stringSchema(),
		"provider_strategy": stringSchema(),
		"fallback_model":    stringSchema(),
		"routes": arraySchema(objectSchema("ModelRouteTarget", "", []string{"provider_id", "upstream_model", "status"}, map[string]any{
			"provider_id":    stringSchema(),
			"upstream_model": stringSchema(),
			"priority":       integerSchema(),
			"weight":         integerSchema(),
			"status":         stringSchema(),
			"capabilities":   enumStringArraySchema(contracttypes.EmbeddedProtocolBindingsContract().Capabilities),
		})),
	})
}

func productRoutePolicySchema() map[string]any {
	return objectSchema("ProductRoutePolicy", "https://tokenplatform.local/contracts/intent/product_route_policy.schema.json", []string{"product_id", "status", "credential_source", "model_scope_mode", "fallback_policy", "billing_mode"}, map[string]any{
		"product_id":                            stringSchema(),
		"revision":                              integerSchema(),
		"product_slug":                          stringSchema(),
		"status":                                stringSchema(),
		"credential_source":                     stringSchema(),
		"allowed_provider_ids":                  stringArraySchema(),
		"allowed_channel_ids":                   stringArraySchema(),
		"allowed_models":                        stringArraySchema(),
		"model_scope_mode":                      stringSchema(),
		"fallback_policy":                       stringSchema(),
		"billing_mode":                          stringSchema(),
		"route_strategy":                        stringSchema(),
		"allow_high_loss_bridge_when_no_native": booleanSchemaWithDefault(false),
		"allow_bridge_on_native_failure":        booleanSchemaWithDefault(false),
		"route_graph_required":                  booleanSchema(),
		"route_graph_enforcement":               stringSchema(),
		"route_graph":                           refSchema("../runtime/product_route_graph.schema.json"),
	})
}

func productPolicyDeleteSchema() map[string]any {
	return objectSchema("ProductPolicyDelete", "https://tokenplatform.local/contracts/intent/product_policy_delete.schema.json", []string{"product_id", "revision"}, map[string]any{
		"product_id": stringSchema(),
		"revision":   integerSchema(),
	})
}

func runtimeIntentSchema() map[string]any {
	return objectSchemaVersion("RuntimeIntent", "https://tokenplatform.local/contracts/intent/runtime_intent.schema.json", CurrentIntentSchemaVersion, []string{"schema_version"}, map[string]any{
		"schema_version":         map[string]any{"type": "integer", "const": CurrentIntentSchemaVersion},
		"provider_endpoints":     arraySchema(refSchema("provider_endpoint.schema.json")),
		"provider_credentials":   arraySchema(refSchema("provider_credential.schema.json")),
		"client_exposures":       arraySchema(refSchema("client_exposure.schema.json")),
		"model_routes":           arraySchema(refSchema("model_route_intent.schema.json")),
		"product_policies":       arraySchema(refSchema("product_route_policy.schema.json")),
		"product_policy_deletes": arraySchema(refSchema("product_policy_delete.schema.json")),
	})
}

func objectSchema(title, id string, required []string, properties map[string]any) map[string]any {
	return objectSchemaVersion(title, id, 1, required, properties)
}

func objectSchemaVersion(title, id string, version int, required []string, properties map[string]any) map[string]any {
	out := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                title,
		"version":              version,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if id != "" {
		out["$id"] = id
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringSchema() map[string]any {
	return map[string]any{"type": "string"}
}

func describeSchema(schema map[string]any, description string) map[string]any {
	schema["description"] = description
	return schema
}

func deprecatedSchema(schema map[string]any, description string) map[string]any {
	schema["deprecated"] = true
	return describeSchema(schema, description)
}

func integerSchema() map[string]any {
	return map[string]any{"type": "integer"}
}

func booleanSchema() map[string]any {
	return map[string]any{"type": "boolean"}
}

func booleanSchemaWithDefault(value bool) map[string]any {
	return map[string]any{"type": "boolean", "default": value}
}

func providerVisibilitySchema() map[string]any {
	schema := enumStringSchema(contracttypes.KnownProviderVisibilities())
	schema["default"] = contracttypes.ProviderVisibilityProductRoute
	return schema
}

func providerCredentialSourceSchema() map[string]any {
	schema := enumStringSchema(contracttypes.KnownProviderCredentialSources())
	schema["default"] = contracttypes.ProviderCredentialSourceKeypool
	return schema
}

func providerRouteKindsSchema() map[string]any {
	schema := enumStringArraySchema(contracttypes.KnownProviderRouteKinds())
	schema["minItems"] = 1
	schema["uniqueItems"] = true
	schema["default"] = []string{contracttypes.ProviderRouteKindModel, contracttypes.ProviderRouteKindProduct}
	return schema
}

func ownerScopeSchema() map[string]any {
	return objectSchema("OwnerScope", "", nil, map[string]any{
		"tenant_id":      stringSchema(),
		"product_id":     stringSchema(),
		"entitlement_id": stringSchema(),
	})
}

func stringArraySchema() map[string]any {
	return arraySchema(stringSchema())
}

func enumStringSchema(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": enumValues(values)}
}

func enumStringArraySchema(values []string) map[string]any {
	return arraySchema(enumStringSchema(values))
}

func enumValues(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func refSchema(ref string) map[string]any {
	return map[string]any{"$ref": ref}
}

func providerIsolationConsistencyRules() []any {
	return []any{
		map[string]any{
			"if":   providerIsolationWhen("visibility", map[string]any{"const": contracttypes.ProviderVisibilityDedicatedOnly}),
			"then": providerIsolationDedicatedThen(),
		},
		map[string]any{
			"if":   providerIsolationWhen("allowed_route_kinds", dedicatedRouteOnlySchema()),
			"then": providerIsolationDedicatedThen(),
		},
		map[string]any{
			"if":   providerIsolationWhen("credential_source", dedicatedCredentialSourceSchema()),
			"then": providerIsolationDedicatedThen(),
		},
		map[string]any{
			"if": providerIsolationWhen("credential_source", map[string]any{
				"const": contracttypes.ProviderCredentialSourceProviderEntitlement,
			}),
			"then": map[string]any{
				"required": []string{"provider_account_ref"},
			},
		},
	}
}

func providerIsolationWhen(field string, propertySchema map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"required":   []string{field},
		"properties": map[string]any{field: propertySchema},
	}
}

func providerIsolationDedicatedThen() map[string]any {
	// ADR-0003 rev (2026-06-25) / ADR-0009: no BYOK, so owner_scope is no longer
	// required for dedicated providers and tenant_id is not mandatory. The
	// load-bearing isolation fields remain visibility / credential_source /
	// allowed_route_kinds. owner_scope, when present, is optional + coarse.
	return map[string]any{
		"required": []string{"visibility", "credential_source", "allowed_route_kinds"},
		"properties": map[string]any{
			"visibility":          map[string]any{"const": contracttypes.ProviderVisibilityDedicatedOnly},
			"credential_source":   dedicatedCredentialSourceSchema(),
			"allowed_route_kinds": dedicatedRouteOnlySchema(),
			"owner_scope":         ownerScopeSchema(),
		},
	}
}

func dedicatedCredentialSourceSchema() map[string]any {
	return map[string]any{"enum": []string{
		contracttypes.ProviderCredentialSourceDedicatedUpstream,
		contracttypes.ProviderCredentialSourceProviderEntitlement,
	}}
}

func dedicatedRouteOnlySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"items":    map[string]any{"const": contracttypes.ProviderRouteKindDedicated},
		"minItems": 1,
		"maxItems": 1,
	}
}
