# Publisher API 字段歧义审计 — 2026-05-16

> 背景:2026-05-16 完成"API 参数实体与 wire 类型分离"重构(commit 88f9670)
> 后,用户要求"基于上面的原则把所有 api 过一遍"——逐字段判断 caller 是否
> 知道(a)要不要填、(b)怎么填、(c)从哪里取。
>
> 涵盖范围:42 个有 body 的端点(§4 user × 9、§5 billing × 3、§6 admin × 16、
> §7 sre × 14)。health/metrics 类无 body 端点不在内。

---

## 📢 2026-05-16 架构决策(审计后追加)

本审计在生成时**误以为**架构是 `control-plane(编译)→ user-service(透传)→ publisher(写)→ gateway`。**事实更复杂**:

- **tokenplatform-publisher**(本仓):网关平台薄写侧,只有 api/outbox/types/publisher 基础设施,**没有 compiler / policy / tenant / workspace 任何编译相关包**。
- **tokenplatform-control-server**:`internal/provisioning/compiler/`(41 个 .go)做编译,`internal/publisher/redis_publisher.go` 直接写 Redis。这是 legacy 通路。
- **codesphere monorepo**(NestJS,billing-main / admin-main / main 三个部署):业务侧服务,已实现 `publisher.client.ts` HTTP 客户端 + 6 个 wire 类型,准备直接调本服务。**这是未来主路径**。

**决策**(2026-05-16,用户拍板):

| 选项 | 决策 |
|---|---|
| α — 把 control-server compile 搬到 publisher | ❌ 不采纳 |
| β — control-server 保留 compile,只切 HTTP | ❌ 不采纳 |
| **✓ 业务侧 compile** — caller 业务服务自己 compile 业务意图为 wire 类型,publisher 保持薄写侧 + 校验 | ✅ 采纳 |

**publisher 的最终责任边界**:
- HTTP 接收 + 鉴权(namespace token)
- Wire shape 校验(DTO 类型 / enum / 单位 / 跨字段约束 / path-body 一致性)
- PostgreSQL outbox at-least-once
- Worker 写 Redis + INCR revision + Pub/Sub
- **不持有业务真值;不做业务 compile**

**Caller 业务服务的责任**:
- 持有自家业务表(tenant / workspace / billing / policy / quota / model catalog 等)
- Compile 业务意图为 wire 类型(EffectivePolicy / ResourceBinding / RouteConfig 等)
- HTTP 推送给 publisher

**control-server 现役 compile + redis_publisher 路径**:caller 业务服务切换到 publisher HTTP 后 retire。当前并存。

**对本审计的影响**:

| 原审计条目 | 决策后状态 |
|---|---|
| P0 #2 "标注为 control-plane 编译产物 / user-service 透传" | **撤回原叙事,改为定稿**:三字段(policies / resource_bindings / allowance_bindings)由 caller 业务侧 compile,publisher 透传 + wire shape 校验 — 见 publisher-api-reference.md §4.1.z 重写版 |
| 其它字段级歧义条目(枚举 / 默认 / 单位 / path-body) | **完全有效**,wire shape 问题与 caller 身份无关。已 ship 的 P0/P1/P2 改造(commit 11e863c / 29dae66 / e38abfe / 7ab07a3)保留 |

**已 ship 跨仓 commit**(publisher + gateway 配对):
- `88f9670` (publisher): DTO 分离
- `11e863c` (publisher): P0+P1 改造 + 审计落盘
- `29dae66` (publisher): P2 enum + 单位防御
- `e38abfe` (publisher) + `7ab07a3` (gateway): B 方案 enforce_model_whitelist
- `bfe34fb` (publisher): 撤回 control-plane 错叙事第一波
- (本 commit): 责任边界定稿

## 1. 总览

| 维度 | 数量 |
|---|---|
| 端点总数(本审计范围) | 42 |
| 字段总数(嵌套结构内每字段计一次) | ≈ 96 |
| 🔴 高歧义(caller 可能填错 / 填不出) | 18 |
| 🟡 中歧义(文档可解决 / 默认值不清) | 22 |
| 🟢 清晰 | ≈ 56 |

## 2. 高歧义字段清单(🔴)

| 端点 | 字段 | 类型 | 歧义类型 a/b/c | 当前文档说明 | 问题描述 | 建议处理 |
|---|---|---|---|---|---|---|
| §4.1 PUT /user/api/v1/keys | `scope_attrs` keys (`tenant_id` / `workspace_id` / `app_id` / 自定义) | `map[string]string` | b/c | "业务身份维度,gateway 透传到 UsageEvent" | 没有 canonical key 白名单;user-service 自行 hardcode 是否会被 gateway 正确聚合?key 命名空间约定(`usr.*` / `ops.*`)未公开 | 文档补 canonical key 表 + value 格式 + 自定义 key 命名约定;标注谁负责保证 key 名稳定 |
| §4.1 | `policies[]` (`types.EffectivePolicy`) | object[] | b/c | "control-plane 编译时把 rate-limit / quota 策略展开" | **control-plane 编译产物**却在 user-service caller DTO 里。user-service 凭什么知道 PolicyID/RuntimeKeys/Conditions 该填什么?caller 极可能不填→key 不限流,或自造→gateway 评估失败 | 文档明确:"policies 是 control-plane 编译产物,user-service 透传即可,不要本地构造";link 到 control-plane 编译契约 |
| §4.1 | `resource_bindings[]` (`types.ResourceBinding`) | object[] | c | "control-plane 编译 quota 时扁平 binding" | 编译产物;JSON tag 跨仓 schema 锁(gateway `model.ResourceBinding` 1:1),caller 对此无感 | 标注 "control-plane 编译产物",必须透传;schema drift 风险写进文档 |
| §4.1 | `allowance_bindings[]` (`types.AllowanceBinding`) | object[] | a/c | "T54 Phase 3 完成后退役" | 退役中状态语义模糊;字段注释"empty=no allowance"vs"reject when bound flag 0"自相矛盾 | 文档明确当下(Phase 1/2/3)字段语义;retire 时间线给定 |
| §4.1 | `allowed_models` | string[] | b | "空数组 = 允许所有" | **反直觉默认值陷阱**:空 vs 全允许 视觉上像"禁用所有" | 字段表上方红字加粗;或拆为 bool 启用开关 |
| §4.1 | `status` | string | b | 表只写 "active" | 完整枚举(active/suspended/revoked)只在 §11.1 注解里;大小写敏感性未说 | 字段表补完整枚举 + 各值语义 |
| §4.1 | `expires_at` | int64 | b | "Unix 秒,0=永不过期" | 毫秒误填无防御;`time.Now().Unix()` 远 > 0,看不出错 | 加 `> 1e12` 告警;文档强调秒级 |
| §4.1 | `ip_whitelist` | string[] | b | "CIDR / IP" | IPv6 zone/缩写支持?单 IP 是否自动转 /32?caller 是否需规范化 | 文档补 |
| §5.1 PUT /billing/api/v1/key-balance/{key_id} | `reason` | string | a/b | 枚举白名单 + 条件必填 | **双向条件约束**:has_balance=false 必填白名单,true 必须为空。字段表"必填"列只写"条件 ★" | "必填"列写成 "条件必填(has_balance=false 时必填白名单;true 时必须为空)";完整白名单 inline |
| §5.1 | path `{key_id}` | string UUID | c | "UUID v4(不是 key_hash)" | billing 怎么获得 key_id?user-service push?查 user-service DB? | 文档加 "前置:billing 必须从 user-service 拿到 key_id";标注查询接口 |
| §6.1 PUT /admin/api/v1/providers/{provider_id} | `auth_type` | string | b | "bearer/x-api-key/jwt/oauth" | oauth 哪种?x-api-key 专 Anthropic 用?jwt 算法?caller 仅看 4 个值不足以决定 | 文档补每个 auth_type → gateway adapter 行为映射 |
| §6.1 | `code` | string | b/c | "规范小写代码,gateway adapter 解析用" | **关键字段语义模糊**:Code 决定 gateway adapter,但 §6.1 写"非必填,等于 id"。"自造 openai 兼容代理"该填什么?跨仓决定 | 列出 gateway 已知 Code 白名单;说明新 Code 影响 |
| §6.1 | `key_strategy` | string | b | "priority/weighted" | legacy 名(fixed_priority/weighted_capacity)是否兼容?空字符串 fallback 链未说 | 文档明确 canonical;fallback 链;T56 Phase 2 切换时间 |
| §6.6 RouteConfig | `providers[].priority` | int | b | "越小越优先" | 取值范围?同 priority tie-break? | 文档补取值范围 + tie-break 规则 |
| §6.6 | `providers[].weight` | int | b | "0⇒当1" | 反直觉:weight=0 不是"禁用"。caller 想禁用得用 status=disabled | 文档加 "weight=0 不等于 0 权重";推荐 status 而非 weight |
| §6.12 KeyPoolEntry | `ciphertext` | string | b/c | "KMS 加密密文" | **谁加密?用什么 KMS key?加密算法?ciphertext 编码(base64/hex)?生成 ciphertext 的链路完全没说**。这是 admin 调本端点最大盲点 | 加专门"ciphertext lifecycle"小节:加密服务 / KMS key / 输出格式 / publisher 不校验格式(错误致 gateway 解密失败)|
| §6.12 | path `{key_id}` vs body `key_id` | string | b | "key_id in path is authoritative" | **path/body 重复字段**:body 留空被 path 覆盖,body 填且不等于 path → 400。字段表写 body.key_id 必填,与代码不一致 | 统一:body.key_id 改成 "可省(path 权威)";其它路径参数同步 |
| §6.16 PlatformConfig | `responses_affinity_enabled` | `*bool` | a/b | "nil ⇒ 默认开" | **指针语义陷阱**:Go 零值 `false` 序列化变"显式关闭";想用默认必须省略 JSON key。Python 调用方直觉冲突 | 字段表加显著提示:"指针;缺省=不写 JSON key;写 false=显式关闭" |
| §6.16 | `retry_budget` 三轴 | object | a/b | "三轴全零→in-code 默认;任一非零→其它零位 unlimited" | **混合默认陷阱**:caller 只想调 max_providers=3 → 其它轴变 unlimited(生产可能爆 retry) | 改硬校验:任一非零时强制三个都非零(publisher 入口加 validate);或文档极显著警示 |
| §7.1 POST /sre/api/v1/keys/{key_hash}/disable | `reason` | string | b/c | "封禁原因,缺失→400" | 自由字符串,无枚举/无长度上限;最终去哪未说(只去 outbox?gateway 看得到吗?) | 文档加示例+用途;考虑长度限制 |
| §7.1 | `operator` | string | b/c | "SRE 工号/邮箱" | 自由字符串,格式未规定 | 文档加格式约束(推荐 SSO username);publisher 不校验需说明 |
| §7.10 PUT /sre/api/v1/overrides | `scope` | string | b/c | "`<provider_code>\|<base_url>\|<upstream_model>`,缺一段→400" | **未开放但已上线的预留端点**(§13 标 ⚠️)。三段格式说了,但 base_url 是否带 scheme、是否 wildcard、provider_code 取 Code 还是 ID 都未说;**publisher 实际不解析 scope(文档"缺一段→400"未实现)** | 文档明确:caller 暂勿用;或 publisher 加 scope 格式校验 |
| §7.10 | `action` | string | b | "disable/enable/..." | 列举不全;gateway 当前不消费;caller 不知道写啥安全 | 列全枚举(即便预留);TBD 也明确标 |
| §7.10 | `param` | `json.RawMessage` | a/b/c | (无明文规则) | 完全自由的 JSON blob;每个 action 对应的 param schema 未约定 | 定义每个 action 的 param schema;标注"暂未消费,结构待定" |
| §7.10 | `effective_from` | int64 | b | "Unix 秒,策略生效起点" | 秒/毫秒坑;过去时间戳合法吗 | 文档加 "过去时间戳=立即生效"语义 |
| §7.14 POST /sre/api/v1/replay | `since` / `until` | string RFC3339 | a/b | "与 batch_id 二选一" | 3-way 互斥规则绕(batch_id AND (since OR until) 同时给→400);**解析失败静默忽略**反直觉(caller 写错格式 → publisher 当字段不存在,可能放过整批) | 加输入组合决策表;解析失败改 400 |

## 3. 中歧义字段清单(🟡)

| 端点 | 字段 | 问题 | 建议 |
|---|---|---|---|
| §4.1 | `key_id` | v4 严校验细则(第 3 段以 4 开头等)未在字段表 | 加"必须 v4,不接受 v1/v3/v5" |
| §4.1 | `key_hash` | 大小写敏感?0x 前缀? | 明确 lowercase-only |
| §4.1 | `max_tokens_per_call` | 负数行为未文档化 | 加边界 |
| §4.1 | `guardrails.allow_tools` / `allow_vision` | `*bool` 指针语义同 §6.16 | 文档明确 nil/true/false 区别 |
| §4.1 | `guardrails.max_messages` / `max_output_tokens` | 0 = 不限制还是 0 个? | 明确 0 语义 |
| §4.5 | `key_hash` | 与 §4.1 同名不同语义,容易混 | §4.5 字段表上方 inline 再写决策树 |
| §6.1 | `name` | 默认空,UI 展示什么 | server fallback 或明示 |
| §6.1 | path `{provider_id}` vs body `id` | handler 不校验一致性 | 统一 path 权威或加校验 |
| §6.3 | path `{model_id}` vs body `model_id` | 同上 | 同上 |
| §6.3 | `capabilities` | 大小写敏感?gateway 容错? | 明确小写 canonical |
| §6.3 | `context_window` / `max_output` | 单位?0 语义? | 文档补 |
| §6.3 | `owned_by` | "推荐填"非 ✓ 非 ✗ | 二选一 |
| §6.6 | `provider_strategy` | publisher 不校验,typo 风险 | publisher 加枚举校验 |
| §6.6 | `default_policy.preferred_providers` | 与 `providers[]` 关系 | 文档补 |
| §6.6 | `default_policy.required_params` | "params" 指请求字段名? | 加示例 |
| §6.6 | `RouteEntry.adapter` | adapter/Code/api_type 三者关系 | 加对比表 |
| §6.6 | `compliance.region` 等 | 已消费 / 预留 | 标注 |
| §6.12 | `rpm_limit` / `tpm_limit` | 与 KeyMeta.policies 双重限流叠加 | 文档补两层 RPM 关系 |
| §6.12 | `status` (cooling) | cooling 谁负责切回 enabled? | 文档补生命周期 |
| §6.16 | timeout 字段 | 0 = reset 到 gateway 兜底 — 想显式设 0 ms 不可能 | 文档明确;或指针化 |
| §6.16 | `provider_budget_policies` | 与 KeyMeta.policies scope 对比 | 加对比说明 |
| §7.14 | `status` | batch_id 模式下被忽略 | inline 提示 |

## 4. 按端点小结

详见原报告第 4 节(每个端点的 🔴 / 🟡 / 🟢 分布)。亮点:
- §4.1 KeyMeta:**6 🔴**(嵌套结构 3 个 + scope_attrs + allowed_models + status)
- §5.1 KeyBalance 单写:**2 🔴**(reason 条件 + key_id 来源)
- §6.12 KeyPoolEntry:**2 🔴**(ciphertext + path/body 重复)
- §6.16 PlatformConfig:**2 🔴**(responses_affinity 指针 + retry_budget 三轴)
- §7.10 ProviderOverride:**4 🔴**(整组预留状态)
- §7.14 Replay:**1 🔴**(since/until 静默忽略)

## 5. 建议的下一步动作(按优先级)

### P0 — 影响 caller 立刻能跑通的必修

1. **§6.12 KeyPoolEntry.ciphertext 来源链路文档化**。最大盲点。专门小节描述:加密服务 / KMS key / 输出格式 / 错误时何时何处报错。
2. **§4.1 policies / resource_bindings / allowance_bindings 标注为 "control-plane 编译产物"**。caller 不应手写;加显眼免责声明 + 链向编译契约。
3. **§5.1 reason 双向条件 + path key_id 来源**。字段表"必填"列改成具体条件;补"key_id 来自 user-service"。
4. **§4.1 allowed_models=[] 反直觉 + §6.16 responses_affinity_enabled 指针语义**。字段表上方红字提醒。

### P1 — 整理后能减少反复问

5. **scope_attrs canonical key 表**(§4.1)。
6. **§7.10 ProviderOverride 预留状态澄清**:文档明确 "gateway 不消费,勿用",或直接从 routes 注册移除。
7. **§6.16 retry_budget 三轴改硬校验**:publisher 入口加 validation,任一非零时三个都必须非零→否则 400。
8. **path/body 同名字段统一处理**:§6.1/§6.3/§6.6/§6.9/§6.12 → 统一为 "path 权威,body 可省,body 给值不等则 400"。

### P2 — 长期收紧 schema

9. **enum 字段加 server-side 校验**:status / auth_type / provider_strategy / key_strategy / adapter / capabilities / override.action / replay.status。typo silent failure 风险消除。
10. **时间字段单位防御**:expires_at / effective_from 加 `> 1e12` 告警(毫秒误填)。
11. **预留 / 未消费字段标注**:KeyPoolMeta.Strategy / PlatformConfig.DefaultKeyPoolStrategy / ProviderInfo.ApiType / §7.10 overrides。字段表加 🪦 [已废弃/未消费]。
12. **SRE reason / operator 落点说明**:§7.1 "落 outbox row 用于审计" — 但仅 PG 表,不到 gateway。caller 期望 gateway 端能看到 operator 会落空。

## 6. 三大盲点(总结)

1. **KeyPoolEntry.ciphertext 加密链路**(§6.12)— 文档完全缺失
2. **KeyMeta 嵌套结构属于 control-plane 编译产物**(§4.1 policies / resource_bindings / allowance_bindings)— caller 边界没画
3. **反直觉默认值**(allowed_models=[] / responses_affinity_enabled=nil / retry_budget 三轴 all-or-nothing)— 各处有提及但都"埋深了"

后两类至少在文档某处提及,但都散在 §15 常见误用 / §11 注解里,建议把高危陷阱从隐蔽处 promote 回 §4-§7 字段表 inline 警示。
