-- 参考信息块按档存库（ADR-368、规格-提示词前缀稳定第七节）：渠道回合交回时把发给模型的参考块原字节写进这里，
-- 重建实例时按（角色、会话、档、窗口）取出、插回 before_seq 之前。一块一行，同一个主键重交就覆盖（Commit 可以重做）。
-- +goose Up
CREATE TABLE ref_blocks (
  character_id TEXT NOT NULL,
  conversation TEXT NOT NULL,
  scope_key TEXT NOT NULL,
  before_seq INTEGER NOT NULL,
  ordinal INTEGER NOT NULL,
  card_hash TEXT NOT NULL,
  memory_refs_json TEXT NOT NULL DEFAULT '[]',
  text TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (character_id, conversation, scope_key, before_seq, ordinal)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE ref_blocks;
