-- +goose Up
-- 悄悄话处理那一轮里 remember 写的事实，带来源：{"batch","source","quotes"}（批号、来源会话、被引事件 id）；平时是空。
ALTER TABLE facts ADD COLUMN whisper_json TEXT;

-- +goose Down
ALTER TABLE facts DROP COLUMN whisper_json;
