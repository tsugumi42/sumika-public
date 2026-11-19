-- +goose Up
-- 运行页质量标签（规格-运行页 第七节）：模型调用以外的任务（发送、链接取数等）的耗时和结局。一行一次，行很小。
-- module 和 class 沿用 ADR-311 的"模块 · 类别"写法，类别用各模块自己的常量（internal/taskrun 放词表）。
-- detail 写入前已脱敏、最多 500 字节（taskrun.Detail），不存会话名、号码、URL、令牌、模型回复原文。
CREATE TABLE task_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 at INTEGER NOT NULL,
 module TEXT NOT NULL,
 outcome TEXT NOT NULL,
 class TEXT NOT NULL DEFAULT '',
 ms INTEGER,
 attempts INTEGER NOT NULL DEFAULT 1,
 root_id TEXT,
 detail TEXT
);
CREATE INDEX ix_task_runs_at ON task_runs(at);
CREATE INDEX ix_task_runs_module ON task_runs(module, at);

-- +goose Down
DROP INDEX ix_task_runs_module;
DROP INDEX ix_task_runs_at;
DROP TABLE task_runs;
