-- +goose Up
-- 消息页只查本页目标的通知，不随会话历史长度扫描。
CREATE INDEX ix_events_notice_target ON events(conversation, reply_to) WHERE kind='notice';

-- +goose Down
DROP INDEX ix_events_notice_target;
