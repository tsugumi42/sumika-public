-- 本地平台（local）自己的表：只有本地话题用（规格-聊天记录按平台分库 第三节）。别的平台的库里没有它。
-- 从 migrations/events/0001_baseline.sql 原样抄来；版本记在 goose_db_version_local，和通用迁移的编号各记各的。
-- 以后本地平台加表加在这里。

-- +goose Up
CREATE TABLE local_topics (id TEXT PRIMARY KEY, character_id TEXT NOT NULL, title TEXT, title_by TEXT, pinned_at INTEGER, archived_at INTEGER, created_at INTEGER NOT NULL, forked_from TEXT, forked_seq INTEGER);
CREATE INDEX ix_local_topics_character ON local_topics(character_id);
