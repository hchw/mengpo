-- Candidate triage artifacts. A periodic LLM scan groups and conflicts recent
-- candidates and may abstract a new user-global candidate from a cross-session
-- common problem. The artifact is append-only and advisory: it records what the
-- model suggested so a later promotion can be explained against it.
CREATE TABLE IF NOT EXISTS candidate_triage (
	id              uuid PRIMARY KEY,
	memory_id       uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
	run_id          text NOT NULL,
	duplicate_group text NOT NULL DEFAULT '',
	conflict_ids    jsonb NOT NULL DEFAULT '[]'::jsonb,
	priority        smallint NOT NULL DEFAULT 0,
	rationale       text NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS candidate_triage_run_idx ON candidate_triage (run_id);
CREATE INDEX IF NOT EXISTS candidate_triage_memory_idx ON candidate_triage (memory_id);
