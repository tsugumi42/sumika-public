-- 冻结的里程碑：trace 库中间版本：trace 0013
-- 生成：cd native && go test ./internal/store -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE budget_reservations (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES root_budget(root_id)
);
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
CREATE TABLE kernel_activities (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 state TEXT NOT NULL,
 error TEXT
);
CREATE TABLE kernel_delivery_links (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 step_id TEXT NOT NULL,
 kind TEXT NOT NULL
);
CREATE TABLE kernel_inbox (
 input_id TEXT PRIMARY KEY,
 domain TEXT NOT NULL,
 client_key TEXT NOT NULL,
 digest TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 mode TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 accepted_at INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL,
 run_id TEXT NOT NULL,
 UNIQUE(domain,client_key),
 UNIQUE(domain,sequence)
);
CREATE TABLE kernel_material_inputs (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 run_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 text TEXT NOT NULL
);
CREATE TABLE kernel_mcp_calls (
 id TEXT PRIMARY KEY, server_id TEXT NOT NULL, state TEXT NOT NULL,
 binding TEXT NOT NULL, reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE kernel_reviews (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 request_json TEXT NOT NULL,
 digest TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL,
 verdict_json TEXT,
 error TEXT NOT NULL DEFAULT '',
 delivered INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL
);
CREATE TABLE kernel_runs (
 run_id TEXT PRIMARY KEY,
 identity_json TEXT NOT NULL,
 state TEXT NOT NULL,
 outcome TEXT,
 revision INTEGER NOT NULL DEFAULT 1,
 drained INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE kernel_task_mailbox (
 task_id TEXT PRIMARY KEY REFERENCES kernel_tasks(id), owner TEXT NOT NULL,
 state TEXT NOT NULL, receiving_run TEXT NOT NULL DEFAULT ''
);
CREATE TABLE kernel_tasks (
 id TEXT PRIMARY KEY, parent_run TEXT NOT NULL, tool_id TEXT NOT NULL,
 payload_digest TEXT NOT NULL, owner TEXT NOT NULL, root_id TEXT NOT NULL,
 state TEXT NOT NULL, payload TEXT NOT NULL, result TEXT NOT NULL DEFAULT '',
 UNIQUE(parent_run,tool_id)
);
CREATE TABLE kernel_web_charges (
 request_id TEXT NOT NULL REFERENCES kernel_web_usage(id),
 dimension TEXT NOT NULL, window TEXT NOT NULL, amount INTEGER NOT NULL,
 PRIMARY KEY(request_id,dimension,window)
);
CREATE TABLE kernel_web_quota (
 dimension TEXT NOT NULL, window TEXT NOT NULL,
 used INTEGER NOT NULL, PRIMARY KEY(dimension,window)
);
CREATE TABLE kernel_web_usage (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, root_id TEXT NOT NULL,
 state TEXT NOT NULL, credits INTEGER NOT NULL, actual INTEGER
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
CREATE TABLE model_runs ( id TEXT PRIMARY KEY, character_id TEXT NOT NULL, conversation TEXT NOT NULL, stage TEXT NOT NULL, model TEXT NOT NULL, started_at INTEGER NOT NULL, ttft_ms INTEGER, total_ms INTEGER, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, sections_json TEXT NOT NULL, injected_json TEXT NOT NULL, request_json TEXT, response_json TEXT, error TEXT , cache_write_tokens INTEGER, route_provider TEXT, route_model TEXT, draft_json TEXT, context_json TEXT, epoch_id TEXT, rebuild TEXT, reasoning_tokens INTEGER, turn_id TEXT, root_id TEXT, run_id TEXT, step_id TEXT, attempt_id TEXT, purpose TEXT, usage_seen INTEGER, incomplete INTEGER, cost_micros INTEGER, reset_reason TEXT);
CREATE TABLE outbound_hits ( id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL, conversation TEXT NOT NULL, event_seq INTEGER NOT NULL, kind TEXT NOT NULL, hit TEXT NOT NULL, dry INTEGER NOT NULL DEFAULT 0);
CREATE TABLE prompt_sections (hash TEXT PRIMARY KEY, text TEXT NOT NULL, first_seen INTEGER NOT NULL);
CREATE TABLE recall_log (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, character_id TEXT NOT NULL,
  conversation TEXT, source TEXT NOT NULL, query TEXT NOT NULL, candidates_json TEXT NOT NULL, chosen_json TEXT NOT NULL, ms INTEGER NOT NULL);
CREATE TABLE result_refs (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES result_roots(root_id),
 owner_json TEXT NOT NULL,
 ref_json TEXT NOT NULL,
 tool_name TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 retained INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE result_reservations (
 tool_id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES result_roots(root_id),
 bytes INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE result_roots (
 root_id TEXT PRIMARY KEY,
 bytes INTEGER NOT NULL DEFAULT 0,
 reserved INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE root_budget (
 root_id TEXT PRIMARY KEY,
 state_json BLOB NOT NULL
);
CREATE TABLE steps ( id TEXT PRIMARY KEY, character_id TEXT, conversation TEXT, turn_id TEXT, event_seq INTEGER, kind TEXT NOT NULL, name TEXT, args_json TEXT, preview TEXT, full_hash TEXT, status TEXT NOT NULL DEFAULT 'running', is_error INTEGER NOT NULL DEFAULT 0, approval_json TEXT, started_at INTEGER, ended_at INTEGER, batch INTEGER NOT NULL DEFAULT 0, details_json TEXT, identity_json TEXT, provider_call_id TEXT, ordinal INTEGER, result_ref TEXT, step_id TEXT);
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
CREATE TABLE usage_rollup_meta (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 off_ms INTEGER NOT NULL
);
CREATE TABLE work_entries (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 ordinal INTEGER NOT NULL,
 run_id TEXT NOT NULL,
 payload BLOB NOT NULL,
 digest TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json,ordinal),
 FOREIGN KEY(root_id,owner_json) REFERENCES work_roots(root_id,owner_json)
);
CREATE TABLE work_roots (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 ordinal INTEGER NOT NULL DEFAULT 0,
 stamp TEXT NOT NULL,
 state_json TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json)
);
CREATE TABLE work_snapshots (
 root_id TEXT NOT NULL,
 owner_json TEXT NOT NULL,
 revision INTEGER NOT NULL,
 through_ordinal INTEGER NOT NULL,
 stamp TEXT NOT NULL,
 prefix_digest TEXT NOT NULL,
 state_json TEXT NOT NULL,
 PRIMARY KEY(root_id,owner_json,revision),
 FOREIGN KEY(root_id,owner_json) REFERENCES work_roots(root_id,owner_json)
);
CREATE INDEX budget_reservations_root ON budget_reservations(root_id);
CREATE INDEX gate_decisions_conv ON gate_decisions (conversation, at);
CREATE INDEX ix_kernel_activity_run ON kernel_activities(run_id,state);
CREATE INDEX ix_kernel_inbox_pending ON kernel_inbox(state,domain,sequence);
CREATE INDEX ix_outbound_hits_at ON outbound_hits(at);
CREATE INDEX ix_recall_log_at ON recall_log (character_id, at);
CREATE INDEX ix_runs_conv ON model_runs(conversation, started_at);
CREATE INDEX ix_runs_failed ON model_runs(started_at) WHERE error IS NOT NULL;
CREATE INDEX ix_runs_started ON model_runs(started_at);
CREATE INDEX ix_runs_turn ON model_runs(turn_id);
CREATE INDEX ix_steps_conv ON steps(conversation, event_seq);
CREATE INDEX ix_steps_turn ON steps(turn_id);
CREATE INDEX kernel_mcp_unknown ON kernel_mcp_calls(server_id,state);
CREATE INDEX kernel_reviews_root ON kernel_reviews(root_id, kind, state);
CREATE UNIQUE INDEX model_runs_attempt ON model_runs(attempt_id) WHERE attempt_id IS NOT NULL;
CREATE INDEX result_refs_root ON result_refs(root_id);
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
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (8, 7, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (9, 8, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (10, 9, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (11, 10, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (12, 11, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (13, 12, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (14, 13, 1, '2026-11-02 00:00:00');
INSERT INTO "model_runs" ("id", "character_id", "conversation", "stage", "model", "started_at", "ttft_ms", "total_ms", "input_tokens", "output_tokens", "cache_read_tokens", "sections_json", "injected_json", "request_json", "response_json", "error", "cache_write_tokens", "route_provider", "route_model", "draft_json", "context_json", "epoch_id", "rebuild", "reasoning_tokens", "turn_id", "root_id", "run_id", "step_id", "attempt_id", "purpose", "usage_seen", "incomplete", "cost_micros", "reset_reason") VALUES ('run-编造-1', '编造角色', 'local:topic:编造', 'chat', '编造模型', 1000, NULL, NULL, 10, 20, 0, '[]', '[]', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "model_runs" ("id", "character_id", "conversation", "stage", "model", "started_at", "ttft_ms", "total_ms", "input_tokens", "output_tokens", "cache_read_tokens", "sections_json", "injected_json", "request_json", "response_json", "error", "cache_write_tokens", "route_provider", "route_model", "draft_json", "context_json", "epoch_id", "rebuild", "reasoning_tokens", "turn_id", "root_id", "run_id", "step_id", "attempt_id", "purpose", "usage_seen", "incomplete", "cost_micros", "reset_reason") VALUES ('run-编造-2', '编造角色', 'qq:group:编造群', 'chat', '编造模型', 3000, NULL, NULL, 30, 40, 0, '[]', '[]', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "steps" ("id", "character_id", "conversation", "turn_id", "event_seq", "kind", "name", "args_json", "preview", "full_hash", "status", "is_error", "approval_json", "started_at", "ended_at", "batch", "details_json", "identity_json", "provider_call_id", "ordinal", "result_ref", "step_id") VALUES ('step-编造-1', '编造角色', 'local:topic:编造', NULL, NULL, 'tool', '编造工具', NULL, NULL, NULL, 'done', 0, NULL, 1050, NULL, 1, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "steps" ("id", "character_id", "conversation", "turn_id", "event_seq", "kind", "name", "args_json", "preview", "full_hash", "status", "is_error", "approval_json", "started_at", "ended_at", "batch", "details_json", "identity_json", "provider_call_id", "ordinal", "result_ref", "step_id") VALUES ('step-编造-2', '编造角色', 'qq:group:编造群', NULL, NULL, 'tool', '编造工具', NULL, NULL, NULL, 'running', 0, NULL, 3050, NULL, 1, NULL, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-1', '编造角色', 'local:topic:编造', 'local', 1000, 1100, 'done', NULL, 0);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-2', '编造角色', 'local:topic:编造', 'local', 2000, 2200, 'stopped', NULL, 0);
INSERT INTO "turns" ("id", "character_id", "conversation", "channel", "started_at", "ended_at", "outcome", "unknown_tools", "ran") VALUES ('turn-编造-3', '编造角色', 'qq:group:编造群', 'qq', 3000, NULL, NULL, NULL, 0);
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
COMMIT;
