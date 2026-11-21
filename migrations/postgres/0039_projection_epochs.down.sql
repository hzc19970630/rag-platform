-- Revert B35 projection epochs and wiki generations. Published pages and the
-- knowledge index are untouched, so readers keep the current per-page view.

ALTER TABLE graph_snapshots DROP COLUMN IF EXISTS target_epoch;
ALTER TABLE projection_runs DROP COLUMN IF EXISTS target_epoch;
ALTER TABLE projection_runs DROP COLUMN IF EXISTS base_epoch;

ALTER TABLE knowledge_projection_states
    DROP COLUMN IF EXISTS invalidated_at,
    DROP COLUMN IF EXISTS last_change_at,
    DROP COLUMN IF EXISTS active_wiki_generation_id,
    DROP COLUMN IF EXISTS relation_applied_epoch,
    DROP COLUMN IF EXISTS wiki_applied_epoch,
    DROP COLUMN IF EXISTS target_epoch;

DROP TABLE IF EXISTS wiki_generation_pages;
DROP TABLE IF EXISTS wiki_generations;
DROP TABLE IF EXISTS projection_changes;
