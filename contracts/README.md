# Publisher Contracts

Publisher owns the contract source of truth under `contracts/`.

- `contracts/intent/*.schema.json` describes billing-to-publisher intent payloads.
- `contracts/runtime/*.schema.json` describes publisher-to-gateway runtime payloads.
- `contracts/golden/*.json` contains canonical fixtures consumed by publisher Go tests,
  the npm package AJV validators, and downstream billing/gateway contract gates.
- `contracts/VERSION` is the semver for the npm package
  `@raytone-lab/publisher-contracts`.

## Capability Axis Freeze

Batch 0 of `ARCH-PASSTHROUGH-REDESIGN.md` freezes the capability/feature/param
axes in `protocol_bindings_contract.json`. Do not add fields or enum values for
`supported_features`, `supported_params`, `capability_profiles`,
`capability_features`, `capability_params`, `capabilityOverrides`, or
`request_param_policy`.

New protocol conversion support must be added as a whole protocol-to-protocol
bridge/profile. It must not create another feature/param/capability admission
axis. The frozen baseline lives at
`contracts/freeze/capability_axes_baseline.json`; changing it requires an
architecture review and an updated `ARCH-REVIEW-APPROVED ... sha256:<snapshot>`
marker.

## Version Negotiation

Contract versioning has two tracks:

1. Schema payload fields keep their own `schema_version` / schema `version`.
   Bump these only when the payload shape changes in a way consumers must
   branch on at decode time.
2. `contracts/VERSION` is the package version published to GitHub Packages.
   Bump it for every published contract package. Use patch for compatible
   fixture/doc/type generation updates, minor for additive schema changes, and
   major for incompatible schema or semantic changes.

Consumers should pin a compatible npm range for TypeScript consumers, or vendor
the schema plus the exact `CONTRACTS_VERSION` for Go consumers. Runtime payloads
must still be checked against their own `schema_version`; the package version is
the distribution/version-negotiation envelope, not a replacement for payload
schema versions.

## NPM Package

Stage 1 publishes `@raytone-lab/publisher-contracts` from this repository to
GitHub Packages under the `@raytone-lab` scope.

```sh
cd contracts/package
npm ci
npm run build
npm run validate:golden
```

The build copies raw schema JSON, generates TypeScript declarations with
`json-schema-to-typescript`, and precompiles AJV validators. The package version
is checked against `../VERSION` before build/publish.

## Golden Fixtures

Golden fixtures are canonical payloads, not examples to hand-edit casually. The
current set includes a channelized shared-key route graph with a `PREFER`
assignment and a `testing` product channel, plus representative runtime
ProviderInfo, KeyPool, KeyMeta, RouteConfig, and ProductRuntimePolicy payloads.

Every golden fixture must pass:

- AJV validation against its matching schema.
- Strict Go decode in `internal/types/runtime_schema_contract_test.go` for
  runtime payloads.
- RuntimeIntent compile coverage in `internal/intent/golden_contract_test.go`
  for intent payloads.

## Bump Flow

1. Change Go structs, schema generation code, or schema JSON.
2. Add or update golden fixtures that prove the new contract surface.
3. Run the relevant failing contract test before refreshing generated artifacts.
4. Refresh generated schemas with `PUBLISHER_UPDATE_SCHEMAS=1` only when the
   schema change is intentional.
5. Bump `contracts/VERSION`.
6. Run `go test ./internal/types ./internal/intent` and
   `cd contracts/package && npm ci && npm run build && npm run validate:golden`.
7. Coordinate downstream billing/gateway consumers before merging breaking
   changes. Gateway consumes vendored schema plus version pin; billing consumes
   the npm package.

Do not publish a package if golden fixtures are missing, stale, or only compile
through mocks.
