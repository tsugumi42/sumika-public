-- 原话搜索候选索引；最终匹配仍由 events.text 的 LIKE 判断。
-- SQLite 的 length/substr 与 LIKE 一样在 NUL 处停止；lower 只折叠 ASCII。
-- +goose Up
CREATE VIRTUAL TABLE events_trigram USING fts5(text, content='events', content_rowid='seq', tokenize='trigram');
CREATE TABLE events_short (
  token TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  event_seq INTEGER NOT NULL,
  PRIMARY KEY(token, created_at DESC, event_seq DESC)
) WITHOUT ROWID;
CREATE INDEX ix_events_short_seq ON events_short(event_seq);
CREATE INDEX ix_events_time ON events(created_at DESC, seq DESC);

-- +goose StatementBegin
CREATE TRIGGER events_search_insert AFTER INSERT ON events BEGIN
  INSERT INTO events_trigram(rowid, text) VALUES (new.seq, new.text);
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE positions(n) AS (
    SELECT 1 WHERE length(new.text) > 0
    UNION ALL SELECT n + 1 FROM positions WHERE n < length(new.text)
  )
  SELECT lower(substr(new.text, n, 1)), new.created_at, new.seq FROM positions
  UNION ALL
  SELECT lower(substr(new.text, n, 2)), new.created_at, new.seq FROM positions WHERE n < length(new.text);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_search_delete AFTER DELETE ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) VALUES ('delete', old.seq, old.text);
  DELETE FROM events_short WHERE event_seq = old.seq;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_search_update AFTER UPDATE OF seq, text, created_at ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) VALUES ('delete', old.seq, old.text);
  DELETE FROM events_short WHERE event_seq = old.seq;
  INSERT INTO events_trigram(rowid, text) VALUES (new.seq, new.text);
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE positions(n) AS (
    SELECT 1 WHERE length(new.text) > 0
    UNION ALL SELECT n + 1 FROM positions WHERE n < length(new.text)
  )
  SELECT lower(substr(new.text, n, 1)), new.created_at, new.seq FROM positions
  UNION ALL
  SELECT lower(substr(new.text, n, 2)), new.created_at, new.seq FROM positions WHERE n < length(new.text);
END;
-- +goose StatementEnd

INSERT INTO events_trigram(events_trigram) VALUES ('rebuild');
INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
WITH RECURSIVE positions(event_seq, created_at, text, n) AS (
  SELECT seq, created_at, text, 1 FROM events WHERE length(text) > 0
  UNION ALL
  SELECT event_seq, created_at, text, n + 1 FROM positions WHERE n < length(text)
)
SELECT lower(substr(text, n, 1)), created_at, event_seq FROM positions
UNION ALL
SELECT lower(substr(text, n, 2)), created_at, event_seq FROM positions WHERE n < length(text);

-- +goose Down
DROP TRIGGER events_search_update;
DROP TRIGGER events_search_delete;
DROP TRIGGER events_search_insert;
DROP INDEX ix_events_time;
DROP INDEX ix_events_short_seq;
DROP TABLE events_short;
DROP TABLE events_trigram;
