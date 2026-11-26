# Gateway Product Runtime Policy Integration

本文档描述 gateway 后续如何消费 publisher 下发的产品运行策略。本轮控制台与 publisher 已提供配置和发布契约，但不修改 gateway 代码；gateway 未接入前，现有路由行为保持不变。

> 2026-06-15 作废说明: 本文中 `provider_entitlement` 运行时调用 entitlement key resolver 的方案已作废。
> 专属上游 credential 改为由 billing 写入 publishable KeyMeta.DedicatedUpstream，publisher 静态透传到
> `gw:keymeta`，gateway 从热加载 KeyMeta 读取并 lazy decrypt；gateway 不再在请求热路径调用 resolver。

## 1. Redis 契约

- Redis Key: `gw:product_policies`
- 类型: Hash
- field: `product_id`
- value: `ProductRuntimePolicy` JSON
- pub/sub channel: `gw:config:update`
- revision domain: `platform`
- revision key: `gw:config:rev:platform`

更新消息:

```json
{
  "version": 2,
  "type": "product_policy_update",
  "product_id": "848dc2dc-c58f-4647-ba20-ed50bf341190",
  "revision": 123
}
```

删除消息:

```json
{
  "version": 2,
  "type": "product_policy_delete",
  "product_id": "848dc2dc-c58f-4647-ba20-ed50bf341190",
  "revision": 124
}
```

缺少某个 `product_id` 的 policy 时，gateway 必须保持当前 legacy routing 行为，不做产品级渠道或凭证约束。

## 2. JSON Schema v1

```json
{
  "schema_version": 1,
  "revision": 1,
  "product_id": "848dc2dc-c58f-4647-ba20-ed50bf341190",
  "product_slug": "Tokenplan",
  "status": "active",
  "credential_source": "platform_pool",
  "allowed_provider_ids": ["zhipu-glm_coding-1a2b3c4d"],
  "allowed_channel_ids": ["channel-id"],
  "allowed_provider_profiles": ["glm_coding"],
  "model_scope_mode": "plan_configured",
  "allowed_models": [],
  "fallback_policy": "within_product",
  "billing_mode": "token_plan_quota",
  "route_strategy": "weight",
  "updated_at": "2026-06-09T00:00:00.000Z"
}
```

字段语义:

- `status`: `active` / `disabled`。`disabled` 时该产品下 API Key 应 fail closed。
- `credential_source`: `platform_pool` 使用现有平台 keypool；`provider_entitlement` 使用订单/订阅激活得到的供应商权益 key。
- `allowed_provider_ids`: runtime provider id 硬边界。
- `allowed_channel_ids`: upstream key/channel id 硬边界。
- `allowed_provider_profiles`: profile 级可选边界。
- `model_scope_mode`: `plan_configured` 沿用套餐/key 现有模型约束；`product_allowlist` 额外检查 `allowed_models`；`upstream_inherited` 不在本地重复配置模型范围，由上游权益决定。
- `fallback_policy`: `none` / `within_product` / `within_provider` / `platform_default`。
- `billing_mode`: 运行时计费语义标签，用于事件、审计和后续 billing gate。

## 3. Gateway 改造点

建议改造位置:

- `internal/configsync/redis_keys.go`: 增加 `KeyProductPolicies = "gw:product_policies"`。
- `internal/configsync/types.go`: 增加 `ProductRuntimePolicy` struct。
- `internal/configsync/catalog_loader.go`: `GatewayConfig` 增加 `ProductPolicies map[string]ProductRuntimePolicy`，`LoadAll` 读取 `HGETALL gw:product_policies`。
- `internal/runtime/config_subscriber.go`: 处理 `product_policy_update` / `product_policy_delete`，按 platform revision 过滤后热更新 catalog/runtime snapshot。
- `internal/model/model_catalog.go`: 存储并暴露 `GetProductRuntimePolicy(productID)`。
- `internal/routing/planner.go`: 在 provider/key 选择前应用产品策略硬约束。

API Key meta 已经通过 `scope_attrs` 下发:

- `usr.product_id`
- `usr.product_slug`

gateway 应以 `usr.product_id` 作为 product policy lookup key。

## 4. 路由执行顺序

建议在 planner 中按以下顺序处理:

1. 从 `RequestContext.ScopeAttrs["usr.product_id"]` 读取产品 ID。
2. 查不到 product policy 时走现有逻辑。
3. policy `status != active` 时拒绝请求。
4. 检查模型范围:
   - `product_allowlist`: requested logical model 必须在 `allowed_models`。
   - `plan_configured`: 沿用现有 key/plan 模型约束。
   - `upstream_inherited`: 不做 Raytone 本地模型白名单判断；旧的 entitlement resolver 约束已作废，专属凭证由 KeyMeta.DedicatedUpstream 静态下发。
5. 解析模型 route 后，按 `allowed_provider_ids` / `allowed_provider_profiles` 过滤 route providers。
6. 选择 key 前，按 `allowed_channel_ids` 过滤 keypool entries。
7. 应用 `credential_source`:
   - `platform_pool`: 从过滤后的现有 keypool 选 key。
   - `provider_entitlement`: 作废旧方案。不得运行时调用 entitlement key resolver；改读 KeyMeta.DedicatedUpstream，缺失或不可用时 fail closed，不能回退到平台公共 keypool。
8. 应用 fallback:
   - `none`: 不扩展 fallback model。
   - `within_product`: fallback 仍必须通过本 product policy。
   - `within_provider`: fallback 只能保留同 provider。
   - `platform_default`: 使用现有平台默认 fallback，但仍必须通过 product policy 的 provider/channel 边界。

## 5. 错误与事件

建议新增 filter reason:

- `product_policy_disabled`
- `product_policy_model_denied`
- `product_policy_provider_denied`
- `product_policy_channel_denied`
- `provider_entitlement_required`
- `provider_entitlement_missing`

所有 usage/process event 应继续携带现有 `scope_attrs`，并在可行时附加 `product_policy_revision`，便于排查某次请求用了哪版策略。

## 6. 验收测试

必须覆盖:

- 没有 product policy 时，现有 routing 完全不变。
- `platform_pool + allowed_provider_ids` 只会选中允许的 provider。
- `allowed_channel_ids` 会过滤同 provider 下未允许的 key。
- `product_allowlist` 拒绝未列入的模型。
- `provider_entitlement` 旧 resolver 路线已作废；KeyMeta.DedicatedUpstream 缺失或不可用时 fail closed，不使用平台 keypool。
- `fallback_policy=none` 禁止 fallback。
- `fallback_policy=within_provider` 不跨 provider fallback。
- `product_policy_delete` 热更新后恢复 legacy routing。

## 7. 回滚策略

控制台删除或停用策略后，publisher 会删除/更新 `gw:product_policies[product_id]` 并发布 platform revision。gateway 应在收到删除消息后移除本地缓存；若 Redis 中没有对应 field，按 legacy routing 处理。
