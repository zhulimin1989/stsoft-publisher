# Publisher API Reference

Publisher 是网关运行时配置的唯一写入服务。调用方通过 HTTP 提交写请求(API Key 注册 / 计费状态 / Provider 配置 / 路由配置 / 应急封禁 等),publisher 持久化后异步投递到 gateway 生效。返回 `202 Accepted` 表示请求已被接受并将在短时间内生效(各端点生效延迟见对应章节)。

- 设计动机与原理:`docs/interface-spec.md`
- 网关侧契约:`tokenplatform-gateway` 仓库

---

## 1. URL 命名空间与鉴权模型

Publisher 把端点按**调用方**切 4 个命名空间。每个 namespace 绑定一个 service identity,后者通过 cfg 文件配置 bearer token。

| URL 前缀 | 期望 caller | service identity (默认) |
|---|---|---|
| `/user/api/v1/*` | user 服务 — 用户 / 账号 / API Key 生命周期 | `user-service` |
| `/billing/api/v1/*` | billing 服务 — 计费 / 余额 | `billing-service` |
| `/admin/api/v1/*` | admin 服务 — 平台运营管理后台 | `admin-service` |
| `/sre/api/v1/*` | SRE 应急通道(当前由 admin 团队兼,token 单独签发或暂复用 admin token)| `sre-oncall` |

**鉴权**:
1. 请求头 `Authorization: Bearer <token>`
2. Token 在 cfg `[tokens]` 段映射到 service identity
3. Service identity 在 cfg `[namespaces]` 段映射到 URL 命名空间
4. URL 前缀必须与 token 对应的 namespace 一致,否则 403

**错误码**:
- `401 UNAUTHORIZED` — header 缺失 / token 未知
- `403 FORBIDDEN` — 跨 namespace / service 未注册

**配置示例**(`config/publisher.cfg`):
```ini
[namespaces]
user-service    = user
billing-service = billing
admin-service   = admin

[tokens]
# 支持数组(逗号分隔),用于无重启轮换
user-service    = user_<csprng-hex>, user_<csprng-hex-new>
billing-service = billing_<csprng-hex>
admin-service   = admin_<csprng-hex>
```

Token 生成:`make new-tokens COUNT=3 PREFIX=svcname`

---

## 2. 通用约定

### 请求

| Header / 字段 | 必需 | 说明 |
|---|---|---|
| `Authorization: Bearer <token>` | ✅ | 鉴权,见 §1 |
| `Content-Type: application/json` | ✅ (有 body 时) | 否则 400 |
| `Idempotency-Key: <string>` | 可选 | 同 key 重复提交直接返回原 batch_id,**不会重复落 Redis** |
| `X-Request-ID: <string>` | 可选 | 贯穿审计链的 caller trace id |

### 响应

写入类端点**全部** `202 Accepted`(已接收,publisher 异步投递到 gateway):
```json
{
  "batch_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "accepted"
}
```

错误格式:
```json
{
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "human-readable explanation",
    "details": {
      "field": "key_hash",
      "got": "..."
    }
  }
}
```

### 错误码表

| HTTP | code | 触发场景 |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | body 字段校验失败 / Content-Type 错 / KeyHash 非 64 hex / KeyID 非 UUID v4 |
| 401 | `UNAUTHORIZED` | Bearer header 缺失/未知 |
| 403 | `FORBIDDEN` | token 与 URL namespace 不匹配 |
| 405 | (mux 默认) | URL 存在但 method 不对 |
| 413 | `PAYLOAD_TOO_LARGE` | bulk 数组超过 `MaxBulkItems=100` |
| 500 | `OUTBOX_UNAVAILABLE` | PG 写入失败,调用方**必须**回滚自己的上游事务 |

---

## 3. 公开端点(无鉴权,网络层隔离)

| Method | Path | 用途 |
|---|---|---|
| GET | `/livez` | 进程存活探针(K8s liveness)— 总返 200 |
| GET | `/readyz` | 依赖就绪探针(K8s readiness)— 实时 ping PG + Redis,任一失败返 503,body 列出各依赖状态 |
| GET | `/healthz` | `/livez` 的兼容别名 |
| GET | `/metrics` | Prometheus 指标(gw_publisher_* 一组) |

---

## 4. `/user/api/v1/*` — 用户 API Key 与吊销集

绑定 `user-service` token。用户在 user 上创建 / 删除 API Key 后,user-server 通过这组端点把数据推到 gateway。

> **publisher 与 caller 的责任边界**(同样适用 §5 / §6 / §7):
>
> **publisher**:接收业务侧的写请求(API Key / 计费状态 / Provider 配置 等),校验 wire shape 后持久化并异步投递到 gateway。
> - HTTP 接收 + 鉴权
> - Wire shape 校验(字段必填 / 枚举 / 单位 / path-body 一致性 / 跨字段约束)
> - 异步持久投递,保证 at-least-once 到达 gateway
> - **不持有业务真值**(tenant 关系 / billing 余额 / policy 模板均在业务侧)
>
> **Caller(业务服务)**:
> - 持有业务真值(tenant / workspace / billing / quota policy / model catalog 配置等)
> - 把业务意图编译成 wire 类型(KeyMeta.Policies / ResourceBindings / RouteConfig 等)
> - HTTP 调本服务推送
> - 跨字段业务一致性自检(如 policy_id 是否对应实际 policy 定义)— publisher 只验 wire shape

### 4.1 PUT `/user/api/v1/keys` — 单 Key 写入

| 维度 | 内容 |
|---|---|
| **命名空间** | user(只接受 user-service token) |
| **用途** | 创建一把新 API key,或修改已有 key 的元数据(allowed_models / ip_whitelist / scope_attrs / status 等) |
| **典型场景** | ① user "创建 key" → user 后端调本接口;② 客户改 key 配置(限模型 / 限 IP / 失效时间);③ 续期 / 状态切换(active ↔ suspended) |
| **何时调** | user 创建 / 修改 key 的每一次写。删 key 用 §4.3,**不要**把 status 改成空 reuse 本接口 |
| **生效时间** | ~10ms,gateway 端立即用新元数据校验后续请求 |

**Body 字段一览**(`KeyMetaRequest`,API DTO 定义见 `internal/api/requests.go`;wire 类型 `KeyMeta` 见 §10.1)。

- `scope_attrs` 是业务身份维度的 opaque map,详见 §4.1.y。
- `policies` / `resource_bindings` / `allowance_bindings`:caller 业务侧 compile 后传入,publisher 不展开业务语义,详见 §4.1.z。

| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `key_id` | `string` (UUID v4) | 必传 | — | UUID v4 严格格式(不接受 v1/v3/v5);格式错 → 400 |
| `key_hash` | `string` (64 hex 小写) | 必传 | — | `HMAC-SHA256(raw_key, gateway_hmac_secret)` lowercase hex;算法以 gateway `internal/auth/keyhash/keyhash.go` 为准。**不是**纯 SHA-256;raw_key 永不传 publisher |
| `status` | `string` enum | 必传 | — | 枚举:`active` / `suspended` / `revoked`(大小写敏感);非枚举 → 400 |
| `alias` | `string` | 可选 | `""` | 人类可读标签,纯展示用 |
| `scope_attrs` | `map[string]string` | 可选 | `nil` | 业务身份维度的 opaque map(tenant / workspace / app / region / 自定义...),透传到 UsageEvent。最多 10 对。详见 §4.1.y |
| `expires_at` | `int64` | 可选 | `0` = 永不过期 | Unix **秒**;`> 1e12` → 400(看起来是毫秒)|
| `ip_whitelist` | `[]string` | 可选 | `[]` = 不限制 | 单 IP 或 CIDR;空列表跳过 IP 检查 |
| `enforce_model_whitelist` | `bool` | 可选 | `false` = 不检查模型 | 要启用模型白名单时填 `true`,此时 `allowed_models` 必须非空 |
| `allowed_models` | `[]string` | 条件必传 | `[]` | `enforce_model_whitelist=true` 时必须非空数组(每项是 logical model id);`enforce=false` 时此字段不生效,填了 gateway 忽略 |
| `policies` | `[]EffectivePolicy` | 可选 | `[]` = 不限流不限额 | 业务侧有 rate-limit / quota 策略时,把策略 compile 成此数组(每条结构见 §10.3,填写约束见 §4.1.z)|
| `resource_bindings` | `[]ResourceBinding` | 可选 | `[]` = gateway 不索引 quota state | 与 `policies` 配对的扁平 binding 数组,业务侧 compile 时一并生成(结构见 §10.A,见 §4.1.z)|
| `allowance_bindings` | `[]AllowanceBinding` | 可选 | `[]` = 无 allowance 检查 | gateway 准入时要查的 allowance flag 列表,业务侧 compile(结构见 §10.B,字段计划退役)|
| `max_tokens_per_call` | `int64` | 可选 | `0` = 不限 | 单次请求 token 上限,正整数 |
| `guardrails` | `GuardrailPolicy` (object) | 可选 | `nil` = 不做内容护栏 | 字段定义见 §10.2 |
| `dedicated_upstream` | `DedicatedUpstreamCredential` (object) | 可选 | `nil` = 走普通平台 keypool | 专属上游 credential 绑定。对象存在时，`provider_account_ref` 对所有新写入必传，且 `ciphertext` 必须以 `kms:` 开头；publisher 校验后透传到 `gw:keymeta`,不解密 |

#### 4.1.x 字段逐项说明

每个字段:**外部是否需要传** / **触发时机** / **默认值** / **数据类型** / **数据样例**。

##### `key_id`
- **是否需要外部传**: **需要** ★。UUID v4,由控制面在创建 key 时分配。Publisher 不分配,不知道映射关系。
- **触发时机**: portal 用户点"创建 key" → user-service 后端 `uuid.New()`,与 raw_key 一同生成。后续修改同一把 key 时复用此 ID。
- **默认值**: 无,必填。Publisher 校验失败 → 400 `KeyID must be a UUID v4`。
- **数据类型**: `string`(UUID v4 字面格式:第 3 段以 `4` 开头,第 4 段以 `8/9/a/b` 开头)
- **数据样例**: `"550e8400-e29b-41d4-a716-446655440000"`

##### `key_hash`
- **是否需要外部传**: **需要** ★。raw_key 是用户面前的明文(`tk-...`),publisher 永远不接收 raw_key — 由 user-service 在生成 / 接收 raw_key 后立即 hash,只把 hash 传给 publisher 与下游。
- **触发时机**: user-service 生成或导入 raw_key 时立即计算,与 key_id 一起入 user-service 自己的 DB,再 push 到 publisher。
- **默认值**: 无,必填。Publisher 校验失败 → 400(必须 64 个 hex 字符,即 HMAC-SHA256 输出长度)。
- **数据类型**: `string`(64-hex,即 `HMAC-SHA256(raw_key, gateway_hmac_secret)` 的 hex 编码,**小写**)
- **算法权威**: gateway `internal/auth/keyhash/keyhash.go::Compute`;`gateway_hmac_secret` 即 gateway 进程 `GATEWAY_HMAC_SECRET` 环境变量,**控制面 / user-service 必须用同一个 secret 计算**(2026-05-16 出过一次回退把 gateway 侧降级为纯 SHA-256 的事故,所有 key 全部 401;算法 / secret 严禁单边改动)
- **常见错误**: 用纯 `sha256(raw_key)`(无 secret)→ hash 与 gateway 端不一致 → gateway 401 `key_not_found`
- **数据样例**: `"a1b2c3d4e5f6071829304152637485969a0b1c2d3e4f5061728394a5b6c7d8e9"`(实际值取决于 `gateway_hmac_secret`,不能用任意线下 sha256 工具复现)

##### `status`
- **是否需要外部传**: **需要** ★。Key 的生命周期状态由控制面 / portal 业务驱动。
- **触发时机**: ① 创建时填 `active`;② 用户主动暂停 / 控制面合规冻结 → `suspended`;③ 永久撤销 → `revoked`(更推荐用 §4.5 加进撤销集)。
- **默认值**: 无,空字符串 publisher 拒 400 `keymeta status required`。
- **数据类型**: `string`(枚举值)
- **数据样例**: `"active"` / `"suspended"` / `"revoked"`

##### `alias`
- **是否需要外部传**: **可选**。
- **触发时机**: portal 用户填的人类可读标签,纯展示 / 审计用,gateway 不做任何业务判断。
- **默认值**: `""`(空)。
- **数据类型**: `string`
- **数据样例**: `"alice-prod-key"`

##### `scope_attrs`
- **是否需要外部传**: **可选,但通常会填**。这是 key 关联的业务身份维度(tenant / workspace / app / department),gateway 透传到 UsageEvent / 计费事件 / 审计日志,**控制面用于聚合统计**。
- **触发时机**: user 服务编译 key 时,把 key 所属的 tenant_id / workspace_id / app_id / 自定义维度填进来。后续这把 key 发出的每条请求都会带上。
- **默认值**: `nil`(等价于 `{}`)。gateway 透传时若空,UsageEvent.Labels 也是空。
- **数据类型**: `map[string]string`(key = 维度名,value = 维度值)
- **数据样例**: `{"tenant_id":"t-001","workspace_id":"ws-42","app_id":"billing-app","department":"fin"}`

##### `expires_at`
- **是否需要外部传**: **可选**(默认永不过期)。
- **触发时机**: 用户在 portal 选 key 失效时间;或 user 服务按 SLA 自动续期 / 截止。
- **默认值**: `0`(永不过期)。Gateway `ExpiryChecker` 比对 `time.Now().Unix() > expires_at` 决定是否拒绝。
- **数据类型**: `int64`(Unix 秒)
- **数据样例**: `1735689600`(2025-01-01 00:00:00 UTC)

##### `ip_whitelist`
- **是否需要外部传**: **可选**(默认无 IP 限制)。
- **触发时机**: 用户在 portal 启用 IP 限制时填;企业账号常用。
- **默认值**: `[]`(无限制)。Gateway `IPWhitelistChecker` 检查 `ClientIP`,空列表跳过检查;非空但不匹配 → 401。
- **数据类型**: `[]string`(纯 IP 或 CIDR)
- **数据样例**: `["10.0.0.0/8", "192.168.1.1", "2001:db8::/32"]`
- **格式细则**(gateway `IPWhitelistChecker` 用 `net.ParseCIDR` / `net.ParseIP`):
  - ✓ 单 IP 接受:`192.168.1.1` / `2001:db8::1`(IPv4 自动按 `/32`,IPv6 自动按 `/128` 处理)
  - ✓ CIDR 接受:`10.0.0.0/8` / `2001:db8::/32`
  - ✓ IPv6 缩写接受:`::1` / `fe80::`
  - ✗ **不支持** IPv6 zone id 后缀:`fe80::1%eth0` 会被 `net.ParseIP` 拒(gateway 准入失败)
  - ✗ **不支持** 端口:`192.168.1.1:8080` 不是合法 IP
  - publisher 自身**不预校验**条目格式(只校验数组本身),错误格式由 gateway 运行时检测;建议 caller 自己 sanity check 后再推

##### `enforce_model_whitelist`
- **是否必传**: 可选;默认 `false`。
- **何时填**: 要给这把 key 启用模型白名单时填 `true`。
- **缺省行为**: `false` ⇒ 不做模型检查,key 可调用任何模型。
- **数据类型**: `bool`
- **数据样例**: `true`

##### `allowed_models`
- **是否必传**: 条件必传 — `enforce_model_whitelist=true` 时**必须非空**,否则 publisher 400 `enforce_model_whitelist=true requires allowed_models non-empty`。
- **何时填**: 与 `enforce=true` 一起填,列出这把 key 允许调用的 logical model id。
- **缺省行为**: 空数组在 `enforce=false` 时无意义(gateway 不读);在 `enforce=true` 时是非法状态被 publisher 拒。
- **数据类型**: `[]string`(每项是 logical model id)
- **数据样例**: `["gpt-4o", "claude-opus-4-7", "gemini-1.5-pro"]`
- **想全禁模型**: 用 `status=suspended` 或 `status=revoked`;**不要**用 `enforce=true` + 空 list(publisher 拒)。模型白名单字段只负责"启用白名单时哪些可用"。

##### `policies` — caller 业务侧 compile 产物
- **谁负责构造**: caller 业务侧。Caller 持有 policy 定义表(rate-limit / quota 规则),持有 tenant ↔ policy 映射关系,持有 workspace 配额订阅。Caller 把这些**业务真值**编译成 `EffectivePolicy[]` wire 形态推过来。
- **publisher 行为**: 只校验 wire shape(JSON 字段、类型、跨字段约束如 control_semantic 配 metric);**不解析业务语义**(不验 policy_id 是否对应实际 policy,不验 limit_value 合理性,不重算 RuntimeKeys)。透传到 Redis。
- **gateway 读取语义**: `UnifiedPolicyGate` 评估;空 → 不评估;非空 → 按 EffectivePolicy 各字段评估限流 / quota。
- **数据类型**: `EffectivePolicy[]`,字段定义见 §10.3。每条 14 字段(version / policy_id / policy_type / control_semantic / metric / scope / scope_id / period_kind / limit_value / enforcement_mode / conditions / runtime_keys / ...)— **业务侧 compile 时必须正确填全**,否则 gateway 评估失败。
- **数据样例**(一把 key 同时配 RPM 和 TPM 两条 policy,**注意:RPM/TPM 是两条独立 EffectivePolicy,各占数组一项,不要试图塞到同一条里**):

  ```json
  [
    {
      "version": 2,
      "policy_id": "rpm-key-550e8400",
      "policy_type": "rate_limit",
      "control_semantic": "window_reject",
      "subject_side": "user",
      "metric": "rpm",
      "scope": "key",
      "scope_id": "550e8400-e29b-41d4-a716-446655440000",
      "window": "60s",
      "limit_value": 600,
      "runtime_keys": {
        "family": "rl",
        "count_suffix": "key:{scope_id}:rpm:{window}:{window_id}"
      }
    },
    {
      "version": 2,
      "policy_id": "tpm-key-550e8400",
      "policy_type": "rate_limit",
      "control_semantic": "window_reject",
      "subject_side": "user",
      "metric": "tpm",
      "scope": "key",
      "scope_id": "550e8400-e29b-41d4-a716-446655440000",
      "window": "60s",
      "limit_value": 200000,
      "runtime_keys": {
        "family": "rl",
        "count_suffix": "key:{scope_id}:tpm:{window}:{window_id}"
      }
    }
  ]
  ```

  关键字段约束(任一不满足 gateway 静默跳过这条 policy):
    - `policy_type` 必须是字面 `"rate_limit"`(**不是** `"rate"`;旧文档曾写错,gateway 端只 match `rate_limit`)
    - `metric` ∈ {`rpm`, `tpm`} — RPM 算请求条数,TPM 算 token 数(input 估算 + max_tokens × 0.5,见 gateway `estimateTokens`)
    - `scope`=`"key"` 时 `scope_id` 必须**等于 KeyMeta.key_id**(scope 还支持 `tenant`/`workspace`/... 时 scope_id 来自 `scope_attrs[...]`)
    - `window` 非空(canonical `"60s"`);`limit_value > 0`
    - `runtime_keys.count_suffix` 占位符 `{scope_id}` / `{window}` / `{window_id}` 由 gateway materializeEffectivePolicy 在请求时展开,**不要 caller 端预先填实际值**
    - 同一把 key 上 RPM + TPM **两层独立叠加**,任一打满都 reject;并发限(concurrency)需要单独的 `policy_type=concurrency`、`metric=cc` policy

##### `resource_bindings` — caller 业务侧 compile 产物
- **谁负责构造**: caller 业务侧。Caller 持有 workspace 树 + policy 所属关系,**编译时把祖先链 + binding 关系扁平化成数组**,让 gateway 准入热路径 O(1) 索引(不动态走 workspace 树)。
- **publisher 行为**: 同上 — 校验 wire shape,透传不展开。
- **gateway 读取语义**: 准入热路径 O(1) 索引;空 → 无 quota 评估;非空 → 按 (scope_type, scope_id, metric) 索引 Redis runtime state。
- **数据类型**: `ResourceBinding[]`,字段定义见 §10.A。每条 10 字段(scope_type / scope_id / metric / unit_kind / state_semantic / reservation_mode / period_kind / source_policy_id / lineage_role 等)— **业务侧 compile 时必须与 policies 对应**。
- **数据样例**: `[{"binding_id":"b1","scope_type":"workspace","scope_id":"ws-42","metric":"amount_usd","unit_kind":"micro","state_semantic":"settle_only","period_kind":"month"}]`

##### `allowance_bindings` — caller 业务侧 compile 产物(过渡)
- **谁负责构造**: caller 业务侧。每条 binding 描述一个 allowance flag(`scope` + `scope_id` + `period`),gateway 准入时按这些 binding 查 allowance 状态。
- **publisher 行为**: 校验 wire shape,透传不展开。
- **gateway 读取语义**: 准入时检查所有 binding 对应的 allowance flag,任一为禁用或缺失 → 拒绝。
- **数据类型**: `AllowanceBinding[]`,字段定义见 §10.B。
- **生命周期**: 计划退役 — 未来 gateway 改用单 key 布尔 flag。caller 短期填空数组即可,退役落定后所有写侧停发该字段。
- **数据样例**: `[{"scope":"tenant","scope_id":"acme","period":"202605"},{"scope":"key","scope_id":"550e8400-...","period":"20260513"}]`

##### `max_tokens_per_call`
- **是否需要外部传**: **可选**(不限时填 0)。
- **触发时机**: 平台 / 用户给单把 key 设单次调用 token 天花板("防极端单次"),与 policy 维度限流互补。
- **默认值**: `0`(不限)。Gateway `SingleCallCapChecker` 看到 0 → 跳过;非零且 `prompt+completion 估值 > 阈值` → 拒。
- **数据类型**: `int64`(token 数)
- **数据样例**: `8192`

##### `guardrails`
- **是否需要外部传**: **可选**(无护栏需求时不填)。
- **触发时机**: 平台 / 用户启用内容护栏(限消息数 / 限工具 / 限视觉 / 限 max_output_tokens)时。
- **默认值**: `nil`(全部不限)。
- **数据类型**: `GuardrailPolicy`,字段定义见 §10.2
- **数据样例**: `{"max_messages":50,"max_output_tokens":4096,"allow_tools":true,"allow_vision":false}`

#### 4.1.x 总结

**真正需要外部 user-service 传**:`key_id` / `key_hash` / `status` (3 个必填)+ 业务侧选项(`alias` / `scope_attrs` / `expires_at` / `ip_whitelist` / `allowed_models` / `policies` / `resource_bindings` / `allowance_bindings` / `max_tokens_per_call` / `guardrails`)。

**永远不要传给 publisher**:raw_key(原始 API key 明文)。raw_key 在 caller 侧 hash 后,只把 hash 传 publisher。

#### 4.1.y scope_attrs

**作用**:`scope_attrs` 是 key 关联的业务身份维度。Wire 形态就是普通 `map[string]string`,publisher 透传不做 key/value 内容校验,gateway 端也不解析任何特定 key 名。

**数量上限**:最多 **10 对 key/value**;超过 → 400。每对都会出现在该 key 发起的每条请求生成的事件里(`event.labels`),留 10 个名额给业务侧选合适的维度组合。

**gateway 与下游消费方怎么用**:
- gateway 把整个 map 透传到 `UsageEvent.labels` / `ErrorEvent.labels`,**不预设任何 key 名**
- 限流 / 配额策略通过 `EffectivePolicy.scope` 字段动态指定查哪个维度(scope=`tenant` 时 gateway 查 `scope_attrs["tenant_id"]`,scope=`region` 时查 `scope_attrs["region_id"]`),caller 加新维度无需 gateway 改代码
- 下游消费方(billing 聚合 / 监控 / 风控)拿 `event.labels` 按需取自己关心的 key 做聚合 / 分桶

**业务侧可以传的维度**(任意命名,gateway 完全不预设):
- 标识类:`tenant_id` / `workspace_id` / `app_id` / `region_id` / `cost_center` / `department` / ...
- 自定义业务维度:推荐 `usr.<name>` 前缀(避免命名冲突)
- 运维 / 实验维度:推荐 `ops.<name>` 前缀

命名稳定 — 一旦定下别再改名,改名会让下游聚合断档。

**约束**:
- Value 必须是字符串(map[string]string;数字 / bool 需先转字符串)
- **避免高基数 value**(每条请求一个不同值,如 request_id)— 会让监控维度爆炸,聚合无意义
- **不要塞敏感信息**(raw_key / 密文 / 身份证 / 邮箱)— 会透传进事件日志造成泄漏

**示例**:
```json
"scope_attrs": {
  "tenant_id": "t-001",
  "workspace_id": "ws-42",
  "app_id": "billing-app",
  "usr.department": "finance",
  "usr.cost_center": "cc-7733",
  "ops.experiment": "rollout-v2"
}
```

#### 4.1.z `policies` / `resource_bindings` / `allowance_bindings` 的编译职责

这三个字段是业务侧的策略 compile 产物,publisher 透传不展开。

**Caller 业务侧的职责**:
- 持有业务真值表(tenant / workspace 树 / policy 定义 / quota 模板 等)
- 把业务意图编译为 wire 类型 — 例如:"这把 key 属于 tenant T,适用 RPM 600 / 月度配额 1000 USD policy" 编译成对应的 `EffectivePolicy` + `ResourceBinding` 数组
- 跨字段一致性自检:同一 `policy_id` 在 `policies` 和 `resource_bindings` 中的 scope / period 必须对应
- 引用的 `policy_id` 必须存在于自家 policy 定义表
- 严格遵守每个子字段的 JSON tag(跟 gateway `model.*` 1:1 锁定,字段子结构见 §10.3 / §10.A / §10.B)

**publisher 的职责**:
- 校验 wire shape:JSON 字段类型、enum 白名单、跨字段约束、单位
- 不解析 `policy_id` 是否对应实际定义
- 不重算 RuntimeKeys / Conditions 子字段
- 不验 limit_value 业务合理性
- 不验 ResourceBinding 与 EffectivePolicy 之间的语义一致性

### 4.2 POST `/user/api/v1/keys/bulk` — 批量写入

| 维度 | 内容 |
|---|---|
| **用途** | 一次性发布多把 key。Body `{"items":[KeyMeta×N]}`,N ≤ 100 |
| **场景** | 冷启动 user-service 把所有未过期 key 推一遍;批量轮换 |
| **何时调** | 冷启动 / DR 重建;日常单条用 §4.1 |
| **生效时间** | ~10ms;**慎用**:批量写会让 gateway 端缓存整体失效,所有 key 首次访问需重新拉取 |

### 4.3 DELETE `/user/api/v1/keys/{key_hash}` — 单 Key 删

| 维度 | 内容 |
|---|---|
| **用途** | 永久删除 key 的元数据 |
| **与 §4.5 撤销集的差异** | 删 = 物理消失,gateway 端 401 `key_not_found`,**不可逆**;撤销集 = 元数据保留但 gateway 拒 401,**可恢复**(§4.7)。**永久作废 / GDPR 删除 / 注销账户** 用 DELETE;**可能恢复** 用撤销集 |
| **路径参数** | `{key_hash}` = `HMAC-SHA256(raw_key, gateway_hmac_secret)` 64-hex 小写 |
| **生效时间** | ~10ms,后续请求 401 |

### 4.4 DELETE `/user/api/v1/keys/bulk` — 批量删

| 维度 | 内容 |
|---|---|
| **用途** | Body `{"key_hashes":["...","..."]}`,N ≤ 100。批量删 |
| **场景** | 注销整个客户的所有 key |

### 4.5 POST `/user/api/v1/revoked-keys` — 撤销集加入(单条)

| 维度 | 内容 |
|---|---|
| **命名空间** | user(SRE 通过 §7.1 走另一路功能等价) |
| **用途** | 把 key 加进撤销集,gateway 立即拒后续请求(401)。元数据保留可恢复 |
| **场景** | ① 客户在 user 主动"撤销 key";② 控制面安全检测出异常 key 自动撤销 |
| **何时用 §4.5 而非 §4.3 DELETE** | DELETE = 物理删,不可逆;撤销集 = 软删,可恢复(§4.7)|
| **生效时间** | < 1s,新请求被拒 401 |

**Body 字段表**:

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `key_hash` ★ | string(64 hex)| ✓ | (无)| `HMAC-SHA256(raw_key, gateway_hmac_secret)`,与 §4.1 的 KeyHash 同一概念 |

### 4.6 POST `/user/api/v1/revoked-keys/bulk` — 撤销集加入(批量)

| 维度 | 内容 |
|---|---|
| **用途** | Body `{"key_hashes":[...]}`,N ≤ 100,一次封多把 |
| **场景** | 批量泄露事件 |

### 4.7 DELETE `/user/api/v1/revoked-keys/{key_hash}` — 撤销集移除(单条)

| 维度 | 内容 |
|---|---|
| **用途** | 把 key 从撤销集移出,gateway 恢复接受该 key 的请求 |
| **场景** | 误判恢复 / 安全调查后确认未泄露 |
| **生效时间** | < 1s,gateway 恢复接受该 key |

### 4.8 DELETE `/user/api/v1/revoked-keys/bulk` — 撤销集移除(批量)

| 维度 | 内容 |
|---|---|
| **用途** | Body `{"key_hashes":[...]}`,一次解封多把 |

### 4.9 PUT `/user/api/v1/revoked-keys` — 撤销集整集替换

| 维度 | 内容 |
|---|---|
| **用途** | 整集替换 — gateway 端撤销集会被 body 的 `key_hashes` 数组完全覆盖。**body 没列的 key 视为解封**(危险) |
| **场景** | 冷启动 user-service 整体 push 撤销列表 / DR 重建 |
| **慎用** | 漏写任何 hash 都会被解除撤销 → 等价于解封;应使用 PATCH 思路:先读现有,合并,再 replace |

**Body 字段表**(§4.6 / §4.8 / §4.9 同形):

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `key_hashes` ★ | string[] | ✓ | (无)| 64-hex 数组,N ≤ 100(全集替换可为空数组 → 清空集合)|

---

## 5. `/billing/api/v1/*` — Key 余额

绑定 `billing-service` token。控制面计费 / 余额扣减服务专用。

### 5.1 PUT `/billing/api/v1/key-balance/{key_id}` — 设单 Key 余额标记

| 维度 | 内容 |
|---|---|
| **命名空间** | billing |
| **用途** | 标记某把 key 当前是否有余额可用;欠费时附带拒因供 gateway / portal 展示 |
| **场景** | ① 充值成功 → has_balance=true;② 余额扣到 0 → false + reason=`balance_depleted`;③ 待对账风险超限 → `settlement_risk_limit_exceeded`;④ 已形成应收 → `account_debt`;⑤ 结算侧待人工复核 → `settlement_review`;⑥ 滚动配额耗尽 → `rolling_quota_exhausted`;⑦ 其他人工复核 / 未知限制原因兜底 → `billing_review` |
| **生效时间** | HTTP 202 只表示已持久写入 Publisher outbox,不表示 Redis/Gateway 已生效;必须轮询 §5.4 delivery receipt |

**Body 字段表**:

- `reason` 是条件必填(双向):`has_balance=false` 时必填且为白名单值;`has_balance=true` 时必须空字符串。两个方向都会 400。
- ⚠️ **白名单的上游事实源是 codesphere-billing 的 `PublisherBalanceReason`**(`apps/api/src/publisher/publisher.types.ts`),
  publisher 侧实现在 `internal/publisher/impl.go` 的 `keyBalanceReasons`。**billing 新增枚举必须 publisher 先行发布**:
  publisher 不认的 reason 不是「降级为未知」而是该 key 的 admission 投影**恒 400**,而 billing 消费
  `gateway.usage_events` 时对 400/422 抛异常 ⇒ 整条 Kafka 分区队头阻塞、该 key 冻结在上一次成功投影的准入态。
  2026-09-08 `settlement_review` 单边上线即触发过这一后果。

| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `has_balance` | `bool` | 必传 | — | `true` = 允许调用,`false` = 拒 |
| `reason` | `string` enum | 条件必传 | — | `has_balance=false` 时**必填**且必须是 `balance_depleted` / `key_spend_limit_exhausted` / `workspace_budget_exhausted` / `rolling_quota_exhausted` / `manual_freeze` / `account_debt` / `settlement_risk_limit_exceeded` / `settlement_review` / `billing_review` 之一;`has_balance=true` 时必须 `""`。任一方向不符 → 400 |
| `decision_id` | UUID | strict 模式必传 | — | Billing 决策的稳定身份;同一 version 重试必须保持相同 ID |
| `decision_version` | 正整数 int64 | strict 模式必传 | — | Billing 拥有的、每 key 单调递增业务水位;不能使用 Publisher 到达顺序 |
| `revision` | 正整数 int64 | 迁移期兼容 | — | 旧别名;与 `decision_version` 同时出现时必须相等。strict 模式不接受它代替显式字段 |

`[key_admission] strict=false` 为默认兼容模式,也可由 `PUBLISHER_KEY_ADMISSION_STRICT=true` 开启。Billing 全量发送 `decision_id + decision_version` 并验证 receipt 后再切 strict。

**Path 参数**:`{key_id}` 是 UUID v4(**不是** key_hash;是 KeyMeta.key_id)。
调用方需要从 user 创建 key 时分配的 UUID 中获取此值,不要用 key_hash(HMAC-SHA256 hex,64 字符)替代,publisher 严格 UUID 校验。

#### 5.1.x 字段逐项说明

##### `has_balance` 🟢 必填
- **是否需要外部传**: 需要。billing 是唯一知道"这把 key 有没有钱"的服务,publisher 无法代替决定。
- **触发时机**: ① billing 扣到 0 → 写 false;② 充值成功 → 写 true;③ 月度结算 / 风控冻结 → false。
- **数据类型**: `bool`
- **数据样例**: `true` / `false`

##### `reason` 🟢 条件必填
- **是否需要外部传**: 需要(`has_balance=false` 时)。审计 + 给 gateway / portal 展示给用户的拒因。
- **触发时机**: 同 `has_balance=false`,与拒绝原因一起填。
- **默认值**: `""`(`has_balance=true` 时**必须**空)
- **数据类型**: `string`(枚举白名单)
- **数据样例**: `"balance_depleted"` / `"key_spend_limit_exhausted"` / `"workspace_budget_exhausted"` / `"manual_freeze"`

### 5.2 PUT `/billing/api/v1/key-balance/bulk` — 批量设余额

| 维度 | 内容 |
|---|---|
| **用途** | Body `{"items":[{key_id,has_balance,decision_id?,decision_version?}],"reason":"..."}`,N ≤ 100;决策字段也可放顶层供全部 item 共用 |
| **场景** | 批量降级:一个 workspace 月度预算耗尽,一次性把名下所有 key 翻 0 |
| **bulk reason 语义** | 顶层 `reason` 被所有 `has_balance=false` 的 item 共用;`has_balance=true` 的 item 不被 reason 影响 |

### 5.3 DELETE `/billing/api/v1/key-balance/{key_id}` — 删余额条目

| 维度 | 内容 |
|---|---|
| **用途** | 写入带 `decision_version` 的 CAS tombstone 并删除主值;水位/墓碑保留,旧 PUT/DELETE 不能复活状态 |
| **场景** | key 已删(§4.3),清理对应的余额记录 |

Body 与 §5.1 使用同一 `decision_id` / `decision_version` 字段。兼容模式允许无 body 的 legacy delete;strict 模式必须发送 JSON body。legacy delete 永远不能覆盖已经存在的显式 Billing decision。

### 5.4 GET `/billing/api/v1/key-admission-deliveries/{batch_id}` — 查询投递回执

202 响应返回的 `batch_id` 用于查询。`status` 枚举:

- `pending`: 尚未执行,正在执行,或 Redis 瞬时失败等待重试;不是终态。
- `applied` / `idempotent` / `superseded`: 可自动处理的投递终态。
- `conflict`: 同 version 的 identity 或 state 不一致;Redis 不变且不会通用重试,需人工处理。
- `dead_letter`: Redis 技术失败耗尽重试,需告警/重放。
- `unverified`: 历史 delivered 行没有 outcome,不得推断为 applied。
- `mixed`: bulk 中出现多个可自动处理的终态(例如部分 applied、部分 superseded);调用方必须读取各 count,不得把整批视为 applied。

Bulk receipt 同时返回 `applied_count`、`idempotent_count`、`superseded_count`、`conflict_count`、`pending_count`、`dead_letter_count` 和 `unverified_count`。

该回执随 Publisher delivered-outbox retention 清理(默认 7 天),只是短期投递证明,不是永久财务 settlement receipt。Billing 必须及时轮询并把最终结果永久落入自己的 evidence/settlement receipt 表。

---

## 6. `/admin/api/v1/*` — Provider / 模型 / KeyPool / 平台配置

绑定 `admin-service` token。平台运维管理。

### 6.1 PUT `/admin/api/v1/providers/{provider_id}` — 注册 / 更新 Provider

| 维度 | 内容 |
|---|---|
| **命名空间** | admin |
| **用途** | 注册上游 LLM Provider:protocol_bindings / base_url / status / key_strategy。**不传 key**(那是 §6.12 keypool)。旧 `api_type` / `api_types` 已拒绝。 |
| **场景** | ① 接入新 Provider;② Provider 升级 base_url;③ 停用(`status=disabled`)→ gateway 路由跳过 |
| **生效时间** | < 1s,gateway 端 ModelCatalog / 路由层立即用新 Provider 配置 |

**Body 字段表**(`ProviderInfo`,见 §10.6):

> **path / body 同名字段**:URL path `{provider_id}` 是权威。body `id` 可省;若填须等于 path,否则 400。
| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `id` | `string` | 与 path 一致或省 | path 值 | path 权威;若 body 填则必须 == path,否则 400 |
| `provider_profile` | `string` | 可选 | `""` | 低维护编译输入。当前 publisher 内置 `openai_compatible`;当 `protocol_bindings` 为空且提供本字段或 `client_compatibility` 时,服务端会编译出显式 `protocol_bindings`。 |
| `client_compatibility` | `[]string` | 可选 | `["all"]`(仅 profile 编译时) | CLI/API 兼容目标。当前支持:`all`、`claude_code`、`codex`、`openclaw`、`gemini_cli`、`cursor`、`cline`、`cherrystudio`、`dify`。 |
| `protocol_bindings` | `[]ProtocolBinding` | 条件必传 | profile 编译时自动生成 | Provider 协议路由唯一输入,决定 gateway 对哪些 inbound protocol/capability/request_contract 放行,以及上游使用哪个 outbound_protocol/path/auth/translator。未使用 `provider_profile` / `client_compatibility` 编译时必须至少 1 条。旧 `api_type` / `api_types` 字段出现即 400。详见 §6.1.1 / §10.6.1。 |
| `default_protocol_binding` | `string` | 可选 | `""` ⇒ 不做隐式 fallback | 默认 binding 名称;若填写,必须引用 `protocol_bindings[].name`,否则 400。 |
| `code` | `string` | 可选 | 等于 `id` | 展示 / 日志用 provider code;不驱动 adapter 解析。adapter 由 `protocol_bindings` 决定。 |
| `name` | `string` | 可选 | `""` | 展示用,UI 列表标签;空字符串建议 fallback 到 `id` 显示 |
| `base_url` | `string` | 必传 | — | 上游 API 根地址(含 scheme,如 `https://api.openai.com/v1`)|
| `status` | `string` enum | 必传 | — | `active`(在用)/ `disabled`(gateway 路由跳过该 provider) |
| `key_strategy` | `string` enum | 可选 | `""` ⇒ 用 gateway 默认策略 | 接受值:`priority`(按 entry 的 priority 升序选,数字小的优先)/ `weight`(按 entry 的 weight 抽签);其它值 gateway 退回默认 |

#### 6.1.1 `protocol_bindings` 约定

Provider 已切到 **protocol_bindings-only**。`api_type` / `api_types` 是旧接口字段,当前 HTTP DTO 会直接返回 400,不会自动迁移或静默忽略。

低维护调用方可以不手写 `protocol_bindings`:传 `provider_profile:"openai_compatible"` 和 `client_compatibility:["all"]` 时,publisher 会生成以下显式 bindings:

| binding | 覆盖的入口 |
|---|---|
| `openai-chat` | OpenAI Chat Completions;Cursor / Cline / CherryStudio / OpenClaw 的 OpenAI-compatible chat |
| `anthropic-messages-to-openai` | Anthropic Messages;Claude Code / OpenClaw 的 Anthropic-compatible chat |
| `responses-to-openai-chat` | OpenAI Responses;Codex |
| `gemini-generate-to-openai` | Gemini generateContent;Gemini CLI |
| `openai-embeddings` | OpenAI Embeddings;Dify embeddings |

这些自动生成的 OpenAI-compatible bindings 使用 `path_prefix:"/v1"`;gateway 会对 `base_url` 已包含 `/v1` 的情况做路径去重。

最小 OpenAI-compatible Chat Provider 示例:

```json
{
  "name": "MiniMax",
  "code": "minimax",
  "base_url": "https://api.minimax.io",
  "status": "active",
  "protocol_bindings": [
    {
      "name": "openai-chat",
      "priority": 100,
      "accepts": [
        {
          "protocol": "openai",
          "capabilities": ["chat"],
          "request_contract": "chat_completions"
        }
      ],
      "outbound_protocol": "openai",
      "path_prefix": "/v1",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "compat_api_type": "openai",
      "capability_profiles": [
        "openai.chat_completions.full",
        "minimax.openai_chat.thinking",
        "minimax.openai_chat.response_extensions"
      ]
    }
  ],
  "default_protocol_binding": "openai-chat"
}
```

`protocol_bindings[]` 基础必填:

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | provider 内唯一、稳定;仅用于 trace / `default_protocol_binding` 引用,代码不要解析命名语义 |
| `accepts` | 是 | 至少 1 条 inbound acceptance tuple |
| `accepts[].protocol` | 是 | gateway ingress 产出的 inbound protocol,如 `openai` / `anthropic` / `gemini` / `glm` |
| `accepts[].capabilities` | 是 | 至少 1 个能力,如 `chat` / `embeddings` / `rerank` / `count_tokens` |
| `accepts[].request_contract` | 是 | 请求语义合同,如 `chat_completions` / `responses` / `anthropic_messages` |
| `outbound_protocol` | 是 | gateway 上游协议 bundle key,如 `openai` / `anthropic` / `gemini` / `glm` |
| `auth_scheme` | 是 | gateway auth scheme key,常见 `bearer` / `x-api-key` / `azure-key` / `google-key` / `jwt-or-bearer` |

可选扩展字段用于表达厂商私有兼容能力,由 gateway profile/capability 层消费:

| 字段 | 类型 | 说明 |
|---|---|---|
| `path_prefix` | string | provider/binding 级路径前缀;最终 path = `base_url + path_prefix + protocol template` |
| `translator` | string | 同协议常用 `passthrough`;跨协议时只能使用当前 bridge 白名单允许的 TranslationMatrix key |
| `compat_api_type` | string | 迁移期 AttemptPlan 兼容字段;通常等于 outbound protocol |
| `capability_profiles` | []string | 复用 gateway 内置能力片段,如 `openai.chat_completions.full` / `minimax.openai_chat.thinking` |
| `supported_features` / `unsupported_features` | []string | binding-local feature 覆盖 |
| `supported_params` / `unsupported_params` | []string | binding-local 请求参数白/黑名单覆盖 |
| `param_constraints` | map[string][]string | 单参数枚举/约束 |
| `feature_constraints` | map[string]string | feature 约束 |
| `combination_constraints` | []CombinationConstraint | 跨参数组合约束,如 `stream_options.include_usage` 要求 `stream=true` |
| `request_param_policy` | map[string]RequestParamPolicy | 参数兼容策略,如 Kimi 固定采样值 |
| `compatibility_mode` | CompatibilityMode | 是否启用 OpenAI client 默认值兼容、drop unsupported defaults 等 |
| `field_disposition` | FieldDispositionConfig | 记录 request/response 字段 preserve/drop/reject/adapt 等处理证据 |

#### 6.1.2 gateway code-bound 值域

以下字段必须与 gateway 代码枚举保持一致,不是自由字符串。admin 页面应下拉选择或由 profile/compiler 生成;publisher 会校验,不合法会 400 或 gateway 刷新时跳过 provider。

| 字段 | 当前允许值 | 含义 / 约束 |
|---|---|---|
| `accepts[].protocol` | `openai` / `responses` / `anthropic` / `gemini` / `glm` / `glm_coding` | 客户端入口协议;`responses` 是 inbound Responses,不要写成 `openai_responses` |
| `accepts[].capabilities[]` | `chat` / `embeddings` / `rerank` / `count_tokens` | 粗能力;必须与 request_contract 和 outbound_protocol 可表达能力匹配 |
| `accepts[].request_contract` | `chat_completions` / `responses` / `anthropic_messages` / `anthropic_count_tokens` / `gemini_generate` / `embeddings` / `rerank` | 请求语义合同;决定 path template 和 translator 选择 |
| `outbound_protocol` | `openai` / `openai_responses` / `anthropic` / `gemini` / `glm` / `glm_coding` / `doubao_ark` / `azure_openai` / `cohere_v2` | 上游协议 bundle;必须有 gateway adapter |
| `auth_scheme` | `bearer` / `x-api-key` / `azure-key` / `google-key` / `jwt-or-bearer` | 上游鉴权方式;不要用旧 `api-key-header` |
| `translator` | `passthrough` / `standard_to_openai` / `standard_to_gemini` / `responses_to_chat_synthesis`；`standard_to_anthropic` 仅为 legacy converter 名称,当前没有对应 bridge 白名单元组 | 请求/响应转换器;跨 wire-family chat 必须同时匹配 gateway TranslationMatrix 和 bridge 白名单;例如手写 OpenAI→Anthropic + `standard_to_anthropic` 当前会被 `cross_protocol_bridge_required` 拒绝 |
| `capability_profiles[]` | 见下方 profile 表 | gateway 内置能力片段;未知 profile 会校验失败 |
| `supported_features` / `unsupported_features` | 见下方 feature 表 | binding-local feature 覆盖;大小写敏感 |
| `supported_params` / `unsupported_params` / `param_constraints` keys / `request_param_policy` keys | 见下方 param 表 | 请求参数白/黑名单、约束、兼容策略 key;大小写敏感 |

当前 gateway 内置 `capability_profiles`:

```text
openai.chat_completions.base
openai.chat_completions.tools
openai.chat_completions.streaming_usage
openai.chat_completions.structured_output
openai.chat_completions.full
minimax.openai_chat.thinking
minimax.openai_chat.response_extensions
openai.responses.base
anthropic.messages.base
anthropic.messages.tools
gemini.generate_content.base
gemini.generate_content.tools
```

当前 gateway feature 值域:

```text
function_tools
streaming_tools
vision
json_mode
json_schema
reasoning
reasoning_split
streaming_usage
responses_stateful
mcp_tools
web_search
code_interpreter
```

当前 gateway param 值域:

```text
tools
tool_choice
response_format
reasoning_effort
thinking
reasoning_split
stream_options
stream
parallel_tool_calls
seed
logprobs
top_logprobs
max_tokens
temperature
top_p
stop
presence_penalty
frequency_penalty
n
```

`combination_constraints` 约束:

- `when` 必须非空。
- `requires` / `conflicts` 至少一个非空。
- field 必须是上方 param 或 param 子路径,如 `stream_options.include_usage` / `response_format.type`。
- field 和 value 都不能为空。

当前 path template 支持的主要组合:

| outbound_protocol | capability | request_contract | path |
|---|---|---|---|
| `openai` | `chat` | `chat_completions` | `/chat/completions` |
| `openai` | `embeddings` | `embeddings` | `/embeddings` |
| `openai_responses` | `chat` | `responses` | `/responses` |
| `anthropic` | `chat` | `anthropic_messages` | `/messages` |
| `anthropic` | `count_tokens` | `anthropic_count_tokens` | `/messages/count_tokens` |
| `gemini` | `chat` | `gemini_generate` | `/models/{upstream_model}:{action}` |
| `gemini` | `embeddings` | `embeddings` | `/models/{upstream_model}:embedContent` |
| `glm` | `chat` | `chat_completions` | `/chat/completions` |
| `glm` | `embeddings` | `embeddings` | `/embeddings` |
| `glm_coding` | `chat` | `chat_completions` | `/chat/completions` |
| `doubao_ark` | `chat` | `chat_completions` | `/chat/completions` |
| `doubao_ark` | `embeddings` | `embeddings` | `/embeddings` |
| `azure_openai` | `chat` | `chat_completions` | `/chat/completions` |
| `azure_openai` | `embeddings` | `embeddings` | `/embeddings` |
| `cohere_v2` | `rerank` | `rerank` | `/rerank` |

常见 provider profile 由 admin/compiler 生成,caller 通常不手写完整 binding:

| Profile | 典型上游 | 说明 |
|---|---|---|
| `openai_compatible` | DeepSeek / OpenAI 兼容代理 | OpenAI Chat Completions 基线 |
| `minimax_openai` | MiniMax | OpenAI 兼容 + MiniMax thinking / response extension profiles |
| `moonshot_openai` | Kimi/Moonshot | OpenAI 兼容 + 固定采样 `request_param_policy` |
| `deepseek_openai` | DeepSeek | OpenAI 兼容基线,保留后续厂商扩展位 |
| `qwen_openai` | DashScope compatible-mode | OpenAI 兼容基线,保留后续厂商扩展位 |

### 6.2 DELETE `/admin/api/v1/providers/{provider_id}` — 删 Provider

| 维度 | 内容 |
|---|---|
| **用途** | 永久退役 Provider |
| **何时 DELETE 而非 status=disabled** | 永久;disabled 可恢复 |
| **顺序** | 应先把引用此 provider 的所有 route 删除/改其它 provider,否则 gateway 选路会跳过该 provider(等同 disabled 效果但 metadata 残留)|

### 6.3 PUT `/admin/api/v1/models/{model_id}/catalog` — 设模型 catalog

| 维度 | 内容 |
|---|---|
| **命名空间** | admin |
| **用途** | 模型元信息(展示名 / capabilities / context_window / max_output / 原厂 owned_by)。**与 route 解耦**:route 写"哪个 provider 服务这个模型",catalog 写"这个模型是什么" |
| **场景** | ① 上架新模型;② 改 max_output 上限;③ 改 owned_by |
| **生效时间** | < 1s,gateway `/v1/models` 立即反映 |

**Body 字段表**(`ModelCatalogEntry`,见 §10.4):

> **path / body 同名字段**:URL path `{model_id}` 是权威。body `model_id` 可省;若填须等于 path,否则 400。
| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `model_id` | `string` | 与 path 一致或省 | path 值 | path 权威;若 body 填则必须 == path |
| `display_name` | `string` | 可选 | `""` ⇒ UI 建议 fallback 到 `model_id` 显示 | UI 标签 |
| `capabilities` | `[]string` enum | 可选 | `[]` ⇒ gateway 默认按 chat 处理 | gateway 当前接受值(**小写,大小写敏感**,以 `tokenplatform-gateway/internal/model/capability.go` 为准):`chat` / `stream` / `tools` / `streaming_tools` / `json_mode` / `vision` / `reasoning` / `embeddings` / `rerank`。legacy 值 `function_calling` 仍接受,gateway 入站时归一为 `tools`。其它 typo 不报错,但 gateway 路由 / 准入会按"未声明该能力"处理 |
| `context_window` | `int` | 可选 | `0` ⇒ gateway 不施加 context 上限 | 模型最大 context token 数(prompt + completion 合计);单位 **token**,不是字符 |
| `max_output` | `int` | 可选 | `0` ⇒ 按 provider 默认 | 单次响应最大 output token 数;单位 **token** |
| `request_param_policy` | `map[string]RequestParamPolicy` | 可选 | `{}` | 模型级请求参数兼容策略,用于固定采样值、枚举值或不支持参数处理;见 §10.6.2 |
| `owned_by` | `string` | 可选 | `""` ⇒ gateway 透出 `/v1/models` 时 fallback 到 `RouteEntry.ProviderID` | **原厂商**(如 `anthropic` 拥有 Claude 系列),与路由层 `ProviderID`(可能是代理)显式区分。建议填准确原厂名,客户端用 `/v1/models` 的 `owned_by` 字段做兼容判断时不会混淆 |

### 6.4 POST `/admin/api/v1/models/catalog/bulk` — 批量 catalog

Body `{"items":[{"model_id":"...","entry":{ModelCatalogEntry}}]}`,N ≤ 100。

### 6.5 DELETE `/admin/api/v1/models/{model_id}/catalog` — 删 catalog

下架模型。注意:**应配套 §6.8 DELETE route**。

### 6.6 PUT `/admin/api/v1/models/{model_id}/route` — 设模型路由

| 维度 | 内容 |
|---|---|
| **命名空间** | admin |
| **用途** | 描述"`gpt-4o` 走哪些 provider / 优先级 / 权重 / strategy / fallback / 默认 RoutePolicy" |
| **场景** | ① 接入新模型配主备路由;② 调整权重(灰度);③ 加 fallback_model;④ 加 alias_target(等价模型映射) |

**Body 字段表**(`RouteConfig`,见 §10.5):

> **path / body 同名字段**:URL path `{model_id}` 是权威。body `model_id` 可省;若填须等于 path,否则 400。
| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `model_id` | `string` | 与 path 一致或省 | path 值 | path 权威 |
| `alias_target` | `string` | 可选 | `""` ⇒ 本 entry 是真实路由 | 非空时本条记录不是路由目标,而是指向 `alias_target` 模型的等价指针;请求路由到本 model_id 时,gateway 重定向到 alias_target,不做 fallback 计量 |
| `providers` | `[]RouteEntry` | 必传 | — | 至少 1 个;空数组 → 400 |
| `fallback_model` | `string` | 可选 | `""` ⇒ 无跨模型 fallback | 跨模型 fallback 链的下一跳逻辑模型名 |
| `provider_strategy` | `string` enum | 可选 | `""` ⇒ 用平台默认 | 接受值:`priority`(按 entry 的 priority 升序选,数字小的优先)/ `weight`(按 weight 抽签)/ `latency`(按观测延迟挑最快)。`least_busy` 仅在 gateway 启动时显式注册对应 selector 后可用,普通 admin 配置不建议使用;其它值 gateway 退回平台默认 |
| `default_policy` | `RoutePolicyConfig` | 可选 | `{}` ⇒ 用 `PlatformConfig.DefaultRoutePolicy` | 见 §10.5.2 |

**RouteEntry 字段**(`providers[]` 每条):

| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `provider_id` | `string` | 必传 | — | 必须引用 §6.1 已注册的 provider id |
| `upstream_model` | `string` | 必传 | — | 上游 API 真实模型名(如 `gpt-4o-2024-08-06`),不是 logical model |
| `priority` | `int` | 必传 | — | **越小越优先**(0 最优先)。取值无强制范围,推荐 0~100。多条同 priority → `weight` strategy 按 weight 抽,其它 strategy 按数组顺序 fallback |
| `weight` | `int` | 可选 | `0` ⇒ 当 `1` 处理(参与 `weight` strategy 抽签,等同其它 weight=1 的 entry)| **`weight=0` 不等于禁用**!想禁用某条 entry 请用 `status=disabled`。`weight` strategy 按权重比例抽:weight 越大被抽中概率越高 |
| `status` | `string` enum | 必传 | — | `active` / `disabled`(disabled → gateway 路由跳过该 entry) |
| `api_version` | `string` | 可选 | `""` | 上游 route 级 API version,如 Azure/Gemini 类上游需要时使用 |
| `anthropic_beta` | `string` | 可选 | `""` | route 级 Anthropic beta header 值,如 `prompt-caching-2024-07-31` |
| `request_param_policy` | `map[string]RequestParamPolicy` | 可选 | `{}` | route 级参数兼容策略,优先级高于模型级策略 |
| `compliance_tags` | `[]string` | 可选 | `[]` | 合规标签(自定义)。`adapter` 已移除,route 不再支持 per-route adapter override;协议由 provider `protocol_bindings` 决定 |
| `compliance` | `CompliancePolicy` | 可选 | `{}` | 合规策略(region / 零数据保留 / 不训练等);gateway 当前仅在标记字段透传,具体行为见 §10.5.3 |

补充约束:

- `model_id` / `fallback_model` / `alias_target` 是 logical model name,不得包含 `/`;不要写 `openai/gpt-4o` 这类 provider-prefixed model。
- `provider_id` 必须能在 §6.1 ProviderInfo 中找到,大小写和空格都不要漂移。
- `upstream_model` 必填,除非上层 compiler 有明确 fallback 到 logical model 的规则。
- `priority` 建议 `0~100` 或 `1~100` 内统一口径;gateway 语义是数字越小越优先。
- `weight` 建议正整数;gateway 对 `<=0` 会按 `1` 处理,但 admin/publisher 应拒绝负数。
- `api_version` 和 `anthropic_beta` 建议由 provider_profile 白名单约束;填错通常表现为上游 400/404。

### 6.7 POST `/admin/api/v1/models/routes/bulk` — 批量 route

Body `{"items":[{"model_id":"...","route":{RouteConfig}}]}`。

### 6.8 DELETE `/admin/api/v1/models/{model_id}/route` — 删 route

模型停售。Gateway 该 model 不再可路由,chat 返 `model_not_found`。

### 6.9 PUT `/admin/api/v1/key-pools/{provider_id}` — 整池替换

| 维度 | 内容 |
|---|---|
| **用途** | 一次性把 provider 名下整池 key 替换 |
| **场景** | 冷启动 / DR 重建 / Provider 全量 key 轮换 |
| **何时不该用** | 日常单条增删用 §6.12 per-entry,整池替换会让 gateway 重拉整 pool **短暂窗口**抖动 |
| **Body** | `KeyPoolConfig`(`{meta:{provider_id,strategy,revision},entries:[KeyPoolEntry]}`)|

### 6.10 POST `/admin/api/v1/key-pools/bulk` — 多 provider 整池

Body `{"items":[{"provider_id":"...","pool":{KeyPoolConfig}}]}`。

### 6.11 DELETE `/admin/api/v1/key-pools/{provider_id}` — 删整池

Provider 退役 / 全部 key 报废。

### 6.12 PUT `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}` ★ 日常路径 ★

| 维度 | 内容 |
|---|---|
| **命名空间** | admin |
| **用途** | 增 / 改单把上游 key。**日常 admin 推荐路径** |
| **场景** | ① 新进一把 OpenAI key;② 改某把 key 的 priority/weight/rpm_limit;③ 把 key 标 `disabled`(永久退池) |
| **生效时间** | < 1s,gateway 端该 provider 的 keypool 立即用新条目 |

**Body 字段表**(`KeyPoolEntry`,见 §10.8):

> **path / body 同名字段**:URL path `{provider_id}` 和 `{key_id}` 都是权威。body `key_id` 可省;若填须等于 path,否则 400。body 中无 `provider_id`(只通过 path 提供)。

| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `key_id` | `string` | 与 path 一致或省 | path 值 | path 权威 |
| `provider_account_ref` | `string` | 历史读取可省；新写入必传 | 省略的新写入返回 400，不会覆盖已有条目 | 控制面 account registry 签发的 opaque stable ID；1..128 ASCII，匹配 `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`，原样透传且 API key 轮换时保持不变。严禁填写 API Key/密文、BaseURL、request ID 或 `key_id`/credential ref |
| `alias` | `string` | 可选 | `""` | 人类可读标签 |
| `allowed_models` | `[]string` | 可选 | `[]` ⇒ 允许该池所有模型 | 限制本条 upstream key 只跑列表内的 logical model;空 = 不限 |
| `ciphertext` | `string` | 必传 | — | KMS 加密密文,详见下方"ciphertext lifecycle" |
| `priority` | `int` | 必传 | — | **越小越优先**(0 最优先)。取值无强制范围;`priority` strategy 选最小;同 priority 多条按 weight 抽 |
| `weight` | `int` | 必传 | `0` ⇒ 当 `1` 处理 | **`weight=0` 不等于禁用**!想退池请用 `status=disabled`。Weighted strategy 按权重比例抽 |
| `rpm_limit` | `int` | 可选 | `0` ⇒ 不限 | 每分钟请求上限,**key-pool entry 维度**(本条 upstream key 自己的上限)|
| `tpm_limit` | `int` | 可选 | `0` ⇒ 不限 | 每分钟 token 上限,key-pool entry 维度 |
| `status` | `string` enum | 必传 | — | 见下方 "status 语义与生命周期" |

#### 6.12.x status 语义与生命周期

| 值 | caller 何时设 | 谁负责切换 | gateway 行为 |
|---|---|---|---|
| `enabled` | 默认上线状态 | caller(admin)初始化 + 故障恢复后手动重置 | 参与路由 |
| `disabled` | 永久退池(下线 / 泄漏 / 撤销)| caller(admin)手动设;**只能 caller 手动切回 `enabled`** | 路由跳过 |
**caller 实操**:写 KeyPoolEntry 只用 `enabled` / `disabled` 两值。gateway 运行时的 cooling / auth degradation 是独立健康状态,不应由 admin/publisher 写入 `KeyPoolEntry.status`。

#### 6.12.y `rpm_limit` / `tpm_limit` 与 `KeyMeta.policies` 的关系

两层限流独立叠加,**取最严**:
- `KeyPoolEntry.rpm_limit / tpm_limit` — **upstream key 维度**:这把 upstream key(可能被多个用户 key 共享使用)每分钟可承担的总请求 / token 上限
- `KeyMeta.policies` — **用户 key 维度**:某把用户 key 自己的限流策略(基于 EffectivePolicy.scope)

执行时 gateway 会同时校验两层,任一层耗尽即拒。

补充约束:

- `priority` 建议正整数,数字越小越优先。
- `weight` 建议正整数;gateway 对 `<=0` 会按 `1` 处理,但 admin/publisher 应拒绝负数。
- `rpm_limit` / `tpm_limit` 必须是非负整数;`0` 表示不限制。
- `allowed_models` 是 logical model 白名单;大小写敏感,必须和 catalog/route 的 `model_id` 一致。
- `enforce_model_whitelist=true` 时 `allowed_models` 应非空;否则 gateway 会按 deny-all 防御处理。

#### 6.12.x ciphertext 来源链路

`ciphertext` 是 **publisher 不参与生成、不校验格式**的纯透传字段;publisher 原样转发到 gateway。gateway 装配上游请求时用 KMS 解出 raw key 作为 Authorization 凭据。

**生成 ciphertext 的责任在 admin 服务**(admin 调本端点前必须先把 raw key 加密)。具体取决于部署形态:

| 部署模式 | 加密侧 | 加密算法 | 输出格式 |
|---|---|---|---|
| **生产(KMS 启用)** | admin 服务收到 raw key 后,调 AWS KMS `Encrypt`(`KMS_KEY_ID` env)产出 `CiphertextBlob` | AES-256-GCM(KMS envelope encryption,KMS 主密钥包裹 DEK)| base64-encoded `CiphertextBlob` 字符串(KMS SDK 默认产出) |
| **本地测试**(`GATEWAY_ALLOW_PLAINTEXT_KEYS=true`)| 不加密 | — | raw key 字符串(gateway 直接当 plaintext 用)|

**gateway 侧的解密流程**:
1. 拿到 `KeyPoolEntry.Ciphertext`
2. 调 `infra.KMS.Decrypt(ciphertext)` → raw API key 字节
3. 装配到上游请求的 `Authorization: Bearer <key>` 或对应 header

**caller 易踩的坑**:
- ❌ **传错 KMS key**:用了不同 region 的 KMS key,gateway 端会 `KMSAccessDenied` → 上游调用全失败。**publisher 此时无感知**(写入成功,运行时才暴露)。
- ❌ **传 raw key 当 ciphertext**:生产模式下 gateway `Decrypt` 失败 → `KeyResolveError`;**publisher 不做格式校验**,写入会成功(silent failure 风险)。
- ❌ **base64 vs binary**:KMS SDK 默认给 base64;若 caller 自己 base64 encode 了 binary blob 又用 binary 传过来,gateway 解码会失败。**统一约定:caller 传 base64 字符串,publisher 透传,gateway base64-decode 后调 KMS**。
- **ciphertext 不能用于身份比对**:同一把 raw key 用 KMS 加密两次会得到不同 ciphertext(KMS 加随机 nonce),解密结果一致但字符串不等。判断"是否同一把 key"应该用 key_id,不用 ciphertext。

### 6.13 POST `/admin/api/v1/key-pools/keys/bulk` — 批量 per-entry 增改

Body `{"items":[{"provider_id":"...","entry":{KeyPoolEntry}}]}`,跨 provider 一次提交。

### 6.14 DELETE `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}` — 删单把 key

### 6.15 DELETE `/admin/api/v1/key-pools/keys/bulk` — 批量删

Body `{"items":[{"provider_id":"...","key_id":"..."}]}`。

### 6.16 PUT `/admin/api/v1/platform-config` — 平台级运行时参数

| 维度 | 内容 |
|---|---|
| **命名空间** | admin |
| **用途** | 全局参数:节点级 rpm/cc / 超时 / retry budget / disclosure / auth 旋钮 / responses affinity |
| **场景** | ① 调整全局超时;② 收紧 / 放宽 retry budget;③ 紧急调高 auth 滥用阈值;④ 关 ResponsesAffinity |
| **生效时间** | < 1s,gateway 端全局参数即刻分发到所有相关组件 |

**Body 字段表**(关键字段,完整 schema 见 §10.9):

- `responses_affinity_enabled` 是 `*bool` 指针:**省略该 JSON key** = `null` = 启用粘性(默认开);显式写 `false` = 关闭粘性。要保持默认行为请不写该 key,**不要写 `false`**。
- `retry_budget` 三轴必须**整组全零或三个全非零**:部分非零部分零会被 publisher 入口 400 拒绝(原因:wire 语义"零"在 `retry_budget` 内表示"该轴 unlimited",与"零=用默认"冲突)。
- 其它 timeout / size 字段:零值表示"用 gateway in-code 默认"(`ApplyDefaults` 加载时翻成 yaml 兜底)。**caller 没法显式设 0 ms**;若要禁用某个 timeout 走 gateway 默认即可。

| 字段 | 类型 | 必填 | publisher 默认 | gateway 兜底(yaml 默认) | 说明 |
|---|---|---|---|---|---|
| `platform_rpm` | int | ✗ | 0 | yaml `platform_rpm` | 节点级 RPM 上限(每个 gateway pod 在本进程内独立计数,不跨 pod 聚合) |
| `platform_cc` | int | ✗ | 0 | yaml `platform_cc` | **节点级 CC 上限** |
| `default_provider_strategy` | string | ✗ | "" | `"weight"` | 模型未指定 strategy 时的兜底 |
| `default_route_policy.allow_fallback` | *bool | ✗ | nil | yaml default | 跨模型 fallback 默认开关 |
| `default_timeout_ms` | int | ✗ | 0 | 60000 | 非流式总超时 |
| `stream_timeout_ms` | int | ✗ | 0 | 300000 | 流式总超时 |
| `first_token_timeout_ms` | int | ✗ | 0 | 30000 | 流式首字节超时 |
| `attempt_timeout_ms` | int | ✗ | 0 | 20000 | 单次上游 attempt 超时 |
| `max_body_size` | int | ✗ | 0 | 10485760 (10MB) | 请求体上限 |
| `max_fallback_depth` | int | ✗ | 0 | 2 | 跨模型 fallback 跳数 |
| `responses_affinity_enabled` | *bool | ✗ | nil ⇒ **true** | true | Responses API session 粘性。生产留空(=on),只在故障应急显式 false |
| `provider_budget_policies` | EffectivePolicy[] | ✗ | `[]` | 无 | provider/account/model 级限流限额 |
| `auth.pre_auth_ip_rpm` | int | ✗ | 0 | 60 | 鉴权前 IP RPM |
| `auth.abuse_fail_threshold` | int | ✗ | 0 | 20 | 滥用判定阈值 |
| `auth.idempotency_ttl_sec` | int | ✗ | 0 | 86400 | Idempotency-Key 留存秒数 |
| `retry_budget.max_keys_per_provider` | int | ✗ | 0 | 2 | 单 provider 内重试上限 |
| `retry_budget.max_providers` | int | ✗ | 0 | 3 | 重试切多少个 provider |
| `retry_budget.total_wall_time_ms` | int64 | ✗ | 0 | 5000 | 重试总墙钟上限 |
| `disclosure_policy.mode` | string | ✗ | "" | `"anonymized"` | `anonymized` / `debug` / `transparent` / `custom` |
| `operational.idempotency_attr` | string | ✗ | "" | `"tenant_id"` | 幂等隔离用的 ScopeAttrs key — gateway 把同一 `Idempotency-Key` header 按这个 attr 的值隔离;缺省时 gateway 仍用 `tenant_id` |
| `operational.kafka_partition_attr` | string | ✗ | "" | `"tenant_id"` | Kafka 事件分区 key — gateway 把事件按这个 attr 的值分区,保证同一值的事件在同一分区(消费顺序稳定)|
| `operational.full_detail_attr` | string | ✗ | "" | `"tenant_id"` | full-detail 事件允许列表匹配用的 attr — gateway 配置的 full-detail 允许列表按这个 attr 的值匹配 |

**必填语义说明**:
- 除 `revision` 之外**所有字段都允许缺**(`omitempty`);publisher 接受空 body。但留空 = 沿用上一版 zero-value,**不**等于"恢复 yaml 兜底"
- Gateway `ApplyDefaults` 在加载时把零值翻成 yaml 兜底,所以 PlatformConfig 缺字段时 gateway 行为是 yaml 默认
- `retry_budget` 三轴 all-or-nothing(publisher 强校验):**整组全零** ⇒ gateway 用 in-code 默认 2/3/5000;**三个全非零** ⇒ 按 caller 给的值生效;**部分非零部分零** → 400 `retry_budget is all-or-nothing — set all three axes or leave all zero`

---

## 7. `/sre/api/v1/*` — 应急工具

绑定 `sre-oncall` token(**当前未签发**,需要时单独配置)。应急通道,绕过 user 常规流程。

### 7.1 POST `/sre/api/v1/keys/{key_hash}/disable` — 应急强停 Key

| 维度 | 内容 |
|---|---|
| **命名空间** | sre |
| **用途** | 紧急封禁 key,gateway 立即拒后续请求 401(同 §4.5 撤销集,但需 SRE token + 强制带 reason/operator 审计字段) |
| **场景** | ① 安全检测出 key 滥用,SRE 一键封;② 紧急合规事件断流 |
| **何时用 §7 而非 §4.5** | 需要审计追溯(谁封、为啥);日常 user 操作走 §4.5 |
| **生效时间** | < 1s,新请求被拒 401 |

**Body 字段表**:

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `reason` ★ | string | ✓ | (无)| 封禁原因,缺失 → 400。**publisher 仅校验非空**,不限长度 / 不限枚举 |
| `operator` ★ | string | ✓ | (无)| 操作人(推荐:SSO username / 邮箱),缺失 → 400。**publisher 仅校验非空**,格式自由 |

**Path 参数**: `{key_hash}` = `HMAC-SHA256(raw_key, gateway_hmac_secret)` 64-hex 小写,publisher `Valid()` 严校验

> **`reason` / `operator` 字段最终落点**:
> **`reason` / `operator` 字段最终落点**:
> - 仅供 publisher 端审计追溯(谁在何时封了谁)。
> - **gateway 端不读这两个字段**,不影响 gateway 拒绝行为(只要 key 进了撤销集,gateway 就 401)。
> - 想给客户看拒绝原因需走另一路:业务侧自维护一份 reason 字段并由 portal 展示。

### 7.2 POST `/sre/api/v1/keys/{key_hash}/enable` — 应急恢复 Key

误判 / 调查结束后恢复。Body 同 §7.1。

### 7.3 POST `/sre/api/v1/keys/bulk/disable` — 批量强停

Body `{"key_hashes":[...],"reason":"<...>","operator":"<...>"}`。批量泄露 / 内部账号合并迁移。

### 7.4 POST `/sre/api/v1/keys/bulk/enable` — 批量恢复

### 7.5 POST `/sre/api/v1/blocked-ips` — 加 IP 黑名单(单条)

| 维度 | 内容 |
|---|---|
| **命名空间** | sre |
| **用途** | 把 IP 加进 IP 黑名单,gateway 在准入链第一步直接拒该 IP 的请求 |
| **场景** | ① 检测到攻击源;② 误用客户 IP 临时封 |
| **何时需要 X-Forwarded-For 配合** | 客户走代理时 gateway 默认看 RemoteAddr。配 `trusted_proxies` 后才认 XFF 头作为客户 IP |
| **生效时间** | < 1s,gateway 端立即拒该 IP |

**Body 字段表**:

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `ip` ★ | string | ✓ | (无)| IPv4 / IPv6 字面值 或 CIDR(如 `1.2.3.4/24`)。`net.ParseIP` 校验失败 → 400 |

### 7.6 POST `/sre/api/v1/blocked-ips/bulk` — 批量加 IP

Body `{"ips":[...]}`。

### 7.7 DELETE `/sre/api/v1/blocked-ips/{ip}` — 解封单 IP

### 7.8 DELETE `/sre/api/v1/blocked-ips/bulk` — 批量解封

### 7.9 PUT `/sre/api/v1/blocked-ips` — 整集替换

Body `{"ips":[...]}`。**慎用**:整集替换 — body 没列的 IP 全部解封。

### 7.10 PUT `/sre/api/v1/overrides` — 路由覆盖(⚠️ 预留,未开放)

> 🚫 **现阶段不要调用**:
> - 端点已注册并接受请求,但 **gateway 路由层尚未消费 override 数据**,写了不会改变路由行为。
> - `scope` 三段格式 publisher 只做非空校验,不解析。
> - `action` 枚举语义未定;`param`(`json.RawMessage`)的 schema 未定。
> - 重新开放需要 gateway 侧实现 routing override 消费 + publisher 补 scope/action/param 校验 + 本文档补 schema。在此之前调用没有实际效果,只会累积无用记录。

| 维度 | 内容 |
|---|---|
| **状态** | ⚠️ 预留;gateway 路由层尚未消费 override 数据,publisher 也未实现 scope/action/param 内容校验 |
| **将来用途** | 应急封某条 (provider, base_url, upstream_model) 三元组,或注入 routing weight 覆盖 |

**Body 字段表**(预留字段,语义未定;**不要在生产调用**):

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `scope` ★ | string | ✓(非空) | (无)| 预期 `<provider_code>\|<base_url>\|<upstream_model>` 三段;publisher 仅校验非空,不解析三段 |
| `action` ★ | string | ✓(非空) | (无)| 预期 `disable` / `enable` / ...;具体枚举未冻结 |
| `param` | json.RawMessage | ✗ | nil | 每个 action 对应的 param schema 未定义;暂时不要传 |
| `reason` | string | ✗ | "" | 审计字段 |
| `effective_from` ★ | int64 | ✓ | (无)| Unix **秒**(不是毫秒);"立即生效"传 `time.Now().Unix()` 或更早的过去时间戳 |
| `effective_until` | int64 | ✗ | nil(永久)| Unix 秒(策略生效终点)|
| `created_by` | string | ✗ | "" | 操作人 |

### 7.11 POST `/sre/api/v1/overrides/bulk` — 批量 override

Body `{"items":[{"scope":"...","override":{ProviderOverride}}]}`。空 items → 400。

### 7.12 DELETE `/sre/api/v1/overrides` — 删 override

Body `{"scope":"..."}`。

### 7.13 DELETE `/sre/api/v1/overrides/bulk` — 批量删

Body `{"scopes":[...]}`。

### 7.14 POST `/sre/api/v1/replay` — 重放未送达的写请求(⚠️ 暂不开放)

> 🚫 **现阶段不要调用**:重放工具未发布。caller 调用没有 SRE 流程兜底,误操作风险大。等 SRE 应急流程与权限准入定稿后开放。下方字段表保留作为契约预览。

| 维度 | 内容 |
|---|---|
| **命名空间** | sre |
| **状态** | ⚠️ 暂不开放,等 SRE 应急流程定稿 |
| **用途** | 重新投递 publisher 内部未成功送达 gateway 的写请求 |
| **场景** | ① gateway 端短时不可达导致大量请求未投递,恢复后批量重投;② 调查某次写入未生效,定位哪条没到 |
| **Response** | 200 + `{"attempted":N,"succeeded":N,"failed":N,"batch_ids":["..."]}` |

**Body 字段表**:

| 字段 | 类型 | 必传 | 默认 / 缺省时行为 | 说明 |
|---|---|---|---|---|
| `batch_id` | `string` (UUID) | 条件必传 | — | 与 `since`/`until` 二选一;指定单个 batch 重放(此模式下 `status` 字段被忽略)|
| `since` | `string` (RFC3339) | 条件必传 | — | 与 `batch_id` 二选一,作为窗口下界。例:`"2026-05-16T08:00:00Z"`。**格式非法 → 400** |
| `until` | `string` (RFC3339) | 条件必传 | — | 窗口上界;`since` / `until` 至少给一个。**格式非法 → 400** |
| `status` | `string` enum | 可选 | `"undelivered"` ⇒ 待投递 + 失败 都重放 | 枚举:`pending` / `failed` / `undelivered` / `all`。仅时间窗模式生效,`batch_id` 模式下忽略 |
| `limit` | `int` | 可选 | `100` | 单次重放上限;最大 10000,超过 → 400 |
| `dry_run` | `bool` | 可选 | `false` | `true` 时只返回命中的 batch_id 列表,不实际重投 |

**输入组合校验**(组合非法 → 400):
- `batch_id` 与 `since`/`until` 同时给 → mutually exclusive
- `batch_id` 不给且 `since` / `until` 都不给 → requires either batch_id or a time window
- `since` / `until` 任一格式非法 → 400(必须 RFC3339)

---

## 8. 幂等

任何写请求都可以带 `Idempotency-Key`(任意字符串,通常用 UUID v4):
- 首次提交 → 返回新 batch_id 并接受请求
- 后续重复提交相同 key → 直接返回首次的 batch_id,不重复执行

调用方不得跨不同请求 body 复用同一个 `Idempotency-Key`;平台既有语义是 replay,即同 key 重试返回原 batch,而不是把"同 key 不同 body"升级为新写或冲突检测。

`POST /admin/api/v1/providers:from-intent` 必须携带 `Idempotency-Key`。该入口会为 intent 内每个产物派生确定性子 key(`<base>/<section>/<entityID>`),使同一个 RuntimeIntent 重试时按 provider/keypool/model_catalog/model_route/product_policy 逐实体 replay 收敛。

从合同包 `3.0.0` 起，RuntimeIntent 写合同使用 `schema_version=2`，且每个 `provider_credentials[]` 必须显式携带 `provider_account_ref`。`provider_endpoints[].provider_account_ref` 在通用 schema 中保持可选，但当 `credential_source=provider_entitlement` 时编译器要求它存在。Publisher 不生成、不 trim、不归一化该 ID；调用方必须原样使用上游账户注册表签发的 1..128 位 ASCII opaque stable ID（`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`）。本次仅将发生形状/语义变化的 `RuntimeIntent`、`ProviderCredential`、`ProviderEndpoint` schema version 升为 2；未变化的 intent component 与嵌套 object 保持 version 1。

**v1 → v2 回填不得复用历史幂等键。** Publisher 的既有 replay 语义会在比较新 body/schema 之前直接返回旧 batch；若回填仍使用 v1 的 base `Idempotency-Key`，HTTP 会成功返回旧 batch_id，但不会生成携带 `provider_account_ref` 的新 outbox/Redis 数据。迁移任务必须使用带迁移版本的新 base key（例如 `<old>/provider-account-ref-v2`），并按 keypool、dedicated upstream、provider entitlement 三条链路读取 Redis 产物确认 ref 已落地，不能只看 2xx。

适用场景:网络抖动重试、上游业务事务 commit-then-publish 双写。

幂等键保留窗口:7 天(可配)。窗口外重新提交视为新写。

---

## 9. Replay 与对账

每个写请求返回的 `batch_id`(UUID v4)是该请求的标识。当 publisher 多次重试仍未投递到 gateway,调用方:

- 监控 publisher Prometheus 指标(`*_pending` / `*_oldest_pending_age_seconds`)发现长时间未送达
- 多次重试仍未投递成功的请求会触发告警,由 SRE 介入人工查处
- 显式重投 API(`POST /sre/api/v1/replay`,§7.14)**暂不开放** — 在 SRE 应急流程定稿之前依靠告警 + 人工查处

新 Worker 首次接触 credential-bearing batch 时，会在执行任何 Redis 操作前读取并检查该完整 batch。若 keypool entry 缺少合法 `provider_account_ref`、dedicated `KeyMeta` 的嵌套 ref 缺失/非法，或 `provider_entitlement` 的 provider-level ref 缺失/非法，所有尚未终结的行直接进入 dead-letter 隔离；不会由新 Worker 先执行同批的 `DEL`/`INCR` 再失败。普通 KeyMeta 和普通 ProviderInfo 不要求该字段。若历史 batch 已被旧 Worker 部分执行，preflight 无法撤销此前 Redis 副作用，部署前必须单独扫描、修复或隔离，不能把“剩余行已 dead-letter”当成目标 Redis 状态正确。

**安全部署顺序**：本变更不能与旧 Publisher/Worker binary 混跑。先暂停 intent 和 credential 写流量，停止所有旧 Publisher/Worker，确认没有 in-flight lease，并排空或隔离历史 credential-bearing batch；随后仅启动一个带 preflight 的新 Worker，最后才恢复已经改为 `schema_version=2`、使用全新迁移幂等键且携带 account ref 的调用方。旧调用方缺 ref 或继续发送 RuntimeIntent v1 时会得到 400（fail-closed），不得为了维持写入可用性绕过。否则旧进程仍可能写入/执行 legacy batch，或在新 Worker 隔离前执行同批 `DEL`。本变更只保证新 Worker 对 credential evidence 的完整批前置检查和单 Worker 内的确定性批次顺序，**不宣称解决**通用的 batch-atomic claim：合法 batch 仍可能被多个 Worker 按行拆分；在 batch-atomic claim/advisory lock 上线前不得恢复多副本。

---

## 10. 关键数据类型

### 10.1 `KeyMeta`(`/user/keys`)

```go
type KeyMeta struct {
    KeyID                 string             `json:"key_id"`                   // UUID v4(必传)
    KeyHash               string             `json:"key_hash"`                 // 64 hex HMAC-SHA256(raw_key, gateway_hmac_secret)(必传)
    Alias                 string             `json:"alias,omitempty"`
    ScopeAttrs            map[string]string  `json:"scope_attrs,omitempty"`
    Status                string             `json:"status"`                   // active / suspended / revoked(必传)
    ExpiresAt             int64              `json:"expires_at"`               // unix seconds,0 = 永不过期
    IPWhitelist           []string           `json:"ip_whitelist"`
    EnforceModelWhitelist bool               `json:"enforce_model_whitelist,omitempty"` // true 时启用 AllowedModels
    AllowedModels         []string           `json:"allowed_models"`           // enforce=true 时必须非空
    Policies              []EffectivePolicy  `json:"policies,omitempty"`       // caller 业务侧编译产物
    ResourceBindings      []ResourceBinding  `json:"resource_bindings,omitempty"`
    AllowanceBindings     []AllowanceBinding `json:"allowance_bindings,omitempty"`
    MaxTokensPerCall      int64              `json:"max_tokens_per_call,omitempty"`
    Guardrails            *GuardrailPolicy   `json:"guardrails,omitempty"`
    DedicatedUpstream     *DedicatedUpstreamCredential `json:"dedicated_upstream,omitempty"`
}
```

`ScopeAttrs` 是数据驱动的观测维度(tenant_id / workspace_id / app_id / department / ...),由 caller 业务侧编译时填,gateway 透传到 UsageEvent。

`ResourceBindings` 是编译后的扁平化执行契约,gateway 用来索引每条 quota / reservation policy 的 runtime 状态(避免在热路径上动态走 workspace 树)。由 caller 业务侧编译填入。

`AllowanceBindings` 是 gateway 准入查询表 — 每条对应一个 (scope, scope_id, period) 三元组的 allowance flag。准入时 gateway 检查所有 binding 的 flag,任一为禁用或缺失 → 拒绝。本字段计划退役(未来收敛为单 key 布尔 flag)。

`DedicatedUpstream` 是可选的 per-user 上游 credential 绑定。运行时 wire 上的 `provider_account_ref` 保持 optional，以便旧 `gw:keymeta` 仍能解码；但 Publisher 对任何新的 dedicated 写入都要求该字段存在且为合法 opaque stable ID。当 `ciphertext` 非空时必须是 KMS 密文引用/包络(`kms:` 前缀),本地 AES-GCM 等非 KMS 密文会被 publisher 拒绝。该字段存在时 publisher 原样透传到 `gw:keymeta`;缺失时普通 KeyMeta 路径保持不变。

```go
type DedicatedUpstreamCredential struct {
    UpstreamKeyID     string                      `json:"upstream_key_id"`
    ProviderAccountRef string                     `json:"provider_account_ref,omitempty"`
    Ciphertext        string                      `json:"ciphertext"`
    CiphertextVersion int64                       `json:"ciphertext_version"`
    CiphertextSHA256  string                      `json:"ciphertext_sha256"`
    KMSKeyID          string                      `json:"kms_key_id"`
    Status            string                      `json:"status"`
    ExpiresAt         int64                       `json:"expires_at"`
    UpdatedAt         string                      `json:"updated_at"`
    FallbackAllowed   bool                        `json:"fallback_allowed"`
    Endpoints         []DedicatedUpstreamEndpoint `json:"endpoints"`
}
```

`ProviderAccountRef` 来自控制面 upstream account registry，并在 credential rotation 时保持稳定。不得用 API Key、密文、BaseURL、request ID、`upstream_key_id` 等值代替；Publisher 只校验非空、长度和 ASCII 字符集，并拒绝与本 lane 的 credential identity/reference 直接相等，不做账户归属验证。

### 10.2 `GuardrailPolicy`

```go
type GuardrailPolicy struct {
    MaxMessages     int   `json:"max_messages,omitempty"`     // cap request.messages 数
    MaxOutputTokens int   `json:"max_output_tokens,omitempty"`// cap request.max_tokens 值
    AllowTools      *bool `json:"allow_tools,omitempty"`
    AllowVision     *bool `json:"allow_vision,omitempty"`
}
```

### 10.3 `EffectivePolicy` — 用户 key RPM/TPM 样例

用户 key 当前只支持 RPM + TPM,字面照填即可(`{scope_id}` 替换成 KeyMeta.key_id):

```json
[
  {
    "version": 2,
    "policy_id": "rpm-key-<keyID>",
    "policy_type": "rate_limit",
    "control_semantic": "window_reject",
    "subject_side": "user",
    "metric": "rpm",
    "scope": "key",
    "scope_id": "<KeyMeta.key_id>",
    "window": "60s",
    "limit_value": 600,
    "runtime_keys": {
      "family": "rl",
      "count_suffix": "key:{scope_id}:rpm:{window}:{window_id}"
    }
  },
  {
    "version": 2,
    "policy_id": "tpm-key-<keyID>",
    "policy_type": "rate_limit",
    "control_semantic": "window_reject",
    "subject_side": "user",
    "metric": "tpm",
    "scope": "key",
    "scope_id": "<KeyMeta.key_id>",
    "window": "60s",
    "limit_value": 200000,
    "runtime_keys": {
      "family": "rl",
      "count_suffix": "key:{scope_id}:tpm:{window}:{window_id}"
    }
  }
]
```

#### 10.3.x 字段说明(每个字段:含义 + 用途 + 用户 key 取值 + 网关认可的完整枚举)

##### `version` — schema 版本号
- **含义**:这条 policy 用的 schema 版本。网关代码会随版本演进改字段,version 让两边能识别兼容性
- **用户 key 取值**:`2`(当前 `PolicySchemaVersion`)
- **完整枚举**:目前仅 `2`。不等 → KeyMeta 加载时 Validate 拒,**整把 KeyMeta 不可用**

##### `policy_id` — 这条 policy 的唯一名字
- **含义**:caller 自定的字符串 ID,用于事件追溯("某请求被哪条 policy 拒了"在 `policy_gate_reject` slog 的 `policy_id` 字段就是它)
- **用户 key 取值**:任意 unique 字符串。建议 `"rpm-key-<keyID>"` / `"tpm-key-<keyID>"` 这种自带语义
- **完整枚举**:无枚举(自由字符串),但**同一 KeyMeta 内 RPM/TPM 两条 entry 的 policy_id 必须不同**(否则去重逻辑会丢一条)

##### `policy_type` — policy 的大类
- **含义**:决定网关用哪条评估链路。`rate_limit` 走 RPM/TPM 计数 + Lua 原子 INCR;其他类型走完全不同的逻辑
- **用户 key 取值**:固定 `"rate_limit"`
- **完整枚举**:`"rate_limit"` / `"concurrency"` / `"budget"`(后两个是 platform-internal,**用户 key 不允许填**)
- ⚠️ **不是** `"rate"`(老文档曾写错,会被网关 Validate 拒)

##### `control_semantic` — 到达上限时的处理语义
- **含义**:窗口/限额打满后,**下一个请求该怎么办**。决定是 reject、降级、还是只记录
- **用户 key 取值**:固定 `"window_reject"`(满了直接 reject 返 429,详见 §10.3.y)
- **完整枚举**(按 policy_type 限值):
  | policy_type | 合法 control_semantic | 行为简述 |
  |---|---|---|
  | `rate_limit` | `"window_reject"` | 当前窗口内打满 → 该窗口剩余时间内所有请求 429 reject;窗口结束自动重置 |
  | `rate_limit` | `"degrade"` | 打满不 reject,走降级链路(换 fallback model 等)。要求 `meta.degrade_target` 非空。**用户 key 当前不支持** |
  | `concurrency` | `"lease"` | 并发租约,请求结束才释放(platform-internal) |
  | `budget` | `"hard_cutoff"` | 配额耗尽直接拒(platform-internal) |
  | `budget` | `"accumulate_record"` | 配额超额只记账不拒(platform-internal) |
  | `budget` | `"degrade"` | 同上 rate_limit 的 degrade(platform-internal) |
- **必填**(不允许空字符串)

##### `subject_side` — 这把 key 的"身份立场"
- **含义**:这个限速是为"谁"立的 — 终端用户、采购侧、还是平台
- **用户 key 取值**:固定 `"user"`
- **完整枚举**:`"user"` / `"procurement"`(采购方) / `"platform"`(平台兜底)
- 用户 key 都是为终端用户立的限速,所以填 `"user"`

##### `metric` — 计什么
- **含义**:在窗口内统计的是什么数量
- **用户 key 取值**:`"rpm"`(请求数)或 `"tpm"`(token 数)
- **完整枚举**:`"rpm"` / `"tpm"` / `"request"` / `"cc"`(并发) / `"token"` / `"tokens"` / `"tokens_total"`(后几个是其他 policy_type 用的,用户 key rate_limit 只用 rpm/tpm)
- ⚠️ `control_semantic="lease"` 要求 `metric="cc"`(用户 key 不涉及)

##### `scope` — 计数维度(按谁分桶)
- **含义**:计数器以什么为粒度。`"key"` 表示**这把 key 自己**有独立计数器;`"tenant"` 表示**整个 tenant 下所有 key 合起来**共用一个计数器
- **用户 key 取值**:固定 `"key"`
- **完整枚举**:`"key"` / `"app"` / `"tenant"` / `"workspace"` / `"provider"` / `"provider_account"` / `"model"`
  - scope=key 时 scope_id 从 KeyMeta.key_id 取
  - scope=tenant / workspace / app 时 scope_id 从请求上下文的 `scope_attrs[<scope>+"_id"]` 取(需要 KeyMeta.scope_attrs 配齐对应字段)

##### `scope_id` — 这个 scope 下的具体 ID 值
- **含义**:跟 `scope` 配对,标识这条 policy 作用在哪个具体对象上
- **用户 key 取值**:**必须严格等于 KeyMeta.key_id**(scope=key 时)
- **完整枚举**:无固定值,跟 scope 配对取
- ⚠️ scope_id 跟 KeyMeta.key_id 不一致 → gateway `matchesPolicySubject` 静默过滤,**这条 policy 完全失效**(没限速效果,也没告警),是最常见的"配了 RPM 但不生效"原因

##### `window` — 计数窗口长度
- **含义**:rate_limit 的滚动窗口大小;每个 window 内独立计数,窗口结束自动归零
- **用户 key 取值**:`"60s"`(规范写法,1 分钟)
- **完整枚举**:自由字符串(`"60s"` / `"1m"` / `"5m"` / `"1h"` 等),网关按字面意义解析
- ⚠️ `control_semantic="window_reject"` 时必填非空,否则 Validate 拒

##### `limit_value` — 上限
- **含义**:窗口内允许的最大计数值
- **用户 key 取值**:正整数(RPM 是请求数,TPM 是 token 数)
- **完整枚举**:任意 ≥ 0 整数(负数 Validate 拒)。`= 0` 不会拒 schema,但 `matchesRateLimitPolicy` 会因为 `limit_value <= 0` 静默跳过 → 跟"没配 policy"一样
- ⚠️ TPM 单位是 **token 不是请求** — TPM=600 意味着每分钟最多 600 token(大约 0.3 个默认请求),不是 600 个请求。网关代码 `suspiciousLowTPMThreshold=4096`,低于这个值会打 Warn 提示"这个 TPM 是不是配错了"

##### `enforcement_mode` — 触发后是否阻断(仅 budget/concurrency 用,rate_limit 不读取)
- **含义**:打满后是真拒,还是只记录("shadow mode")。**用户 key 当前完全不读这个字段**(rate_limit 是否阻断只看 `control_semantic`)
- **用户 key 取值**:**留空**(任何值都不影响 rate_limit 行为)
- **完整枚举**:`""`(默认 block)/ `"block"` / `"hard_reject"`(都 → block)/ `"cap"` / `"record_only"`(都 → cap,只记录不拒)/ `"degrade"`(要求 `meta.degrade_target`)
- ⚠️ 老文档曾写 `"enforce"` / `"shadow"` 这两个值,**都是错的**,网关不认识 → 进 `normalizeEnforcementModeSemantic` 的 default 分支,fallback 到 block

##### `runtime_keys.family` — Redis key namespace
- **含义**:网关在 Redis 里用什么前缀给这条 policy 的计数器分桶。决定计数器跟哪一类策略共享 namespace
- **用户 key 取值**:固定 `"rl"`(rate_limit 类)
- **完整枚举**:`"rl"`(rate_limit)/ `"cc"`(concurrency)/ `"budget"`(budget)
- ⚠️ **必须跟 policy_type 匹配**:rate_limit→rl,concurrency→cc,budget→budget。不匹配 Validate 拒
- ⚠️ **不是** `"rate_limit"`(老文档曾写错)

##### `runtime_keys.count_suffix` — Redis 计数 key 的 suffix 模板
- **含义**:网关在 admission 时拼出最终 Redis key 的格式串,占位符在 materialize 时被替换成具体值
- **用户 key 取值**:`"key:{scope_id}:rpm:{window}:{window_id}"`(RPM)/ `"key:{scope_id}:tpm:{window}:{window_id}"`(TPM)
- **占位符语义**:
  - `{scope_id}` → 上面那个 scope_id 字段值
  - `{window}` → 窗口名(`60s`)
  - `{window_id}` → 当前窗口的实例 id(网关按时间算)
- ⚠️ **不要预拼实际 key**(不要把 `count_key` 字段填上真实 string),`count_key` 字段留空让网关自己根据 `count_suffix` + 占位符展开

##### 其他字段(用户 key 用不到,留空即可)
- `period_kind` / `period_id`:仅 `policy_type=budget` 使用(配额周期类型),用户 key rate_limit 留空。**填了反而触发 Validate 拒**("period_kind 只允许 budget")
- `unlimited`:`true` 时跳过这条 policy(等价不限速,只记录用)。用户 key 想生效就留 `false` 或不填
- `conditions`:可选,缩窄 policy 生效范围(只对某 model / provider / protocol / call_type / 时间段生效)。用户 key 想全局生效就留空
- `meta`:`control_semantic=degrade` 时填 `degrade_target`;`period_kind=contract` 时填 `contract_id`。用户 key 当前 control_semantic 固定 `window_reject`,留空

#### 10.3.y `window_reject` 是什么意思

control_semantic 的取值,语义:**当前 `window` 内累计达到 `limit_value` 后,下一个请求直接 reject 返 429**。

举例(`window=60s, limit_value=600`):

```
t=0s    第 1 个请求 → counter=1   ✓ 通过
t=1s    第 2 个请求 → counter=2   ✓
...
t=10s   第 600 个请求 → counter=600 ✓(刚好打满)
t=15s   第 601 个请求 → counter=601 > 600 → ✗ 429 rpm_exceeded
...
t=60s   counter TTL 到,Redis 自动删 key
t=61s   下一个请求 → counter=1   ✓ 新窗口开始
```

特性:
- **固定窗口,不是滑动**:窗口边界处可能 burst(t=59s 打满 600 + t=61s 再打 600 = 跨 2s 实际 1200,但平均不超 600)
- **Redis TTL 自动释放**:窗口结束自动重置,gateway 不需主动清
- **打满后剩余时间全 reject**:不会"间隔分摊"

跟 control_semantic 另一可选值 `degrade` 的区别(不在用户 key 范围,只列对照):`degrade` 是"到上限后不 reject,改走 fallback / 降级链路"。用户 key 当前不支持,只 `window_reject`。

#### 10.3.z 运行规则(gateway 怎么用这些 policy)

请求进网关时:

1. **加载 KeyMeta** — gateway 用 API key HMAC 后查 `gw:keymeta` Hash,反序列化出整个 KeyMeta 对象(含 policies 数组)
2. **PolicyResolver 匹配** — 遍历 policies,按下列条件过滤:
   - `policy_type == "rate_limit"`(其他类型当前用户 key 不该有)
   - `metric ∈ {rpm, tpm}`
   - `limit_value > 0`,`window` 非空,`unlimited == false`
   - `scope_id` 跟当前请求上下文匹配(scope=key 时严格等于 KeyMeta.key_id)
   - `conditions`(若填了)匹配当前 request 的 model/provider/protocol/call_type
   - 任一不满足 → **静默跳过这条 policy**(不告警,不计数)
3. **UnifiedPolicyGate 执行** — 把匹配到的 policies 一批送到 Lua 脚本:
   - 对每条 policy 拿 `runtime_keys.count_suffix` 展开成最终 Redis key
   - `INCRBY` 计数(RPM 加 1,TPM 加估算 token 数)
   - 第一次 INCR 时设 `PEXPIRE = window 毫秒数`(60s = 60000)
   - `current > limit_value` → **整批 reject**,返 `429 rpm_exceeded` / `429 tpm_exceeded`(含 `Retry-After` header)
4. **响应阶段 TPMCorrector** — 上游返完整 usage 后,gateway 用 `actual - estimated` 修正 TPM counter(estimated 是 admission 时按 bodySize/4 + max_tokens/2 算的)

观测:
- pass:无 slog(高 QPS 下不打)
- reject:gateway slog `"policy_gate_reject"`,含 `metric` / `current` / `limit` / `policy_id`
- 0 policy 匹配(常见配置 bug):Debug 级 `"policy_gate empty match"`,含 `policy_count_in_meta` 帮排查 publisher 是否下发了

响应 header:
- `X-RateLimit-Limit-RPM` / `X-RateLimit-Remaining-RPM`(匹配到 RPM policy 时返,无论 200 还是 429)
- `X-RateLimit-Limit-TPM` / `X-RateLimit-Remaining-TPM`(同上)
- 429 时多一个 `Retry-After` header(秒数)

### 10.4 `ModelCatalogEntry`

```go
type ModelCatalogEntry struct {
    ModelID            string                        `json:"model_id"`
    DisplayName        string                        `json:"display_name"`
    Capabilities       []string                      `json:"capabilities"`
    ContextWindow      int                           `json:"context_window"`
    MaxOutput          int                           `json:"max_output"`
    RequestParamPolicy map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
    OwnedBy            string                        `json:"owned_by"`
}
```

`OwnedBy` 是逻辑模型的**原厂商**(如 anthropic 拥有 Claude),与 `RouteEntry.ProviderID`(路由 target,可能是代理)显式区分。Gateway 把这个字段透传到 `ModelRoute.OwnedBy`,`/v1/models` 用它填 `owned_by` 字段;缺失时 fallback 到 ProviderID。

### 10.5 `RouteConfig`

```go
type RouteConfig struct {
    ModelID          string            `json:"model_id"`
    AliasTarget      string            `json:"alias_target,omitempty"`   // 等价模型映射(非降级)
    Providers        []RouteEntry      `json:"providers"`
    FallbackModel    string            `json:"fallback_model,omitempty"`
    ProviderStrategy string            `json:"provider_strategy,omitempty"`  // priority / weight / latency / ...
    DefaultPolicy    RoutePolicyConfig `json:"default_policy,omitempty"`
}
```

`AliasTarget` 非空时本条 entry 不是路由目标,gateway 视为指向 AliasTarget 的等价指针(不算 fallback,不另开 metering)。

#### 11.5.1 `RouteEntry`

```go
type RouteEntry struct {
    ProviderID         string                        `json:"provider_id"`
    UpstreamModel      string                        `json:"upstream_model"`
    Priority           int                           `json:"priority"`
    Weight             int                           `json:"weight"`
    Status             string                        `json:"status"`
    APIVersion         string                        `json:"api_version,omitempty"`
    AnthropicBeta      string                        `json:"anthropic_beta,omitempty"`
    ComplianceTags     []string                      `json:"compliance_tags,omitempty"`
    Compliance         CompliancePolicy              `json:"compliance,omitempty"`
    RequestParamPolicy map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
}
```


#### 11.5.2 `RoutePolicyConfig`

```go
type RoutePolicyConfig struct {
    PreferredProviders   []string         `json:"preferred_providers,omitempty"`
    AllowFallback        *bool            `json:"allow_fallback,omitempty"` // null ⇒ 用平台默认
    RequiredCapabilities []string         `json:"required_capabilities,omitempty"`
    RequiredParams       []string         `json:"required_params,omitempty"`
    ComplianceTags       []string         `json:"compliance_tags,omitempty"`
    Compliance           CompliancePolicy `json:"compliance,omitempty"`
}
```

`AllowFallback` 用指针区分**未设(走 platform 默认)** vs **显式 false(本路由禁止跨模型 fallback)**。

### 10.6 `ProviderInfo`

```go
type ProviderInfo struct {
    ID                     string            `json:"id"`
    Code                   string            `json:"code,omitempty"`
    Name                   string            `json:"name"`
    BaseURL                string            `json:"base_url"`
    Status                 string            `json:"status"`
    KeyStrategy            string            `json:"key_strategy,omitempty"`
    ProviderProfile        string            `json:"provider_profile,omitempty"`
    ProviderAccountRef     string            `json:"provider_account_ref,omitempty"`
    Visibility             string            `json:"visibility,omitempty"`
    CredentialSource       string            `json:"credential_source,omitempty"`
    AllowedRouteKinds      []string          `json:"allowed_route_kinds,omitempty"`
    OwnerScope             *OwnerScope       `json:"owner_scope,omitempty"`
    ProtocolBindings       []ProtocolBinding `json:"protocol_bindings,omitempty"`
    DefaultProtocolBinding string            `json:"default_protocol_binding,omitempty"`
}
```

> 2026-06-03: Provider 已切到 protocol_bindings-only。旧 `ApiType string` / `ApiTypes []string` / `AuthType string` 都不再接受;HTTP body 出现 `api_type` 或 `api_types` 会 400。

`KeyStrategy` 是 per-provider 的 key-pool selector strategy。值:`priority`(按 entry 的 priority 升序选)/ `weight`(按 entry 的 weight 抽签)。协议和 auth 不再由 key_strategy 或 api_types 推导,而由 `ProtocolBindings` 声明。

`ProviderAccountRef` 是 provider-level credential lane 的可选传播字段。新写入若 `credential_source=provider_entitlement` 则必须提供合法 opaque ref；普通 keypool provider 不要求它，因为权威值位于每个 `KeyPoolEntry`，dedicated per-key 场景则位于 `KeyMeta.dedicated_upstream`。当前 Publisher 仅验证并序列化该字段，不解析 entitlement credential，也不据此做路由；这是 propagation-only 合同，实际解析/准入由后续 Gateway/控制面能力负责。

#### 10.6.1 `ProtocolBinding`

```go
type ProtocolBinding struct {
    Name                   string                       `json:"name"`
    Priority               int                          `json:"priority,omitempty"`
    Accepts                []ProtocolBindingAccept      `json:"accepts"`
    OutboundProtocol       string                       `json:"outbound_protocol"`
    PathPrefix             string                       `json:"path_prefix,omitempty"`
    AuthScheme             string                       `json:"auth_scheme,omitempty"`
    TranslatorKey          string                       `json:"translator,omitempty"`
    CompatAPIType          string                       `json:"compat_api_type,omitempty"`
    CapabilityProfiles     []string                     `json:"capability_profiles,omitempty"`
    SupportedFeatures      []string                     `json:"supported_features,omitempty"`
    UnsupportedFeatures    []string                     `json:"unsupported_features,omitempty"`
    SupportedParams        []string                     `json:"supported_params,omitempty"`
    UnsupportedParams      []string                     `json:"unsupported_params,omitempty"`
    ParamConstraints       map[string][]string          `json:"param_constraints,omitempty"`
    FeatureConstraints     map[string]string            `json:"feature_constraints,omitempty"`
    CombinationConstraints []CombinationConstraint      `json:"combination_constraints,omitempty"`
    RequestParamPolicy     map[string]RequestParamPolicy `json:"request_param_policy,omitempty"`
    CompatibilityMode      *CompatibilityMode           `json:"compatibility_mode,omitempty"`
    FieldDisposition       *FieldDispositionConfig      `json:"field_disposition,omitempty"`
}

type ProtocolBindingAccept struct {
    Protocol        string   `json:"protocol"`
    Capabilities    []string `json:"capabilities"`
    RequestContract string   `json:"request_contract,omitempty"`
    Models          []string `json:"models,omitempty"`
}
```

#### 10.6.2 `RequestParamPolicy` / compatibility / field disposition

```go
type RequestParamPolicy struct {
    Category         string   `json:"category,omitempty"`
    UpstreamBehavior string   `json:"upstream_behavior,omitempty"` // fixed / enum
    FixedValue       string   `json:"fixed_value,omitempty"`
    AllowedValues    []string `json:"allowed_values,omitempty"`
    OnEqual          string   `json:"on_equal,omitempty"`          // preserve / omit / normalize / reject ...
    OnMismatch       string   `json:"on_mismatch,omitempty"`
}

type CompatibilityMode struct {
    OpenAIClientDefaults         *bool `json:"openai_client_defaults,omitempty"`
    DropUnsupportedDefaultParams *bool `json:"drop_unsupported_default_params,omitempty"`
    RejectSemanticParams         *bool `json:"reject_semantic_params,omitempty"`
}

type FieldDispositionConfig struct {
    Request  map[string]string `json:"request,omitempty"`
    Response map[string]string `json:"response,omitempty"`
}
```

Publisher 当前校验的 `RequestParamPolicy` 枚举:
- `category`: 空 / `sampling` / `generation_count` / `semantic` / `content` / `observability`
- `upstream_behavior`: 空 / `fixed` / `enum`
- `fixed_value`:当 `upstream_behavior=fixed` 时必填
- `allowed_values`:当 `upstream_behavior=enum` 时建议非空
- `on_equal` / `on_mismatch`: 空 / `preserve` / `omit` / `omit_with_warning` / `normalize` / `normalize_with_warning` / `reject`
- `field_disposition` action: `preserve` / `adapt` / `drop` / `reject` / `preserve_vendor_extra` / `passthrough_raw`
- `compatibility_mode`:三个 boolean 开关:`openai_client_defaults` / `drop_unsupported_default_params` / `reject_semantic_params`

### 10.7 `KeyPoolConfig`(整池)

```go
type KeyPoolConfig struct {
    Meta    KeyPoolMeta    `json:"meta"`
    Entries []KeyPoolEntry `json:"entries"`
}
type KeyPoolMeta struct {
    ProviderID string `json:"provider_id"`
    Strategy   string `json:"strategy,omitempty"`   // priority / weight
}
```

> `KeyPoolMeta.Strategy` 当前作为 fallback 兼容路径;新写入请用 `ProviderInfo.KeyStrategy`(§10.6)。Gateway 优先读 ProviderInfo,空时才回退到 KeyPoolMeta。本字段计划退役。

### 10.8 `KeyPoolEntry`(单条上游 key)

```go
type KeyPoolEntry struct {
    KeyID              string   `json:"key_id"`
    ProviderAccountRef string   `json:"provider_account_ref,omitempty"`
    Alias              string   `json:"alias,omitempty"`
    AllowedModels      []string `json:"allowed_models,omitempty"`
    Ciphertext         string   `json:"ciphertext"`            // KMS 加密后的密钥密文(明文不进 publisher)
    Priority           int      `json:"priority"`
    Weight             int      `json:"weight"`
    RPMLimit           int      `json:"rpm_limit"`
    TPMLimit           int      `json:"tpm_limit"`
    Status             string   `json:"status"`                // enabled / disabled(admin writes); gateway runtime may expose decrypt_failed/auth_degraded separately
}
```

`provider_account_ref` 是计量对账身份，不是凭据身份：同一厂商账户更换 `key_id`/密文时必须继续使用同一个值；不同厂商账户不能复用。Publisher 将其视作控制面签发的 opaque stable ID，只校验 `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$` 并原样透传，无法判断账户归属；调用前必须由控制面 account registry 校验该 ID 属于目标 provider/vendor account。旧 Redis 条目可不带该字段并继续解码，但所有新整池、单条和 bulk 写入都必须提供，缺失时 fail-closed，避免旧客户端 PUT 静默擦除已有值；完成历史回填前不得开启 Billing evidence ENFORCE。

### 10.9 `PlatformConfig`

```go
type PlatformConfig struct {
    PlatformRPM              int                      `json:"platform_rpm"`     // 节点级,非集群
    PlatformCC               int                      `json:"platform_cc"`      // 节点级,非集群
    DefaultProviderStrategy  string                   `json:"default_provider_strategy"`
    DefaultRoutePolicy       DefaultRoutePolicyConfig `json:"default_route_policy,omitempty"`
    DefaultTimeoutMs         int                      `json:"default_timeout_ms"`
    StreamTimeoutMs          int                      `json:"stream_timeout_ms"`
    FirstTokenTimeoutMs      int                      `json:"first_token_timeout_ms"`
    AttemptTimeoutMs         int                      `json:"attempt_timeout_ms"`
    MaxBodySize              int                      `json:"max_body_size"`
    MaxFallbackDepth         int                      `json:"max_fallback_depth"`
    DisclosurePolicy         DisclosurePolicy         `json:"disclosure_policy,omitempty"`
    ProviderBudgetPolicies   []EffectivePolicy        `json:"provider_budget_policies,omitempty"`
    Auth                     AuthRuntimeConfig        `json:"auth,omitempty"`
    RetryBudget              RetryBudgetConfig        `json:"retry_budget,omitempty"`
    ResponsesAffinityEnabled *bool                    `json:"responses_affinity_enabled,omitempty"`
    Operational              OperationalAttrs         `json:"operational,omitempty"`
}

type OperationalAttrs struct {
    IdempotencyAttr    string `json:"idempotency_attr,omitempty"`    // default "tenant_id"
    KafkaPartitionAttr string `json:"kafka_partition_attr,omitempty"` // default "tenant_id"
    FullDetailAttr     string `json:"full_detail_attr,omitempty"`    // default "tenant_id"
}
```

**关键语义**:

- **`PlatformRPM` / `PlatformCC` 是节点级**:每个 gateway pod 在本进程内独立计数,不跨 pod 聚合。10 个 pod 配 `platform_rpm=60000` 实际允许 600000 rpm。请按每 pod 容量上限配,不是集群总量。
- **`ResponsesAffinityEnabled` 是指针** — `nil` ⇒ 默认开(`true`),只有显式 `false` 才关闭 Responses API stateful session 粘性。生产环境 99% 留空即可,只有应急 / 测试场景才给指针 false。
- **`DefaultKeyPoolStrategy`** 当前 gateway 不消费(per-provider `ProviderInfo.KeyStrategy` 已经够用),保留为可能的未来字段。
- **`ProviderBudgetPolicies`** 复用 `EffectivePolicy` schema(§10.3),区别仅在 gateway 由 `CombinedProviderGate` 评估而非 `KeyValidator`。
- **`Auth`** 是 gateway 预鉴权层(PreAuth IP / AbuseDetector / Idempotency)的运行时旋钮,零值字段表示"保留 gateway cold-start yaml 默认",**不是清零**。
- **`RetryBudget`** 三轴(keys/providers/wall-time)全零时 gateway 用 in-code 默认(2/3/5000ms);任一轴非零则把零视为"该轴无限"。

#### 11.9.1 `DefaultRoutePolicyConfig`

```go
type DefaultRoutePolicyConfig struct {
    AllowFallback *bool `json:"allow_fallback,omitempty"`
}
```

平台级 `RoutePolicy` 兜底 — 当 `RouteConfig.DefaultPolicy.AllowFallback` 为 nil 时用此处值。当前只暴露 `AllowFallback`(影响最大),其它字段后续按需补,不破坏既有 payload。

#### 11.9.2 `AuthRuntimeConfig`

```go
type AuthRuntimeConfig struct {
    PreAuthIPRPM       int `json:"pre_auth_ip_rpm,omitempty"`
    PreAuthIPWindowMs  int `json:"pre_auth_ip_window_ms,omitempty"`
    AbuseFailThreshold int `json:"abuse_fail_threshold,omitempty"`
    AbuseWindowSec     int `json:"abuse_window_sec,omitempty"`
    AbuseBanTTLSec     int `json:"abuse_ban_ttl_sec,omitempty"`
    IdempotencyTTLSec  int `json:"idempotency_ttl_sec,omitempty"`
}
```

#### 11.9.3 `RetryBudgetConfig`

```go
type RetryBudgetConfig struct {
    MaxKeysPerProvider int   `json:"max_keys_per_provider,omitempty"`
    MaxProviders       int   `json:"max_providers,omitempty"`
    TotalWallTimeMs    int64 `json:"total_wall_time_ms,omitempty"`
}
```

典型生产值:`max_keys_per_provider=1~2`,`max_providers=3`,`total_wall_time_ms=5000`。

### 10.A `ResourceBinding`

```go
type ResourceBinding struct {
    BindingID       string `json:"binding_id,omitempty"`
    ScopeType       string `json:"scope_type"`        // tenant / workspace / app / key / provider / ...
    ScopeID         string `json:"scope_id"`
    Metric          string `json:"metric"`            // amount_usd / rpm / tpm / cc / ...
    UnitKind        string `json:"unit_kind,omitempty"`        // micro / nano / count
    StateSemantic   string `json:"state_semantic,omitempty"`   // hard_cutoff / settle_only
    ReservationMode string `json:"reservation_mode,omitempty"` // rolling / fixed_window / ...
    PeriodKind      string `json:"period_kind,omitempty"`      // minute / hour / day / month
    SourcePolicyID  string `json:"source_policy_id,omitempty"`
    LineageRole     string `json:"lineage_role,omitempty"`
}
```

扁平化静态执行契约:caller(user 服务)把 workspace 祖先链 + policy 所属关系编译进每条 binding,gateway 在准入热路径里 O(1) 索引,不再现取 workspace 树。JSON tag 必须与 gateway `model.ResourceBinding` 严格一致 — gateway 跨仓 schema regression test `TestPublisherSchema_KeyMeta_ResourceBindings` 锁住任何漂移。

### 10.B `AllowanceBinding`

```go
type AllowanceBinding struct {
    Scope   string `json:"scope"`     // tenant / workspace / app / key / ...
    ScopeID string `json:"scope_id"`
    Period  string `json:"period"`    // 202605 / 20260513 / ...
}
```

准入查询表:每条对应一个 (scope, scope_id, period) 三元组的 allowance flag。gateway 准入时一次性查所有 binding 的 flag,任一为禁用或缺失 → 拒绝。

> **退役计划**:本字段计划退役,未来收敛为单 key 布尔 flag。退役落定后所有写侧停发该字段,caller 可以停写。

### 10.10 限制常量

| Const | 值 | 用途 |
|---|---|---|
| `MaxBulkItems` | 100 | 单次 bulk 请求 items 数上限 |
| Idempotency-Key 保留期 | 7 天 | 窗口外重复 key 视作新写 |
| 单条请求最大重试次数 | 12 | 超过后请求进入告警状态,需要 SRE 显式 replay 或人工查处 |
| 重试退避 | 1s 起,2x 指数,上限 60s | publisher 内部重试节奏 |

---

## 11. JSON 请求体示例

每个示例都可以直接套到 curl,把 `$TOK` 换成 bearer token 即可。

### 12.1 User namespace

**PUT `/user/api/v1/keys` —— 写单条 user key**

```bash
curl -X PUT -H "Authorization: Bearer $TOK" \
     -H "Content-Type: application/json" \
     -H "Idempotency-Key: 9b4e8c10-7e1f-4d6f-9a2c-0f3d8c2a1b55" \
     -d '{
  "key_id": "550e8400-e29b-41d4-a716-446655440000",
  "key_hash": "a1b2c3d4e5f6071829304152637485969a0b1c2d3e4f5061728394a5b6c7d8e9",
  "alias": "alice-prod-key",
  "scope_attrs": {
    "tenant_id": "t-001",
    "workspace_id": "ws-42",
    "app_id": "billing-app",
    "department": "fin"
  },
  "status": "active",
  "expires_at": 1735689600,
  "ip_whitelist": ["10.0.0.0/8", "192.168.1.1"],
  "allowed_models": ["gpt-4o", "claude-opus-4-7"],
  "policies": [
    {
      "version": 2,
      "policy_id": "rpm-key-550e8400",
      "policy_type": "rate_limit",
      "control_semantic": "window_reject",
      "subject_side": "user",
      "metric": "rpm",
      "scope": "key",
      "scope_id": "550e8400-e29b-41d4-a716-446655440000",
      "window": "60s",
      "limit_value": 600,
      "runtime_keys": {
        "family": "rl",
        "count_suffix": "key:{scope_id}:rpm:{window}:{window_id}"
      }
    },
    {
      "version": 2,
      "policy_id": "tpm-key-550e8400",
      "policy_type": "rate_limit",
      "control_semantic": "window_reject",
      "subject_side": "user",
      "metric": "tpm",
      "scope": "key",
      "scope_id": "550e8400-e29b-41d4-a716-446655440000",
      "window": "60s",
      "limit_value": 200000,
      "runtime_keys": {
        "family": "rl",
        "count_suffix": "key:{scope_id}:tpm:{window}:{window_id}"
      }
    }
  ],
  "max_tokens_per_call": 8192,
  "guardrails": {
    "max_messages": 50,
    "max_output_tokens": 4096,
    "allow_tools": true,
    "allow_vision": false
  }
}' http://publisher.local:9080/user/api/v1/keys
```

**POST `/user/api/v1/keys/bulk` —— 批量增改**

(`schema_version` / `revision` 同 §4.1 自动填,bulk 路径每条 item 都会过 `fillKeyMetaDefaults`)

```json
{
  "items": [
    { "key_id": "...uuid-1...", "key_hash": "...hex-1...", "status": "active", "expires_at": 0, "ip_whitelist": [], "allowed_models": [] },
    { "key_id": "...uuid-2...", "key_hash": "...hex-2...", "status": "active", "expires_at": 0, "ip_whitelist": [], "allowed_models": [] }
  ]
}
```

**DELETE `/user/api/v1/keys/{key_hash}`** — 无 body,`key_hash` 在 path 里。

**DELETE `/user/api/v1/keys/bulk` —— 批量删**

```json
{
  "key_hashes": [
    "a1b2c3d4e5f6071829304152637485969a0b1c2d3e4f5061728394a5b6c7d8e9",
    "b2c3d4e5f6071829304152637485969a0b1c2d3e4f5061728394a5b6c7d8e9f0"
  ]
}
```

**POST `/user/api/v1/revoked-keys` —— 加吊销集(单条)**

```json
{ "key_hash": "a1b2c3d4e5f6071829304152637485969a0b1c2d3e4f5061728394a5b6c7d8e9" }
```

**POST `/user/api/v1/revoked-keys/bulk`、DELETE `/user/api/v1/revoked-keys/bulk`、PUT `/user/api/v1/revoked-keys`(整集替换)**

三者 body 形态完全相同:

```json
{ "key_hashes": ["...hex-1...", "...hex-2..."] }
```

---

### 12.2 Billing namespace

**PUT `/billing/api/v1/key-balance/{key_id}` —— 写单条余额**

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
     -d '{
  "has_balance": true,
  "decision_id": "5d63f865-ec6f-4a92-8db1-5951ab338342",
  "decision_version": 42
}' "http://publisher.local:9080/billing/api/v1/key-balance/550e8400-e29b-41d4-a716-446655440000"
```

`has_balance=false` 时**必须**带 `reason`(白名单值之一);`has_balance=true` 时 `reason` 必须为空:

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
     -d '{
  "has_balance": false,
  "reason": "account_debt",
  "decision_id": "d20f5c42-619d-48c1-b9a6-99a081927cc7",
  "decision_version": 43
}' "http://publisher.local:9080/billing/api/v1/key-balance/550e8400-e29b-41d4-a716-446655440000"
```

**PUT `/billing/api/v1/key-balance/bulk` —— 批量**

```json
{
  "reason": "settlement_risk_limit_exceeded",
  "decision_id": "76ff77dd-9a83-49e2-bda0-70a832d660d2",
  "decision_version": 44,
  "items": [
    { "key_id": "...uuid-1...", "has_balance": false },
    { "key_id": "...uuid-2...", "has_balance": false }
  ]
}
```

**DELETE `/billing/api/v1/key-balance/{key_id}`** — strict 模式发送:

```json
{"decision_id":"a14f1187-234c-4246-ac71-3bdba7e712dc","decision_version":45}
```

写接口返回 202 后用 `GET /billing/api/v1/key-admission-deliveries/{batch_id}` 轮询;不要把 202 当成已写入 Redis。

---

### 12.3 Admin namespace

**PUT `/admin/api/v1/providers/{provider_id}` —— 写 provider**

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
     -d '{
  "id": "openai-prod",
  "name": "OpenAI Production",
  "base_url": "https://api.openai.com",
  "status": "active",
  "protocol_bindings": [
    {
      "name": "openai-chat",
      "priority": 100,
      "accepts": [
        {"protocol":"openai","capabilities":["chat"],"request_contract":"chat_completions"}
      ],
      "outbound_protocol": "openai",
      "path_prefix": "/v1",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "compat_api_type": "openai",
      "capability_profiles": ["openai.chat_completions.full"]
    }
  ],
  "default_protocol_binding": "openai-chat"
}' http://publisher.local/admin/api/v1/providers/openai-prod
```

不同厂商不要再改 `api_types`;应选择或生成对应 `protocol_bindings`:
- OpenAI-compatible:`outbound_protocol=openai`, `path_prefix=/v1`, `auth_scheme=bearer`, profile `openai.chat_completions.full`
- MiniMax OpenAI 兼容:在 OpenAI-compatible 基础上加 `minimax.openai_chat.thinking` / `minimax.openai_chat.response_extensions`
- Moonshot/Kimi:在 OpenAI-compatible 基础上加固定采样 `request_param_policy`
- Anthropic native:`accepts.protocol=anthropic`, `request_contract=anthropic_messages`, `outbound_protocol=anthropic`, `auth_scheme=x-api-key`
- Gemini native:`accepts.protocol=gemini`, `outbound_protocol=gemini`, `auth_scheme=google-key`

**字段更新历史**:
- 2026-06-03:旧 `api_type` / `api_types` 全面退役,Provider 写入改为 `protocol_bindings` / `default_protocol_binding`。
- 2026-05-20 PR-22AN:旧 `api_type: string` 曾临时升级为 `api_types: []string`;该过渡形态现已废弃。

**DELETE `/admin/api/v1/providers/{provider_id}`** — 无 body。

**PUT `/admin/api/v1/models/{model_id}/catalog` —— 写模型目录**

```json
{
  "model_id": "gpt-4o",
  "display_name": "GPT-4o",
  "capabilities": ["chat", "vision", "tools"],
  "context_window": 128000,
  "max_output": 16384,
  "owned_by": "openai"
}
```

**POST `/admin/api/v1/models/catalog/bulk`**

```json
{
  "items": [
    { "model_id": "gpt-4o", "entry": { "revision": 1715760000000000, "model_id": "gpt-4o", "display_name": "GPT-4o", "capabilities": ["chat"], "context_window": 128000, "max_output": 16384, "owned_by": "openai" }},
    { "model_id": "claude-opus-4-7", "entry": { "revision": 1715760000000001, "model_id": "claude-opus-4-7", "display_name": "Claude Opus 4.7", "capabilities": ["chat", "tools"], "context_window": 200000, "max_output": 8192, "owned_by": "anthropic" }}
  ]
}
```

**PUT `/admin/api/v1/models/{model_id}/route` —— 写路由**

```json
{
  "revision": 1715760000000000,
  "model_id": "gpt-4o",
  "provider_strategy": "weight",
  "fallback_model": "gpt-4o-mini",
  "providers": [
    {
      "provider_id": "openai",
      "upstream_model": "gpt-4o",
      "priority": 1,
      "weight": 80,
      "status": "active",
      "compliance_tags": []
    },
    {
      "provider_id": "azure-openai",
      "upstream_model": "gpt-4o",
      "priority": 2,
      "weight": 20,
      "status": "active"
    }
  ],
  "default_policy": {
    "preferred_providers": ["openai"],
    "required_capabilities": ["chat"]
  }
}
```

**POST `/admin/api/v1/models/routes/bulk`**

```json
{
  "items": [
    { "model_id": "gpt-4o", "route": { "revision": 1715760000000000, "model_id": "gpt-4o", "providers": [...] }},
    { "model_id": "claude-opus-4-7", "route": { "revision": 1715760000000001, "model_id": "claude-opus-4-7", "providers": [...] }}
  ]
}
```

**DELETE `/admin/api/v1/models/{model_id}/catalog`、`/route`** — 无 body。

**PUT `/admin/api/v1/key-pools/{provider_id}` —— 整池替换(冷启动/灾难恢复)**

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
     -d '{
  "meta": {
    "provider_id": "openai",
    "strategy": "weight"
  },
  "entries": [
    {
      "key_id": "k-001",
      "provider_account_ref": "provider-account-openai-prod",
      "alias": "openai-prod-key-1",
      "allowed_models": ["gpt-4o", "gpt-4o-mini"],
      "ciphertext": "vault:v1:CGq4...",
      "priority": 1,
      "weight": 50,
      "rpm_limit": 600,
      "tpm_limit": 200000,
      "status": "enabled"
    },
    {
      "key_id": "k-002",
      "provider_account_ref": "provider-account-openai-prod",
      "alias": "openai-prod-key-2",
      "allowed_models": ["gpt-4o"],
      "ciphertext": "vault:v1:Xy12...",
      "priority": 2,
      "weight": 50,
      "rpm_limit": 600,
      "tpm_limit": 200000,
      "status": "enabled"
    }
  ]
}' http://publisher.local:9080/admin/api/v1/key-pools/openai
```

**POST `/admin/api/v1/key-pools/bulk` —— 跨 provider 整池批量**

```json
{
  "items": [
    { "provider_id": "openai",    "pool": { "meta": {...}, "entries": [...] } },
    { "provider_id": "anthropic", "pool": { "meta": {...}, "entries": [...] } }
  ]
}
```

**DELETE `/admin/api/v1/key-pools/{provider_id}`** — 删整池,无 body。

**PUT `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}` —— 单条 entry 增/改(日常用)**

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
     -d '{
  "key_id": "k-001",
  "provider_account_ref": "provider-account-openai-prod",
  "alias": "openai-prod-key-1",
  "allowed_models": ["gpt-4o"],
  "ciphertext": "vault:v1:CGq4...",
  "priority": 1,
  "weight": 50,
  "rpm_limit": 600,
  "tpm_limit": 200000,
  "status": "enabled"
}' http://publisher.local:9080/admin/api/v1/key-pools/openai/keys/k-001
```

> body 里的 `key_id` 可省;若给了,**必须**跟 path 里的一致,否则 400。

**POST `/admin/api/v1/key-pools/keys/bulk` —— 跨 provider 单 entry 批量**

```json
{
  "items": [
    { "provider_id": "openai",    "entry": { "key_id": "k-001", "provider_account_ref": "provider-account-openai-prod", "ciphertext": "...", "priority": 1, "weight": 50, "rpm_limit": 600, "tpm_limit": 200000, "status": "enabled" } },
    { "provider_id": "anthropic", "entry": { "key_id": "a-014", "provider_account_ref": "provider-account-anthropic-prod", "ciphertext": "...", "priority": 1, "weight": 100, "rpm_limit": 300, "tpm_limit": 100000, "status": "enabled" } }
  ]
}
```

**DELETE `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}`** — 无 body。

**DELETE `/admin/api/v1/key-pools/keys/bulk` —— 跨 provider 单 entry 批量删**

```json
{
  "items": [
    { "provider_id": "openai",    "key_id": "k-001" },
    { "provider_id": "anthropic", "key_id": "a-014" }
  ]
}
```

**PUT `/admin/api/v1/platform-config`**

```json
{
  "revision": 1715760000000000,
  "platform_rpm": 50000,
  "platform_cc": 5000,
  "default_provider_strategy": "weight",
  "default_keypool_strategy": "weight",
  "default_timeout_ms": 60000,
  "stream_timeout_ms": 300000,
  "first_token_timeout_ms": 30000,
  "attempt_timeout_ms": 60000,
  "max_body_size": 10485760,
  "max_fallback_depth": 2,
  "disclosure_policy": {
    "mode": "standard",
    "overrides": {
      "expose_provider_name": false,
      "expose_provider_account": false,
      "expose_upstream_status": true,
      "model_field_strategy": "logical"
    }
  }
}
```

---

### 12.4 SRE namespace

**POST `/sre/api/v1/keys/{key_hash}/disable` / `/enable`** — 无 body,`key_hash` 在 path。

**POST `/sre/api/v1/keys/bulk/disable` / `/enable`**

```json
{ "key_hashes": ["...hex-1...", "...hex-2..."] }
```

**POST `/sre/api/v1/blocked-ips` —— 单 IP**

```json
{ "ip": "1.2.3.4" }
```

支持 CIDR(如 `10.0.0.0/8`)。

**POST `/sre/api/v1/blocked-ips/bulk`、DELETE `…/bulk`、PUT `/sre/api/v1/blocked-ips`(整集替换)**

```json
{ "ips": ["1.2.3.4", "10.0.0.0/8"] }
```

**DELETE `/sre/api/v1/blocked-ips/{ip}`** — `ip` 在 path,URL-encode 一下 `/`(CIDR 写成 `10.0.0.0%2F8`)。

**POST `/sre/api/v1/replay` —— 重投**(⚠️ 暂不开放,字段示例仅作契约预览)

按 batch 重投:

```json
{ "batch_id": "550e8400-e29b-41d4-a716-446655440000" }
```

按时间窗 + 状态:

```json
{
  "since": 1715760000,
  "until": 1715763600,
  "status": "undelivered",
  "limit": 100,
  "dry_run": false
}
```

`status` 取值:`pending` / `failed` / `undelivered`(= pending+failed,默认) / `all`。`dry_run=true` 只返回会被命中的 batch_ids,不真投。

Response 200:

```json
{
  "attempted": 5,
  "succeeded": 5,
  "failed": 0,
  "batch_ids": ["..."]
}
```

> `/sre/api/v1/overrides*` 现阶段不开放,故省略示例。

---

### 12.5 公开端点

**GET `/livez` / `/healthz`**

```json
{ "status": "ok" }
```

**GET `/readyz`**

```json
{
  "status": "ok",
  "checks": { "postgres": "ok", "redis": "ok" }
}
```

失败时 503,body 同形态:

```json
{
  "status": "not ready",
  "checks": { "postgres": "ok", "redis": "fail: dial tcp: ..." }
}
```

**GET `/metrics`** — Prometheus 文本格式,非 JSON。

---

## 12. 端点全表(46 个 = 4 public + 9 user + 3 billing + 16 admin + 14 sre)

| # | Method | Path | Namespace |
|---|---|---|---|
| 1 | GET | `/healthz` | public |
| 2 | GET | `/livez` | public |
| 3 | GET | `/readyz` | public |
| 4 | GET | `/metrics` | public |
| 5 | PUT | `/user/api/v1/keys` | user |
| 6 | POST | `/user/api/v1/keys/bulk` | user |
| 7 | DELETE | `/user/api/v1/keys/{key_hash}` | user |
| 8 | DELETE | `/user/api/v1/keys/bulk` | user |
| 9 | POST | `/user/api/v1/revoked-keys` | user |
| 10 | POST | `/user/api/v1/revoked-keys/bulk` | user |
| 11 | DELETE | `/user/api/v1/revoked-keys/{key_hash}` | user |
| 12 | DELETE | `/user/api/v1/revoked-keys/bulk` | user |
| 13 | PUT | `/user/api/v1/revoked-keys` | user |
| 14 | PUT | `/billing/api/v1/key-balance/{key_id}` | billing |
| 15 | PUT | `/billing/api/v1/key-balance/bulk` | billing |
| 16 | DELETE | `/billing/api/v1/key-balance/{key_id}` | billing |
| 16a | GET | `/billing/api/v1/key-admission-deliveries/{batch_id}` | billing |
| 17 | PUT | `/admin/api/v1/providers/{provider_id}` | admin |
| 18 | DELETE | `/admin/api/v1/providers/{provider_id}` | admin |
| 19 | PUT | `/admin/api/v1/models/{model_id}/catalog` | admin |
| 20 | POST | `/admin/api/v1/models/catalog/bulk` | admin |
| 21 | DELETE | `/admin/api/v1/models/{model_id}/catalog` | admin |
| 22 | PUT | `/admin/api/v1/models/{model_id}/route` | admin |
| 23 | POST | `/admin/api/v1/models/routes/bulk` | admin |
| 24 | DELETE | `/admin/api/v1/models/{model_id}/route` | admin |
| 25 | PUT | `/admin/api/v1/key-pools/{provider_id}` | admin |
| 26 | POST | `/admin/api/v1/key-pools/bulk` | admin |
| 27 | DELETE | `/admin/api/v1/key-pools/{provider_id}` | admin |
| 28 | PUT | `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}` | admin |
| 29 | POST | `/admin/api/v1/key-pools/keys/bulk` | admin |
| 30 | DELETE | `/admin/api/v1/key-pools/{provider_id}/keys/{key_id}` | admin |
| 31 | DELETE | `/admin/api/v1/key-pools/keys/bulk` | admin |
| 32 | PUT | `/admin/api/v1/platform-config` | admin |
| 33 | POST | `/sre/api/v1/keys/{key_hash}/disable` | sre |
| 34 | POST | `/sre/api/v1/keys/{key_hash}/enable` | sre |
| 35 | POST | `/sre/api/v1/keys/bulk/disable` | sre |
| 36 | POST | `/sre/api/v1/keys/bulk/enable` | sre |
| 37 | POST | `/sre/api/v1/blocked-ips` | sre |
| 38 | POST | `/sre/api/v1/blocked-ips/bulk` | sre |
| 39 | DELETE | `/sre/api/v1/blocked-ips/{ip}` | sre |
| 40 | DELETE | `/sre/api/v1/blocked-ips/bulk` | sre |
| 41 | PUT | `/sre/api/v1/blocked-ips` | sre |
| 42 | PUT | `/sre/api/v1/overrides` | sre (⚠️ 暂不开放) |
| 43 | POST | `/sre/api/v1/overrides/bulk` | sre (⚠️ 暂不开放) |
| 44 | DELETE | `/sre/api/v1/overrides` | sre (⚠️ 暂不开放) |
| 45 | DELETE | `/sre/api/v1/overrides/bulk` | sre (⚠️ 暂不开放) |
| 46 | POST | `/sre/api/v1/replay` | sre (⚠️ 暂不开放) |

---


## 13. 场景速查表

按"我想干什么"反查端点。

| 场景 | 端点 | namespace |
|---|---|---|
| 创建 / 修改 API key | `PUT /user/api/v1/keys`(§4.1)| user |
| 删 API key(物理) | `DELETE /user/api/v1/keys/{hash}`(§4.3)| user |
| 客户主动撤销 key(可恢复) | `POST /user/api/v1/revoked-keys`(§4.5)| user |
| 恢复撤销的 key | `DELETE /user/api/v1/revoked-keys/{hash}`(§4.7)| user |
| 充值 / 改余额标记 | `PUT /billing/api/v1/key-balance/{id}`(§5.1)| billing |
| 删余额条目 | `DELETE /billing/api/v1/key-balance/{id}`(§5.3)| billing |
| 接入新 Provider | `PUT /admin/api/v1/providers/{id}`(§6.1)| admin |
| 上架新模型 | `PUT /admin/api/v1/models/{id}/catalog`(§6.3)+ `PUT .../route`(§6.6)| admin |
| 加一把上游 key | `PUT /admin/api/v1/key-pools/{provider}/keys/{key_id}`(§6.12)| admin |
| 调全局参数 | `PUT /admin/api/v1/platform-config`(§6.16)| admin |
| 应急封 key | `POST /sre/api/v1/keys/{hash}/disable`(§7.1)| sre |
| 应急封 IP | `POST /sre/api/v1/blocked-ips`(§7.5)| sre |
| 重试失败 batch | `POST /sre/api/v1/replay`(§7.14,⚠️ 暂不开放)| sre |

---


## 14. Admin 低维护接入指南

本节面向 admin / user / billing 等调用方,目标是让运营只维护“业务意图”,由服务端编译成 publisher / gateway 需要的严格 wire schema。不要把 `protocol_bindings`、`request_param_policy`、constraints、`field_disposition` 等复杂字段直接暴露给普通页面配置。

### 14.1 推荐配置分层

| 层级 | 谁维护 | 内容 | 是否直接写 publisher |
|---|---|---|---|
| 运营意图层 | admin 页面 | provider、provider_profile、base_url、key、开通模型、上游模型名、优先级、权重、限流 | 否 |
| 编译层 | admin / publisher compiler | 根据 provider profile 生成 Provider / ModelCatalog / Route / KeyPool payload | 是 |
| 协议事实层 | publisher / gateway | `protocol_bindings`、capability profiles、参数策略、组合约束、字段处置证据 | 是 |

普通页面应优先提供 provider preset/profile,例如:

| provider | 推荐 `provider_profile` | 默认 exposure | 说明 |
|---|---|---|---|
| openai | `openai_compatible` | `openai.chat` | OpenAI Chat Completions 兼容 |
| minimax | `minimax_openai` | `openai.chat` | MiniMax OpenAI 兼容端点及私有扩展 |
| kimi / moonshot | `moonshot_openai` | `openai.chat` | Kimi 固定采样值等参数兼容策略由 profile 生成 |
| deepseek | `deepseek_openai` | `openai.chat` | DeepSeek OpenAI 兼容端点 |
| qwen | `qwen_openai` | `openai.chat` | Qwen / DashScope OpenAI 兼容端点 |
| anthropic | `anthropic_official` | `anthropic.messages` | Anthropic Messages 原生端点 |
| google / gemini | `gemini_official` | `gemini.generate_content` | Gemini 原生端点 |
| azure | `azure_openai` | `openai.chat` | Azure OpenAI deployment / api-version 由 route 或 profile 承载 |

> `provider_profile` 可以由调用方自己的 compiler 展开,也可以在 ProviderInfo 写入时交给 publisher 的低维护 compiler 展开。当前 publisher 只内置 `openai_compatible` + `client_compatibility` 到 `protocol_bindings` 的编译;其它 profile 仍应由 admin/control-server 侧编译后再写入 §6.1 ProviderInfo、§6.3 ModelCatalogEntry、§6.6 RouteConfig、§6.9/§6.12 KeyPool。

### 14.2 页面建议只暴露这些字段

Channel / Provider 页面建议维护:

| 字段 | 用途 | 编译到 publisher |
|---|---|---|
| `provider` / `provider_code` | 厂商代码,如 `minimax`、`kimi` | ProviderInfo.`id` / `code` |
| `provider_profile` | 厂商协议预设 | 生成 `protocol_bindings` / constraints / policies |
| `endpoint_variant` | 同厂商端点变体,如 `standard`、`responses`、`azure` | 影响 path、headers、api_version 或 binding 选择 |
| `base_url` | 上游根地址 | ProviderInfo.`base_url` |
| `api_key` | 上游 key 明文输入 | 服务端加密后写 KeyPoolEntry.`ciphertext` |
| `models` | 本渠道开通的逻辑模型 | ModelCatalog / Route |
| `upstream_model_id` | 上游真实模型名 | RouteEntry.`upstream_model` |
| `enabled` | 是否启用该模型路由 | RouteEntry.`status` |
| `routing_priority` | 路由优先级 | RouteEntry.`priority` |
| `weight` | 路由或 key 选择权重 | RouteEntry.`weight` 或 KeyPoolEntry.`weight` |
| `max_rpm` / `max_tpm` | 上游 key 维度限流 | KeyPoolEntry.`rpm_limit` / `tpm_limit` |

普通页面不建议直接维护:

- `protocol_bindings` / `default_protocol_binding`
- `compat_api_type` / `translator` / `request_contract`
- `supported_params` / `unsupported_params` / `param_constraints`
- `feature_constraints` / `combination_constraints`
- `request_param_policy`
- `field_disposition`

如需排障,可以提供“编译结果预览”,但默认只读;人工覆盖应放到受控高级入口并记录审计。

### 14.3 编译输出要求

一次 channel/model 变更通常应编译为以下 publisher 写入:

| 资源 | 端点 | 触发场景 |
|---|---|---|
| ProviderInfo | `PUT /admin/api/v1/providers/{provider_id}` | 新增/修改 provider、base_url、profile、key_strategy |
| ModelCatalogEntry | `PUT /admin/api/v1/models/{model_id}/catalog` 或 bulk | 上架模型、修改能力、上下文、价格展示所需元数据 |
| RouteConfig | `PUT /admin/api/v1/models/{model_id}/route` 或 bulk | 开通/关闭某 provider 路由、修改上游模型名、优先级、权重 |
| KeyPoolConfig / KeyPoolEntry | `PUT /admin/api/v1/key-pools/{provider_id}` 或 per-entry | 新增/修改上游 key、限流、key 选择策略 |
| PlatformConfig | `PUT /admin/api/v1/platform-config` | 全局默认值或开关变化,非日常 channel 操作 |

编译层必须做 conformance 校验:

- Provider body 不得再包含 legacy `api_type` / `api_types`。
- `default_protocol_binding` 必须引用现有 `protocol_bindings[].name`。
- `RouteEntry.provider_id` 必须存在对应 ProviderInfo。
- `RouteEntry.upstream_model` 为空时必须有明确 fallback 规则。
- `request_param_policy`、constraints、`field_disposition` 必须来自 profile 或受控高级配置,不能由普通页面自由拼 JSON。
- 每个 profile 至少有一条白名单测试,验证该 profile 编译出的参数集合符合 gateway 支持范围。

### 14.4 示例: MiniMax 简化输入到 publisher payload

Admin 页面保存的简化输入示例:

```json
{
  "provider": "minimax",
  "provider_profile": "minimax_openai",
  "endpoint_variant": "standard",
  "base_url": "https://api.minimax.io",
  "models": [
    {
      "model_name": "minimax-m2.5",
      "upstream_model": "MiniMax-M2.5",
      "enabled": true,
      "routing_priority": 10,
      "capabilities": ["chat", "tools", "streaming"],
      "context_window": 200000,
      "max_output_tokens": 8192
    }
  ]
}
```

编译后的 ProviderInfo 示例:

```json
{
  "code": "minimax",
  "name": "MiniMax",
  "base_url": "https://api.minimax.io",
  "status": "active",
  "protocol_bindings": [
    {
      "name": "openai-chat",
      "priority": 100,
      "accepts": [
        {
          "protocol": "openai",
          "capabilities": ["chat"],
          "request_contract": "chat_completions"
        }
      ],
      "outbound_protocol": "openai",
      "path_prefix": "/v1",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "compat_api_type": "openai",
      "capability_profiles": [
        "openai.chat_completions.full",
        "minimax.openai_chat.thinking",
        "minimax.openai_chat.response_extensions"
      ]
    }
  ],
  "default_protocol_binding": "openai-chat"
}
```

编译后的 RouteConfig 示例:

```json
{
  "model": "minimax-m2.5",
  "providers": [
    {
      "provider_id": "minimax",
      "upstream_model": "MiniMax-M2.5",
      "priority": 10,
      "weight": 1,
      "status": "active"
    }
  ]
}
```

### 14.5 对 codesphere-admin 的迁移建议

如果 admin 当前仍使用 `sdkType`,建议按以下方式迁移:

| 旧字段 / 值 | 新含义 | 迁移建议 |
|---|---|---|
| `sdkType` | legacy adapter 选择 | 保留兼容,新页面改用 `provider_profile` |
| `openai_completions` | OpenAI Chat Completions 兼容 | `provider_profile=openai_compatible` |
| `openai_responses` | OpenAI Responses | `provider_profile=openai_responses`, exposure 加 `openai.responses` |
| `anthropic` | Anthropic Messages | `provider_profile=anthropic_official` |
| `gemini` | Gemini 原生 | `provider_profile=gemini_official` |
| `glm` | 智谱 GLM | `provider_profile=zhipu_glm` |
| `azure` | Azure OpenAI | `provider_profile=azure_openai` |
| `kimi` | Moonshot/Kimi OpenAI 兼容 | `provider_profile=moonshot_openai` |
| `minimax` | MiniMax OpenAI 兼容 | `provider_profile=minimax_openai` |
| `deepseek` | DeepSeek OpenAI 兼容 | `provider_profile=deepseek_openai` |
| `qwen` | Qwen OpenAI 兼容 | `provider_profile=qwen_openai` |

推荐落地顺序:

1. Channel schema 增加 `provider_profile`、`endpoint_variant`、`exposures`、`publisher_provider_id`,保留 `sdkType` 兼容旧数据。
2. Channel 页面将“SDK Type”替换为“厂商预设/Profile”,选择 provider 时自动给推荐 profile。
3. 后端增加 gateway config compiler,把 Channel + ModelCatalog/ModelPricing + per-model override 编译为 Provider / Catalog / Route / KeyPool。
4. PublisherSyncJob 保存编译后的资源类型、path、payload 摘要和失败原因,页面展示只读预览。
5. 为每个 provider profile 增加 conformance 测试,确保参数白名单、组合约束和字段处置策略可解释。

---

## 15. 最新接口变更记录

| 日期 | 变更 | 影响 |
|---|---|---|
| 2026-06-03 | Provider 写入切到 `protocol_bindings` / `default_protocol_binding`;旧 `api_type` / `api_types` 请求直接 400 | admin caller 必须传显式 binding;gateway 不再接受 provider legacy api_types |
| 2026-06-03 | `ModelCatalogEntry` / `RouteEntry` 增加 `request_param_policy`;`RouteEntry` 增加 `api_version` / `anthropic_beta` | 支持模型/profile/route 级参数兼容策略和厂商 header/version 扩展 |
| 2026-06-03 | `ProtocolBinding` 增加 capability profile、param/feature/combination constraints、compatibility_mode、field_disposition | 支撑在 OpenAI/Anthropic 等基础协议上组合厂商私有扩展 |

## 16. 常见误用

| 误用 | 后果 | 正确做法 |
|---|---|---|
| 直接传 raw_key 给 publisher | publisher 不接受 raw key,接的是 `HMAC-SHA256(raw_key, gateway_hmac_secret)` hash | user-service 用 gateway 共享的 HMAC secret 算好 hash 再传 |
| 用纯 `sha256(raw_key)`(无 secret)算 key_hash | hash 与 gateway 不一致 → gateway 401 `key_not_found` | 用 `HMAC-SHA256(raw_key, gateway_hmac_secret)`;secret 须与 `GATEWAY_HMAC_SECRET` 完全一致 |
| `KeyMeta.key_id` 用顺序整数或非 v4 UUID | publisher 校验失败 → 400 `KeyID must be a UUID v4` | 用 UUID v4(`crypto/rand` 设位)|
| `revision` 留 0 | publisher Lua CAS 拒 — 不能比之前 0 老 | `time.Now().UnixMicro()` |
| `allowed_models=[]` 想表达"全部禁用" | 实际是"允许所有"。要禁用整个 key 用 `status="suspended"` 或撤销集 | 真要白名单的话明确填模型列表 |
| `responses_affinity_enabled=false` 长期生效 | OpenAI Responses API stateful 调用 `invalid_encrypted_content` | 生产留空(默认 on),只在故障应急显式给 false |
| 用 §6.9 整池 PUT 做日常单条增删 | 每次触发 gateway 重拉整池,**短暂窗口**抖动 | 用 §6.12 per-entry |
| 用 §4.9 整集替换 PUT revoked-keys 做日常撤销 | 漏写就是解封 | 用 §4.5 / §4.6 增量 |
| `has_balance=false` 不带 reason | 400 INVALID_ARGUMENT | reason 必须是白名单值之一 |
| Replay 既不传 `batch_id` 也不传时间窗 | 400 INVALID_ARGUMENT | 二选一 |
| `retry_budget` 只填 max_providers 想 max_keys_per_provider 取默认 | 其它零位被视为"该轴 unlimited",生产可能爆 retry tail | 三轴**要么全零**(取 in-code 默认)**要么全显式给**

---
