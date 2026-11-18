-- 本地群有独立身份和有序成员；不从消息作者猜群成员。
-- +goose Up
CREATE TABLE local_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    next_position INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE local_group_members (
    group_id TEXT NOT NULL REFERENCES local_groups(id) ON DELETE CASCADE,
    character_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    auto_count INTEGER NOT NULL DEFAULT 0,
    last_auto_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, character_id),
    UNIQUE (group_id, position)
);
CREATE INDEX ix_local_group_members_character ON local_group_members(character_id, group_id);
