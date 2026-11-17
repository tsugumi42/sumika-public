-- 召回日志（规格-记忆-v2 第七节第 11 步）：每次检索记一行，考题和以后调参从这里取样。
-- DDL 照 Bun 的 daemon/plugins/memory.ts:234–237 原样抄，列名和类型都一样；索引也是。
-- 只给 Go 自己的库用：Bun 管着的真实库里这张表是 Bun 自己建的，Go 的迁移不碰它（store.Baseline() 只到版本 1）。
-- 导入历史时这张表整份搬（规格-从Bun导入历史 第八节），在这条迁移之前 stage 的 trace 库里还没有它。

-- +goose Up
CREATE TABLE recall_log (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL,
  conversation TEXT, source TEXT NOT NULL, query TEXT NOT NULL, candidates_json TEXT NOT NULL, chosen_json TEXT NOT NULL, ms INTEGER NOT NULL);
CREATE INDEX ix_recall_log_at ON recall_log (character_id, at);
