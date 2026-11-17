-- 冻结的里程碑：Bun 基础表加上写入侧迁移 0001～0003（goose 0003，没有 sumika_schema）
-- 生成：cd native && go test ./internal/memory -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE episodes (
  id TEXT PRIMARY KEY, conversation TEXT NOT NULL, text TEXT NOT NULL, visibility TEXT NOT NULL,
  from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, created_at INTEGER NOT NULL,
  scope TEXT, origin TEXT, audience_at_origin TEXT, event_at INTEGER, learned_at INTEGER, status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE fact_evidence (
  fact_id TEXT NOT NULL,
  day TEXT NOT NULL,
  conversation TEXT NOT NULL,
  seq INTEGER,
  speaker TEXT,
  by_subject INTEGER NOT NULL DEFAULT 0,
  verdict TEXT,
  at INTEGER NOT NULL,
  PRIMARY KEY (fact_id, day, conversation)
) WITHOUT ROWID;
CREATE TABLE fact_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT, fact_id TEXT NOT NULL, op TEXT NOT NULL, at INTEGER NOT NULL, by TEXT NOT NULL, reason TEXT, before_json TEXT, after_json TEXT);
CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, text TEXT NOT NULL, subject TEXT, visibility TEXT NOT NULL,
  source_seqs TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'active', superseded_by TEXT, created_at INTEGER NOT NULL,
  origin TEXT, conversation TEXT, audience_at_origin TEXT, speech_act TEXT, attributed_to TEXT,
  scope TEXT, no_mention INTEGER NOT NULL DEFAULT 0,
  event_at INTEGER, learned_at INTEGER, valid_from INTEGER, valid_to INTEGER, temporal TEXT, confirmed_by TEXT, only_her INTEGER NOT NULL DEFAULT 1, learned_by TEXT, whisper_json TEXT);
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE item_scopes (
  ref TEXT NOT NULL,
  scope TEXT NOT NULL,
  PRIMARY KEY (ref, scope)
) WITHOUT ROWID;
CREATE TABLE memory_jobs (conversation TEXT NOT NULL, from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, version INTEGER NOT NULL,
  done_at INTEGER NOT NULL, counts_json TEXT, PRIMARY KEY (conversation, from_seq, to_seq, version));
CREATE TABLE memory_meta (
  key TEXT PRIMARY KEY,
  value INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE shared_outbox (
 id TEXT PRIMARY KEY,
 payload TEXT NOT NULL
);
CREATE TABLE summary_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL, failures INTEGER NOT NULL DEFAULT 0, retry_at INTEGER);
INSERT INTO "episodes" ("id", "conversation", "text", "visibility", "from_seq", "to_seq", "created_at", "scope", "origin", "audience_at_origin", "event_at", "learned_at", "status") VALUES ('episode-编造-1', 'local:topic:编造', '编造的片段一', 'owner', 1, 2, 1000, NULL, NULL, NULL, NULL, NULL, 'active');
INSERT INTO "episodes" ("id", "conversation", "text", "visibility", "from_seq", "to_seq", "created_at", "scope", "origin", "audience_at_origin", "event_at", "learned_at", "status") VALUES ('episode-编造-2', 'local:topic:编造', '编造的片段二', 'owner', 3, 4, 2000, NULL, NULL, NULL, NULL, NULL, 'active');
INSERT INTO "fact_revisions" ("id", "fact_id", "op", "at", "by", "reason", "before_json", "after_json") VALUES (1, 'fact-编造-1', 'create', 1000, '编造角色', '编造的创建', NULL, NULL);
INSERT INTO "fact_revisions" ("id", "fact_id", "op", "at", "by", "reason", "before_json", "after_json") VALUES (2, 'fact-编造-2', 'confirm', 2000, 'owner', '编造的确认', NULL, NULL);
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal", "confirmed_by", "only_her", "learned_by", "whisper_json") VALUES ('fact-编造-1', 'fact', '编造的事实一', 'owner', 'owner', '[]', 'active', NULL, 1000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL, NULL, 1, NULL, NULL);
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal", "confirmed_by", "only_her", "learned_by", "whisper_json") VALUES ('fact-编造-2', 'preference', '编造的事实二', 'owner', 'owner', '[]', 'active', NULL, 2000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL, NULL, 1, NULL, NULL);
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal", "confirmed_by", "only_her", "learned_by", "whisper_json") VALUES ('fact-编造-3', 'fact', '编造的事实三', '编造甲', 'owner', '[]', 'active', NULL, 3000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL, NULL, 1, NULL, NULL);
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (1, 0, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (2, 1, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (3, 2, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (4, 3, 1, '2026-11-02 00:00:00');
INSERT INTO "item_scopes" ("ref", "scope") VALUES ('e:episode-编造-1', 'owner');
INSERT INTO "item_scopes" ("ref", "scope") VALUES ('e:episode-编造-2', 'owner');
INSERT INTO "item_scopes" ("ref", "scope") VALUES ('f:fact-编造-1', 'owner');
INSERT INTO "item_scopes" ("ref", "scope") VALUES ('f:fact-编造-2', 'owner');
INSERT INTO "item_scopes" ("ref", "scope") VALUES ('f:fact-编造-3', 'owner');
INSERT INTO "memory_jobs" ("conversation", "from_seq", "to_seq", "version", "done_at", "counts_json") VALUES ('local:topic:编造', 1, 10, 1, 1000, NULL);
INSERT INTO "memory_jobs" ("conversation", "from_seq", "to_seq", "version", "done_at", "counts_json") VALUES ('qq:group:编造群', 1, 20, 1, 2000, NULL);
INSERT INTO "memory_meta" ("key", "value") VALUES ('generation', 0);
INSERT INTO "summary_state" ("conversation", "last_seq", "updated_at", "failures", "retry_at") VALUES ('local:topic:编造', 10, 1000, 0, NULL);
INSERT INTO "summary_state" ("conversation", "last_seq", "updated_at", "failures", "retry_at") VALUES ('qq:group:编造群', 20, 2000, 0, NULL);
COMMIT;
