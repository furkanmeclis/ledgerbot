-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE drafts (
    id          text PRIMARY KEY,
    company_id  text        NOT NULL,
    actor_id    text        NOT NULL,
    capability  text        NOT NULL,
    status      text        NOT NULL,
    payload     jsonb       NOT NULL,
    summary     text        NOT NULL DEFAULT '',
    external_id text        NOT NULL DEFAULT '',
    channel     text        NOT NULL,
    sender      text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX drafts_company_created ON drafts (company_id, created_at DESC);
CREATE INDEX drafts_pending_sender ON drafts (company_id, channel, sender, created_at DESC) WHERE status = 'pending';
CREATE INDEX drafts_sender ON drafts (sender);

CREATE TABLE fingerprints (
    company_id  text        NOT NULL,
    fp          text        NOT NULL,
    draft_id    text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, fp)
);

CREATE TABLE ai_usage (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id     text             NOT NULL,
    purpose        text             NOT NULL,
    provider       text             NOT NULL,
    model          text             NOT NULL,
    input_tokens   integer          NOT NULL DEFAULT 0,
    output_tokens  integer          NOT NULL DEFAULT 0,
    audio_seconds  double precision NOT NULL DEFAULT 0,
    cost_micro_usd bigint           NOT NULL DEFAULT 0,
    at             timestamptz      NOT NULL DEFAULT now()
);
CREATE INDEX ai_usage_company_at ON ai_usage (company_id, at);

CREATE TABLE audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id  text        NOT NULL,
    actor_id    text        NOT NULL DEFAULT '',
    channel     text        NOT NULL DEFAULT '',
    action      text        NOT NULL,
    draft_id    text        NOT NULL DEFAULT '',
    detail      jsonb,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_company_id ON audit_log (company_id, id DESC);

CREATE TABLE deletion_requests (
    id           text PRIMARY KEY,
    company_id   text        NOT NULL DEFAULT '',
    channel      text        NOT NULL DEFAULT '',
    subject      text        NOT NULL,
    status       text        NOT NULL,
    code         text        NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    removed      bigint      NOT NULL DEFAULT 0
);

CREATE TABLE channel_connections (
    id          text PRIMARY KEY,
    company_id  text        NOT NULL DEFAULT '',
    kind        text        NOT NULL,
    mode        text        NOT NULL DEFAULT '',
    name        text        NOT NULL DEFAULT '',
    enabled     boolean     NOT NULL DEFAULT true,
    config      bytea,
    status      text        NOT NULL DEFAULT 'new',
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX channel_connections_company ON channel_connections (company_id);

-- Listener leases: one worker owns a polling connection (Telegram, IMAP) at a time.
CREATE TABLE channel_leases (
    connection_id text PRIMARY KEY REFERENCES channel_connections (id) ON DELETE CASCADE,
    owner         text        NOT NULL,
    expires_at    timestamptz NOT NULL
);

CREATE TABLE idempotency_keys (
    company_id  text        NOT NULL,
    key         text        NOT NULL,
    status      integer     NOT NULL,
    body        bytea       NOT NULL,
    hash        text        NOT NULL,
    expires_at  timestamptz NOT NULL,
    PRIMARY KEY (company_id, key)
);
CREATE INDEX idempotency_expires ON idempotency_keys (expires_at);

CREATE TABLE throttles (
    company_id  text        NOT NULL,
    key         text        NOT NULL,
    until       timestamptz NOT NULL,
    PRIMARY KEY (company_id, key)
);

-- Short-lived media blobs referenced by queued jobs (keeps job rows small).
CREATE TABLE blobs (
    id          text PRIMARY KEY,
    data        bytea       NOT NULL,
    expires_at  timestamptz NOT NULL
);
CREATE INDEX blobs_expires ON blobs (expires_at);

-- Dev channel outbox shared by all replicas (demo dashboard reads it).
CREATE TABLE dev_outbox (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id  text        NOT NULL,
    recipient   text        NOT NULL,
    text        text        NOT NULL,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dev_outbox_recipient ON dev_outbox (company_id, recipient, id DESC);

-- Fake data provider ledger (development / demo only).
CREATE TABLE fake_commits (draft_id text PRIMARY KEY);
CREATE TABLE fake_ledger (
    draft_id    text        NOT NULL,
    company_id  text        NOT NULL,
    kind        text        NOT NULL,
    customer_id text        NOT NULL DEFAULT '',
    product_id  text        NOT NULL DEFAULT '',
    amount      bigint      NOT NULL DEFAULT 0,
    quantity    bigint      NOT NULL DEFAULT 0,
    at          timestamptz NOT NULL
);
CREATE INDEX fake_ledger_company ON fake_ledger (company_id, at);

-- RAG: per-company chunks. Dimension-specific HNSW indexes are created by the
-- RAG module for the configured embedding model.
CREATE TABLE rag_chunks (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id  text        NOT NULL,
    source      text        NOT NULL, -- document, record, note
    ref         text        NOT NULL DEFAULT '',
    content     text        NOT NULL,
    embedding   vector,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rag_chunks_company ON rag_chunks (company_id);

-- +goose Down
DROP TABLE rag_chunks, fake_ledger, fake_commits, dev_outbox, blobs, throttles, idempotency_keys, channel_leases,
    channel_connections, deletion_requests, audit_log, ai_usage, fingerprints, drafts;
