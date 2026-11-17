-- +goose Up
CREATE TABLE link_results (
  clean_url TEXT PRIMARY KEY,
  fetched_at TEXT NOT NULL,
  result_json BLOB NOT NULL
);
CREATE TABLE link_aliases (
  clean_url TEXT PRIMARY KEY,
  target_url TEXT NOT NULL,
  resolved_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE link_aliases;
DROP TABLE link_results;
