-- Stream event replay must be scoped to the same tenant/user/request tuple as
-- messages. Without user_id, two users in one tenant that supplied the same
-- request id could collide on (tenant_id, request_id, sequence) and a replay
-- could read the other user's events.
ALTER TABLE stream_events ADD COLUMN IF NOT EXISTS user_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE stream_events se
SET user_id = m.user_id
FROM messages m
WHERE se.user_id = ''
  AND m.tenant_id = se.tenant_id
  AND m.request_id = se.request_id
  AND m.role = 'user';

DROP INDEX IF EXISTS stream_events_request_scope_idx;
CREATE UNIQUE INDEX IF NOT EXISTS stream_events_request_scope_idx
    ON stream_events(tenant_id, user_id, request_id, sequence);