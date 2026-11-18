-- 手动恢复和自动重试分别计时；最新快照固定只有 active / last，不积累任务历史。
-- +goose Up
ALTER TABLE channel_gaps ADD COLUMN last_manual_ms INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_channel_gaps_window ON channel_gaps(channel, conversation, from_ms, id);
CREATE TABLE channel_alignment_latest (
    account TEXT NOT NULL,
    slot TEXT NOT NULL CHECK(slot IN ('active', 'last')),
    job_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    gap_id INTEGER NOT NULL DEFAULT 0,
    trigger TEXT NOT NULL,
    state TEXT NOT NULL,
    started_ms INTEGER NOT NULL,
    ended_ms INTEGER NOT NULL,
    inserted INTEGER NOT NULL,
    duplicates INTEGER NOT NULL,
    messages TEXT NOT NULL,
    media_json TEXT NOT NULL,
    reason TEXT NOT NULL,
    covered INTEGER NOT NULL,
    PRIMARY KEY(account, slot)
);
