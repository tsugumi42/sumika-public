-- QQ 平台的历史归档表（规格-从Bun导入历史 第四节）：分库规格里写"用到哪张建哪张"，完整导入历史时这些表要先把结构建出来，
-- 不然本次 Go 库缺历史内容。表结构从 migrations/events/0001_baseline.sql 原样抄来（那是 Bun 基线的镜像），不在这里加
-- 运行期补录、媒体下载或自检续跑的逻辑。版本记在 goose_db_version_qq。

-- +goose Up
CREATE TABLE qq_groups (gid TEXT PRIMARY KEY, name TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE qq_read (conversation TEXT PRIMARY KEY, read_at INTEGER NOT NULL);
CREATE TABLE qq_avatars (key TEXT PRIMARY KEY, hash TEXT, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_forwards (id TEXT PRIMARY KEY, tree_json TEXT NOT NULL, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_stickers (hash TEXT PRIMARY KEY, first_seen INTEGER NOT NULL);
CREATE TABLE qq_sync (account TEXT PRIMARY KEY, through INTEGER NOT NULL);
CREATE TABLE qq_selftest_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, account TEXT NOT NULL, peer TEXT NOT NULL, app TEXT, started_at INTEGER NOT NULL, ended_at INTEGER, log_json TEXT NOT NULL, sent_json TEXT NOT NULL);
