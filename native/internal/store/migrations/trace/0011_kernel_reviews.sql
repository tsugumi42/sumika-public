-- +goose Up
CREATE TABLE kernel_reviews (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 request_json TEXT NOT NULL,
 digest TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL,
 verdict_json TEXT,
 error TEXT NOT NULL DEFAULT '',
 delivered INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL
);
CREATE INDEX kernel_reviews_root ON kernel_reviews(root_id, kind, state);

-- +goose Down
DROP TABLE kernel_reviews;
