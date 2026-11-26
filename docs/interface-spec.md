# Publisher interface spec

This repository owns the standalone publisher service that is the only writer
for gateway data-plane Redis (`gw:*` keys).

## Core contract

- caller services write through HTTP namespaces (`portal`, `billing`, `ops`, `sre`)
- publisher durably persists every write into a PostgreSQL outbox
- a background worker drains outbox rows into Redis with at-least-once delivery
- gateway nodes consume the resulting Redis state / PubSub changes

Authoritative HTTP route details live in **`docs/publisher-api-reference.md`** — 单一文档,既是字典(§11 数据类型 / §12 JSON 示例 / §13 端点全表 / §14 变更历史)也是操作手册(§4-§7 每个端点带"命名空间 / 用途 / 调用场景 / 字段表含必填 / 何时调 / 下游 gateway 效果"五元组,§13 场景速查,§15 常见误用)。

## Schema alignment with gateway

`internal/types/dataplane.go` is the wire schema gateway reads. Field-name
drift between publisher write side and gateway read side silently breaks
admission / routing / policy evaluation, so the gateway repo holds a
cross-repo regression suite:

- File: `tokenplatform-gateway/internal/configsync/publisher_schema_test.go`
- Each test pins a hand-crafted JSON payload against the gateway's
  `configsync.*` / `model.*` struct unmarshal. A publisher rename without
  a matching gateway update breaks the gateway CI before the bad payload
  ever reaches Redis.

When changing publisher wire types, follow the field-evolution rules
documented in `tokenplatform-gateway/docs/2026-05-15-redis-key-naming-spec.md` §7:

| Change | Rollout order |
|---|---|
| Add field | publisher writes → gateway tolerates (omitempty / pointer) — safe to ship alone |
| Drop field | gateway stops reading → publisher stops writing → delete struct field — needs a Phase 1 gateway change before publisher cuts over |
| Rename | dual-name period (gateway accepts both via UnmarshalJSON alias) → publisher switches writes → gateway drops old name — never ship as one commit |
| Semantic break (same name, different meaning) | bump `schema_version` so gateway can branch on it |

## Recent alignment passes

| Date | Drift fixed |
|---|---|
| 2026-05-15 | T41 / T42 / T43 / T44 / T58: strategy field rename, PlatformConfig field gaps, KeyMeta.AllowanceBindings tag, SecurityPolicy Set-shape, ProviderInfo.KeyStrategy. |
| 2026-05-16 | T46 / T52 / T53 / T57 / T59 / T60 / T61 + T54 Phase 1 + T56 Phase 1 — see `publisher-api-reference.md` changelog row for the field-level inventory. |
| 2026-06-03 | Provider interface cut over to protocol-bindings-only: `api_type` / `api_types` are rejected; `protocol_bindings` + `default_protocol_binding` are authoritative. ModelCatalog/Route add `request_param_policy`; Route adds `api_version` / `anthropic_beta`; ProtocolBinding adds capability profiles, param/value/combination constraints, compatibility_mode, and field_disposition. |

## Tests to run after schema changes

1. **Local publisher tests**: `go test ./... -count=1` in this repo (catches
   compile breaks + serialisation tests in `internal/types`).
2. **Cross-repo regression**: `go test ./internal/configsync/...
   -run TestPublisherSchema -count=1` in `tokenplatform-gateway` (catches
   wire drift gateway side).
3. If touching a hot field (KeyMeta / RouteConfig / PlatformConfig), also
   run `go test ./internal/configsync/... -count=1` in gateway so the full
   catalog_loader / config_subscriber paths exercise the new shape.
