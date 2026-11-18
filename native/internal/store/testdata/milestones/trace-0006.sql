-- 冻结的里程碑：2026-11-02 切 Go 那天的 trace 库：trace 0006
-- 生成：cd native && go test ./internal/store -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE gate_decisions (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL, conversation TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, features TEXT, probability REAL, threshold REAL, expected REAL, target REAL, actual INTEGER, frequency REAL NOT NULL, verdict TEXT NOT NULL, reason TEXT NOT NULL, outcome TEXT, outcome_reason TEXT);
CREATE TABLE gate_modes (conversation TEXT PRIMARY KEY, mode TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE gate_rules (conversation TEXT NOT NULL, slot TEXT NOT NULL DEFAULT '', frequency REAL NOT NULL, until INTEGER, PRIMARY KEY (conversation, slot));
CREATE TABLE gate_state (key TEXT PRIMARY KEY, state TEXT NOT NULL, since_seq INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE link_aliases (
  clean_url TEXT PRIMARY KEY,
  target_url TEXT NOT NULL,
  resolved_at TEXT NOT NULL
);
CREATE TABLE link_results (
  clean_url TEXT PRIMARY KEY,
  fetched_at TEXT NOT NULL,
  result_json BLOB NOT NULL
);
CREATE TABLE maintenance_runs (started_at INTEGER PRIMARY KEY, ms INTEGER NOT NULL, total_bytes INTEGER NOT NULL, sizes_json TEXT NOT NULL, cleaned_json TEXT NOT NULL);
CREATE TABLE model_runs ( id TEXT PRIMARY KEY, character_id TEXT NOT NULL, conversation TEXT NOT NULL, stage TEXT NOT NULL, model TEXT NOT NULL, started_at INTEGER NOT NULL, ttft_ms INTEGER, total_ms INTEGER, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, sections_json TEXT NOT NULL, injected_json TEXT NOT NULL, request_json TEXT, response_json TEXT, error TEXT , cache_write_tokens INTEGER, route_provider TEXT, route_model TEXT, draft_json TEXT, context_json TEXT, epoch_id TEXT, rebuild TEXT, reasoning_tokens INTEGER, turn_id TEXT);
CREATE TABLE outbound_hits ( id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL, conversation TEXT NOT NULL, event_seq INTEGER NOT NULL, kind TEXT NOT NULL, hit TEXT NOT NULL, dry INTEGER NOT NULL DEFAULT 0);
CREATE TABLE prompt_sections (hash TEXT PRIMARY KEY, text TEXT NOT NULL, first_seen INTEGER NOT NULL);
CREATE TABLE recall_log (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL,
  conversation TEXT, source TEXT NOT NULL, query TEXT NOT NULL, candidates_json TEXT NOT NULL, chosen_json TEXT NOT NULL, ms INTEGER NOT NULL);
CREATE TABLE steps ( id TEXT PRIMARY KEY, character_id TEXT, conversation TEXT, turn_id TEXT, event_seq INTEGER, kind TEXT NOT NULL, name TEXT, args_json TEXT, preview TEXT, full_hash TEXT, status TEXT NOT NULL DEFAULT 'running', is_error INTEGER NOT NULL DEFAULT 0, approval_json TEXT, started_at INTEGER, ended_at INTEGER, batch INTEGER NOT NULL DEFAULT 0, details_json TEXT);
CREATE TABLE turns (
  id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  conversation TEXT NOT NULL,
  channel TEXT,
  started_at INTEGER NOT NULL,
  ended_at INTEGER,
  outcome TEXT,
  unknown_tools TEXT
, ran INTEGER NOT NULL DEFAULT 0);
CREATE TABLE usage_daily ( day TEXT NOT NULL, character_id TEXT NOT NULL, model TEXT NOT NULL, stage TEXT NOT NULL, runs INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cache_read_tokens INTEGER NOT NULL, total_ms INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (day, character_id, model, stage));
CREATE INDEX gate_decisions_conv ON gate_decisions (conversation, at);
CREATE INDEX ix_outbound_hits_at ON outbound_hits(at);
CREATE INDEX ix_recall_log_at ON recall_log (character_id, at);
CREATE INDEX ix_runs_conv ON model_runs(conversation, started_at);
CREATE INDEX ix_runs_turn ON model_runs(turn_id);
CREATE INDEX ix_steps_conv ON steps(conversation, event_seq);
CREATE INDEX ix_steps_turn ON steps(turn_id);
CREATE INDEX turns_conversation ON turns(character_id, conversation, started_at);
INSERT INTO "gate_decisions" ("id", "at", "character_id", "conversation", "seq", "batch", "features", "probability", "threshold", "expected", "target", "actual", "frequency", "verdict", "reason", "outcome", "outcome_reason") VALUES (1, 1000, '编造角色', 'local:topic:编造', 1, 1, NULL, NULL, NULL, NULL, NULL, NULL, 0.5, 'speak', '编造的理由一', NULL, NULL);
INSERT INTO "gate_decisions" ("id", "at", "character_id", "conversation", "seq", "batch", "features", "probability", "threshold", "expected", "target", "actual", "frequency", "verdict", "reason", "outcome", "outcome_reason") VALUES (2, 2000, '编造角色', 'qq:group:编造群', 2, 1, NULL, NULL, NULL, NULL, NULL, NULL, 0.25, 'skip', '编造的理由二', NULL, NULL);
INSERT INTO "gate_state" ("key", "state", "since_seq", "updated_at") VALUES ('gate-编造-1', 'open', 1, 1000);
INSERT INTO "gate_state" ("key", "state", "since_seq", "updated_at") VALUES ('gate-编造-2', 'closed', 2, 2000);
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (1, 0, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (2, 1, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (3, 2, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (4, 3, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (5, 4, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (6, 5, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (7, 6, 1, '2026-11-02 00:00:00');
INSERT INTO "model_runs" ("id", "character_id", "conversation", "stage", "model", "started_at", "ttft_ms", "total_ms", "input_tokens", "output_tokens", "cache_read_tokens", "sections_json", "injected_json", "request_json", "response_json", "error", "cache_write_tokens", "route_provider", "route_model", "draft_json", "context_json", "epoch_id", "rebuild", "reasoning_tokens", "turn_id") VALUES ('run-编造-1', '编造角色', 'local:topic:编造', 'chat', '编造模型', 1000, NULL, NULL, 10, 20, 0, '[]', '[]', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "model_runs" ("id", "character_id", "conversation", "stage", "model", "started_at", "ttft_ms", "total_ms", "input_tokens", "output_tokens", "cache_read_tokens", "sections_json", "injected_json", "request_json", "response_json", "error", "cache_write_tokens", "route_provider", "route_model", "draft_json", "context_json", "epoch_id", "rebuild", "reasoning_tokens", "turn_id") VALUES ('run-编造-2', '编造角色', 'qq:group:编造群', 'chat', '编造模型', 3000, NULL, NULL, 30, 40, 0, '[]', '[]', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "steps" ("id", "character_id", "conversation", "turn_id", "event_seq", "kind", "name", "args_json", "preview", "full_hash", "status", "is_error", "approval_json", "started_at", "ended_at", "batch", "details_json") VALUES ('step-编造-1', '编造角色', 'local:topic:编造', NULL, NULL, 'tool', '编造工具', NULL, NULL, NULL, 'done', 0, NULL, 1050, NULL, 1, NULL);
INSERT INTO "steps" ("id", "character_id", "conversation", "turn_id", "event_seq", "kind", "name", "args_json", "preview", "full_hash", "status", "is_error", "approval_json", "started_at", "ended_at", "batch", "details_json") VALUES ('step-编造-2', '编造角色', 'qq:group:编造群', NULL, NULL, 'tool', '编造工具', NULL, NULL, NULL, 'running', 0, NULL, 3050, NULL, 1, NULL);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-1', '编造角色', 'local:topic:编造', 'local', 1000, 1100, 'done', NULL, 0);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-2', '编造角色', 'local:topic:编造', 'local', 2000, 2200, 'stopped', NULL, 0);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-3', '编造角色', 'qq:group:编造群', 'qq', 3000, NULL, NULL, NULL, 0);
COMMIT;
