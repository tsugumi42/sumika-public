-- 收到的图补下载的状态（规格-QQ收图下载和断线补录 4.3）。只加表，不改旧表。
-- 每个平台库一份：一行是一条事件的一个图片 / 表情段（event_seq + 段在 $.segs 里的下标）。
-- 状态记在这里，不记在 events.segments_json 的段里：下载不下来时段里的地址、文件名原样留着，以后还能再试。
-- url_sig 是入队时那个地址的 sha256 前 16 位：地址里带 rkey，不存地址本身；回填 hash 时用它核对"段还是入队时的那一段"。
-- last_error 只写短原因，不含地址和查询串。

-- +goose Up
CREATE TABLE media_fetch (
  event_seq  INTEGER NOT NULL,
  idx        INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  url_sig    TEXT NOT NULL,
  state      TEXT NOT NULL,
  tries      INTEGER NOT NULL DEFAULT 0,
  next_at    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  hash       TEXT,
  last_error TEXT,
  PRIMARY KEY (event_seq, idx)
);

-- +goose Down
DROP TABLE media_fetch;
