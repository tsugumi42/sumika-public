-- trace.db 的基线：2026-10-25 从真实库 sqlite_master 原样导出（16 项），和 Bun 版各模块建的表一模一样。
-- 只给 Go 新建的库用（测试的临时库、全新安装）；Bun 管着的真实库不跑这个（规格-Go后台 四点五）。
-- 以后改表另写一个迁移文件，不改这个。

-- +goose Up
CREATE TABLE gate_decisions (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL, conversation TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, features TEXT, probability REAL, threshold REAL, expected REAL, target REAL, actual INTEGER, frequency REAL NOT NULL, verdict TEXT NOT NULL, reason TEXT NOT NULL, outcome TEXT, outcome_reason TEXT);
CREATE TABLE gate_modes (conversation TEXT PRIMARY KEY, mode TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE gate_rules (conversation TEXT NOT NULL, slot TEXT NOT NULL DEFAULT '', frequency REAL NOT NULL, until INTEGER, PRIMARY KEY (conversation, slot));
CREATE TABLE gate_state (key TEXT PRIMARY KEY, state TEXT NOT NULL, since_seq INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE maintenance_runs (started_at INTEGER PRIMARY KEY, ms INTEGER NOT NULL, total_bytes INTEGER NOT NULL, sizes_json TEXT NOT NULL, cleaned_json TEXT NOT NULL);
CREATE TABLE model_runs ( id TEXT PRIMARY KEY, character_id TEXT NOT NULL, conversation TEXT NOT NULL, stage TEXT NOT NULL, model TEXT NOT NULL, started_at INTEGER NOT NULL, ttft_ms INTEGER, total_ms INTEGER, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, sections_json TEXT NOT NULL, injected_json TEXT NOT NULL, request_json TEXT, response_json TEXT, error TEXT , cache_write_tokens INTEGER, route_provider TEXT, route_model TEXT, draft_json TEXT, context_json TEXT, epoch_id TEXT, rebuild TEXT, reasoning_tokens INTEGER, turn_id TEXT);
CREATE TABLE outbound_hits ( id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL, conversation TEXT NOT NULL, event_seq INTEGER NOT NULL, kind TEXT NOT NULL, hit TEXT NOT NULL, dry INTEGER NOT NULL DEFAULT 0);
CREATE TABLE prompt_sections (hash TEXT PRIMARY KEY, text TEXT NOT NULL, first_seen INTEGER NOT NULL);
CREATE TABLE steps ( id TEXT PRIMARY KEY, character_id TEXT, conversation TEXT, turn_id TEXT, event_seq INTEGER, kind TEXT NOT NULL, name TEXT, args_json TEXT, preview TEXT, full_hash TEXT, status TEXT NOT NULL DEFAULT 'running', is_error INTEGER NOT NULL DEFAULT 0, approval_json TEXT, started_at INTEGER, ended_at INTEGER, batch INTEGER NOT NULL DEFAULT 0);
CREATE TABLE usage_daily ( day TEXT NOT NULL, character_id TEXT NOT NULL, model TEXT NOT NULL, stage TEXT NOT NULL, runs INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cache_read_tokens INTEGER NOT NULL, total_ms INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (day, character_id, model, stage));
CREATE INDEX gate_decisions_conv ON gate_decisions (conversation, at);
CREATE INDEX ix_outbound_hits_at ON outbound_hits(at);
CREATE INDEX ix_runs_conv ON model_runs(conversation, started_at);
CREATE INDEX ix_runs_turn ON model_runs(turn_id);
CREATE INDEX ix_steps_conv ON steps(conversation, event_seq);
CREATE INDEX ix_steps_turn ON steps(turn_id);
