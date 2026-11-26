-- 2026-05-21: per-request unique id propagated end-to-end (HTTP → DB → audit).
-- Publisher middleware mints a UUID v7 on every authenticated request,
-- stamps it on every outbox row written by the handler, returns it in the
-- X-Request-Id response header. Caller services correlate their own logs
-- against this id; SRE replay / dead-letter inspection groups all rows of
-- one HTTP request even when the same caller_service + caller_trace_id +
-- dedup_key are reused (different request, same client correlation id).
--
-- Index supports the most common query: "show me every outbox row created
-- by request_id=X" — used by audit & on-call triage paths.
ALTER TABLE publisher_outbox
    ADD COLUMN caller_request_id TEXT;

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_request_id
    ON publisher_outbox (caller_request_id)
    WHERE caller_request_id IS NOT NULL;
