-- 冻结的里程碑：2026-11-02 切 Go 那天的 QQ 平台库：chat 0007、qq 0004
-- 生成：cd native && go test ./internal/store -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE blob_refs ( hash TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY (hash, owner_kind, owner_id));
CREATE TABLE blobs ( hash TEXT PRIMARY KEY, size INTEGER NOT NULL, mime TEXT NOT NULL, kind TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
CREATE TABLE chain_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL);
CREATE TABLE channel_msgs (
  channel TEXT NOT NULL,
  conversation TEXT NOT NULL,
  platform_id TEXT NOT NULL,
  event_seq INTEGER NOT NULL REFERENCES events(seq),
  PRIMARY KEY (channel, conversation, platform_id)
);
CREATE TABLE context_epochs (id TEXT PRIMARY KEY, conversation TEXT NOT NULL, character_id TEXT NOT NULL, from_seq INTEGER NOT NULL, summary TEXT NOT NULL, kind TEXT NOT NULL, trigger TEXT NOT NULL, tokens_before INTEGER NOT NULL, tokens_after INTEGER NOT NULL, dropped INTEGER NOT NULL, kept INTEGER NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL, error TEXT, ms INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE event_links (from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, kind TEXT NOT NULL, strength TEXT NOT NULL, why TEXT NOT NULL, PRIMARY KEY (from_seq, to_seq, kind));
CREATE TABLE events ( seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, channel TEXT NOT NULL, conversation TEXT NOT NULL, actor_kind TEXT NOT NULL, actor_id TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, segments_json TEXT, reply_to TEXT, created_at INTEGER NOT NULL , origin TEXT);
CREATE TABLE forward_media_fetch (
 forward_id TEXT NOT NULL,
 node INTEGER NOT NULL,
 idx INTEGER NOT NULL,
 conversation TEXT NOT NULL,
 kind TEXT NOT NULL,
 url_sig TEXT NOT NULL,
 state TEXT NOT NULL,
 tries INTEGER NOT NULL DEFAULT 0,
 next_at INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL,
 hash TEXT,
 last_error TEXT,
 PRIMARY KEY(forward_id,node,idx)
);
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE goose_db_version_qq (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE link_snapshots (
  hash TEXT PRIMARY KEY,
  result_json BLOB NOT NULL
);
CREATE TABLE media_checks (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, ok INTEGER NOT NULL, brief TEXT, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE media_descriptions (hash TEXT NOT NULL, model TEXT NOT NULL, prompt_version TEXT NOT NULL, prep_version INTEGER NOT NULL, note_id INTEGER NOT NULL DEFAULT 0, ok INTEGER NOT NULL, category TEXT, brief TEXT, result_json TEXT, error TEXT, ms INTEGER, input_tokens INTEGER, output_tokens INTEGER, at INTEGER NOT NULL, PRIMARY KEY (hash, model, prompt_version, prep_version, note_id));
CREATE TABLE media_fetch (
  event_seq  INTEGER NOT NULL,
  idx        INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  url_sig    TEXT NOT NULL,
  state      TEXT NOT NULL,
  tries      INTEGER NOT NULL DEFAULT 0,
  next_at    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  hash       TEXT,
  last_error TEXT, width INTEGER NOT NULL DEFAULT 0, height INTEGER NOT NULL DEFAULT 0, animated INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (event_seq, idx)
);
CREATE TABLE media_notes (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, note TEXT NOT NULL, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE message_links (
  conversation TEXT NOT NULL,
  message_seq INTEGER NOT NULL CHECK (message_seq > 0),
  clean_url TEXT NOT NULL,
  previous_count INTEGER NOT NULL,
  result_json BLOB, snapshot TEXT,
  PRIMARY KEY (conversation, message_seq, clean_url)
);
CREATE TABLE observations ( character_id TEXT NOT NULL, event_seq INTEGER NOT NULL REFERENCES events(seq), PRIMARY KEY (character_id, event_seq) );
CREATE TABLE outbound_parts (
  plan_id TEXT NOT NULL REFERENCES outbound_plans(id),
  idx INTEGER NOT NULL,
  kind TEXT NOT NULL,
  text TEXT,
  ref TEXT,
  pause_ms INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  platform_id TEXT,
  via TEXT,
  err TEXT,
  sent_at INTEGER,
  PRIMARY KEY (plan_id, idx)
);
CREATE TABLE outbound_plans (
  id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  conversation TEXT NOT NULL,
  event_seq INTEGER REFERENCES events(seq),
  account TEXT NOT NULL,
  origin TEXT NOT NULL,
  kind TEXT NOT NULL,
  reason TEXT,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  done_at INTEGER
);
CREATE TABLE qq_alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, account TEXT NOT NULL, text TEXT NOT NULL, detail TEXT);
CREATE TABLE qq_avatars (key TEXT PRIMARY KEY, hash TEXT, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_forwards (id TEXT PRIMARY KEY, tree_json TEXT NOT NULL, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_groups (gid TEXT PRIMARY KEY, name TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE qq_people (uid TEXT PRIMARY KEY, nickname TEXT, cards_json TEXT NOT NULL DEFAULT '{}', aliases_json TEXT NOT NULL DEFAULT '[]', last_seen INTEGER);
CREATE TABLE qq_read (conversation TEXT PRIMARY KEY, read_at INTEGER NOT NULL);
CREATE TABLE qq_selftest_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, account TEXT NOT NULL, peer TEXT NOT NULL, app TEXT, started_at INTEGER NOT NULL, ended_at INTEGER, log_json TEXT NOT NULL, sent_json TEXT NOT NULL);
CREATE TABLE qq_stickers (hash TEXT PRIMARY KEY, first_seen INTEGER NOT NULL);
CREATE TABLE qq_sync (account TEXT PRIMARY KEY, through INTEGER NOT NULL);
CREATE INDEX event_links_to ON event_links (to_seq, kind);
CREATE INDEX ix_blob_refs_owner ON blob_refs(owner_kind, owner_id);
CREATE INDEX ix_context_epochs_conv ON context_epochs(conversation, created_at);
CREATE INDEX ix_events_conv ON events(conversation, seq);
CREATE INDEX ix_events_conv_time ON events(conversation, created_at, seq);
CREATE INDEX ix_message_links_count ON message_links(conversation, clean_url);
CREATE INDEX ix_obs_seq ON observations(event_seq, character_id);
CREATE INDEX ix_outbound_parts_sent ON outbound_parts(sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX ix_outbound_plans_conv ON outbound_plans(conversation, created_at);
CREATE INDEX ix_outbound_plans_open ON outbound_plans(state) WHERE state = 'sending';
CREATE INDEX media_checks_hash ON media_checks (hash, id);
CREATE INDEX media_notes_hash ON media_notes (hash, id);
INSERT INTO "chain_state" ("conversation", "last_seq") VALUES ('qq:group:编造群', 3);
INSERT INTO "chain_state" ("conversation", "last_seq") VALUES ('qq:group:编造群二', 1);
INSERT INTO "channel_msgs" ("channel", "conversation", "platform_id", "event_seq") VALUES ('qq', 'qq:group:编造群', 'platform-编造-1', 1);
INSERT INTO "channel_msgs" ("channel", "conversation", "platform_id", "event_seq") VALUES ('qq', 'qq:group:编造群二', 'platform-编造-2', 2);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (1, 'evt-编造-1', 'qq', 'qq:group:编造群', 'human', 'u-编造-甲', 'message', '编造的第一句', NULL, NULL, 1000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (2, 'evt-编造-2', 'qq', 'qq:group:编造群', 'character', '编造角色', 'message', '编造的第二句', NULL, NULL, 2000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (3, 'evt-编造-3', 'qq', 'qq:group:编造群', 'human', 'u-编造-乙', 'message', '编造的第三句', NULL, NULL, 3000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (4, 'evt-编造-4', 'qq', 'qq:group:编造群二', 'human', 'u-编造-甲', 'message', '别的会话编造的话', NULL, NULL, 4000, NULL);
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (1, 0, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (2, 1, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (3, 2, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (4, 3, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (5, 4, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (6, 5, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (7, 6, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (8, 7, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_qq" ("id", "version_id", "is_applied", "tstamp") VALUES (1, 0, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_qq" ("id", "version_id", "is_applied", "tstamp") VALUES (2, 1, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_qq" ("id", "version_id", "is_applied", "tstamp") VALUES (3, 2, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_qq" ("id", "version_id", "is_applied", "tstamp") VALUES (4, 3, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_qq" ("id", "version_id", "is_applied", "tstamp") VALUES (5, 4, 1, '2026-11-02 00:00:00');
INSERT INTO "media_fetch" ("event_seq", "idx", "kind", "url_sig", "state", "tries", "next_at", "updated_at", "hash", "last_error", "width", "height", "animated") VALUES (1, 0, 'image', 'sig-编造-1', 'done', 0, 0, 1000, NULL, NULL, 0, 0, 0);
INSERT INTO "media_fetch" ("event_seq", "idx", "kind", "url_sig", "state", "tries", "next_at", "updated_at", "hash", "last_error", "width", "height", "animated") VALUES (2, 0, 'image', 'sig-编造-2', 'pending', 0, 0, 2000, NULL, NULL, 0, 0, 0);
INSERT INTO "qq_alerts" ("id", "at", "kind", "account", "text", "detail") VALUES (1, 1000, 'down', 'u-编造-账号', '编造的掉线', NULL);
INSERT INTO "qq_alerts" ("id", "at", "kind", "account", "text", "detail") VALUES (2, 2000, 'up', 'u-编造-账号', '编造的恢复', NULL);
INSERT INTO "qq_people" ("uid", "nickname", "cards_json", "aliases_json", "last_seen") VALUES ('u-编造-甲', '编造甲', '{}', '[]', 1000);
INSERT INTO "qq_people" ("uid", "nickname", "cards_json", "aliases_json", "last_seen") VALUES ('u-编造-乙', '编造乙', '{}', '[]', 2000);
INSERT INTO "qq_sync" ("account", "through") VALUES ('u-编造-账号', 100);
INSERT INTO "qq_sync" ("account", "through") VALUES ('u-编造-账号二', 200);
COMMIT;
