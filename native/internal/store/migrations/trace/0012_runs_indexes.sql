-- +goose Up
-- 运行页（规格-运行页 第二节第 5 条）：按时间范围统计用量、列最近的失败，原来 model_runs 只有按会话和回合的索引。只加索引，不动数据。
CREATE INDEX ix_runs_started ON model_runs(started_at);
CREATE INDEX ix_runs_failed ON model_runs(started_at) WHERE error IS NOT NULL;

-- +goose Down
DROP INDEX ix_runs_failed;
DROP INDEX ix_runs_started;
