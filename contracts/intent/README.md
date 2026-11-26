# Intent Contracts

Publisher owns these intent contracts. They describe business intent accepted by
the publisher intent endpoint before it is compiled into gateway runtime config.

Intent payloads must not carry gateway registry keys such as translators,
request contracts, outbound protocols, auth schemes, or bridges. Those fields are
derived by `internal/intent.Compile` from provider profiles and client exposure.

Refresh schemas with:

```sh
PUBLISHER_UPDATE_SCHEMAS=1 go test ./internal/intent -run TestIntentSchemaContracts
```

Every schema carries `$id` and `version`. `RuntimeIntent` uses
`intent.CurrentIntentSchemaVersion`; component schemas bump independently only
when their own shape or semantics change. The source-of-truth matrix is
`intentSchemaContracts()` and is enforced by
`TestIntentSchemaVersionsMatchContractMatrix`—do not mechanically stamp the
RuntimeIntent version onto unchanged nested components.
