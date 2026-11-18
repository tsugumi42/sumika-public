-- +goose Up
-- 回合耗时时间线（规格-回合耗时时间线）：每一轮各环节花了多久。只追加，不存正文、参数、路径。
CREATE TABLE turn_spans (
 turn_id TEXT NOT NULL,
 seq INTEGER NOT NULL,
 kind TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '',
 started_at INTEGER NOT NULL,
 ms INTEGER NOT NULL,
 outcome TEXT NOT NULL DEFAULT 'ok',
 PRIMARY KEY (turn_id, seq)
) WITHOUT ROWID;
CREATE INDEX ix_turn_spans_at ON turn_spans(started_at);

-- +goose Down
DROP INDEX ix_turn_spans_at;
DROP TABLE turn_spans;
