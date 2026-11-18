-- edit 给界面的结果详情（规格-Go后台 T12）。Bun 的 steps 没有这一列；Go 用 JSON 文本保存，没详情的仍是 NULL。
-- 只升级 Go 自己的 trace 库，不改 Bun 的基线镜像。

-- +goose Up
ALTER TABLE steps ADD COLUMN details_json TEXT;
