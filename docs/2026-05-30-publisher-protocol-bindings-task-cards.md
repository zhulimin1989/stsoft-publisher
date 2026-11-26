# Publisher protocol_bindings 任务卡(补 gateway 设计三处缺口)

- 日期:2026-05-30
- 关联:`tokenplatform-gateway/docs/2026-05-27-binding-based-routing-redesign.md`(§21 / §23.11 / §23.11.1)
- 作者:publisher 团队
- 状态:Draft —— 仅补 gateway 文档在 publisher 侧没说清的三处契约缺口,不重述 PR7.1–7.6

## 0. 背景:补哪三处

gateway 文档的 publisher 任务(§23.11 PR7.1–7.6)对「单 provider DTO 链路」拆得已经足够独立、可回滚。但有三处会在跨仓 / agent 执行时形成真空,本文把它们补成 publisher 仓**可独立排期、自带 blockedBy/out-of-scope/可粘贴门禁**的任务卡:

| 缺口 | gateway 文档现状 | 本文卡片 |
|---|---|---|
| A. 跨仓「单一真值源」物理同步机制 | PR0 在 gateway 仓导出枚举/matrix/fixture,但 publisher 怎么物理消费没说;§23.7.4 只说「维护跨仓 fixture」 | **PR7.0** |
| B. bulk snapshot / validate API(cutover 关键链路) | §21.5 / §21.7 标「建议新增」,**未进 PR DAG**,无任务卡 | **PR7.7 / PR7.8** |
| C. publisher schema-mode 的具体配置载体 | §21.3 只给 3 个 mode 名,**无 env/config key/默认值** | **PR7.5b** |

> 现状锚点(执行 agent 不必重新发现):
> - `internal/types/dataplane.go:298` `ProviderInfo{ ApiTypes []string }`,无 `ProtocolBindings`。
> - `internal/api/requests.go:188` `ProviderInfoRequest` + `effectiveApiTypes()`(:208)+ `ToProviderInfo()`(:218)。
> - `internal/publisher/provider_validate.go:29` `validateProviderInfo` 强制 `ApiTypes` 非空。
> - `internal/api/middleware.go:483` `decodeJSON` **未开 `DisallowUnknownFields`**(:474 TODO)。
> - `internal/publisher/impl.go:1452` `CurrentProviderInfoSchemaVersion = 1`;`fillProviderInfoDefaults`(:1513)。
> - `internal/publisher/publisher.go:96` `PublishProvider(ctx, providerID, info)`;无 provider snapshot/bulk_replace。
> - `internal/config/config.go:33` `Config{ HTTP, PG, Redis, Worker, Callers }`;env 覆盖在 `Load()`(:81)用 `os.Getenv`。
> - 既有跨仓 struct 纪律:`dataplane.go:57/105` 注释「JSON tags MUST match gateway … exactly」,由 gateway `internal/configsync/publisher_schema_test.go` 反向 pin。

---

## PR7.0 —— 跨仓契约消费机制(publisher 侧单一真值源)

**必须最先,是 PR7.1–7.8 全部 blockedBy 的前置。**

| 项 | 内容 |
|---|---|
| 数据载体 | `internal/types/protocol_binding.go`(新增镜像 struct)+ `internal/types/testdata/protocol_bindings_contract.json`(checked-in 共享 fixture) |
| 风险类 | 加法 |
| 内容 | (1) 在 publisher 镜像 `ProtocolBinding` / `ProtocolBindingAccept`,沿用既有「JSON tags MUST match gateway exactly」纪律(对标 `ResourceBinding` `dataplane.go:88`);**先不接入 `ProviderInfo`**(那是 PR7.1)。(2) 落盘**物理同步机制**:从 gateway 仓 `internal/configsync/testdata/protocol_bindings_contract.json`(PR0 产物)拷贝一份进 publisher `internal/types/testdata/`,用 sha256 守卫两份逐字节一致。(3) 新增 `make sync-binding-contract` 或脚本,记录 source-of-truth = gateway 仓路径 |
| 物理同步决策 | 推荐「committed copy + checksum 守卫 test + 手动/CI 重拉」,而非 go module 共享(两仓无共享 module)或 submodule(运维重)。该决策必须在本卡 PR 描述里写死,不留给后续 PR 重判 |
| 验证(可粘贴) | `go build ./... && go test ./internal/types/...`;跨仓一致性:`diff <(sha256sum internal/types/testdata/protocol_bindings_contract.json \| cut -d' ' -f1) <(sha256sum ../tokenplatform-gateway/internal/configsync/testdata/protocol_bindings_contract.json \| cut -d' ' -f1)`(必须无 diff) |
| blockedBy | gateway PR0(fixture/枚举已导出) |
| out-of-scope | 不改 `ProviderInfo`;不改 `ProviderInfoRequest`;不接任何读/写路径;不做枚举 enum 锁死(gateway 权威) |
| 回滚 | 纯新增文件,直接 revert |

> 缺了 PR7.0,PR7.1 / PR7.7 / PR7.8 会各自「记得」`ProtocolBinding` 长什么样 —— 正是 gateway §23 警告的 `gemini_generate` 命名漂移在 publisher 侧的复刻。契约 fixture 必须先落盘 + 有守卫。

---

## PR7.5b —— schema-mode 配置载体(把 §21.3 的 mode 名落到具体 env/config)

| 项 | 内容 |
|---|---|
| 数据载体 | `internal/config/config.go` `Config`(新增 `ProviderSchemaMode` 字段)+ `Load()` env 覆盖 |
| 风险类 | 加法 |
| 内容 | (1) `Config` 增 `ProviderSchemaMode string`,枚举 `legacy_accept` / `dual_accept_shadow` / `protocol_bindings_only_strict`,**默认 `legacy_accept`**(对齐 §21.10 当前态,保守)。(2) `Load()` 加 `os.Getenv("PUBLISHER_PROVIDER_SCHEMA_MODE")` 覆盖(对标 :224 `HTTP_PORT` 既有模式),非法值启动 fail-fast。(3) 计入 `EnvOverrides` 启动日志。(4) 本卡**只引入字段 + 解析 + 校验**,provider 链路暂不读它(读路径在 PR7.1/7.4/PRN+2 按 mode 分支) |
| 验证(可粘贴) | `go test ./internal/config/...`;断言:未设环境变量默认 `legacy_accept`;`PUBLISHER_PROVIDER_SCHEMA_MODE=bogus` 时 `Load()` 返回 error |
| blockedBy | —(可与 PR7.0 并行;不依赖 fixture) |
| out-of-scope | 不在本卡接 strict 行为(strict 是 PRN+2);不改 `validateProviderInfo`;不改 DTO |
| 回滚 | 删字段 + 删 env 分支;无存量数据依赖 |

> 把 mode「具体载体」单独成卡,是因为 gateway 侧 §23.4 把所有开关命名了(`GATEWAY_*`),publisher 侧只有 mode 名没有 key —— 不钉死载体,PR7.1/7.4 会各自发明配置入口。

---

## PR7.7 —— Provider validate dry-run 端点(§21.7,cutover 前置 audit)

| 项 | 内容 |
|---|---|
| 数据载体 | publisher 接口 + admin handler(新增 `ValidateProviderSnapshot`,**只读,不写 Redis/outbox**) |
| 风险类 | 加法 |
| 内容 | (1) `Publisher` 接口加 `ValidateProviderSnapshot(ctx, []types.ProviderInfo) (ValidateReport, error)`,复用 `validateProviderInfo` 的 shape 校验逐 provider 跑,聚合返回 `{provider_id, field, reason}`(对齐 gateway §21.7 错误示例)。(2) HTTP `POST /admin/api/v1/providers:validate`,**绝不经 outbox/commit**。(3) 受 §types.MaxBulkItems=100 约束(`dataplane.go:531`)。(4) dry-run 只做 publisher 侧 shape 校验,不锁 gateway enum(边界同 `provider_validate.go:14` 注释) |
| 验证(可粘贴) | `go test ./internal/publisher/... ./internal/api/...`;断言:含非法 binding 的 batch 返回逐项 errors 且 **Redis/outbox 零写入**(用现有 fake outbox 断言 0 行);合法 batch 返回 `ok:true` |
| blockedBy | PR7.0(镜像 struct + fixture)、PR7.1(`ProviderInfo.ProtocolBindings` 字段)、PR7.4(`validateProviderInfo` 已支持 binding 校验) |
| out-of-scope | 不写任何 Redis;不做 bulk publish(那是 PR7.8);不做 strict 拒 legacy(PRN+2) |
| 回滚 | 删接口方法 + 路由;无写副作用,无存量依赖 |

---

## PR7.8 —— Provider bulk snapshot / replace(§21.7 / §21.12,Phase B/D cutover 写入)

| 项 | 内容 |
|---|---|
| 数据载体 | publisher 接口 + admin handler(新增 `PublishProviderSnapshot`,**全量校验通过才写,all-or-nothing**) |
| 风险类 | 切行为(新增写路径) |
| 内容 | (1) `Publisher` 接口加 `PublishProviderSnapshot(ctx, []types.ProviderInfo) (types.BatchID, error)`,对标既有 `PublishProviderOverrideBulk`(`impl.go:1089`)的 batch 形态。(2) **必须先全量 `ValidateProviderSnapshot` 通过再写**,任一 provider 失败整批 reject(不允许部分成功,对齐 §21.7)。(3) 写入仍走 `commitWithRevision(..., DomainTopology, ...)`(对标 `impl.go:1016`),**不绕过 outbox/revision/PubSub**。(4) 推进 topology revision,发 provider bulk/full reload 事件。(5) HTTP `PUT /admin/api/v1/providers:snapshot` |
| 验证(可粘贴) | `go test ./internal/publisher/... ./internal/api/...`;断言:批内任一 provider 校验失败 ⇒ outbox 0 行(原子性);全通过 ⇒ N 行 + 一个 batch_id + revision 单调递增;复用 PR7.0 fixture 跑端到端 round-trip |
| blockedBy | PR7.7(共享 validate 逻辑)、PR7.0、PR7.1、PR7.4 |
| out-of-scope | 不实现 snapshot_epoch / active-pointer 切换(gateway 侧 §16,publisher 只保证原子全量写 + revision);不做 strict 拒 legacy(PRN+2);不动 key/balance/security 接口(§21.5) |
| 回滚 | feature flag 关闭路由即退回单 provider `PublishProvider`;接口为纯新增,无既有行为改动 |

> PR7.8 是 §16.2/§16.4 cutover 的 publisher 侧落点。没有它,Phase B「全量校验后一次性写入」「失败整批 reject」只能靠 100 次单 provider upsert 拼,破坏原子性与回滚。

---

## 与 §23.11.1 十条硬约束的逐条对账

| # | 约束 | 本文卡片是否满足 |
|---|---|---|
| 1 | 单一数据载体 | ✅ PR7.0=types fixture;PR7.5b=config;PR7.7/7.8=publisher 接口。各卡单一载体 |
| 2 | 单一新读路径 / 默认 shadow | ✅ PR7.0/7.5b 不接读路径;PR7.7 只读不写;PR7.8 是唯一新写路径且带 flag |
| 3 | 独立回滚 | ✅ 每卡「回滚」行均不依赖后续 PR |
| 4 | 门禁可粘贴命令 | ✅ 每卡给了 `go test` / `diff sha256` / 原子性断言命令 |
| 5 | flag 可关 | ✅ PR7.5b 即 flag 本体;PR7.8 路由可 flag 关 |
| 6 | adapter 不批量 | N/A(publisher 无 adapter) |
| 7 | flag 粒度自洽 | ✅ PR7.8 声明「flag 关 = 退回单 provider publish」,与第 3 条不冲突 |
| 8 | strict / 删除独立 | ✅ strict 明确踢给 PRN+2;本文不含 strict 与 legacy 删除 |
| 9 | context 预算上限 | ✅ 每卡触及文件 ≤ 3,需通读设计 ≤ 2 节(§21 + §23.11) |
| 10 | 显式 blockedBy + out-of-scope | ✅ 每卡均填(补齐了 gateway PR7.x 表格缺的 out-of-scope 列) |

## 执行顺序(publisher 仓内 DAG)

```
gateway PR0 ──► PR7.0 ──┬─► PR7.1(gateway 文档已有)──► PR7.4 ──► PR7.7 ──► PR7.8
                        └─► (PR7.5b 可与 PR7.0 并行,无 fixture 依赖)
PRN+2(strict 切换)在 PR7.8 稳定后单独窗口,本文不覆盖。
```
