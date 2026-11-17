-- +goose Up
CREATE TABLE kernel_inbox (
 input_id TEXT PRIMARY KEY,
 domain TEXT NOT NULL,
 client_key TEXT NOT NULL,
 digest TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 mode TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 accepted_at INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL,
 run_id TEXT NOT NULL,
 UNIQUE(domain,client_key),
 UNIQUE(domain,sequence)
);
CREATE INDEX ix_kernel_inbox_pending ON kernel_inbox(state,domain,sequence);
CREATE TABLE kernel_runs (
 run_id TEXT PRIMARY KEY,
 identity_json TEXT NOT NULL,
 state TEXT NOT NULL,
 outcome TEXT,
 revision INTEGER NOT NULL DEFAULT 1,
 drained INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE kernel_activities (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 state TEXT NOT NULL,
 error TEXT
);
CREATE INDEX ix_kernel_activity_run ON kernel_activities(run_id,state);
CREATE TABLE kernel_delivery_links (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 step_id TEXT NOT NULL,
 kind TEXT NOT NULL
);
CREATE TABLE kernel_material_inputs (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 run_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 text TEXT NOT NULL
);
ALTER TABLE steps ADD COLUMN identity_json TEXT;
ALTER TABLE steps ADD COLUMN provider_call_id TEXT;
ALTER TABLE steps ADD COLUMN ordinal INTEGER;

-- +goose Down
DROP TABLE kernel_material_inputs;
ALTER TABLE steps DROP COLUMN ordinal;
ALTER TABLE steps DROP COLUMN provider_call_id;
ALTER TABLE steps DROP COLUMN identity_json;
DROP TABLE kernel_delivery_links;
DROP TABLE kernel_activities;
DROP TABLE kernel_runs;
DROP TABLE kernel_inbox;
