# Provider Profile 配置分层、演进路线与主流厂商接入样例

日期: 2026-06-03

## 1. 背景与结论

新分支已经把 gateway 的 provider 路由从 legacy `api_type` / `api_types` 切到 `protocol_bindings`。这解决了“一个 provider 多协议、多 endpoint、多厂商扩展”的表达问题,但也带来一个产品风险:如果把 `protocol_bindings`、`request_param_policy`、`field_disposition`、constraints 等字段直接暴露给 admin,普通运营基本无法正确维护。

本方案的核心结论:

```text
用户维护业务意图。
系统维护协议事实。
高级差异走受控配置。
```

对应到落地形态:

```text
admin
  只维护 ProviderIntent: 厂商、profile、base_url、key、模型、路由策略、限流。

admin / publisher
  admin 维护采购侧 provider/channel/model/key 与平台侧模型/路由策略;publisher 接收 admin 编译或提交的 ProviderInfo / ModelCatalog / Route / KeyPool 并分发给 gateway。

gateway
  只消费已展开、可校验的 protocol_bindings / route / keypool / catalog。
```

这既避免用户维护复杂协议配置,又避免厂商接口变化时所有变更都绑定 gateway 发版。

### 1.1 当前最终服务边界

当前最终架构中,控制侧职责拆分为三个服务:

| 服务 | 主要职责 | 与本文关系 |
|---|---|---|
| admin | 维护采购侧 provider/channel/model/key 配置,以及平台侧模型、路由、profile 选择、发布策略 | ProviderIntent 的主要录入和编译入口 |
| user | 维护用户侧 key、key 状态、权限、模型白名单等 user-key 数据 | 对应 publisher `/user/api/v1/*` 数据,不负责 provider profile |
| billing | 维护 balance、余额状态、计费相关数据 | 对应 publisher `/billing/api/v1/*` 数据,不负责 provider profile |

因此本文后续提到的 provider/profile/model/route/keypool 配置,默认归属 admin;user 和 billing 只负责用户 key 与 balance 相关数据。

---

## 2. 三类配置边界

### 2.1 为什么要分三类

模型厂商接入配置本质上有三种性质:

| 类型 | 谁理解 | 谁维护 | 改错后果 | 推荐入口 |
|---|---|---|---|---|
| 业务意图 | 用户 / 运营 | admin | 可通过测试连接、路由策略调整发现 | 普通页面 |
| 协议事实 | gateway / publisher / 平台维护者 | profile registry / compiler | provider 被跳过、请求转错、参数丢失、响应解析失败 | 系统内置生成 |
| 受控高级差异 | 高级管理员 / 平台维护者 | 白名单配置 + 审计 + 灰度 | 影响特定 endpoint / 模型 / beta 能力 | 高级折叠区或发布流程 |

所以需要分成:

```text
用户可配置
系统内置生成
高级受控配置
```

---

## 3. 用户可配置

这一类字段描述“我要接哪个上游、开哪些模型、流量怎么走”。它们是业务配置,用户能理解,也适合放到页面。

### 3.1 `provider`

含义:厂商代码。

示例:

```text
openai
minimax
kimi
deepseek
qwen
anthropic
gemini
azure
doubao
zhipu
mistral
groq
cohere
```

用途:

- 决定页面展示、logo、默认 base_url、推荐 profile。
- 不直接决定 gateway adapter;真正 adapter 由 `provider_profile` 编译出的 `protocol_bindings` 决定。

### 3.2 `provider_profile`

含义:厂商协议预设。

示例:

```text
openai_compatible
openai_responses
minimax_openai
moonshot_openai
deepseek_openai
qwen_openai
anthropic_official
gemini_official
azure_openai
doubao_ark_openai
zhipu_glm
zhipu_glm_coding
mistral_openai
groq_openai
cohere_v2_rerank
```

用途:

- 用户只选择“这个厂商走哪种接入方式”。
- 系统根据 profile 自动生成协议绑定、参数策略、字段处置、约束。
- 同一厂商可以有多个 profile,例如 Qwen 可以有 `qwen_openai`、`qwen_openai_thinking`、`qwen_dashscope_native`。

### 3.3 `base_url`

含义:上游 API 根地址。

示例:

```text
https://api.openai.com/v1
https://api.deepseek.com
https://api.moonshot.cn/v1
https://dashscope.aliyuncs.com/compatible-mode/v1
https://api.anthropic.com
https://generativelanguage.googleapis.com/v1beta
https://ark.cn-beijing.volces.com/api/v3
```

要求:

- 页面可以给默认值,用户可改。
- 不允许用户手写完整 path,例如 `/chat/completions`、`/messages`、`/models/{model}:generateContent`;这些由 profile / endpoint variant 生成。

### 3.4 `api_key`

含义:上游厂商 key。

要求:

- 前端只负责录入。
- 后端加密存储或转换为 secret ref。
- 发布到 gateway 侧时变成 KeyPoolEntry 的密文或引用。
- 用户不配置 header 名称、JWT 生成逻辑、query key 位置;这些由 `auth_scheme` 生成。

### 3.5 `models`

含义:当前 channel/provider 开通哪些逻辑模型。

示例:

```text
gpt-4.1
claude-sonnet-4
kimi-k2
minimax-m2.5
qwen-plus
deepseek-chat
```

用途:

- 编译为 ModelCatalogEntry 和 RouteConfig。
- 模型是否上架、价格、展示顺序、计划可用范围属于业务侧。

### 3.6 `upstream_model`

含义:发给上游厂商的真实模型名。

示例:

```text
逻辑模型 gpt-4.1        -> upstream_model gpt-4.1
逻辑模型 claude-sonnet-4 -> upstream_model claude-sonnet-4-20250514
逻辑模型 minimax-m2.5   -> upstream_model MiniMax-M2.5
逻辑模型 azure-gpt-4.1  -> upstream_model my-gpt-4-1-deployment
```

用途:

- 编译到 `RouteEntry.upstream_model`。
- Azure 场景下通常是 deployment name。

### 3.7 `priority`

含义:路由优先级。

用途:

- 主备路由。
- 成本优先 / 质量优先路由。
- 编译到 `RouteEntry.priority`。

### 3.8 `weight`

含义:同优先级下的流量权重。

用途:

- 多 provider 分流。
- 新渠道灰度。
- 成本优化。

注意区分:

```text
route weight = provider 级路由权重
key weight   = provider 内多把 key 的选择权重
```

### 3.9 `rpm` / `tpm`

含义:

```text
rpm = 每分钟请求数限制
tpm = 每分钟 token 数限制
```

用途:

- 编译到 `KeyPoolEntry.rpm_limit` / `KeyPoolEntry.tpm_limit`。
- 用于厂商套餐额度、key 限额和成本控制。

### 3.10 `endpoint_variant`

含义:同一个 `provider_profile` 下的端点变体。

示例:

```text
chat_completions
responses
embeddings
rerank
messages
generate_content
count_tokens
azure_deployment
glm_coding
```

要求:

- 用户只能下拉选择,不能手写 path。
- 系统根据 variant 生成 `request_contract`、path、adapter、translator。

---

## 4. 系统内置生成

这一类字段是协议事实。它们是 gateway 必须消费的运行时配置,但不应该由普通用户维护。

### 4.1 `protocol_bindings`

含义:provider 协议路由绑定。

它告诉 gateway:

```text
这个 provider 接受哪些 inbound protocol
支持哪些 capability
请求语义是什么 request_contract
上游使用什么 outbound_protocol
path_prefix 是什么
认证方式是什么
是否需要 translator
```

示例:

```json
{
  "name": "openai-chat",
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
  "translator": "passthrough"
}
```

为什么系统生成:

- 网关需要它做路由。
- 普通用户不知道 inbound/outbound protocol、request_contract、translator 的组合规则。
- 配错会导致 provider 被跳过或请求失败。

### 4.2 `translator`

含义:请求/响应协议转换器。

当前网关已知值包括:

```text
passthrough
standard_to_openai
standard_to_gemini
responses_to_chat_synthesis
```

`standard_to_anthropic` 是 legacy converter 名称,但当前 explicit bridge 白名单没有
OpenAI→Anthropic chat 元组;手写该组合会被 publisher/gateway 以
`cross_protocol_bridge_required` 拒绝。Anthropic 官方上游应生成同协议
`anthropic`→`anthropic` + `passthrough`;当前白名单内跨协议示例是
`anthropic`→`openai` + `bridge:"anthropic_to_openai_chat"`。

为什么系统生成:

- translator 是代码能力,不是用户配置。
- 它必须和 gateway 已实现转换器匹配。
- 用户选错会导致请求体/响应体错误。

### 4.3 `request_contract`

含义:请求语义契约。

示例:

```text
chat_completions
responses
anthropic_messages
anthropic_count_tokens
gemini_generate
embeddings
rerank
```

为什么系统生成:

- 它决定 path template 和 adapter 行为。
- 用户只应该选择 `endpoint_variant=chat_completions`,系统生成 `request_contract=chat_completions`。

### 4.4 `capability_profiles`

含义:能力模板组合。

示例:

```text
openai.chat_completions.full
openai.chat_completions.tools
openai.chat_completions.structured_output
openai.chat_completions.streaming_usage
minimax.openai_chat.thinking
minimax.openai_chat.response_extensions
anthropic.messages.tools
gemini.generate_content.tools
```

为什么系统生成:

- 它描述协议/厂商支持哪些参数、能力、额外字段。
- 用户不应该知道 MiniMax 要加哪些 response extensions,也不应该知道 Gemini thinking budget 对应哪个字段。

### 4.5 `request_param_policy`

含义:请求参数兼容策略。

典型用途:

```text
某参数上游固定为一个值
客户端传相同值时 omit
客户端传不同值时 reject
某参数不支持时 drop 或 reject
```

示例:

```json
{
  "temperature": {
    "category": "sampling",
    "upstream_behavior": "fixed",
    "fixed_value": "1",
    "on_equal": "omit",
    "on_mismatch": "reject"
  }
}
```

为什么系统生成:

- 这是最容易引发线上兼容问题的地方。
- Kimi、MiniMax、Qwen、DeepSeek 等模型级参数差异不能交给普通用户维护。
- 需要和 conformance 测试绑定。

### 4.6 `field_disposition`

含义:字段处理策略和观测证据配置。

动作包括:

```text
preserve
adapt
drop
reject
preserve_vendor_extra
passthrough_raw
```

示例:

```json
{
  "request": {
    "thinking": "preserve",
    "reasoning_split": "preserve"
  },
  "response": {
    "reasoning_details": "preserve_vendor_extra",
    "base_resp": "preserve_vendor_extra"
  }
}
```

为什么系统生成:

- 它用于解释字段为什么被保留、转换、丢弃或拒绝。
- 是审计和排障证据,不是业务配置。

### 4.7 `param_constraints`

含义:单个参数的取值约束。

示例:

```text
tool_choice: auto / none / required / named_function
response_format: json_object / json_schema
stream_options: include_usage
parallel_tool_calls: true / false
```

为什么系统生成:

- 来自协议事实和厂商文档。
- 人手维护容易漏或填错。

### 4.8 `combination_constraints`

含义:多个参数之间的组合约束。

示例:

```text
stream_options.include_usage=true 要求 stream=true
reasoning_split=true 要求 thinking present
```

为什么系统生成:

- 这是协议规则,不是运营配置。
- 需要由 profile + conformance 测试维护。

---

## 5. 高级受控配置

这一类字段不适合普通用户随便配置,但也不能完全写死。应通过白名单、下拉、审计、校验、灰度控制。

### 5.1 `api_version`

主要用于:

```text
Azure OpenAI
部分 preview endpoint
部分厂商版本化 API
```

控制原则:

- 从白名单选择。
- 按 provider_profile / endpoint_variant 限定。
- 可在 model/route 级生效。
- 有默认值和回滚能力。

### 5.2 `anthropic_beta`

主要用于 Anthropic beta header。

示例:

```text
output-128k-2025-02-19
interleaved-thinking-2025-05-14
```

控制原则:

- profile 提供可选 beta 列表。
- 页面下拉选择。
- 默认不启用。
- 启用需要提示、审计和灰度。

### 5.3 `custom_headers` 白名单

含义:允许给上游请求加少数受控 header。

允许示例:

```text
anthropic-beta
OpenAI-Beta
厂商 trace header
企业私有网关白名单 header
```

禁止用户配置:

```text
Authorization
api-key
Host
Content-Length
Transfer-Encoding
```

认证类 header 必须由系统 `auth_scheme` 生成。

### 5.4 `vendor_extra` 开关

含义:是否保留厂商私有响应字段。

示例:

```text
MiniMax reasoning_details / base_resp
部分厂商 safety / moderation / trace 字段
```

控制原则:

- 页面只暴露 `preserve_vendor_extra=true/false`。
- 不让用户维护每个字段的 `field_disposition`。
- 默认由 profile 决定。

---

## 6. 四阶段落地路线

三类配置定义“谁维护什么”,四阶段定义“怎么从简单内置演进到可热更新、可灰度、可回滚、可验证”。两者不冲突。

### 6.1 第一阶段:代码内置默认 profile,admin 只选择 profile

目标:

```text
先把用户从复杂协议配置中解放出来。
```

实现:

- gateway/admin/publisher 内置或共享默认 `ProfileRegistry`;admin 侧用于编译采购侧配置,publisher 侧用于校验和分发,gateway 侧用于运行时校验。
- admin 页面只展示用户可配置字段。
- 用户选择 `provider_profile`,系统自动生成 `protocol_bindings`、`capability_profiles`、`request_param_policy` 等。
- 高级受控配置先只开放少量白名单字段。

### 6.2 第二阶段:admin/publisher 增加 profile override API

目标:

```text
厂商接口细节变化时,不必每次都发 gateway。
```

实现:

- 增加 ProfileOverride API。
- 允许平台维护者 patch 某个 profile 的参数策略、字段处置、constraints。
- override 不是普通用户配置,必须走校验和审计。

示例:

```json
{
  "profile_id": "moonshot_openai",
  "profile_version": "2026-06-10",
  "base_profile": "openai_compatible",
  "patch": {
    "request_param_policy": {
      "temperature": {
        "upstream_behavior": "fixed",
        "fixed_value": "1",
        "on_equal": "omit",
        "on_mismatch": "reject"
      }
    }
  },
  "status": "active"
}
```

### 6.3 第三阶段:profile version + 灰度 + 回滚

目标:

```text
profile 改动不应瞬间影响所有渠道。
```

实现:

- 每个 profile 带 `profile_version`。
- 支持 stable / canary / disabled。
- 支持按 provider/model/channel 灰度。
- 支持一键回滚上一版 profile。

用户配置仍然可以简单:

```json
{
  "provider_profile": "moonshot_openai"
}
```

系统解析为:

```json
{
  "provider_profile": "moonshot_openai",
  "profile_version": "2026-06-03-stable"
}
```

### 6.4 第四阶段:profile conformance 自动测试和线上观测闭环

目标:

```text
每次 profile 变更都能证明它是可用的。
```

conformance 覆盖:

- protocol binding shape。
- 参数白名单。
- 参数取值约束。
- 参数组合约束。
- 请求转换。
- 响应转换。
- field disposition 证据。
- mock 或真实 upstream smoke test。

线上观测:

- 按 `profile_id` / `profile_version` 统计成功率。
- 上游 400/401/429/5xx。
- 参数 drop/reject/omit 计数。
- tools 调用失败率。
- stream 中断率。
- 模型维度错误率。

---

## 7. 推荐统一配置模型

### 7.1 Admin 侧 ProviderIntent

```ts
type ProviderIntent = {
  provider: string;
  providerProfile: string;
  endpointVariant?: string;
  baseUrl: string;
  auth: {
    type: "bearer" | "x-api-key" | "azure-key" | "google-key" | "jwt-or-bearer";
    secretRef: string;
  };
  keyStrategy?: "priority" | "weight";
  models: ModelIntent[];
};

type ModelIntent = {
  model: string;
  upstreamModel: string;
  enabled: boolean;
  capabilities: string[];
  contextWindow?: number;
  maxOutputTokens?: number;
  priority?: number;
  weight?: number;
  rpmLimit?: number;
  tpmLimit?: number;
  apiVersion?: string;
  anthropicBeta?: string;
  vendorExtra?: boolean;
};
```

### 7.2 Compiler 输出

ProviderIntent 编译为 publisher 写入:

```text
ProviderInfo
ModelCatalogEntry
RouteConfig
KeyPoolEntry
```

普通页面可以展示“编译预览”,但默认只读。

---

## 8. 必须与 gateway 代码保持一致的值域和配置约束

本节是 admin / publisher / profile compiler 必须遵守的配置边界。这里的很多值是 gateway 代码绑定枚举(code-bound enum),不是自由字符串。admin 页面应使用下拉/模板生成,不要让用户手填。

### 8.1 code-bound 字段总表

| 字段 | 所在配置 | 当前允许值 | 代码来源 | 填错后果 |
|---|---|---|---|---|
| `accepts[].protocol` | `ProtocolBinding` | `openai` / `responses` / `anthropic` / `gemini` / `glm` / `glm_coding` | `internal/bindingspec/spec.go` | provider 校验失败,被跳过 |
| `accepts[].capabilities[]` | `ProtocolBinding` | `chat` / `embeddings` / `rerank` / `count_tokens` | `internal/bindingspec/spec.go` | provider 校验失败 |
| `accepts[].request_contract` | `ProtocolBinding` | `chat_completions` / `responses` / `anthropic_messages` / `anthropic_count_tokens` / `gemini_generate` / `embeddings` / `rerank` | `internal/bindingspec/spec.go` | provider 校验失败或 path template 不存在 |
| `outbound_protocol` | `ProtocolBinding` | `openai` / `openai_responses` / `anthropic` / `gemini` / `glm` / `glm_coding` / `doubao_ark` / `azure_openai` / `cohere_v2` | `internal/bindingspec/spec.go` | provider 校验失败 |
| `auth_scheme` | `ProtocolBinding` | `bearer` / `x-api-key` / `azure-key` / `google-key` / `jwt-or-bearer` | `internal/bindingspec/spec.go` | provider 校验失败或上游认证错误 |
| `translator` | `ProtocolBinding` | `passthrough` / `standard_to_openai` / `standard_to_gemini` / `responses_to_chat_synthesis`;`standard_to_anthropic` 当前没有 bridge 白名单元组 | `internal/bindingspec/spec.go` | provider 校验失败或请求转换错误;OpenAI→Anthropic + `standard_to_anthropic` 会被 `cross_protocol_bridge_required` 拒绝 |
| `capability_profiles[]` | `ProtocolBinding` | 见 §8.3 | `internal/capabilityprofile/profile.go` | provider 校验失败 |
| `supported_features[]` / `unsupported_features[]` | `ProtocolBinding` | 见 §8.4 | `internal/capabilityspec/spec.go` | provider 校验失败 |
| `supported_params[]` / `unsupported_params[]` / `param_constraints` keys / `request_param_policy` keys | `ProtocolBinding` / profile | 见 §8.5 | `internal/capabilityspec/spec.go` | provider 校验失败 |
| `request_param_policy.category` | `RequestParamPolicy` | 空 / `sampling` / `generation_count` / `semantic` / `content` / `observability` | `internal/capabilityprofile/param_policy.go` | provider 校验失败 |
| `request_param_policy.upstream_behavior` | `RequestParamPolicy` | 空 / `fixed` / `enum` | `internal/capabilityprofile/param_policy.go` | provider 校验失败 |
| `request_param_policy.on_equal` / `on_mismatch` | `RequestParamPolicy` | 空 / `preserve` / `omit` / `omit_with_warning` / `normalize` / `normalize_with_warning` / `reject` | `internal/capabilityprofile/param_policy.go` | provider 校验失败或兼容策略错误 |
| `field_disposition.*` value | `FieldDispositionConfig` | `preserve` / `adapt` / `drop` / `reject` / `preserve_vendor_extra` / `passthrough_raw` | `internal/capabilityprofile/field_disposition.go` | provider 校验失败 |
| `compat_api_type` | `ProtocolBinding` | `openai` / `openai_responses` / `anthropic` / `gemini` / `glm` / `glm_coding` / `zhipu` / `doubao` / `azure` / `cohere_v2` | `internal/providerprofile/builtin_*.go` | provider 校验失败;迁移兼容字段,新逻辑尽量不用 |
| `provider_strategy` / `default_provider_strategy` | Route / Platform | 建议 `priority` / `weight` / `latency`;`preferred_order` 由 preferred providers 触发 | `internal/routing/strategy_registry.go` | 未知值可能回退或无法选择预期策略 |
| `key_strategy` / `KeyPoolMeta.strategy` | Provider / KeyPool | `priority` / `weight` | `internal/routing/key_strategy_registry.go` | 未知值回退到默认 key selector |
| Provider/Route status | Provider / Route | `active` / `disabled` | `configsync/types.go` + planner 过滤逻辑 | 非 active/disabled 语义不明确,可能不可路由 |
| KeyPoolEntry status | KeyPool | admin 只写 `enabled` / `disabled`;gateway 运行时还可能看到 `decrypt_failed` / `auth_degraded` | `internal/model/model_catalog.go` | 非 `enabled` 不参与规划 |
| KeyMeta status | User key | `active` / `suspended` / `revoked` | `internal/model/keymeta.go` | 非 active 会被鉴权/准入拒绝 |

### 8.2 `ProtocolBinding` 结构约束

`ProtocolBinding` 不是自由 JSON,必须满足以下 shape 和 registry 校验:

- `protocol_bindings` 必须非空。
- `protocol_bindings[].name` 必填,且 provider 内唯一。
- `protocol_bindings[].accepts` 必须非空。
- `accepts[].protocol` 必填且必须是 §8.1 inbound protocol。
- `accepts[].capabilities` 必须非空且每个值必须是 §8.1 capability。
- `accepts[].request_contract` 必填且必须是 §8.1 request contract。
- `outbound_protocol` 必填且必须是 §8.1 outbound protocol。
- `auth_scheme` 必填且必须是 §8.1 auth scheme。
- `default_protocol_binding` 如果填写,必须引用已有 binding name。
- `outbound_protocol + capability + request_contract` 必须存在 path template;例如 `glm` 不能声明 `count_tokens`,因为当前没有对应 path template。
- 非 `passthrough` 的 `translator` 必须匹配 translation matrix;不能任意组合。
- 多个 binding 如果对同一请求匹配结果同级且无法唯一排序,provider 会因 ambiguous binding 被跳过。

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

> 注意:表里是 gateway adapter path template;provider 的 `base_url` / `path_prefix` 仍由 profile/variant 决定。

### 8.3 当前内置 `capability_profiles` 值域

当前 gateway 只接受以下 profile 名称:

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

新增 profile 时必须同步 gateway 代码或通过后续受控 profile override 机制确保 gateway 能识别;否则 provider 校验失败。

### 8.4 当前 `features` 值域

`ProtocolBinding.supported_features` / `unsupported_features` 只能使用:

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

这些是请求语义/能力标签,不是 UI 展示标签。admin 页面可以展示更友好的文案,但提交给 compiler/gateway 必须使用上述值。

### 8.5 当前 `params` 值域

`ProtocolBinding.supported_params` / `unsupported_params` / `param_constraints` / `request_param_policy` 只能引用:

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

组合约束 `combination_constraints` 的 field 也必须是上述参数,或参数的子路径,例如:

```text
stream_options.include_usage
response_format.type
```

且每条组合约束必须满足:

- `when` 非空。
- `requires` 和 `conflicts` 至少一个非空。
- field 和 value 都不能为空。

### 8.6 用户可配置字段的范围建议

这些字段虽然不是全部由 gateway 强校验,但 admin/publisher 应提前限制,避免生成不可用配置。

| 字段 | 推荐限制 | gateway 当前语义 |
|---|---|---|
| `provider` / `provider_id` | 非空;建议小写字母、数字、`-`、`_`;全局唯一 | 用作 provider map key;大小写漂移会导致 route 找不到 provider |
| `provider_profile` | 必须来自 ProfileRegistry 下拉 | 不直接进 gateway;compiler 用它生成 code-bound 字段 |
| `base_url` | 合法 URL;不带 endpoint path 或只带 profile 允许的 path root | gateway transport 拼接 path;错误会导致上游请求失败 |
| `api_key` | 非空;后端加密;不回显明文 | gateway 只看到解密后的 pool key plaintext |
| `model` / `model_id` | 非空;不能包含 `/`;建议裸模型名 | gateway `ValidateClientModelName` 拒绝带 provider 前缀的模型名 |
| `upstream_model` | 非空;允许厂商原始模型名/deployment name | adapter 会把它发给上游或拼入 path |
| `priority` | 整数;建议 `1..100`;数字越小优先级越高 | priority selector 按升序选择 |
| `weight` | 整数;建议 `1..100` 或 `1..10000`;不要填负数 | weight selector 中 `<=0` 会按 `1` 处理 |
| `rpm_limit` / `tpm_limit` | 整数;`0` 表示不限制;正数表示限额 | 负数无业务意义,admin 应拒绝 |
| `context_window` / `max_output` | 整数;`0` 表示未知/不声明;正数表示模型上限 | 用于 catalog 展示和请求约束 |
| `api_version` | 只能从 provider_profile 允许列表选 | gateway 透给 adapter;填错通常上游 400/404 |
| `anthropic_beta` | 当前 gateway route wire 是单个 string;如果 UI 多选,compiler 必须明确合并规则或禁止多选 | 透给 Anthropic header;填错通常上游 400 |
| `custom_headers` | 只允许白名单 header;禁止认证/传输类 header | gateway 不应让用户覆盖 Authorization / Host 等关键 header |
| `vendor_extra` | boolean | compiler 映射为受控 `field_disposition`,不要直接让用户编辑字段列表 |

### 8.7 status / strategy 语义

| 字段 | 可写值 | 说明 |
|---|---|---|
| `ProviderInfo.status` | `active` / `disabled` | 非 active provider 不应参与路由 |
| `RouteEntry.status` | `active` / `disabled` | disabled route 不参与模型路由 |
| `KeyPoolEntry.status` | admin 写 `enabled` / `disabled` | 只有 `enabled` key 会进入规划;`decrypt_failed`、`auth_degraded` 是 gateway 运行时/解密状态,不是 admin 常规写入 |
| `KeyMeta.status` | `active` / `suspended` / `revoked` | user key 鉴权状态 |
| `ProviderInfo.key_strategy` | `priority` / `weight` | provider 内 key 选择策略;空值回退默认 |
| `KeyPoolMeta.strategy` | `priority` / `weight` | 兼容 fallback;新写入优先用 ProviderInfo.key_strategy |
| `RouteConfig.provider_strategy` | `priority` / `weight` / `latency` | 模型 provider 选择策略;`preferred_order` 由 preferred providers 触发,不建议普通配置直接写 |
| `PlatformConfig.default_provider_strategy` | `priority` / `weight` / `latency` | 平台默认 provider 选择策略 |

### 8.8 `RequestParamPolicy` 约束

`request_param_policy` 的 key 必须是 §8.5 参数名。每个 policy 的字段限制:

| 字段 | 允许值 / 约束 |
|---|---|
| `category` | 空 / `sampling` / `generation_count` / `semantic` / `content` / `observability` |
| `upstream_behavior` | 空 / `fixed` / `enum` |
| `fixed_value` | 当 `upstream_behavior=fixed` 时必填 |
| `allowed_values` | 当 `upstream_behavior=enum` 时建议非空 |
| `on_equal` | 空 / `preserve` / `omit` / `omit_with_warning` / `normalize` / `normalize_with_warning` / `reject` |
| `on_mismatch` | 同 `on_equal` |

普通 admin 页面不应直接编辑该结构;应由 profile/version 生成。

### 8.9 `FieldDisposition` 约束

`field_disposition.request` / `field_disposition.response` 的 key 是字段路径,value 只能是:

```text
preserve
adapt
drop
reject
preserve_vendor_extra
passthrough_raw
```

字段路径和值都不能为空。普通用户只应看到 `vendor_extra` 这类高层开关。

### 8.10 `CompatibilityMode` 约束

`compatibility_mode` 当前是三个 boolean 开关:

```text
openai_client_defaults
drop_unsupported_default_params
reject_semantic_params
```

它用于控制 OpenAI client 默认参数的兼容处理。普通页面不应开放;profile 或 profile override 可以设置。

### 8.11 `revision` / schema version / 时间类字段

| 字段 | 约束 |
|---|---|
| `schema_version` | 由 publisher/gateway schema 决定,调用方通常不手填 |
| `revision` | publisher 写非零递增/时间戳版本;admin 不应手填任意值 |
| `expires_at` | Unix 秒;`0` 通常表示永不过期 |
| `default_timeout_ms` / `stream_timeout_ms` / `first_token_timeout_ms` | 毫秒;`0` 通常由 gateway fallback 到默认值 |
| `attempt_timeout_ms` | 毫秒;`0` 表示不启用 per-attempt deadline |
| `sse_keepalive_interval_ms` | 毫秒;`0` 使用默认,负数显式关闭,正数必须小于链路最小 idle timeout |
| `max_body_size` | 字节;`0` fallback 默认;正数表示硬限制 |

### 8.12 admin 配置防错要求

admin/publisher 在写入 publisher 前应做这些校验:

1. 所有 code-bound 字段只能从枚举表选择。
2. `provider_profile + endpoint_variant` 必须能编译出至少一条合法 `protocol_binding`。
3. 编译后的 `protocol_bindings` 必须通过 gateway 同款 validator。
4. `model` 不能包含 `/`;禁止 `openai/gpt-4o` 这类 provider-prefixed model。
5. `upstream_model` 必填,除非 profile 明确允许 fallback 到 logical model。
6. `priority`、`weight`、`rpm/tpm`、`context_window`、`max_output` 必须是非负整数;其中 priority/weight 建议正整数。
7. route/provider/key status 必须是允许值。
8. `api_version`、`anthropic_beta`、`custom_headers` 必须走 profile 白名单。
9. 普通用户不能直接编辑 `translator`、`outbound_protocol`、`request_contract`、`request_param_policy`、`field_disposition`、constraints。
10. 每个 profile/version 必须有 conformance 测试,避免“页面可保存但 gateway 不可用”。


---

## 9. 主流厂商接入对齐

### 9.1 总览矩阵

| 厂商 | 官网接口形态 | 当前网关覆盖判断 | 推荐 profile | 需要注意的缺口 |
|---|---|---|---|---|
| OpenAI | Chat Completions / Responses / Embeddings | 能 | `openai_compatible` / `openai_responses` | Responses 内置工具、MCP、code interpreter、web search 需要独立 profile 开关 |
| Azure OpenAI | deployment 路径 + `api-version` | 能 | `azure_openai` | `api_version` 必须受控,不让用户手写 path |
| Anthropic | Messages / Count Tokens / tools / thinking / beta header | 基本能 | `anthropic_official` | thinking、beta header 需要 profile/route 字段 |
| Gemini | generateContent / tools / safetySettings / thinkingBudget / embeddings | 基本能 | `gemini_official` | `safety_settings`、`thinking_budget` 应做 Gemini profile |
| DeepSeek | OpenAI-compatible Chat Completions | 能 | `deepseek_openai` | reasoner 模型特殊行为需要模型级 policy |
| Kimi / Moonshot | OpenAI-compatible Chat Completions | 能,需 profile 补强 | `moonshot_openai` | 固定采样值、tools 兼容、partial assistant 等需要 policy/profile |
| Qwen / DashScope | OpenAI-compatible mode,另有 DashScope 原生能力 | Chat 基本能 | `qwen_openai` | `extra_body`、thinking、原生接口需要 endpoint variant |
| MiniMax | OpenAI-compatible Chat,含 thinking / response extensions | 能 | `minimax_openai` | 自动挂 MiniMax thinking 和 response extensions |
| Doubao / Volc Ark | OpenAI-compatible Chat,路径通常 `/api/v3` | Chat 能 | `doubao_ark_openai` | Ark Responses/MCP 需要新增 variant/profile |
| Mistral | OpenAI-like Chat / tools / json / stream | 基本能 | `mistral_openai` | `safe_prompt`、prefix、guardrail 类私有字段需 profile |
| Groq | OpenAI-compatible Chat | 能 | `groq_openai` | 主要是模型能力差异 |
| Cohere | v2 chat / rerank | 部分能 | `cohere_v2_rerank` | Chat v2 未完整时不要开放 chat profile |
| Zhipu GLM | GLM/OpenAI-like Chat,JWT/bearer,GLM coding endpoint | 能 | `zhipu_glm` / `zhipu_glm_coding` | GLM 与 GLM coding 需要 multi-binding |

官方文档参考:

- OpenAI API: https://platform.openai.com/docs/api-reference
- Anthropic API: https://docs.anthropic.com/en/api/messages
- Google Gemini API: https://ai.google.dev/api
- Azure OpenAI REST API: https://learn.microsoft.com/en-us/azure/ai-services/openai/reference
- DeepSeek API: https://api-docs.deepseek.com/api/create-chat-completion
- Moonshot/Kimi API: https://platform.moonshot.cn/docs/api/chat
- DashScope OpenAI compatible: https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope
- MiniMax Chat Completion: https://platform.minimaxi.com/document/ChatCompletion%20v2
- Volcengine Ark: https://www.volcengine.com/docs/82379/1298454
- Mistral API: https://docs.mistral.ai/api/
- Groq OpenAI compatibility: https://console.groq.com/docs/openai
- Cohere Rerank API: https://docs.cohere.com/reference/rerank

---

## 10. 厂商配置样例

### 10.1 OpenAI-compatible 通用样例

用户配置:

```json
{
  "provider": "deepseek",
  "provider_profile": "deepseek_openai",
  "endpoint_variant": "chat_completions",
  "base_url": "https://api.deepseek.com",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-deepseek-main"
  },
  "models": [
    {
      "model": "deepseek-chat",
      "upstream_model": "deepseek-chat",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "json_mode"],
      "priority": 10,
      "weight": 100,
      "rpm_limit": 60,
      "tpm_limit": 100000
    }
  ]
}
```

系统生成的 ProviderInfo 片段:

```json
{
  "protocol_bindings": [
    {
      "name": "openai-chat",
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
      "capability_profiles": ["openai.chat_completions.full"]
    }
  ],
  "default_protocol_binding": "openai-chat"
}
```

### 10.2 MiniMax

用户配置:

```json
{
  "provider": "minimax",
  "provider_profile": "minimax_openai",
  "endpoint_variant": "chat_completions",
  "base_url": "https://api.minimax.io",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-minimax-main"
  },
  "models": [
    {
      "model": "minimax-m2.5",
      "upstream_model": "MiniMax-M2.5",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "reasoning"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

系统生成重点:

```json
{
  "capability_profiles": [
    "openai.chat_completions.full",
    "minimax.openai_chat.thinking",
    "minimax.openai_chat.response_extensions"
  ],
  "field_disposition": {
    "request": {
      "thinking": "preserve",
      "reasoning_split": "preserve"
    },
    "response": {
      "reasoning_details": "preserve_vendor_extra",
      "input_sensitive": "preserve_vendor_extra",
      "output_sensitive": "preserve_vendor_extra",
      "base_resp": "preserve_vendor_extra"
    }
  }
}
```

### 10.3 Kimi / Moonshot

用户配置:

```json
{
  "provider": "kimi",
  "provider_profile": "moonshot_openai",
  "endpoint_variant": "chat_completions",
  "base_url": "https://api.moonshot.cn/v1",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-kimi-main"
  },
  "models": [
    {
      "model": "kimi-k2",
      "upstream_model": "kimi-k2",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

系统生成重点:

```json
{
  "protocol_bindings": [
    {
      "name": "openai-chat",
      "outbound_protocol": "openai",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "capability_profiles": ["openai.chat_completions.full"],
      "request_param_policy": {
        "temperature": {
          "category": "sampling",
          "upstream_behavior": "fixed",
          "fixed_value": "1",
          "on_equal": "omit",
          "on_mismatch": "reject"
        },
        "top_p": {
          "category": "sampling",
          "upstream_behavior": "fixed",
          "fixed_value": "1",
          "on_equal": "omit",
          "on_mismatch": "reject"
        }
      }
    }
  ]
}
```

说明:具体固定值和策略必须以最终 conformance fixture 和厂商文档核对结果为准;页面不暴露这些策略细节。

### 10.4 Anthropic

用户配置:

```json
{
  "provider": "anthropic",
  "provider_profile": "anthropic_official",
  "endpoint_variant": "messages",
  "base_url": "https://api.anthropic.com",
  "auth": {
    "type": "x-api-key",
    "secret_ref": "provider-key-anthropic-main"
  },
  "models": [
    {
      "model": "claude-sonnet-4",
      "upstream_model": "claude-sonnet-4-20250514",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "reasoning"],
      "priority": 10,
      "weight": 100,
      "anthropic_beta": "output-128k-2025-02-19"
    }
  ]
}
```

系统生成重点:

```json
{
  "protocol_bindings": [
    {
      "name": "anthropic-messages",
      "accepts": [
        {
          "protocol": "anthropic",
          "capabilities": ["chat"],
          "request_contract": "anthropic_messages"
        }
      ],
      "outbound_protocol": "anthropic",
      "path_prefix": "/v1",
      "auth_scheme": "x-api-key",
      "translator": "passthrough",
      "capability_profiles": [
        "anthropic.messages.base",
        "anthropic.messages.tools"
      ]
    }
  ],
  "default_protocol_binding": "anthropic-messages"
}
```

### 10.5 Gemini

用户配置:

```json
{
  "provider": "google",
  "provider_profile": "gemini_official",
  "endpoint_variant": "generate_content",
  "base_url": "https://generativelanguage.googleapis.com/v1beta",
  "auth": {
    "type": "google-key",
    "secret_ref": "provider-key-gemini-main"
  },
  "models": [
    {
      "model": "gemini-2.5-pro",
      "upstream_model": "gemini-2.5-pro",
      "enabled": true,
      "capabilities": ["chat", "tools", "vision", "streaming", "reasoning"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

系统生成重点:

```json
{
  "protocol_bindings": [
    {
      "name": "gemini-generate-content",
      "accepts": [
        {
          "protocol": "gemini",
          "capabilities": ["chat"],
          "request_contract": "gemini_generate"
        },
        {
          "protocol": "openai",
          "capabilities": ["chat"],
          "request_contract": "chat_completions"
        }
      ],
      "outbound_protocol": "gemini",
      "path_prefix": "",
      "auth_scheme": "google-key",
      "translator": "standard_to_gemini",
      "capability_profiles": [
        "gemini.generate_content.base",
        "gemini.generate_content.tools"
      ]
    }
  ]
}
```

### 10.6 Azure OpenAI

用户配置:

```json
{
  "provider": "azure",
  "provider_profile": "azure_openai",
  "endpoint_variant": "chat_completions",
  "base_url": "https://my-resource.openai.azure.com",
  "auth": {
    "type": "azure-key",
    "secret_ref": "provider-key-azure-main"
  },
  "models": [
    {
      "model": "gpt-4.1",
      "upstream_model": "my-gpt-4-1-deployment",
      "api_version": "2025-01-01-preview",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "json_mode"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

系统生成重点:

```json
{
  "protocol_bindings": [
    {
      "name": "azure-openai-chat",
      "outbound_protocol": "azure_openai",
      "path_prefix": "/openai/deployments/{upstream_model}",
      "auth_scheme": "azure-key",
      "translator": "passthrough",
      "capability_profiles": ["openai.chat_completions.full"]
    }
  ]
}
```

### 10.7 Qwen / DashScope

用户配置:

```json
{
  "provider": "qwen",
  "provider_profile": "qwen_openai",
  "endpoint_variant": "compatible_chat",
  "base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-qwen-main"
  },
  "models": [
    {
      "model": "qwen-plus",
      "upstream_model": "qwen-plus",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "json_mode"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

建议 variant:

```text
qwen_openai
qwen_openai_thinking
qwen_dashscope_native
qwen_multimodal
```

普通用户先只看到 `qwen_openai`;thinking / extra_body / 多模态能力通过 profile/variant 控制。

### 10.8 Doubao / Volc Ark

用户配置:

```json
{
  "provider": "doubao",
  "provider_profile": "doubao_ark_openai",
  "endpoint_variant": "chat_completions",
  "base_url": "https://ark.cn-beijing.volces.com/api/v3",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-doubao-main"
  },
  "models": [
    {
      "model": "doubao-seed-1.6",
      "upstream_model": "doubao-seed-1-6-250615",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "reasoning"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

系统生成重点:

```json
{
  "protocol_bindings": [
    {
      "name": "doubao-ark-chat",
      "outbound_protocol": "doubao_ark",
      "path_prefix": "/api/v3",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "capability_profiles": ["openai.chat_completions.full"]
    }
  ]
}
```

### 10.9 Mistral / Groq

Mistral 用户配置:

```json
{
  "provider": "mistral",
  "provider_profile": "mistral_openai",
  "base_url": "https://api.mistral.ai/v1",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-mistral-main"
  },
  "models": [
    {
      "model": "mistral-large",
      "upstream_model": "mistral-large-latest",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming", "json_mode"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

Groq 用户配置:

```json
{
  "provider": "groq",
  "provider_profile": "groq_openai",
  "base_url": "https://api.groq.com/openai/v1",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-groq-main"
  },
  "models": [
    {
      "model": "llama-3.3-70b-versatile",
      "upstream_model": "llama-3.3-70b-versatile",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

两者基础形态都可走 OpenAI-compatible binding。Mistral 的 `safe_prompt`、prefix、guardrail 类私有字段应走后续 profile 扩展,不开放普通用户手填。

### 10.10 Cohere Rerank

用户配置:

```json
{
  "provider": "cohere",
  "provider_profile": "cohere_v2_rerank",
  "endpoint_variant": "rerank",
  "base_url": "https://api.cohere.com/v2",
  "auth": {
    "type": "bearer",
    "secret_ref": "provider-key-cohere-main"
  },
  "models": [
    {
      "model": "rerank-v3.5",
      "upstream_model": "rerank-v3.5",
      "enabled": true,
      "capabilities": ["rerank"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

说明:如果 `cohere_v2` chat outbound adapter 未完整支持,admin 不应开放 Cohere chat profile。

### 10.11 Zhipu GLM

用户配置:

```json
{
  "provider": "zhipu",
  "provider_profile": "zhipu_glm",
  "endpoint_variant": "chat_completions",
  "base_url": "https://open.bigmodel.cn/api/paas/v4",
  "auth": {
    "type": "jwt-or-bearer",
    "secret_ref": "provider-key-zhipu-main"
  },
  "models": [
    {
      "model": "glm-4.6",
      "upstream_model": "glm-4.6",
      "enabled": true,
      "capabilities": ["chat", "tools", "streaming"],
      "priority": 10,
      "weight": 100
    }
  ]
}
```

GLM coding 应作为单独 endpoint variant 或第二条 binding:

```text
provider_profile = zhipu_glm_coding
endpoint_variant = glm_coding
```

---

## 11. profile registry 建议结构

Profile registry 不应完全手写 raw `protocol_bindings`,而应维护“模板 + 版本 + patch”。

```json
{
  "id": "minimax_openai",
  "version": "2026-06-03",
  "display_name": "MiniMax OpenAI Compatible",
  "base_profile": "openai_compatible",
  "endpoint_variants": {
    "chat_completions": {
      "outbound_protocol": "openai",
      "path_prefix": "/v1",
      "auth_scheme": "bearer",
      "translator": "passthrough",
      "accepts": [
        {
          "protocol": "openai",
          "capabilities": ["chat"],
          "request_contract": "chat_completions"
        }
      ],
      "capability_profiles": [
        "openai.chat_completions.full",
        "minimax.openai_chat.thinking",
        "minimax.openai_chat.response_extensions"
      ]
    }
  },
  "field_disposition": {
    "request": {
      "thinking": "preserve",
      "reasoning_split": "preserve"
    },
    "response": {
      "reasoning_details": "preserve_vendor_extra",
      "base_resp": "preserve_vendor_extra"
    }
  }
}
```

维护规则:

- L0:代码内置默认 profile。
- L1:admin/publisher 下发官方 profile override。
- L2:provider/model/route 级少量 override。
- 优先级:`route override > model override > provider profile override > built-in profile`。
- L2 不允许普通用户覆盖 translator、outbound_protocol、完整 request_param_policy、完整 field_disposition。

---

## 12. 当前缺口与待办

| 待办 | 原因 | 建议优先级 |
|---|---|---|
| 增加 `ProfileRegistry` / compiler | 降低 admin 配置复杂度 | P0 |
| codesphere-admin 将 `sdkType` 迁移为 `providerProfile` / `endpointVariant` | 旧 `sdkType` 是 legacy adapter 思维 | P0 |
| 增加 `moonshot_openai` profile 与 conformance | Kimi 参数兼容问题高发 | P0 |
| 增加 `qwen_openai` / `qwen_openai_thinking` variant | Qwen OpenAI 兼容和私有 extra_body 需区分 | P1 |
| 增加 `anthropic.messages.thinking` profile | Anthropic thinking / beta 能力不能裸露给用户 | P1 |
| 增加 `gemini.generate_content.safety` / `thinking` profile | Gemini safety_settings / thinking_budget 需要受控 | P1 |
| 完善 `azure_openai` api_version 白名单 | Azure 版本错误会直接 400/404 | P1 |
| 明确 Cohere chat 是否开放 | 当前更适合先开放 rerank | P2 |
| profile version / canary / rollback | 避免 profile 改动影响所有渠道 | P2 |
| 按 profile_version 建立线上观测 | 形成配置变更闭环 | P2 |

---

## 13. 最终原则

1. 普通用户只维护业务意图,不维护协议事实。
2. `protocol_bindings` 对 gateway 必要,但不应成为普通页面配置项。
3. 厂商私有参数和返回字段应沉淀为 profile/variant,不能用自由 JSON 让用户碰运气。
4. profile 默认代码内置,后续支持 override、version、canary、rollback。
5. 每个 profile 变更必须有 conformance 测试和线上观测证据。
