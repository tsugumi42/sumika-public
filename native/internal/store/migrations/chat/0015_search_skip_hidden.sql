-- 定期任务那一轮的话（待分类、回了 [无事]）不进原话搜索索引。
-- 这两种来源在 history.NotSaidSQL 里：她没说出口、不进历史、不显示，搜索结果本来也会在读出后丢掉，
-- 但它们仍占着索引：每个定期任务每轮多出几行，搜"无事"这类词会先翻过一堆暗号。
-- 来源改变（RetagOrigin 把待分类改成可见或 [无事]）时也要同步：改成可见就补进索引，改成隐藏就摘出来。
-- 隐藏的来源名要和 history.OriginLoopPending、history.OriginLoopQuiet 一致（store 的测试守着）。
-- +goose Up
DROP TRIGGER events_search_insert;
DROP TRIGGER events_search_update;

-- +goose StatementBegin
CREATE TRIGGER events_search_insert AFTER INSERT ON events WHEN COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet') BEGIN
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

-- 摘旧的只在旧行确实进过索引时做（FTS5 的 delete 要给当初索引的原文，没进过的行不能 delete）；加新的只在新行可见时做
-- +goose StatementBegin
CREATE TRIGGER events_search_update AFTER UPDATE OF seq, text, created_at, origin ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) SELECT 'delete', old.seq, old.text WHERE COALESCE(old.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  DELETE FROM events_short WHERE event_seq = old.seq;
  INSERT INTO events_trigram(rowid, text) SELECT new.seq, new.text WHERE COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE positions(n) AS (
    SELECT 1 WHERE length(new.text) > 0 AND COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet')
    UNION ALL SELECT n + 1 FROM positions WHERE n < length(new.text)
  )
  SELECT lower(substr(new.text, n, 1)), new.created_at, new.seq FROM positions
  UNION ALL
  SELECT lower(substr(new.text, n, 2)), new.created_at, new.seq FROM positions WHERE n < length(new.text);
END;
-- +goose StatementEnd

-- 已经在索引里的隐藏行摘出来（行本身和它的原文都留在 events 里，运行记录不动）
INSERT INTO events_trigram(events_trigram, rowid, text) SELECT 'delete', seq, text FROM events WHERE COALESCE(origin, '') IN ('automation:loop-pending', 'automation:loop-quiet');
DELETE FROM events_short WHERE event_seq IN (SELECT seq FROM events WHERE COALESCE(origin, '') IN ('automation:loop-pending', 'automation:loop-quiet'));

-- +goose Down
DROP TRIGGER events_search_update;
DROP TRIGGER events_search_insert;

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
DELETE FROM events_short;
INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
WITH RECURSIVE positions(event_seq, created_at, text, n) AS (
  SELECT seq, created_at, text, 1 FROM events WHERE length(text) > 0
  UNION ALL
  SELECT event_seq, created_at, text, n + 1 FROM positions WHERE n < length(text)
)
SELECT lower(substr(text, n, 1)), created_at, event_seq FROM positions
UNION ALL
SELECT lower(substr(text, n, 2)), created_at, event_seq FROM positions WHERE n < length(text);
