-- 我定的：直接给下载状态加展示元数据，不另建按相同主键关联的表。
-- 只加列，旧行的 0 交给下载器后台补扫；读消息绝不打开媒体文件。
-- +goose Up
ALTER TABLE media_fetch ADD COLUMN width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media_fetch ADD COLUMN height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media_fetch ADD COLUMN animated INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE media_fetch DROP COLUMN animated;
ALTER TABLE media_fetch DROP COLUMN height;
ALTER TABLE media_fetch DROP COLUMN width;
