-- The embedding binding is part of the knowledge base contract: a vector is
-- only comparable with vectors from the same model and dimension. The binding
-- is written at creation and cannot be changed once the knowledge base has
-- documents; switching the deployment model is a managed rebuild that updates
-- both the chunks and this binding.
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS embedding_model VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS embedding_dimension INTEGER NOT NULL DEFAULT 0;