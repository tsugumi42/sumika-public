-- 冻结的里程碑：Bun 时代的角色记忆库：只有基础五张表，没有 goose 表
-- 生成：cd native && go test ./internal/memory -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE episodes (
  id TEXT PRIMARY KEY, conversation TEXT NOT NULL, text TEXT NOT NULL, visibility TEXT NOT NULL,
  from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, created_at INTEGER NOT NULL,
  scope TEXT, origin TEXT, audience_at_origin TEXT, event_at INTEGER, learned_at INTEGER, status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE fact_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT, fact_id TEXT NOT NULL, op TEXT NOT NULL, at INTEGER NOT NULL, by TEXT NOT NULL, reason TEXT);
CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, text TEXT NOT NULL, subject TEXT, visibility TEXT NOT NULL,
  source_seqs TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'active', superseded_by TEXT, created_at INTEGER NOT NULL,
  origin TEXT, conversation TEXT, audience_at_origin TEXT, speech_act TEXT, attributed_to TEXT,
  scope TEXT, no_mention INTEGER NOT NULL DEFAULT 0,
  event_at INTEGER, learned_at INTEGER, valid_from INTEGER, valid_to INTEGER, temporal TEXT);
CREATE TABLE memory_jobs (conversation TEXT NOT NULL, from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, version INTEGER NOT NULL,
  done_at INTEGER NOT NULL, PRIMARY KEY (conversation, from_seq, to_seq, version));
CREATE TABLE summary_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO "episodes" ("id", "conversation", "text", "visibility", "from_seq", "to_seq", "created_at", "scope", "origin", "audience_at_origin", "event_at", "learned_at", "status") VALUES ('episode-编造-1', 'local:topic:编造', '编造的片段一', 'owner', 1, 2, 1000, NULL, NULL, NULL, NULL, NULL, 'active');
INSERT INTO "episodes" ("id", "conversation", "text", "visibility", "from_seq", "to_seq", "created_at", "scope", "origin", "audience_at_origin", "event_at", "learned_at", "status") VALUES ('episode-编造-2', 'local:topic:编造', '编造的片段二', 'owner', 3, 4, 2000, NULL, NULL, NULL, NULL, NULL, 'active');
INSERT INTO "fact_revisions" ("id", "fact_id", "op", "at", "by", "reason") VALUES (1, 'fact-编造-1', 'create', 1000, '编造角色', '编造的创建');
INSERT INTO "fact_revisions" ("id", "fact_id", "op", "at", "by", "reason") VALUES (2, 'fact-编造-2', 'confirm', 2000, 'owner', '编造的确认');
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal") VALUES ('fact-编造-1', 'fact', '编造的事实一', 'owner', 'owner', '[]', 'active', NULL, 1000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal") VALUES ('fact-编造-2', 'preference', '编造的事实二', 'owner', 'owner', '[]', 'active', NULL, 2000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "facts" ("id", "kind", "text", "subject", "visibility", "source_seqs", "status", "superseded_by", "created_at", "origin", "conversation", "audience_at_origin", "speech_act", "attributed_to", "scope", "no_mention", "event_at", "learned_at", "valid_from", "valid_to", "temporal") VALUES ('fact-编造-3', 'fact', '编造的事实三', '编造甲', 'owner', '[]', 'active', NULL, 3000, NULL, NULL, NULL, NULL, NULL, 'owner', 0, NULL, NULL, NULL, NULL, NULL);
INSERT INTO "memory_jobs" ("conversation", "from_seq", "to_seq", "version", "done_at") VALUES ('local:topic:编造', 1, 10, 1, 1000);
INSERT INTO "memory_jobs" ("conversation", "from_seq", "to_seq", "version", "done_at") VALUES ('qq:group:编造群', 1, 20, 1, 2000);
INSERT INTO "summary_state" ("conversation", "last_seq", "updated_at") VALUES ('local:topic:编造', 10, 1000);
INSERT INTO "summary_state" ("conversation", "last_seq", "updated_at") VALUES ('qq:group:编造群', 20, 2000);
COMMIT;
