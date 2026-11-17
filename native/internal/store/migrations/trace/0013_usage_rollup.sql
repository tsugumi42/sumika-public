-- +goose Up
-- 运行页用量的汇总表（规格-运行页 第二节第 5 条：全表查询超预算才做；10 万行全部范围实测 1.0 秒，见 internal/runstats 的压测）。
-- 一行 = 一个本地时区的"天序号" × purpose × stage × model 的合计；天序号 = (started_at + 偏移) / 一天的毫秒数，
-- 偏移存在 usage_rollup_meta（SQL 里没有时区，由 runstats.SyncRollup 在启动时按本地时区写）。
-- 三个触发器在写 model_runs 的同一个事务里累加、扣减，AddRun 的 upsert（ON CONFLICT DO UPDATE）也自动对账，写入侧不用改。
-- meta 没有行时触发器什么都不做、读的人也不用汇总表：迁移完到第一次 SyncRollup 之间是空的，SyncRollup 从原始行重建。
-- 列的算法和 runstats.usageSQL 一字对应（usage_seen 为 NULL 当拿到了、NULL 的缓存写和思考当 0、耗时只算成功的），
-- runstats 的测试拿汇总表和原始查询互相对拍。
CREATE TABLE usage_rollup_meta (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 off_ms INTEGER NOT NULL
);
CREATE TABLE usage_rollup (
 day INTEGER NOT NULL,
 purpose TEXT NOT NULL,
 stage TEXT NOT NULL,
 model TEXT NOT NULL,
 runs INTEGER NOT NULL,
 failed INTEGER NOT NULL,
 no_usage INTEGER NOT NULL,
 uncached INTEGER NOT NULL,
 cache_read INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,
 output INTEGER NOT NULL,
 reasoning INTEGER NOT NULL,
 ok_runs INTEGER NOT NULL,
 ok_ms INTEGER NOT NULL,
 PRIMARY KEY (day, purpose, stage, model)
) WITHOUT ROWID;

-- +goose StatementBegin
CREATE TRIGGER model_runs_rollup_ai AFTER INSERT ON model_runs
WHEN EXISTS (SELECT 1 FROM usage_rollup_meta)
BEGIN
 INSERT INTO usage_rollup (day, purpose, stage, model, runs, failed, no_usage, uncached, cache_read, cache_write, output, reasoning, ok_runs, ok_ms)
 VALUES ((NEW.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000, COALESCE(NEW.purpose, ''), NEW.stage, NEW.model, 1,
  NEW.error IS NOT NULL, COALESCE(NEW.usage_seen, 1) = 0 OR COALESCE(NEW.incomplete, 0) = 1,
  NEW.input_tokens, NEW.cache_read_tokens, COALESCE(NEW.cache_write_tokens, 0), NEW.output_tokens, COALESCE(NEW.reasoning_tokens, 0),
  NEW.error IS NULL AND NEW.total_ms IS NOT NULL, CASE WHEN NEW.error IS NULL AND NEW.total_ms IS NOT NULL THEN NEW.total_ms ELSE 0 END)
 ON CONFLICT (day, purpose, stage, model) DO UPDATE SET
  runs = runs + excluded.runs, failed = failed + excluded.failed, no_usage = no_usage + excluded.no_usage,
  uncached = uncached + excluded.uncached, cache_read = cache_read + excluded.cache_read, cache_write = cache_write + excluded.cache_write,
  output = output + excluded.output, reasoning = reasoning + excluded.reasoning, ok_runs = ok_runs + excluded.ok_runs, ok_ms = ok_ms + excluded.ok_ms;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER model_runs_rollup_ad AFTER DELETE ON model_runs
WHEN EXISTS (SELECT 1 FROM usage_rollup_meta)
BEGIN
 UPDATE usage_rollup SET
  runs = runs - 1, failed = failed - (OLD.error IS NOT NULL),
  no_usage = no_usage - (COALESCE(OLD.usage_seen, 1) = 0 OR COALESCE(OLD.incomplete, 0) = 1),
  uncached = uncached - OLD.input_tokens, cache_read = cache_read - OLD.cache_read_tokens, cache_write = cache_write - COALESCE(OLD.cache_write_tokens, 0),
  output = output - OLD.output_tokens, reasoning = reasoning - COALESCE(OLD.reasoning_tokens, 0),
  ok_runs = ok_runs - (OLD.error IS NULL AND OLD.total_ms IS NOT NULL),
  ok_ms = ok_ms - CASE WHEN OLD.error IS NULL AND OLD.total_ms IS NOT NULL THEN OLD.total_ms ELSE 0 END
 WHERE day = (OLD.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000 AND purpose = COALESCE(OLD.purpose, '') AND stage = OLD.stage AND model = OLD.model;
 DELETE FROM usage_rollup
 WHERE day = (OLD.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000 AND purpose = COALESCE(OLD.purpose, '') AND stage = OLD.stage AND model = OLD.model AND runs <= 0;
END;
-- +goose StatementEnd

-- 改了会进汇总的列：先按旧值扣掉，再按新值加上（AddRun 的 upsert 会改 stage、total_ms、error）
-- +goose StatementBegin
CREATE TRIGGER model_runs_rollup_au AFTER UPDATE OF started_at, stage, model, purpose, error, total_ms, usage_seen, incomplete,
 input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, reasoning_tokens ON model_runs
WHEN EXISTS (SELECT 1 FROM usage_rollup_meta)
BEGIN
 UPDATE usage_rollup SET
  runs = runs - 1, failed = failed - (OLD.error IS NOT NULL),
  no_usage = no_usage - (COALESCE(OLD.usage_seen, 1) = 0 OR COALESCE(OLD.incomplete, 0) = 1),
  uncached = uncached - OLD.input_tokens, cache_read = cache_read - OLD.cache_read_tokens, cache_write = cache_write - COALESCE(OLD.cache_write_tokens, 0),
  output = output - OLD.output_tokens, reasoning = reasoning - COALESCE(OLD.reasoning_tokens, 0),
  ok_runs = ok_runs - (OLD.error IS NULL AND OLD.total_ms IS NOT NULL),
  ok_ms = ok_ms - CASE WHEN OLD.error IS NULL AND OLD.total_ms IS NOT NULL THEN OLD.total_ms ELSE 0 END
 WHERE day = (OLD.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000 AND purpose = COALESCE(OLD.purpose, '') AND stage = OLD.stage AND model = OLD.model;
 DELETE FROM usage_rollup
 WHERE day = (OLD.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000 AND purpose = COALESCE(OLD.purpose, '') AND stage = OLD.stage AND model = OLD.model AND runs <= 0;
 INSERT INTO usage_rollup (day, purpose, stage, model, runs, failed, no_usage, uncached, cache_read, cache_write, output, reasoning, ok_runs, ok_ms)
 VALUES ((NEW.started_at + (SELECT off_ms FROM usage_rollup_meta)) / 86400000, COALESCE(NEW.purpose, ''), NEW.stage, NEW.model, 1,
  NEW.error IS NOT NULL, COALESCE(NEW.usage_seen, 1) = 0 OR COALESCE(NEW.incomplete, 0) = 1,
  NEW.input_tokens, NEW.cache_read_tokens, COALESCE(NEW.cache_write_tokens, 0), NEW.output_tokens, COALESCE(NEW.reasoning_tokens, 0),
  NEW.error IS NULL AND NEW.total_ms IS NOT NULL, CASE WHEN NEW.error IS NULL AND NEW.total_ms IS NOT NULL THEN NEW.total_ms ELSE 0 END)
 ON CONFLICT (day, purpose, stage, model) DO UPDATE SET
  runs = runs + excluded.runs, failed = failed + excluded.failed, no_usage = no_usage + excluded.no_usage,
  uncached = uncached + excluded.uncached, cache_read = cache_read + excluded.cache_read, cache_write = cache_write + excluded.cache_write,
  output = output + excluded.output, reasoning = reasoning + excluded.reasoning, ok_runs = ok_runs + excluded.ok_runs, ok_ms = ok_ms + excluded.ok_ms;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER model_runs_rollup_au;
DROP TRIGGER model_runs_rollup_ad;
DROP TRIGGER model_runs_rollup_ai;
DROP TABLE usage_rollup;
DROP TABLE usage_rollup_meta;
