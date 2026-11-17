-- QQ 平台的提醒记录（规格-Go后台装配 第七节）：试运行统计要把掉线的时段排除掉，kind 是 down / up / kicked / mismatch / send-failed，
-- 和 Bun 版 daemon/plugins/qq-health.ts 记的一样。
-- 表结构从 migrations/events/0001_baseline.sql 原样抄来，导入时不用转换；版本记在 goose_db_version_qq。

-- +goose Up
CREATE TABLE qq_alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, account TEXT NOT NULL, text TEXT NOT NULL, detail TEXT);
