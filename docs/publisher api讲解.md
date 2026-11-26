下面我按 “publisher 接口 → 最终写给 gateway 的数据 → 页面/控制面应该怎么配置” 重新梳理一遍。核心结论先说：

  publisher 不是业务配置系统，它是 “gateway 数据面 Redis 的唯一写入器”。
  gateway 真正需要的数据主要分 9 类：

  1. gw:keymeta：客户端 API Key 元数据，决定用户 key 能不能进网关。
  2. gw:key_balance / gw:key_balance_reason：余额/额度准入开关。
  3. gw:revoked_keys：已吊销 key 集合。
  4. gw:model_catalog：模型目录，描述模型是什么。
  5. gw:routes：模型路由，描述模型走哪个 provider / 上游模型。
  6. gw:providers：provider 协议绑定、base_url、状态、key 选择策略。
  7. gw:keypool:{provider_id} + gw:keypool_meta：上游 provider key 池。
  8. gw:platform:config：平台默认策略、超时、重试、熔断/披露等全局配置。
  9. gw:ip_blocklist / override / security 数据：SRE 应急控制。

  你现在前端 tmpconfig1 主要是在配第 4/5/6/7/8 类：provider、model catalog、route、keypool、platform config。客户端用户 key 另走 portal key 创建接口。

  ———

  ## 1. Public 接口

  这些不是配置网关数据的，只是服务探活和监控。

  ┌──────────────┬─────────────────────────────────────────┐
  │ 接口         │ 作用                                    │
  ├──────────────┼─────────────────────────────────────────┤
  │ GET /healthz │ 应用存活检查，只表示 publisher 进程活着 │
  │ GET /livez   │ K8s liveness probe                      │
  │ GET /readyz  │ readiness，通常会检查 PG/Redis 可用     │
  │ GET /metrics │ Prometheus 指标                         │
  └──────────────┴─────────────────────────────────────────┘

  页面不需要配置这些。

  ———

  ## 2. User Namespace：客户端 API Key 数据

  路径：/user/api/v1/*
  调用方：user-service / control-server 的用户 key 编译器。
  最终给 gateway 的核心数据是：gw:keymeta 和 gw:revoked_keys。

  ### 2.1 PUT /user/api/v1/keys

  写入单个客户端 API Key 元数据。

  gateway 用它判断：

  - 这个 key 是否存在
  - key 是否 active
  - key 属于哪个 tenant/workspace
  - 是否过期
  - 是否限制模型
  - 有哪些 RPM/TPM/并发策略
  - 是否有单次 max_tokens 限制
  - 是否有 allowance/balance 绑定

  核心字段：

  ┌─────────────────────────┬───────────────────────────────────────────┬────────────────────────────────────────────┐
  │ 字段                    │ 怎么填                                    │ 含义                                       │
  ├─────────────────────────┼───────────────────────────────────────────┼────────────────────────────────────────────┤
  │ key_id                  │ UUID v4                                   │ key 的内部 ID                              │
  │ key_hash                │ HMAC-SHA256(raw_key, gateway_hmac_secret) │ gateway 不存明文 key，只用 hash 查         │
  │ alias                   │ 可选，如 prod-key-1                       │ 展示/排查用                                │
  │ scope_attrs             │ 必填，至少 tenant/workspace               │ 业务身份上下文，如 tenant_id、workspace_id │
  │ status                  │ active / suspended / revoked 等           │ 非 active 会被 gateway 拒绝                │
  │ expires_at              │ Unix seconds，0 表示不过期                │ 注意不是毫秒                               │
  │ ip_whitelist            │ IP/CIDR 数组                              │ 空表示不限制                               │
  │ enforce_model_whitelist │ bool                                      │ 是否启用模型白名单                         │
  │ allowed_models          │ 模型名数组                                │ 只有 enforce_model_whitelist=true 时才强制 │
  │ policies                │ EffectivePolicy 数组                      │ RPM/TPM/并发等准入策略                     │
  │ resource_bindings       │ 资源运行时绑定                            │ gateway 查 quota/runtime state 用          │
  │ allowance_bindings      │ allowance 绑定                            │ gateway 查余额/套餐 allow flag             │
  │ max_tokens_per_call     │ int                                       │ 单次请求最大 output token                  │
  │ guardrails              │ object                                    │ 工具、视觉、最大消息数等限制               │
  └─────────────────────────┴───────────────────────────────────────────┴────────────────────────────────────────────┘

  怎么配：

  - 普通用户 key：
      - status=active
      - scope_attrs.tenant_id / workspace_id 必须有
      - policies 至少有 key 级 RPM/TPM
      - 如果要限制模型：enforce_model_whitelist=true + allowed_models=["xxx"]
      - 如果允许所有模型：enforce_model_whitelist=false

  常见坑：

  - allowed_models=[] 不是 “禁用所有模型”，而是 “不限制模型”，除非 enforce_model_whitelist=true。
  - key_hash 必须和 gateway 用同一个 HMAC secret 算，否则 gateway 永远 401。
  - expires_at 是秒，不是毫秒。

  ### 2.2 POST /user/api/v1/keys/bulk

  批量写 keymeta。

  适合：

  - 冷启动同步
  - 大批量用户 key 迁移
  - 从 control-server DB replay 出全量 keymeta

  Body 通常是：

  {
    "items": [
      { "key_hash": "...", "meta": { "...": "KeyMeta" } }
    ]
  }

  ### 2.3 DELETE /user/api/v1/keys/{key_hash}

  从 gw:keymeta 删除一个客户端 key。

  用途：

  - 物理删除 key
  - 用户彻底删除 API Key

  ### 2.4 DELETE /user/api/v1/keys/bulk

  批量删除 keymeta。

  ### 2.5 POST /user/api/v1/revoked-keys

  把 key_hash 加入 gw:revoked_keys。

  用途：

  - 软吊销
  - 应急封禁
  - 即使 gw:keymeta 还在，gateway 也会拒绝

  ### 2.6 POST /user/api/v1/revoked-keys/bulk

  批量加入 revoked set。

  ### 2.7 DELETE /user/api/v1/revoked-keys/{key_hash}

  恢复被吊销的 key。

  ### 2.8 DELETE /user/api/v1/revoked-keys/bulk

  批量恢复吊销。

  ### 2.9 PUT /user/api/v1/revoked-keys

  全量替换 revoked set。

  适合：

  - 安全系统全量重放
  - disaster recovery

  ———

  ## 3. Billing Namespace：余额准入数据

  路径：/billing/api/v1/*
  调用方：billing-service。
  最终给 gateway 的数据是 gw:key_balance / gw:key_balance_reason。

  gateway 用它判断 key 是否还有余额/额度。

  ### 3.1 PUT /billing/api/v1/key-balance/{key_id}

  设置单个 key 的余额标记。

  常见字段：

  ┌─────────────┬────────┬─────────────────────────────────────┐
  │ 字段        │ 怎么填 │ 含义                                │
  ├─────────────┼────────┼─────────────────────────────────────┤
  │ has_balance │ bool   │ true 允许，false 拒绝               │
  │ reason      │ string │ false 时建议填，如 balance_depleted │
  └─────────────┴────────┴─────────────────────────────────────┘

  效果：

  - has_balance=true：gateway 允许继续走后续准入
  - has_balance=false：gateway 可以直接拒绝请求

  ### 3.2 PUT /billing/api/v1/key-balance/bulk

  批量设置余额标记。

  ### 3.3 DELETE /billing/api/v1/key-balance/{key_id}

  删除余额标记。

  注意：删除不一定等于有余额，要看 gateway 当前默认策略和 allowance 绑定设计。

  ———

  ## 4. Admin Namespace：网关核心拓扑配置

  路径：/admin/api/v1/*
  调用方：admin-service / control-server compiler。
  这是你现在最关心的部分。

  ———

  # 4.1 Provider：gw:providers

  ## PUT /admin/api/v1/providers/{provider_id}

  注册或更新 provider。

  这是新方案最关键的接口。现在已经是 protocol-bindings-only：

  - 旧字段 api_type 不接受
  - 旧字段 api_types 不接受
  - 请求体里出现就 400
  - 必须传 protocol_bindings

  ### Provider 是什么？

  Provider 表示一个上游供应商或代理，例如：

  - OpenAI 官方
  - MiniMax
  - Moonshot/Kimi
  - DeepSeek
  - Qwen/DashScope compatible-mode
  - Anthropic
  - Gemini
  - 自建 OpenAI-compatible proxy

  gateway 通过 provider 知道：

  - 上游 base_url 是什么
  - 当前 provider 是否 active
  - provider 支持哪些 inbound 协议请求
  - 转发到上游时用什么 outbound protocol
  - 用什么 auth scheme
  - 是否需要 translator
  - 支持哪些参数、特性、私有扩展

  ### 字段说明

  ┌──────────────────────────┬──────┬──────────────────────────────────────────────────────┐
  │ 字段                     │ 必填 │ 怎么填                                               │
  ├──────────────────────────┼──────┼──────────────────────────────────────────────────────┤
  │ id                       │ 可选 │ 可省，path {provider_id} 是权威；如果填必须等于 path │
  │ code                     │ 可选 │ 人类可读 code，如 minimax、moonshot、deepseek        │
  │ name                     │ 可选 │ 展示名，如 MiniMax Production                        │
  │ base_url                 │ 必填 │ 上游根地址，如 https://api.minimax.io                │
  │ status                   │ 必填 │ active / disabled                                    │
  │ key_strategy             │ 可选 │ 上游 key 选择策略，常用 priority / weight            │
  │ protocol_bindings        │ 必填 │ Provider 协议绑定数组                                │
  │ default_protocol_binding │ 可选 │ 默认 binding 名称，必须引用已有 binding              │
  └──────────────────────────┴──────┴──────────────────────────────────────────────────────┘

  ### protocol_bindings 是什么？

  它替代旧的 api_types。

  旧设计：

  {
    "api_types": ["openai"]
  }

  问题是太粗，只能说 “像 OpenAI”，不能表达：

  - MiniMax 是 OpenAI-compatible 但多了 thinking 字段
  - Kimi 是 OpenAI-compatible 但采样参数必须固定
  - 某 provider 支持 OpenAI Chat 但不支持 Responses
  - 某 provider 需要私有 header 或字段处理
  - 某些参数要 drop / normalize / reject

  新设计：

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
        "compat_api_type": "openai",
        "capability_profiles": [
          "openai.chat_completions.full"
        ]
      }
    ],
    "default_protocol_binding": "openai-chat"
  }

  ### ProtocolBinding 字段

  ┌────────────────────────────┬──────┬────────────────────────────────────────────────────────────────┐
  │ 字段                       │ 必填 │ 含义                                                           │
  ├────────────────────────────┼──────┼────────────────────────────────────────────────────────────────┤
  │ name                       │   是 │ binding 名称，provider 内唯一                                  │
  │ priority                   │ 可选 │ 多 binding 匹配时的优先级                                      │
  │ accepts                    │   是 │ 这个 binding 接受哪些 inbound 请求                             │
  │ accepts[].protocol         │   是 │ inbound protocol，如 openai / anthropic / gemini / glm         │
  │ accepts[].capabilities     │   是 │ 能力，如 chat / tools / embeddings                             │
  │ accepts[].request_contract │   是 │ 请求语义，如 chat_completions / responses / anthropic_messages │
  │ accepts[].models           │ 可选 │ 只允许某些 logical model 使用这个 binding                      │
  │ outbound_protocol          │   是 │ 转上游时用什么协议，如 openai                                  │
  │ path_prefix                │ 可选 │ 上游路径前缀，如 /v1                                           │
  │ auth_scheme                │   是 │ 鉴权方式，如 bearer / x-api-key                                │
  │ translator                 │ 可选 │ 翻译器，同协议常用 passthrough                                 │
  │ compat_api_type            │ 可选 │ 迁移兼容字段，通常等于 openai                                  │
  │ capability_profiles        │ 可选 │ 能力 profile 组合                                              │
  │ supported_features         │ 可选 │ 明确支持的 feature                                             │
  │ unsupported_features       │ 可选 │ 明确不支持的 feature                                           │
  │ supported_params           │ 可选 │ 支持参数白名单                                                 │
  │ unsupported_params         │ 可选 │ 不支持参数黑名单                                               │
  │ param_constraints          │ 可选 │ 参数取值约束                                                   │
  │ feature_constraints        │ 可选 │ feature 约束                                                   │
  │ combination_constraints    │ 可选 │ 参数组合约束                                                   │
  │ request_param_policy       │ 可选 │ 参数兼容策略                                                   │
  │ compatibility_mode         │ 可选 │ OpenAI client 默认参数兼容行为                                 │
  │ field_disposition          │ 可选 │ 字段 preserve/drop/reject/adapt 证据                           │
  └────────────────────────────┴──────┴────────────────────────────────────────────────────────────────┘

  ### 常见 provider 怎么填？

  #### OpenAI-compatible 基线

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
    "capability_profiles": ["openai.chat_completions.full"]
  }

  #### MiniMax OpenAI-compatible

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

  #### Kimi/Moonshot OpenAI-compatible

  Kimi 的特殊点不是协议不同，而是参数兼容策略不同，比如固定采样参数：

  {
    "capability_profiles": [
      "openai.chat_completions.full"
    ],
    "request_param_policy": {
      "temperature": {
        "category": "sampling",
        "upstream_behavior": "fixed",
        "fixed_value": "1",
        "on_equal": "omit",
        "on_mismatch": "omit_with_warning"
      },
      "top_p": {
        "category": "sampling",
        "upstream_behavior": "fixed",
        "fixed_value": "0.95",
        "on_equal": "omit",
        "on_mismatch": "omit_with_warning"
      }
    }
  }

  但正常情况下，前端不应该让人手写这些，应该由 control-server compiler 根据 provider_profile=moonshot_openai 自动生成。对于通用 OpenAI-compatible 上游，publisher 也内置了 provider_profile=openai_compatible + client_compatibility 的轻量编译路径，可生成 Claude Code / Codex / Gemini CLI / Cursor / Cline / CherryStudio / Dify 等常用入口所需的显式 protocol_bindings。

  ### 前端应该怎么配置 Provider？

  页面只让用户填：

  ┌──────────────────┬────────────────────────────────────────────────────────────────┐
  │ 前端字段         │ 后端 compiler 生成                                             │
  ├──────────────────┼────────────────────────────────────────────────────────────────┤
  │ provider code    │ ProviderInfo.code                                              │
  │ display name     │ ProviderInfo.name                                              │
  │ base_url         │ ProviderInfo.base_url                                          │
  │ status           │ ProviderInfo.status                                            │
  │ provider_profile │ protocol_bindings / capability_profiles / request_param_policy │
  │ endpoint_variant │ path/auth/translator 差异                                      │
  │ exposures        │ accepts[]                                                      │
  └──────────────────┴────────────────────────────────────────────────────────────────┘

  不要让前端直接暴露完整 protocol_bindings，否则复杂度太高。

  ———

  ## DELETE /admin/api/v1/providers/{provider_id}

  删除 provider。

  什么时候用：

  - provider 永久退役
  - 不再保留历史配置

  如果只是临时停用，应该用：

  {
    "status": "disabled"
  }

  不要直接 delete，否则 route 还引用它时 gateway 会出现无可用 provider。

  ———

  # 4.2 Model Catalog：gw:model_catalog

  ## PUT /admin/api/v1/models/{model_id}/catalog

  写模型目录。

  ### Model Catalog 是什么？

  它描述模型 “是什么”，不描述走哪里。

  例如：

  - gpt-4o 是 chat 模型
  - 支持 tools
  - 支持 vision
  - context window 是 128k
  - max output 是 16k
  - owned_by 是 openai

  ### 字段

  ┌──────────────────────┬──────┬───────────────────────────────────────────────────────────────┐
  │ 字段                 │ 必填 │ 怎么填                                                        │
  ├──────────────────────┼──────┼───────────────────────────────────────────────────────────────┤
  │ model_id             │ 可选 │ path 是权威；填了必须等于 path                                │
  │ display_name         │ 可选 │ 展示名                                                        │
  │ capabilities         │ 可选 │ chat / tools / streaming / vision / json_mode / embeddings 等 │
  │ context_window       │ 可选 │ 上下文窗口 token 数                                           │
  │ max_output           │ 可选 │ 最大输出 token 数                                             │
  │ request_param_policy │ 可选 │ 模型级参数兼容策略                                            │
  │ owned_by             │ 可选 │ 原厂，如 openai / minimax / moonshot                          │
  └──────────────────────┴──────┴───────────────────────────────────────────────────────────────┘

  示例：

  {
    "model_id": "minimax-m2.5",
    "display_name": "MiniMax M2.5",
    "capabilities": ["chat", "tools", "streaming"],
    "context_window": 200000,
    "max_output": 8192,
    "owned_by": "minimax"
  }

  怎么填：

  - 如果是聊天模型：至少 capabilities=["chat"]
  - 支持工具：加 tools
  - 支持流式：加 streaming
  - 支持图片：加 vision
  - owned_by 建议填原厂，不填 gateway 可能 fallback 到 provider_id

  ———

  ## POST /admin/api/v1/models/catalog/bulk

  批量写模型目录。

  适合：

  - 一次上架多个模型
  - 全量同步
  - 环境初始化

  Body：

  {
    "items": [
      {
        "model_id": "minimax-m2.5",
        "entry": {
          "display_name": "MiniMax M2.5",
          "capabilities": ["chat", "tools"],
          "context_window": 200000,
          "max_output": 8192,
          "owned_by": "minimax"
        }
      }
    ]
  }

  ———

  ## DELETE /admin/api/v1/models/{model_id}/catalog

  删除模型目录。

  注意：通常要同时删除 route，否则 gateway 可能有 route 但无 catalog，行为不完整。

  ———

  # 4.3 Model Route：gw:routes

  ## PUT /admin/api/v1/models/{model_id}/route

  写模型路由。

  ### Route 是什么？

  它描述 “这个 logical model 走哪个 provider，转成哪个 upstream model”。

  例如：

  用户请求：

  {
    "model": "minimax-m2.5"
  }

  gateway 查 route 后知道：

  - provider 是 minimax
  - 上游模型名是 MiniMax-M2.5
  - provider strategy 是 priority
  - keypool 怎么选
  - 是否有 fallback model

  ### 字段

  ┌───────────────────┬──────┬──────────────────────────────────────────┐
  │ 字段              │ 必填 │ 含义                                     │
  ├───────────────────┼──────┼──────────────────────────────────────────┤
  │ model_id          │ 可选 │ path 是权威                              │
  │ alias_target      │ 可选 │ 等价模型别名，不是 fallback              │
  │ providers         │ 必填 │ 路由候选 provider 数组                   │
  │ fallback_model    │ 可选 │ 跨模型 fallback 下一跳                   │
  │ provider_strategy │ 可选 │ priority / weight / latency / least_busy │
  │ default_policy    │ 可选 │ 路由默认策略                             │
  └───────────────────┴──────┴──────────────────────────────────────────┘

  ### providers[] / RouteEntry 字段

  ┌──────────────────────┬──────┬────────────────────────────────────────────┐
  │ 字段                 │ 必填 │ 怎么填                                     │
  ├──────────────────────┼──────┼────────────────────────────────────────────┤
  │ provider_id          │   是 │ 必须引用已注册 provider                    │
  │ upstream_model       │   是 │ 上游真实模型名                             │
  │ priority             │   是 │ 越小越优先                                 │
  │ weight               │ 可选 │ weight strategy 下的权重；0 会按 1 处理    │
  │ status               │   是 │ active / disabled                          │
  │ api_version          │ 可选 │ Azure/Gemini 等需要版本时填                │
  │ anthropic_beta       │ 可选 │ Anthropic beta header 值                   │
  │ request_param_policy │ 可选 │ route 级参数兼容策略                       │
  │ compliance_tags      │ 可选 │ 合规标签                                   │
  │ compliance           │ 可选 │ region / zero data retention / no training │
  └──────────────────────┴──────┴────────────────────────────────────────────┘

  示例：

  {
    "model_id": "minimax-m2.5",
    "provider_strategy": "priority",
    "providers": [
      {
        "provider_id": "minimax",
        "upstream_model": "MiniMax-M2.5",
        "priority": 100,
        "weight": 1,
        "status": "active"
      }
    ]
  }

  ### 什么时候用 alias_target？

  比如你想让：

  - kimi-k2.6-latest
  - 实际等价于 kimi-k2.6

  可以写：

  {
    "model_id": "kimi-k2.6-latest",
    "alias_target": "kimi-k2.6",
    "providers": []
  }

  但要确认 gateway 当前 alias 行为和校验要求。

  ### 什么时候用 fallback_model？

  比如主模型不可用时降级：

  {
    "model_id": "gpt-4o",
    "fallback_model": "gpt-4o-mini"
  }

  注意 fallback 是跨模型降级，会影响计费、观测、披露策略。

  ———

  ## POST /admin/api/v1/models/routes/bulk

  批量写 route。

  适合：

  - 一次同步所有模型路由
  - 灰度切换
  - DR 重建

  ———

  ## DELETE /admin/api/v1/models/{model_id}/route

  删除模型路由。

  删除后：

  - gateway 该模型不可路由
  - 客户端请求会类似 model_not_found / no route

  ———

  # 4.4 KeyPool：上游 key 池

  最终写：

  - gw:keypool:{provider_id}
  - gw:keypool_meta

  gateway 用它选择上游 provider key。

  ———

  ## PUT /admin/api/v1/key-pools/{provider_id}

  整池替换。

  ### 什么时候用？

  - 冷启动
  - 全量恢复
  - provider 大规模 key 轮换

  不建议日常用，因为整池替换会 DEL 后 HSET，短时间可能抖动。

  ### Body

  {
    "meta": {
      "provider_id": "minimax",
      "strategy": "priority"
    },
    "entries": [
      {
        "key_id": "uuid-v4",
        "alias": "minimax-key-1",
        "ciphertext": "encrypted-or-dev-plaintext",
        "priority": 100,
        "weight": 1,
        "rpm_limit": 1000,
        "tpm_limit": 1000000,
        "status": "enabled"
      }
    ]
  }

  ### KeyPoolEntry 字段

  ┌────────────────┬──────┬────────────────────────────────────────────────┐
  │ 字段           │ 必填 │ 含义                                           │
  ├────────────────┼──────┼────────────────────────────────────────────────┤
  │ key_id         │   是 │ 上游 key ID，推荐 UUID                         │
  │ alias          │ 可选 │ 展示/排查用                                    │
  │ allowed_models │ 可选 │ 限制这把上游 key 只能跑哪些 logical model      │
  │ ciphertext     │   是 │ 上游 key 密文；本地 dev 可明文，但生产必须密文 │
  │ priority       │   是 │ 越小越优先                                     │
  │ weight         │   是 │ 权重，0 不是禁用                               │
  │ rpm_limit      │ 可选 │ 上游 key 每分钟请求上限                        │
  │ tpm_limit      │ 可选 │ 上游 key 每分钟 token 上限                     │
  │ status         │   是 │ enabled / cooling / disabled 等                │
  └────────────────┴──────┴────────────────────────────────────────────────┘

  注意：

  - status=enabled 才可用。
  - ciphertext 如果无法被 gateway 解密，会导致 “empty plaintext” 或 upstream dispatch 失败。
  - 本地验证时 gateway 要允许 plaintext，否则 keypool 看起来有 key 但转发不了。

  ———

  ## POST /admin/api/v1/key-pools/bulk

  批量整池替换。

  ———

  ## DELETE /admin/api/v1/key-pools/{provider_id}

  删除 provider 的整个 keypool。

  ———

  ## PUT /admin/api/v1/key-pools/{provider_id}/keys/{key_id}

  日常推荐接口：单条 upstream key 增/改。

  适合：

  - 新加一把 provider key
  - 修改 rpm/tpm
  - 修改 priority/weight
  - cooling/disabled 一把 key

  Body 只传 KeyPoolEntry，provider_id/key_id 以 path 为准。

  ———

  ## POST /admin/api/v1/key-pools/keys/bulk

  批量增改 keypool entry。

  ———

  ## DELETE /admin/api/v1/key-pools/{provider_id}/keys/{key_id}

  删除单把上游 key。

  ———

  ## DELETE /admin/api/v1/key-pools/keys/bulk

  批量删除上游 key。

  ———

  # 4.5 Platform Config：gw:platform:config

  ## PUT /admin/api/v1/platform-config

  写全局默认配置。

  gateway 用它做：

  - 平台级 RPM/并发限制
  - 默认 provider strategy
  - 默认 keypool strategy
  - 默认超时
  - fallback 深度
  - 请求体大小限制
  - 披露策略
  - retry budget
  - auth health 等

  常见字段：

  ┌───────────────────────────┬──────────────────────┬───────────────────────────────────┐
  │ 字段                      │ 怎么填               │ 含义                              │
  ├───────────────────────────┼──────────────────────┼───────────────────────────────────┤
  │ platform_rpm              │ int                  │ 单 gateway 节点平台 RPM 上限      │
  │ platform_cc               │ int                  │ 单 gateway 节点平台并发上限       │
  │ default_provider_strategy │ priority / weight 等 │ 模型 route 没指定时用             │
  │ default_keypool_strategy  │ priority / weight    │ keypool 默认选择策略              │
  │ default_timeout_ms        │ 如 60000             │ 默认请求超时                      │
  │ stream_timeout_ms         │ 如 300000            │ 流式请求超时                      │
  │ first_token_timeout_ms    │ 如 30000             │ 首 token 超时                     │
  │ attempt_timeout_ms        │ int                  │ 单次 attempt 超时                 │
  │ max_body_size             │ int                  │ 请求体最大字节数                  │
  │ max_fallback_depth        │ int                  │ fallback 最大深度                 │
  │ disclosure_policy         │ object               │ 返回中是否暴露真实 provider/model │
  │ retry_budget              │ object               │ 单请求 retry 限额                 │
  │ default_route_policy      │ object               │ 默认路由策略                      │
  └───────────────────────────┴──────────────────────┴───────────────────────────────────┘

  ### retry_budget 特别注意

  当前 publisher 校验：三轴 all-or-nothing。

  要么全 0：

  {
    "retry_budget": {
      "max_keys_per_provider": 0,
      "max_providers": 0,
      "total_wall_time_ms": 0
    }
  }

  表示 gateway 用内置默认。

  要么三个都填：

  {
    "retry_budget": {
      "max_keys_per_provider": 2,
      "max_providers": 3,
      "total_wall_time_ms": 5000
    }
  }

  不要只填一个，否则会被 publisher 拒绝或造成语义误解。

  ———

  ## 5. SRE Namespace：应急控制

  路径：/sre/api/v1/*
  调用方：sre-oncall。

  这些接口不是常规配置页面主流程，但生产必须有。

  ———

  ## 5.1 Key 禁用/启用

  ┌──────────────────────────────────────────┬────────────────────┐
  │ 接口                                     │ 作用               │
  ├──────────────────────────────────────────┼────────────────────┤
  │ POST /sre/api/v1/keys/{key_hash}/disable │ 应急禁用客户端 key │
  │ POST /sre/api/v1/keys/{key_hash}/enable  │ 恢复 key           │
  │ POST /sre/api/v1/keys/bulk/disable       │ 批量禁用           │
  │ POST /sre/api/v1/keys/bulk/enable        │ 批量恢复           │
  └──────────────────────────────────────────┴────────────────────┘

  最终影响：

  - gw:keymeta status
  - 或 revoked/security 相关数据

  ———

  ## 5.2 IP 黑名单

  ┌─────────────────────────────────────┬────────────────┐
  │ 接口                                │ 作用           │
  ├─────────────────────────────────────┼────────────────┤
  │ POST /sre/api/v1/blocked-ips        │ 加单个 IP/CIDR │
  │ POST /sre/api/v1/blocked-ips/bulk   │ 批量加         │
  │ DELETE /sre/api/v1/blocked-ips/{ip} │ 删除单个       │
  │ DELETE /sre/api/v1/blocked-ips/bulk │ 批量删除       │
  │ PUT /sre/api/v1/blocked-ips         │ 全量替换       │
  └─────────────────────────────────────┴────────────────┘

  最终写：

  - gw:ip_blocklist

  gateway 用于 pre-auth 或 auth 期间拒绝来源 IP。

  ———

  ## 5.3 Provider Override

  ┌───────────────────────────────────┬─────────────────┐
  │ 接口                              │ 作用            │
  ├───────────────────────────────────┼─────────────────┤
  │ PUT /sre/api/v1/overrides         │ 写单条 override │
  │ POST /sre/api/v1/overrides/bulk   │ 批量写          │
  │ DELETE /sre/api/v1/overrides      │ 删除单条        │
  │ DELETE /sre/api/v1/overrides/bulk │ 批量删除        │
  └───────────────────────────────────┴─────────────────┘

  用途：

  - 临时禁用 provider
  - 临时禁用 provider account
  - 临时切流
  - 某 provider 预算耗尽时自动 override

  ———

  ## 5.4 Replay

  ┌─────────────────────────┬────────────────────────────┐
  │ 接口                    │ 作用                       │
  ├─────────────────────────┼────────────────────────────┤
  │ POST /sre/api/v1/replay │ 重放 outbox / 修复失败投递 │
  └─────────────────────────┴────────────────────────────┘

  当前文档标注暂不开放或受限使用，主要给 SRE/运维恢复数据一致性。

  ———

  # 6. 从 “页面配置” 到 “gateway 可用” 的完整链路

  如果你在 front tmpconfig1 配一个 MiniMax 模型，理想链路是：

  ## Step 1：Provider 配置

  页面填写：

  provider_code = minimax
  display_name = MiniMax
  base_url = https://api.minimax.io
  api_style = openai
  provider_profile = minimax_openai
  endpoint_variant = standard
  exposures = openai.chat
  status = active

  control-server compiler 生成 publisher provider body：

  {
    "code": "minimax",
    "name": "MiniMax",
    "base_url": "https://api.minimax.io",
    "status": "active",
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

  publisher 写：

  gw:providers

  ———

  ## Step 2：Model Catalog 配置

  页面填写：

  model_name = minimax-m2.5
  display_name = MiniMax M2.5
  category = chat
  capabilities = chat, tools, streaming
  context_window = 200000
  max_output_tokens = 8192
  owned_by = minimax

  publisher 写：

  gw:model_catalog[minimax-m2.5]

  ———

  ## Step 3：Route 配置

  页面填写：

  logical model = minimax-m2.5
  provider = minimax
  provider account = default
  upstream_model_name = MiniMax-M2.5
  priority = 100
  weight = 1
  status = active

  publisher 写：

  gw:routes[minimax-m2.5]

  gateway 得到：

  {
    "model_id": "minimax-m2.5",
    "providers": [
      {
        "provider_id": "minimax-provider-id",
        "upstream_model": "MiniMax-M2.5",
        "priority": 100,
        "weight": 1,
        "status": "active"
      }
    ],
    "provider_strategy": "priority"
  }

  ———

  ## Step 4：Provider KeyPool 配置

  页面填写：

  account = default
  credential plaintext = sk-xxx
  rpm_limit = 1000
  tpm_limit = 1000000
  concurrency = 100
  priority = 100
  weight = 1
  status = enabled

  control-server 负责加密或本地 plaintext dev 处理，然后 publisher 写：

  gw:keypool:{provider_id}
  gw:keypool_meta

  ———

  ## Step 5：Platform Config

  页面或默认值：

  {
    "platform_rpm": 10000,
    "platform_cc": 500,
    "default_provider_strategy": "priority",
    "default_keypool_strategy": "priority",
    "default_timeout_ms": 60000,
    "stream_timeout_ms": 300000,
    "first_token_timeout_ms": 30000,
    "max_body_size": 10485760
  }

  publisher 写：

  gw:platform:config

  ———

  ## Step 6：用户 API Key

  用户在 portal 创建 API key 后，control-server 编译：

  gw:keymeta
  gw:key_balance / allowance

  gateway 才能接受客户端请求。

  ———

  # 7. 最小可用配置清单

  一个模型要能被 gateway 正常调用，至少需要这些数据同时存在：

  ┌──────────────────────┬──────────────────────────────────┬─────────────────────────────────────────────────┐
  │ 数据                 │ Redis                            │ 来源接口                                        │
  ├──────────────────────┼──────────────────────────────────┼─────────────────────────────────────────────────┤
  │ 客户端 key           │ gw:keymeta                       │ /user/api/v1/keys                               │
  │ key 未被吊销         │ gw:revoked_keys 不包含它         │ /user/api/v1/revoked-keys                       │
  │ key 有余额/allowance │ gw:key_balance / allowance flags │ /billing/api/v1/key-balance/* 或 control-server │
  │ provider             │ gw:providers                     │ /admin/api/v1/providers/{id}                    │
  │ provider key         │ gw:keypool:{provider_id}         │ /admin/api/v1/key-pools/*                       │
  │ model catalog        │ gw:model_catalog                 │ /admin/api/v1/models/{id}/catalog               │
  │ model route          │ gw:routes                        │ /admin/api/v1/models/{id}/route                 │
  │ platform defaults    │ gw:platform:config               │ /admin/api/v1/platform-config                   │
  └──────────────────────┴──────────────────────────────────┴─────────────────────────────────────────────────┘

  缺任何一个，gateway 都可能失败：

  - 缺 keymeta：401
  - key 被 revoked：401/403
  - 缺 balance/allowance：402/403
  - 缺 provider：503/no provider
  - 缺 keypool：503/no upstream key
  - 缺 catalog/route：model_not_found
  - provider 还是旧 api_types：新 gateway 会拒绝该 provider
  - keypool ciphertext 解不开：upstream dispatch 失败

  ———

  # 8. 前端复杂度应该怎么降

  你们前面定的大方向是对的：front 不应该直接配 publisher/gateway 的完整 wire schema。

  前端只配 “运营意图”：

  ┌─────────────────────┬────────────────────────────┐
  │ model name          │ capability_profiles 细节   │
  │ capabilities 勾选   │ param constraints 原始结构 │
  │ upstream model name │ field_disposition          │
  │ credential/rpm/tpm  │ combination constraints    │
  │ priority/weight     │ auth scheme 细节           │
  └─────────────────────┴────────────────────────────┘

  server/compiler 负责把意图展开成 publisher 接口需要的严格 schema。

  也就是：
  这个方向是正确的。front 页面越接近 “运营配置”，越不容易出错；publisher/gateway schema 越严格，越适合由 compiler 生成。
