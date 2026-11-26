# Provider protocol binding note (2026-06-03)

> 本文原来记录 legacy `api_types` / `EndpointApiType` 对照。当前 publisher 最新接口已经切到 **protocol_bindings-only**：`PUT /admin/api/v1/providers/{provider_id}` 请求体中出现 `api_type` 或 `api_types` 会直接 400。
>
> 新接口请以 `docs/publisher-api-reference.md` §6.1 / §10.6 为准。旧表只保留为迁移参考,不能作为新调用方字段来源。


  ┌───────────────────────────────────────────┬──────────────────┐
  │                  Pattern                  │ EndpointApiType  │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/chat/completions                 │ openai           │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/responses                        │ openai_responses │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/messages                         │ anthropic        │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/messages/count_tokens            │ anthropic        │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1beta/models/                      │ gemini           │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/embeddings                       │ openai           │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /v1/rerank                           │ openai           │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /api/paas/v4/chat/completions        │ glm              │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /api/coding/paas/v4/chat/completions │ glm_coding       │
  ├───────────────────────────────────────────┼──────────────────┤
  │ POST /api/paas/v4/embeddings              │ glm              │
  └───────────────────────────────────────────┴──────────────────┘