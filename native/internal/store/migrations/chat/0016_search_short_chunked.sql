-- 一两个字的搜索索引（events_short）改成切块生成：写一条长消息不再卡住写库。
-- 旧写法对每个位置做 substr(new.text, n, …)：SQLite 按字定位要从头数，长文本是平方级，
-- 8768 字约 0.3～0.5 秒、2 万字约 1.8 秒、10 万字约 51 秒（2026-11-11 实测，规格-网页界面和Electrobun 第十节）。
-- 新写法先把文本切成 64 字一块（每块多带下一块的第一个字，跨块的两字词不漏），块内照旧逐位取；
-- chunks 必须 MATERIALIZED，否则 SQLite 把它内联进连接，每个位置又从头切一次块。
-- 切块仍要从头数到块起点，总量是字数平方的 1/64：8768 字约 15～25 ms、2 万字约 0.1 秒、10 万字约 0.9 秒、20 万字约 2.9 秒（modernc 实测），日常消息够用。
-- 产出和旧写法逐条相同（同一批样本：空串、NUL、表情、块边界 63/64/65/128/129，store 的测试守着），所以已有的索引行不用重建。
-- 隐藏来源的过滤和 0015 一字不差。
-- +goose Up
DROP TRIGGER events_search_insert;
DROP TRIGGER events_search_update;

-- +goose StatementBegin
CREATE TRIGGER events_search_insert AFTER INSERT ON events WHEN COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet') BEGIN
  INSERT INTO events_trigram(rowid, text) VALUES (new.seq, new.text);
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE src(t, len) AS (SELECT new.text, length(new.text)),
    starts(s) AS (SELECT 1 FROM src WHERE len > 0 UNION ALL SELECT s + 64 FROM starts, src WHERE s + 64 <= len),
    chunks(c, k, cl) AS MATERIALIZED (SELECT substr(t, s, 65), min(64, len - s + 1), length(substr(t, s, 65)) FROM starts, src),
    pos(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM pos WHERE n < 64)
  SELECT lower(substr(c, n, 1)), new.created_at, new.seq FROM chunks, pos WHERE n <= k
  UNION ALL
  SELECT lower(substr(c, n, 2)), new.created_at, new.seq FROM chunks, pos WHERE n <= k AND n < cl;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_search_update AFTER UPDATE OF seq, text, created_at, origin ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) SELECT 'delete', old.seq, old.text WHERE COALESCE(old.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  DELETE FROM events_short WHERE event_seq = old.seq;
  INSERT INTO events_trigram(rowid, text) SELECT new.seq, new.text WHERE COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE src(t, len) AS (SELECT new.text, length(new.text) WHERE COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet')),
    starts(s) AS (SELECT 1 FROM src WHERE len > 0 UNION ALL SELECT s + 64 FROM starts, src WHERE s + 64 <= len),
    chunks(c, k, cl) AS MATERIALIZED (SELECT substr(t, s, 65), min(64, len - s + 1), length(substr(t, s, 65)) FROM starts, src),
    pos(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM pos WHERE n < 64)
  SELECT lower(substr(c, n, 1)), new.created_at, new.seq FROM chunks, pos WHERE n <= k
  UNION ALL
  SELECT lower(substr(c, n, 2)), new.created_at, new.seq FROM chunks, pos WHERE n <= k AND n < cl;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER events_search_update;
DROP TRIGGER events_search_insert;

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
