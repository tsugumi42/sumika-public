-- 缺口独立保留；账号检查位置可以继续记录，连续证明由 qq/0007 按账号和会话单独保存。
-- +goose Up
CREATE TABLE channel_gaps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    conversation TEXT NOT NULL,
    from_ms INTEGER NOT NULL,
    to_ms INTEGER NOT NULL,
    reason TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'open',
    tries INTEGER NOT NULL DEFAULT 0,
    last_try_ms INTEGER NOT NULL,
    created_ms INTEGER NOT NULL,
    updated_ms INTEGER NOT NULL,
    UNIQUE(channel, conversation, from_ms, to_ms)
);
CREATE INDEX ix_channel_gaps_retry ON channel_gaps(state, last_try_ms);
