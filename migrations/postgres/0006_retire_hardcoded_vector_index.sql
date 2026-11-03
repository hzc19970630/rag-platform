-- The vector dimension is a deployment contract (EMBEDDING_DIMENSION) and an
-- HNSW index cannot be re-dimensioned in place. The halfvec(3) chunks index
-- created in 0004 was a local acceptance shortcut; the production index is now
-- owned by the managed `embedctl create-index` command, which builds halfvec(N)
-- for the configured dimension before retrieval traffic is enabled.
--
-- platform_index_probe_vector_idx (0001/0002) is intentionally kept: it is the
-- frozen M0 database-stack compatibility probe, not a retrieval index.
DROP INDEX IF EXISTS chunks_vector_idx;