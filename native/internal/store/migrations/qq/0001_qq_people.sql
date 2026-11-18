-- QQ 平台自己的表（规格-聊天记录按平台分库 第三节）：用到哪张建哪张，第一批只建 qq_people（记忆读它的名字）。
-- 补录进度 qq_sync、表情包 qq_stickers 等到那两步再加；qq_msgs 不建（Go 里已经由 channel_msgs 取代，导入时转换）。
-- 表结构从 migrations/events/0001_baseline.sql 原样抄来，导入时不用转换；版本记在 goose_db_version_qq。
-- 以后 QQ 平台加表加在这里。

-- +goose Up
CREATE TABLE qq_people (uid TEXT PRIMARY KEY, nickname TEXT, cards_json TEXT NOT NULL DEFAULT '{}', aliases_json TEXT NOT NULL DEFAULT '[]', last_seen INTEGER);
