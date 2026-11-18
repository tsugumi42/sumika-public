-- 同一份结果只存一次：关联只记内容哈希，缓存刷新以后旧消息仍指向当时那份。
-- 0004 时写进 message_links.result_json 的旧行照旧读那一列，新行那一列留空。
-- +goose Up
CREATE TABLE link_snapshots (
  hash TEXT PRIMARY KEY,
  result_json BLOB NOT NULL
);
ALTER TABLE message_links ADD COLUMN snapshot TEXT;

-- +goose Down
ALTER TABLE message_links DROP COLUMN snapshot;
DROP TABLE link_snapshots;
