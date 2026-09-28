ALTER TABLE memory_nodes
    ADD COLUMN IF NOT EXISTS embedding vector(384);

CREATE INDEX IF NOT EXISTS memory_nodes_embedding_hnsw_idx
    ON memory_nodes USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;
