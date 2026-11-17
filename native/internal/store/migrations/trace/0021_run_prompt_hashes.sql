-- +goose Up
-- 提示词缓存排查（data/dev/cache-audit-20261113.md 改法 0）：每次请求真正发出去的 messages 逐条记角色、字节数和哈希链，
-- tools 记字节数和哈希；不存正文。相邻两次请求比链就知道前缀断在第几条。词表见 llm.PromptHashes。
-- 只加列，旧行留 NULL。
ALTER TABLE model_runs ADD COLUMN prompt_hashes_json TEXT;

-- +goose Down
ALTER TABLE model_runs DROP COLUMN prompt_hashes_json;
