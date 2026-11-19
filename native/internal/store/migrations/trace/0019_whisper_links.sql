-- +goose Up
-- 悄悄话和它引起的处理的关联（规格-群聊引用和路由 15.2）：对话日志按来源会话和批号取，只存关联，不存聊天内容。
-- 一行 = 一条悄悄话指给一位角色。时间是毫秒；0 表示还没有。
-- 取号前查过 main 和所有分支、worktree：trace 迁移最大是 0018，不撞号。
CREATE TABLE whisper_links (
 batch TEXT NOT NULL,                       -- 悄悄话批号（同一次发送共用）
 character_id TEXT NOT NULL,                -- 被指的角色
 source_conversation TEXT NOT NULL,         -- 来源会话键：日志按它取
 whisper_conversation TEXT NOT NULL,        -- 她的悄悄话会话键（处理那一轮的 steps、model_runs 记在它名下）
 event_seq INTEGER NOT NULL DEFAULT 0,      -- 悄悄话事件在悄悄话会话里的 seq
 delivery TEXT NOT NULL DEFAULT 'internal', -- internal（后台处理）；steer（插进回合）本版还没有
 steer_step INTEGER NOT NULL DEFAULT 0,     -- 只有 steer 用
 root_run_id TEXT NOT NULL DEFAULT '',      -- 引起的根运行（回合 id）；排队中、还没拿到时为空
 note_seq INTEGER NOT NULL DEFAULT 0,       -- 处理记录事件在悄悄话会话里的 seq（完成时才有）：按它取，不去事件表里翻 JSON
 state TEXT NOT NULL,                       -- queued / processing / done / failed / cancelled / timed_out / interrupted
 detail TEXT NOT NULL DEFAULT '',
 source_busy INTEGER NOT NULL DEFAULT 0,    -- 悄悄话到时她在来源会话里有回合在跑
 selftest INTEGER NOT NULL DEFAULT 0,       -- 来源是自检话题（ctl 只读得到这些）
 created_at INTEGER NOT NULL,
 started_at INTEGER NOT NULL DEFAULT 0,     -- 开始处理
 ended_at INTEGER NOT NULL DEFAULT 0,       -- 到终态
 PRIMARY KEY (batch, character_id)
) WITHOUT ROWID;
CREATE INDEX ix_whisper_links_source ON whisper_links(source_conversation, created_at, batch);

-- +goose Down
DROP INDEX ix_whisper_links_source;
DROP TABLE whisper_links;
