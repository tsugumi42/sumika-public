-- +goose Up
-- 缺口与提醒是派生状态；人工确认独立于 filled 和连续水位，可以撤销。
ALTER TABLE channel_gaps ADD COLUMN acknowledged_ms INTEGER NOT NULL DEFAULT 0;
