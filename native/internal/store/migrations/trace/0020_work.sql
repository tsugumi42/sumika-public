-- +goose Up
-- 工作、触发、运行三种持久对象（规格-Bun后台第3批 第十三节；讨论稿 方案-会话结构-角色话题群聊 第 9 节的退路版）。
-- 目标、定期任务、提醒、后台整理以后都落在这三张表上，不再各存一套。时间一律毫秒；0 表示还没有。
-- 取号前查过 main 和所有本地分支：trace 迁移最大是 0019，不撞号（2026-11-11，已请主会话在 main 记下）。
CREATE TABLE works (
 work_id TEXT PRIMARY KEY,                 -- ULID，稳定
 kind TEXT NOT NULL,                       -- goal / loop / reminder / consolidate（以后加别的）
 character_id TEXT NOT NULL,               -- 主负责的角色
 title TEXT NOT NULL DEFAULT '',
 body TEXT NOT NULL DEFAULT '',            -- 目标或任务原文
 status TEXT NOT NULL,                     -- active / paused / done / cancelled
 status_reason TEXT NOT NULL DEFAULT '',
 source_ref TEXT NOT NULL DEFAULT '{}',    -- JSON：从哪来的（会话键 + 事件 seq，或设置页、配置）；只说来源，不带授权
 home_conversation TEXT NOT NULL DEFAULT '', -- 结果默认回哪个会话
 state_json TEXT NOT NULL DEFAULT '{}',    -- 各类自己的状态（预算、裁判记录、通知档、投递对象……）
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_works_character ON works(character_id, status, updated_at);

CREATE TABLE work_triggers (
 trigger_id TEXT PRIMARY KEY,              -- ULID
 work_id TEXT NOT NULL REFERENCES works(work_id),
 kind TEXT NOT NULL,                       -- after_turn / interval / schedule / at / self / idle
 spec_json TEXT NOT NULL DEFAULT '{}',     -- 间隔、时刻、星期、时区、条数阈值
 next_at INTEGER NOT NULL DEFAULT 0,       -- 下一次该跑的时刻；after_turn、idle 不按钟走，是 0
 enabled INTEGER NOT NULL DEFAULT 1,
 missed TEXT NOT NULL DEFAULT 'merge_once', -- 错过的几次：merge_once 合并补一次 / skip 不补
 last_fired_at INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_work_triggers_due ON work_triggers(enabled, next_at);
CREATE INDEX ix_work_triggers_work ON work_triggers(work_id);

CREATE TABLE work_runs (
 run_id TEXT PRIMARY KEY,                  -- ULID
 work_id TEXT NOT NULL REFERENCES works(work_id),
 trigger_id TEXT NOT NULL DEFAULT '',      -- 手动跑的是空
 scheduled_at INTEGER NOT NULL DEFAULT 0,  -- 这次本该什么时候跑（合并补跑时是最近错过的那一次）
 dedupe_key TEXT NOT NULL UNIQUE,          -- trigger_id + scheduled_at：同一次触发不会跑两遍
 context TEXT NOT NULL,                    -- continuous 接着那个会话的上下文 / independent 新开、只读工作摘要
 conversation TEXT NOT NULL DEFAULT '',    -- 实际在哪个会话跑
 kernel_run_id TEXT NOT NULL DEFAULT '',   -- 连到 kernel_runs：过程和审批照旧挂在那边
 state TEXT NOT NULL,                      -- fired / queued / running / settled / delivered / delivery_unknown
 outcome TEXT NOT NULL DEFAULT '',
 note TEXT NOT NULL DEFAULT '',            -- 这次的结论，给下次和界面看
 missed INTEGER NOT NULL DEFAULT 0,        -- 这次合并了几次错过的
 created_at INTEGER NOT NULL,
 started_at INTEGER NOT NULL DEFAULT 0,
 ended_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_work_runs_work ON work_runs(work_id, created_at);
-- 一件工作同时只有一个在排队或在跑
CREATE UNIQUE INDEX ux_work_runs_live ON work_runs(work_id) WHERE state IN ('queued', 'running');

-- +goose Down
DROP INDEX ux_work_runs_live;
DROP INDEX ix_work_runs_work;
DROP TABLE work_runs;
DROP INDEX ix_work_triggers_work;
DROP INDEX ix_work_triggers_due;
DROP TABLE work_triggers;
DROP INDEX ix_works_character;
DROP TABLE works;
