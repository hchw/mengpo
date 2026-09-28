CREATE INDEX IF NOT EXISTS memory_nodes_user_scope_status_idx
    ON memory_nodes (user_id, scope_type, scope_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS memory_nodes_type_status_confidence_idx
    ON memory_nodes (memory_type, status, confidence DESC, updated_at DESC);

CREATE INDEX IF NOT EXISTS memory_nodes_content_fts_idx
    ON memory_nodes USING gin (to_tsvector('simple', coalesce(content_text, '')));
