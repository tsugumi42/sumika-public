-- 关联按会话选平台库；快照不随共享缓存刷新而改变。
-- +goose Up
CREATE TABLE message_links (
  conversation TEXT NOT NULL,
  message_seq INTEGER NOT NULL CHECK (message_seq > 0),
  clean_url TEXT NOT NULL,
  previous_count INTEGER NOT NULL,
  result_json BLOB,
  PRIMARY KEY (conversation, message_seq, clean_url)
);
CREATE INDEX ix_message_links_count ON message_links(conversation, clean_url);

-- +goose Down
DROP TABLE message_links;
