-- Request ids are client-supplied and only meaningful inside a tenant/user/
-- conversation scope. The old global UNIQUE(request_id, role) allowed a
-- caller who knew another request id to read that answer by asking for the
-- same id, and made identical ids collide across tenants.
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_request_id_role_key;
CREATE UNIQUE INDEX IF NOT EXISTS messages_request_scope_idx
    ON messages(tenant_id, user_id, request_id, role);

-- ReplayEvents already verifies the request belongs to the caller before
-- reading stream_events; keep the uniqueness scope aligned with that check.
ALTER TABLE stream_events DROP CONSTRAINT IF EXISTS stream_events_request_id_sequence_key;
CREATE UNIQUE INDEX IF NOT EXISTS stream_events_request_scope_idx
    ON stream_events(tenant_id, request_id, sequence);