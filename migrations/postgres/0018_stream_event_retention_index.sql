-- Stream events are replay buffers, not history: the worker purges rows older
-- than STREAM_EVENT_RETENTION_DAYS (default 7) in id order. The request-scoped
-- unique index cannot serve that scan, so without this index the batched DELETE
-- degrades into a full table scan as replay traffic accumulates.
CREATE INDEX IF NOT EXISTS stream_events_created_at_idx
    ON stream_events(created_at, id);