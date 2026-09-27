UPDATE memory_nodes SET embedding_status = 'stale';
DROP INDEX IF EXISTS memory_nodes_embedding_hnsw_idx;
ALTER TABLE memory_nodes DROP COLUMN IF EXISTS embedding;
