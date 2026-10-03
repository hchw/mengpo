-- Resize embeddings for the Chinese/multilingual model (bge-small-zh-v1.5,
-- 512 dimensions). Existing 384-dimensional vectors are incompatible, so they
-- are dropped and marked stale; the worker's RebuildForModel pass re-embeds
-- every node that has content under the new model identity.
UPDATE memory_nodes SET embedding_status = 'stale';
DROP INDEX IF EXISTS memory_nodes_embedding_hnsw_idx;
ALTER TABLE memory_nodes DROP COLUMN IF EXISTS embedding;
ALTER TABLE memory_nodes ADD COLUMN embedding vector(512);
CREATE INDEX IF NOT EXISTS memory_nodes_embedding_hnsw_idx
	ON memory_nodes USING hnsw (embedding vector_cosine_ops)
	WHERE embedding IS NOT NULL;
