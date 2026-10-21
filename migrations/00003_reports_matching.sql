-- +goose Up
ALTER TABLE fake_ledger ADD COLUMN vat bigint NOT NULL DEFAULT 0;
ALTER TABLE fake_ledger ADD COLUMN ref text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE fake_ledger DROP COLUMN ref;
ALTER TABLE fake_ledger DROP COLUMN vat;
