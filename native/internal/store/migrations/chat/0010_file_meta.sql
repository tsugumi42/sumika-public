-- 文件的实际种类供展示使用，避免打开消息时重新读媒体头。
-- +goose Up
ALTER TABLE media_fetch ADD COLUMN format TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE media_fetch DROP COLUMN format;
