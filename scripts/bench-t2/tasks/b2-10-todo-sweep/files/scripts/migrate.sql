-- 建表脚本（示例）
-- TODO P1: 迁移前先备份数据库
CREATE TABLE IF NOT EXISTS shelf_items (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  isbn TEXT
);
-- TODO P3 - 给 shelf_items.isbn 加索引
