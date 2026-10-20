-- +goose Up
-- "her zamanki gibi": latest committed draft per actor.
CREATE INDEX drafts_actor_committed ON drafts (company_id, actor_id, created_at DESC) WHERE status = 'committed';
CREATE INDEX rag_chunks_company_source ON rag_chunks (company_id, source);

-- +goose Down
DROP INDEX IF EXISTS rag_chunks_company_source;
DROP INDEX IF EXISTS drafts_actor_committed;
