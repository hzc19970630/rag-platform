-- The embedding model becomes part of the stored contract: a vector is only
-- comparable with vectors produced by the same model and dimension. Recording
-- the model name lets operators rebuild exactly the affected chunks instead of
-- reprocessing every knowledge base.
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS embedding_model VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE platform_index_probe ADD COLUMN IF NOT EXISTS embedding_model VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS chunks_embedding_model_idx ON chunks(embedding_model, embedding_dimension);

-- Existing M2 keeps its 3-dimensional acceptance index. A production index
-- cannot be created with a runtime environment variable inside a static SQL
-- migration. The deployment/rebuild command must create halfvec(N), where N
-- equals EMBEDDING_DIMENSION, before enabling that model for retrieval.
