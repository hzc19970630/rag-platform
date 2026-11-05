-- Task history retention (TASK_HISTORY_RETENTION_DAYS, default 90). The purge
-- scans terminal ingestion pipeline runs by age and resolved dead letters by
-- resolution time; without these indexes each hourly sweep degrades into a
-- sequential scan of the full history.
CREATE INDEX IF NOT EXISTS pipeline_runs_status_updated_idx ON pipeline_runs(status, updated_at);
CREATE INDEX IF NOT EXISTS task_dead_letters_status_resolved_idx ON task_dead_letters(status, resolved_at);
