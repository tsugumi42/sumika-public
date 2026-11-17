-- 角色库（characters/<id>/memory.db）的写入侧：规格-记忆-v2 13.2.1、13.2.12 的 W2。
-- 在 Bun 版建好的库上加列、加表，不改 Bun 认识的列；Bun 的迁移（migrateM1、migrateM2）看到多出来的东西不会去动。
-- 现在只在库的副本上跑（测试、对拍）；真库等切换日（第 5 步）备份以后再跑，见 Migrate 的注释。
-- 以后改表另写一个迁移文件，不改这个。撤回这一步：删掉这个文件和 migrate.go（副本直接扔掉重拷）。

-- +goose Up

-- 谁放行的：ai / owner（4.4）。整理只能取代、撤销 ai 的有效事实，其余（NULL 即导入的、owner、不认识的值）受保护（write.go 的 protectedFromAI）
ALTER TABLE facts ADD COLUMN confirmed_by TEXT;

-- 修订前后各是什么样（小 JSON：状态、范围、放行的人），回查"为什么变了"（13.2.11 第 23 条）
ALTER TABLE fact_revisions ADD COLUMN before_json TEXT;
ALTER TABLE fact_revisions ADD COLUMN after_json TEXT;

-- 每一批整理数过什么（13.2.9）
ALTER TABLE memory_jobs ADD COLUMN counts_json TEXT;

-- 退避（13.2.5）：失败几次、什么时候再试，和游标同一行、同一个事务；重启不清零
ALTER TABLE summary_state ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE summary_state ADD COLUMN retry_at INTEGER;

-- 一条记忆的范围是一组（ADR-159 第 4 条）。这张表是真源，facts.scope、episodes.scope 只放第一个值做兼容；
-- memory-v2.db 里的全文表、向量表是从这里派生的，删了能重建
CREATE TABLE item_scopes (
  ref TEXT NOT NULL,
  scope TEXT NOT NULL,
  PRIMARY KEY (ref, scope)
) WITHOUT ROWID;
INSERT INTO item_scopes (ref, scope) SELECT 'f:' || id, coalesce(nullif(scope, ''), 'owner') FROM facts;
INSERT INTO item_scopes (ref, scope) SELECT 'e:' || id, coalesce(nullif(scope, ''), 'owner') FROM episodes;

-- 证据（4.4）：某一天、某个会话里出现过这句话算一次，同一天同会话说几遍还是一次
CREATE TABLE fact_evidence (
  fact_id TEXT NOT NULL,
  day TEXT NOT NULL,
  conversation TEXT NOT NULL,
  seq INTEGER,
  speaker TEXT,
  by_subject INTEGER NOT NULL DEFAULT 0,
  verdict TEXT,
  at INTEGER NOT NULL,
  PRIMARY KEY (fact_id, day, conversation)
) WITHOUT ROWID;

-- 库自己的几个数：generation 是"世代号"，忘掉、真删时加一，在途的整理结果比对不上就整批丢掉（13.2.4 第 6 步）
CREATE TABLE memory_meta (
  key TEXT PRIMARY KEY,
  value INTEGER NOT NULL
) WITHOUT ROWID;
INSERT INTO memory_meta (key, value) VALUES ('generation', 0);
