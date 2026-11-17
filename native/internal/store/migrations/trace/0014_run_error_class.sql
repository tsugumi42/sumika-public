-- +goose Up
-- 运行页质量标签（规格-运行页 第七节）：模型请求失败的原因类别，词表在 internal/llm（ClassifyError）。
-- 只加列，旧行留 NULL：读的时候对 error 原文现算（llm.ClassifyText），不回写真实库。
ALTER TABLE model_runs ADD COLUMN error_class TEXT;

-- +goose Down
ALTER TABLE model_runs DROP COLUMN error_class;
