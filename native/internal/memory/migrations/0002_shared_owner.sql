-- +goose Up
ALTER TABLE facts ADD COLUMN only_her INTEGER NOT NULL DEFAULT 1;
ALTER TABLE facts ADD COLUMN learned_by TEXT;
CREATE TABLE shared_outbox (
 id TEXT PRIMARY KEY,
 payload TEXT NOT NULL
);

-- +goose Down
DROP TABLE shared_outbox;
ALTER TABLE facts DROP COLUMN learned_by;
ALTER TABLE facts DROP COLUMN only_her;
