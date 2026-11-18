-- 下载时记尺寸，旧 done 行由后台补扫；查看转发时不打开图片文件。
-- +goose Up
ALTER TABLE forward_media_fetch ADD COLUMN width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forward_media_fetch ADD COLUMN height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forward_media_fetch ADD COLUMN animated INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE forward_media_fetch DROP COLUMN animated;
ALTER TABLE forward_media_fetch DROP COLUMN height;
ALTER TABLE forward_media_fetch DROP COLUMN width;
