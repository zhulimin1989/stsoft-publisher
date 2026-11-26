# Runtime Config Contracts

## Owner

The publisher team owns these runtime config contracts. Gateway consumes the
published payloads, so runtime schema changes require lockstep coordination with
gateway readers before publisher starts writing a breaking version.

## Source Of Truth

- Go structs: `internal/types/dataplane.go` and `internal/types/protocol_binding.go`
- Version constants: `internal/publisher/impl.go`
- Generated schemas: `contracts/runtime/*.schema.json`
- Managed protocol binding contract: `contracts/runtime/protocol_bindings_contract.json`
- Lockstep test: `internal/types/runtime_schema_contract_test.go`

`capability_profiles`, `capability_features`, and `capability_params` are frozen
by `contracts/freeze/capability_axes_baseline.json`. Do not add a new
capability/feature/param axis here; add an architecture-reviewed
protocol-to-protocol bridge/profile instead.

## Regeneration

Run this after an intentional struct or version change:

```sh
PUBLISHER_UPDATE_SCHEMAS=1 go test ./internal/types -run 'TestRuntimeConfigSchemaContracts|TestProtocolBindingsContractManagedUnderContracts' -count=1
```

Then review the JSON diff before committing.

## Version Rules

- Additive optional fields keep the current schema version.
- Removing or renaming a JSON field, changing a JSON type, or changing field
  semantics requires bumping the matching `Current*SchemaVersion` constant.
- `ProviderInfo.protocol_bindings` and `ProtocolBinding.bridge` must stay in
  parity with the gateway-owned protocol binding contract.
- Do not expose business intent fields here. Runtime config is the compiled
  gateway-facing output.
