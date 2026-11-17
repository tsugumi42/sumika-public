-- 回合的结局（规格-Go后台 8.2 表一 A9"进程被杀以后"）。只加表，不改 0001 里的任何一张旧表；只给 Go 自己的库用。
--
-- 回合一开始先记一行（先记再跑，照 OpenCode 的 Execution.Started），回合定了时写结局：done / stopped / error。
-- 只改还没写结局的那一行，所以一个回合只有一个结局。进程在回合定了之前被杀，这一行一直没有结局；
-- 下一个进程建 Hub 时收拾（SweepInterrupted）：结局记成 interrupted，那时还停在执行中的工具名记进 unknown_tools（JSON 数组），
-- 下一句话提醒她一次"这几个工具不知道结果"。

-- +goose Up
CREATE TABLE turns (
  id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  conversation TEXT NOT NULL,
  channel TEXT,
  started_at INTEGER NOT NULL,
  ended_at INTEGER,
  outcome TEXT,
  unknown_tools TEXT
);
CREATE INDEX turns_conversation ON turns(character_id, conversation, started_at);
