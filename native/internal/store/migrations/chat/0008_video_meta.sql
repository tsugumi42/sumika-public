-- 视频元数据和封面与下载结果同事务写入，读消息不打开文件。
-- +goose Up
ALTER TABLE media_fetch ADD COLUMN cover_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE media_fetch ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media_fetch ADD COLUMN size INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE media_fetch DROP COLUMN size;
ALTER TABLE media_fetch DROP COLUMN duration_ms;
ALTER TABLE media_fetch DROP COLUMN cover_hash;
