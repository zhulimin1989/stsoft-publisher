-- 2026-05-22: actor_id attribution column.
--
-- caller_service / caller_trace_id / caller_request_id 已能定位是哪个服务、
-- 哪条 trace、哪次 HTTP 请求触发的写;但缺**哪个最终用户/管理员账户**这一
-- 个粒度 —— 审计场景常问"上礼拜哪个 admin 把 keypool 改成 X 的"。
--
-- 取值约定(暂用最小可行版):token 前 10 个字符。token 自身在 publisher
-- 端永远是 server-minted unguessable secret(见 cmd/publisher-token),前
-- 10 字符做 attribution 信号既能区分不同 actor,又不暴露 full token(forge
-- 攻击仍需 full token 才能通过 middleware 验证)。未来若 token claim 加
-- 真实 user_id,此列改为存 user_id 即可,不破 schema。
--
-- 索引:(actor_id, created_at DESC) — 支持"某用户最近发起的 batch 列表"
-- 这类典型审计查询。WHERE actor_id IS NOT NULL 让历史数据(actor_id NULL)
-- 不进 index。

ALTER TABLE publisher_outbox
    ADD COLUMN IF NOT EXISTS actor_id TEXT;

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_actor
    ON publisher_outbox (actor_id, created_at DESC)
    WHERE actor_id IS NOT NULL;
