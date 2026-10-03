-- Revert to the English all-MiniLM-L6-v2 model's 384-dimensional embeddings.
UPDATE memory_nodes SET embedding_status = 'stale';
DROP INDEX IF EXISTS memory_nodes_embedding_hnsw_idx;
ALTER TABLE memory_nodes DROP COLUMN IF EXISTS embedding;
ALTER TABLE memory_nodes ADD COLUMN embedding vector(384);
CREATE INDEX IF NOT EXISTS memory_nodes_embedding_hnsw_idx
	ON memory_nodes USING hnsw (embedding vector_cosine_ops)
	WHERE embedding IS NOT NULL;
