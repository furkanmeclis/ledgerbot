-- +goose Up
-- Users reaching a central number or bot who belong to several companies.
CREATE TABLE active_companies (
    channel     text        NOT NULL,
    address     text        NOT NULL,
    company_id  text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, address)
);

-- +goose Down
DROP TABLE active_companies;
