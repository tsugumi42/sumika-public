-- +goose Up
CREATE TABLE result_roots (
 root_id TEXT PRIMARY KEY,
 bytes INTEGER NOT NULL DEFAULT 0,
 reserved INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE result_reservations (
 tool_id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES result_roots(root_id),
 bytes INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE result_refs (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES result_roots(root_id),
 owner_json TEXT NOT NULL,
 ref_json TEXT NOT NULL,
 tool_name TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 retained INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX result_refs_root ON result_refs(root_id);
CREATE TABLE work_roots (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 ordinal INTEGER NOT NULL DEFAULT 0,
 stamp TEXT NOT NULL,
 state_json TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json)
);
CREATE TABLE work_entries (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 ordinal INTEGER NOT NULL,
 run_id TEXT NOT NULL,
 payload BLOB NOT NULL,
 digest TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json,ordinal),
 FOREIGN KEY(root_id,owner_json) REFERENCES work_roots(root_id,owner_json)
);
CREATE TABLE work_snapshots (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 revision INTEGER NOT NULL,
 through_ordinal INTEGER NOT NULL,
 stamp TEXT NOT NULL,
 prefix_digest TEXT NOT NULL,
 state_json TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json,revision),
 FOREIGN KEY(root_id,owner_json) REFERENCES work_roots(root_id,owner_json)
);
ALTER TABLE steps ADD COLUMN result_ref TEXT;
ALTER TABLE steps ADD COLUMN step_id TEXT;

-- +goose Down
ALTER TABLE steps DROP COLUMN step_id;
ALTER TABLE steps DROP COLUMN result_ref;
DROP TABLE work_snapshots;
DROP TABLE work_entries;
DROP TABLE work_roots;
DROP TABLE result_refs;
DROP TABLE result_reservations;
DROP TABLE result_roots;
