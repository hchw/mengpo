-- Near-duplicate grouping can fall back to lexical (trigram) similarity when a
-- memory has no usable embedding. pg_trgm is installed in the shared public
-- schema; the operator class is resolved through the tenant search_path. The
-- GIN index keeps the fallback bounded as a tenant's memory tree grows.
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX IF NOT EXISTS memory_nodes_content_text_trgm_idx
    ON memory_nodes USING gin (content_text gin_trgm_ops);
