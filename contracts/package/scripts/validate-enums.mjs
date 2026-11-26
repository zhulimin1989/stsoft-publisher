import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const contractsPackage = require("../dist/index.js");
const {
  LOSSY_TIERS,
  DEDICATED_PROTOCOL_CHANNELS,
  validateProductRuntimePolicy,
  validateRouteConfig,
  validateClientExposure,
  validateKeyMeta,
  validateModelRouteIntent,
  validateProviderEndpoint,
  validateProviderInfo
} = contractsPackage;

// ADR-0015 S5b: the product/route loss-strategy enum (auto/passthrough_only)
// is retired; the package must no longer export it.
if ("PROTOCOL_STRATEGIES" in contractsPackage) {
  throw new Error(`retired PROTOCOL_STRATEGIES export resurfaced: ${JSON.stringify(contractsPackage.PROTOCOL_STRATEGIES)}`);
}
if (JSON.stringify(DEDICATED_PROTOCOL_CHANNELS) !== JSON.stringify(["passthrough", "responses_to_chat", "anthropic_to_chat"])) {
  throw new Error(`DEDICATED_PROTOCOL_CHANNELS export drifted: ${JSON.stringify(DEDICATED_PROTOCOL_CHANNELS)}`);
}
if (JSON.stringify(LOSSY_TIERS) !== JSON.stringify(["low", "high"])) {
  throw new Error(`LOSSY_TIERS export drifted: ${JSON.stringify(LOSSY_TIERS)}`);
}

const baseKeyMetaWithDedicatedEndpoints = {
  schema_version: 2,
  revision: 1700000000000001,
  key_id: "client-key-dedicated-coding",
  key_hash: "sha256:fixture-client-key-dedicated-coding",
  status: "active",
  expires_at: 1893456000,
  ip_whitelist: [],
  allowed_models: ["glm-4.5"],
  dedicated_upstream: {
    upstream_key_id: "coding-plan-key-001",
    ciphertext: "kms:projects/demo/locations/global/keyRings/ring/cryptoKeys/key:ciphertext",
    ciphertext_version: 2147483648,
    ciphertext_sha256: "sha256:dedicated-ciphertext",
    kms_key_id: "kms-key-coding",
    status: "active",
    expires_at: 1893456000,
    updated_at: "2026-06-15T00:00:00Z",
    fallback_allowed: false,
    endpoints: [
      {
        client_protocol: "anthropic",
        provider_id: "glm-anthropic",
        protocol_strategy: "passthrough"
      }
    ]
  }
};

const cases = [
  {
    name: "provider_endpoint.provider_profile",
    validate: validateProviderEndpoint,
    payload: {
      id: "pc_bad",
      provider_profile: "not_a_profile",
      base_url: "https://example.invalid",
      status: "active"
    },
    keyword: "enum"
  },
  {
    name: "provider_info.provider_profile",
    validate: validateProviderInfo,
    payload: {
      id: "pc_bad",
      name: "bad profile provider",
      base_url: "https://example.invalid",
      status: "active",
      provider_profile: "not_a_profile"
    },
    keyword: "enum"
  },
  {
    name: "provider_info.visibility",
    validate: validateProviderInfo,
    payload: {
      id: "pc_bad_visibility",
      name: "bad visibility provider",
      base_url: "https://example.invalid",
      status: "active",
      visibility: "public"
    },
    keyword: "enum"
  },
  {
    name: "provider_info.credential_source",
    validate: validateProviderInfo,
    payload: {
      id: "pc_bad_credential_source",
      name: "bad credential source provider",
      base_url: "https://example.invalid",
      status: "active",
      credential_source: "platform_pool"
    },
    keyword: "enum"
  },
  {
    name: "provider_info.allowed_route_kinds",
    validate: validateProviderInfo,
    payload: {
      id: "pc_bad_route_kind",
      name: "bad route kind provider",
      base_url: "https://example.invalid",
      status: "active",
      allowed_route_kinds: ["admin_route"]
    },
    keyword: "enum"
  },
  {
    name: "client_exposure.served_inbound_protocols",
    validate: validateClientExposure,
    payload: {
      provider_id: "pc_101",
      served_inbound_protocols: ["not_a_protocol"],
      capabilities: ["chat"],
      token_count_strategy: "unsupported"
    },
    keyword: "enum"
  },
  {
    name: "client_exposure.capabilities",
    validate: validateClientExposure,
    payload: {
      provider_id: "pc_101",
      served_inbound_protocols: ["anthropic"],
      capabilities: ["not_a_capability"],
      token_count_strategy: "unsupported"
    },
    keyword: "enum"
  },
  {
    name: "client_exposure.token_count_strategy",
    validate: validateClientExposure,
    payload: {
      provider_id: "pc_101",
      served_inbound_protocols: ["anthropic"],
      capabilities: ["chat"],
      token_count_strategy: "not_a_strategy"
    },
    keyword: "enum"
  },
  {
    name: "model_route_intent.capabilities",
    validate: validateModelRouteIntent,
    payload: {
      logical_model: "kimi-k2",
      capabilities: ["not_a_capability"],
      routes: [
        {
          provider_id: "pc_101",
          upstream_model: "kimi-k2",
          status: "active"
        }
      ]
    },
    keyword: "enum"
  },
  {
    name: "model_route_intent.routes[].capabilities",
    validate: validateModelRouteIntent,
    payload: {
      logical_model: "kimi-k2",
      routes: [
        {
          provider_id: "pc_101",
          upstream_model: "kimi-k2",
          status: "active",
          capabilities: ["not_a_capability"]
        }
      ]
    },
    keyword: "enum"
  },
  {
    name: "key_meta.dedicated_upstream.endpoints[].client_protocol",
    validate: validateKeyMeta,
    payload: {
      ...baseKeyMetaWithDedicatedEndpoints,
      dedicated_upstream: {
        ...baseKeyMetaWithDedicatedEndpoints.dedicated_upstream,
        endpoints: [
          {
            client_protocol: "gemini",
            provider_id: "glm-gemini"
          }
        ]
      }
    },
    keyword: "enum"
  },
  {
    name: "key_meta.dedicated_upstream.endpoints[].protocol_strategy",
    validate: validateKeyMeta,
    payload: {
      ...baseKeyMetaWithDedicatedEndpoints,
      dedicated_upstream: {
        ...baseKeyMetaWithDedicatedEndpoints.dedicated_upstream,
        endpoints: [
          {
            client_protocol: "openai",
            provider_id: "glm-openai",
            protocol_strategy: "bridge_first"
          }
        ]
      }
    },
    keyword: "enum"
  },
  {
    // ADR-0015 S5b: field retired from the contract; any payload still
    // carrying it (whatever the value) is rejected as an unknown property.
    name: "route_config.providers[].protocol_strategy_override (retired)",
    validate: validateRouteConfig,
    payload: {
      model_id: "glm-5.1",
      providers: [
        {
          provider_id: "pc_openai",
          upstream_model: "glm-5.1",
          priority: 10,
          weight: 100,
          status: "active",
          protocol_strategy_override: "auto"
        }
      ]
    },
    keyword: "additionalProperties"
  },
  {
    name: "route_config.providers[].allow_high_loss_bridge_when_no_native",
    validate: validateRouteConfig,
    payload: {
      model_id: "glm-5.1",
      providers: [
        {
          provider_id: "pc_openai",
          upstream_model: "glm-5.1",
          priority: 10,
          weight: 100,
          status: "active",
          allow_high_loss_bridge_when_no_native: true
        }
      ]
    },
    keyword: "additionalProperties"
  },
  {
    // ADR-0015 S5b: field retired from the contract; any payload still
    // carrying it (whatever the value) is rejected as an unknown property.
    name: "product_runtime_policy.protocol_strategy (retired)",
    validate: validateProductRuntimePolicy,
    payload: {
      product_id: "prod-coding",
      status: "active",
      credential_source: "platform_pool",
      allowed_provider_ids: [],
      allowed_channel_ids: [],
      allowed_provider_profiles: [],
      model_scope_mode: "plan_configured",
      allowed_models: [],
      fallback_policy: "within_product",
      billing_mode: "token_plan_quota",
      protocol_strategy: "auto"
    },
    keyword: "additionalProperties"
  }
];

for (const testCase of cases) {
  const ok = testCase.validate(testCase.payload);
  if (ok) {
    throw new Error(`${testCase.name} accepted illegal enum value`);
  }
  const errors = testCase.validate.errors || [];
  if (!errors.some((error) => error.keyword === testCase.keyword)) {
    throw new Error(`${testCase.name} rejected without ${testCase.keyword} error: ${JSON.stringify(errors)}`);
  }
}

console.log(`validated ${cases.length} schema enum rejection cases`);
