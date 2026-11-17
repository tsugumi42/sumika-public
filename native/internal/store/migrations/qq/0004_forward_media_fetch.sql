-- 合并转发层里的图片任务，与事件图片复用同一下载器；会话仅用于提交后通知，不当授权凭据。
-- +goose Up
CREATE TABLE forward_media_fetch (
 forward_id TEXT NOT NULL,
 node INTEGER NOT NULL,
 idx INTEGER NOT NULL,
 conversation TEXT NOT NULL,
 kind TEXT NOT NULL,
 url_sig TEXT NOT NULL,
 state TEXT NOT NULL,
 tries INTEGER NOT NULL DEFAULT 0,
 next_at INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL,
 hash TEXT,
 last_error TEXT,
 PRIMARY KEY(forward_id,node,idx)
);
-- +goose Down
DROP TABLE forward_media_fetch;
