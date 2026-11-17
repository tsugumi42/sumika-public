-- 转发文件和视频复用已有下载状态表，元数据与原件一起提交。
-- +goose Up
ALTER TABLE forward_media_fetch ADD COLUMN cover_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE forward_media_fetch ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forward_media_fetch ADD COLUMN size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forward_media_fetch ADD COLUMN format TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE forward_media_fetch DROP COLUMN format;
ALTER TABLE forward_media_fetch DROP COLUMN size;
ALTER TABLE forward_media_fetch DROP COLUMN duration_ms;
ALTER TABLE forward_media_fetch DROP COLUMN cover_hash;
