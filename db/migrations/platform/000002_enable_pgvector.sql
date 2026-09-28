-- pgvector is installed once in public; tenant schemas reference the shared type.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
