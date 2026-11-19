-- 按会话选工作目录（规格-按会话选工作目录）。日常 console:<角色> 在 local_topics 里没有行，所以单开一张表，不改旧表。
-- 只加一张空表，没有数据搬动；回退 = 删这张表，丢的只是绑定。IF NOT EXISTS 让手工补过的库也能重跑。
-- +goose Up
CREATE TABLE IF NOT EXISTS local_workspaces (
    conversation TEXT PRIMARY KEY,
    cwd TEXT NOT NULL,
    root TEXT NOT NULL,
    real_path TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_local_workspaces_recent ON local_workspaces(updated_at);
