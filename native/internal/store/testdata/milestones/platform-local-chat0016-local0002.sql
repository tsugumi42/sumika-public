-- 冻结的里程碑：本地平台库最新减一：chat 0016、local 0002（local 轨中间和最新减一同号，保留两份不同 chat 版本）
-- 生成：cd native && go test ./internal/store -run TestGenerateMilestones -update
-- 数据全是编的；名字、号码不是真实用户或消息。
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE blob_refs ( hash TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY (hash, owner_kind, owner_id));
CREATE TABLE blobs ( hash TEXT PRIMARY KEY, size INTEGER NOT NULL, mime TEXT NOT NULL, kind TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
CREATE TABLE chain_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL);
CREATE TABLE channel_alignment_latest (
    account TEXT NOT NULL,
    slot TEXT NOT NULL CHECK(slot IN ('active', 'last')),
    job_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    gap_id INTEGER NOT NULL DEFAULT 0,
    trigger TEXT NOT NULL,
    state TEXT NOT NULL,
    started_ms INTEGER NOT NULL,
    ended_ms INTEGER NOT NULL,
    inserted INTEGER NOT NULL,
    duplicates INTEGER NOT NULL,
    messages TEXT NOT NULL,
    media_json TEXT NOT NULL,
    reason TEXT NOT NULL,
    covered INTEGER NOT NULL,
    PRIMARY KEY(account, slot)
);
CREATE TABLE channel_gaps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    conversation TEXT NOT NULL,
    from_ms INTEGER NOT NULL,
    to_ms INTEGER NOT NULL,
    reason TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'open',
    tries INTEGER NOT NULL DEFAULT 0,
    last_try_ms INTEGER NOT NULL,
    created_ms INTEGER NOT NULL,
    updated_ms INTEGER NOT NULL, last_manual_ms INTEGER NOT NULL DEFAULT 0,
    UNIQUE(channel, conversation, from_ms, to_ms)
);
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
CREATE TABLE events_short (
  token TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  event_seq INTEGER NOT NULL,
  PRIMARY KEY(token, created_at DESC, event_seq DESC)
) WITHOUT ROWID;
CREATE VIRTUAL TABLE events_trigram USING fts5(text, content='events', content_rowid='seq', tokenize='trigram');
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE goose_db_version_local (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
CREATE TABLE link_snapshots (
  hash TEXT PRIMARY KEY,
  result_json BLOB NOT NULL
);
CREATE TABLE local_group_members (
    group_id TEXT NOT NULL REFERENCES local_groups(id) ON DELETE CASCADE,
    character_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    auto_count INTEGER NOT NULL DEFAULT 0,
    last_auto_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, character_id),
    UNIQUE (group_id, position)
);
CREATE TABLE local_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    next_position INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE local_topics (id TEXT PRIMARY KEY, character_id TEXT NOT NULL, title TEXT, title_by TEXT, pinned_at INTEGER, archived_at INTEGER, created_at INTEGER NOT NULL, forked_from TEXT, forked_seq INTEGER);
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
  last_error TEXT, width INTEGER NOT NULL DEFAULT 0, height INTEGER NOT NULL DEFAULT 0, animated INTEGER NOT NULL DEFAULT 0, cover_hash TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, format TEXT NOT NULL DEFAULT '',
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
CREATE INDEX event_links_to ON event_links (to_seq, kind);
CREATE INDEX ix_blob_refs_owner ON blob_refs(owner_kind, owner_id);
CREATE INDEX ix_channel_gaps_retry ON channel_gaps(state, last_try_ms);
CREATE INDEX ix_channel_gaps_window ON channel_gaps(channel, conversation, from_ms, id);
CREATE INDEX ix_context_epochs_conv ON context_epochs(conversation, created_at);
CREATE INDEX ix_events_conv ON events(conversation, seq);
CREATE INDEX ix_events_conv_time ON events(conversation, created_at, seq);
CREATE INDEX ix_events_notice_target ON events(conversation, reply_to) WHERE kind='notice';
CREATE INDEX ix_events_short_seq ON events_short(event_seq);
CREATE INDEX ix_events_time ON events(created_at DESC, seq DESC);
CREATE INDEX ix_local_group_members_character ON local_group_members(character_id, group_id);
CREATE INDEX ix_local_topics_character ON local_topics(character_id);
CREATE INDEX ix_message_links_count ON message_links(conversation, clean_url);
CREATE INDEX ix_obs_seq ON observations(event_seq, character_id);
CREATE INDEX ix_outbound_parts_sent ON outbound_parts(sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX ix_outbound_plans_conv ON outbound_plans(conversation, created_at);
CREATE INDEX ix_outbound_plans_open ON outbound_plans(state) WHERE state = 'sending';
CREATE INDEX media_checks_hash ON media_checks (hash, id);
CREATE INDEX media_notes_hash ON media_notes (hash, id);
INSERT INTO "chain_state" ("conversation", "last_seq") VALUES ('local:topic:编造', 3);
INSERT INTO "chain_state" ("conversation", "last_seq") VALUES ('local:topic:编造二', 1);
INSERT INTO "channel_msgs" ("channel", "conversation", "platform_id", "event_seq") VALUES ('local', 'local:topic:编造', 'platform-编造-1', 1);
INSERT INTO "channel_msgs" ("channel", "conversation", "platform_id", "event_seq") VALUES ('local', 'local:topic:编造二', 'platform-编造-2', 2);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (1, 'evt-编造-1', 'local', 'local:topic:编造', 'human', 'u-编造-甲', 'message', '编造的第一句', NULL, NULL, 1000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (2, 'evt-编造-2', 'local', 'local:topic:编造', 'character', '编造角色', 'message', '编造的第二句', NULL, NULL, 2000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (3, 'evt-编造-3', 'local', 'local:topic:编造', 'human', 'u-编造-乙', 'message', '编造的第三句', NULL, NULL, 3000, NULL);
INSERT INTO "events" ("seq", "id", "channel", "conversation", "actor_kind", "actor_id", "kind", "text", "segments_json", "reply_to", "created_at", "origin") VALUES (4, 'evt-编造-4', 'local', 'local:topic:编造二', 'human', 'u-编造-甲', 'message', '别的会话编造的话', NULL, NULL, 4000, NULL);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('一', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('一句', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('句', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的第', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第一', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编造', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造的', 1000, 1);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('二', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('二句', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('句', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的第', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第二', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编造', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造的', 2000, 2);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('三', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('三句', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('句', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的第', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('第三', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编造', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造的', 3000, 3);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('会', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('会话', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('别', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('别的', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的会', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('的话', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('编造', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('话', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('话编', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造', 4000, 4);
INSERT INTO "events_short" ("token", "created_at", "event_seq") VALUES ('造的', 4000, 4);
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
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (15, 14, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (16, 15, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version" ("id", "version_id", "is_applied", "tstamp") VALUES (17, 16, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_local" ("id", "version_id", "is_applied", "tstamp") VALUES (1, 0, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_local" ("id", "version_id", "is_applied", "tstamp") VALUES (2, 1, 1, '2026-11-02 00:00:00');
INSERT INTO "goose_db_version_local" ("id", "version_id", "is_applied", "tstamp") VALUES (3, 2, 1, '2026-11-02 00:00:00');
INSERT INTO "local_group_members" ("group_id", "character_id", "position", "auto_count", "last_auto_at") VALUES ('group-编造-1', '编造角色', 0, 0, 0);
INSERT INTO "local_group_members" ("group_id", "character_id", "position", "auto_count", "last_auto_at") VALUES ('group-编造-2', '编造角色', 0, 0, 0);
INSERT INTO "local_groups" ("id", "name", "created_at", "next_position") VALUES ('group-编造-1', '编造的群', 1000, 0);
INSERT INTO "local_groups" ("id", "name", "created_at", "next_position") VALUES ('group-编造-2', '编造的另一个群', 2000, 0);
INSERT INTO "local_topics" ("id", "character_id", "title", "title_by", "pinned_at", "archived_at", "created_at", "forked_from", "forked_seq") VALUES ('topic-编造-1', '编造角色', '编造的话题', NULL, NULL, NULL, 1000, NULL, NULL);
INSERT INTO "local_topics" ("id", "character_id", "title", "title_by", "pinned_at", "archived_at", "created_at", "forked_from", "forked_seq") VALUES ('topic-编造-2', '编造角色', '编造的另一个话题', NULL, NULL, NULL, 2000, NULL, NULL);
INSERT INTO "media_fetch" ("event_seq", "idx", "kind", "url_sig", "state", "tries", "next_at", "updated_at", "hash", "last_error", "width", "height", "animated", "cover_hash", "duration_ms", "size", "format") VALUES (1, 0, 'image', 'sig-编造-1', 'done', 0, 0, 1000, NULL, NULL, 0, 0, 0, '', 0, 0, '');
INSERT INTO "media_fetch" ("event_seq", "idx", "kind", "url_sig", "state", "tries", "next_at", "updated_at", "hash", "last_error", "width", "height", "animated", "cover_hash", "duration_ms", "size", "format") VALUES (2, 0, 'image', 'sig-编造-2', 'pending', 0, 0, 2000, NULL, NULL, 0, 0, 0, '', 0, 0, '');
CREATE TRIGGER events_search_delete AFTER DELETE ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) VALUES ('delete', old.seq, old.text);
  DELETE FROM events_short WHERE event_seq = old.seq;
END;
CREATE TRIGGER events_search_insert AFTER INSERT ON events WHEN COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet') BEGIN
  INSERT INTO events_trigram(rowid, text) VALUES (new.seq, new.text);
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE src(t, len) AS (SELECT new.text, length(new.text)),
    starts(s) AS (SELECT 1 FROM src WHERE len > 0 UNION ALL SELECT s + 64 FROM starts, src WHERE s + 64 <= len),
    chunks(c, k, cl) AS MATERIALIZED (SELECT substr(t, s, 65), min(64, len - s + 1), length(substr(t, s, 65)) FROM starts, src),
    pos(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM pos WHERE n < 64)
  SELECT lower(substr(c, n, 1)), new.created_at, new.seq FROM chunks, pos WHERE n <= k
  UNION ALL
  SELECT lower(substr(c, n, 2)), new.created_at, new.seq FROM chunks, pos WHERE n <= k AND n < cl;
END;
CREATE TRIGGER events_search_update AFTER UPDATE OF seq, text, created_at, origin ON events BEGIN
  INSERT INTO events_trigram(events_trigram, rowid, text) SELECT 'delete', old.seq, old.text WHERE COALESCE(old.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  DELETE FROM events_short WHERE event_seq = old.seq;
  INSERT INTO events_trigram(rowid, text) SELECT new.seq, new.text WHERE COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet');
  INSERT OR IGNORE INTO events_short(token, created_at, event_seq)
  WITH RECURSIVE src(t, len) AS (SELECT new.text, length(new.text) WHERE COALESCE(new.origin, '') NOT IN ('automation:loop-pending', 'automation:loop-quiet')),
    starts(s) AS (SELECT 1 FROM src WHERE len > 0 UNION ALL SELECT s + 64 FROM starts, src WHERE s + 64 <= len),
    chunks(c, k, cl) AS MATERIALIZED (SELECT substr(t, s, 65), min(64, len - s + 1), length(substr(t, s, 65)) FROM starts, src),
    pos(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM pos WHERE n < 64)
  SELECT lower(substr(c, n, 1)), new.created_at, new.seq FROM chunks, pos WHERE n <= k
  UNION ALL
  SELECT lower(substr(c, n, 2)), new.created_at, new.seq FROM chunks, pos WHERE n <= k AND n < cl;
END;
INSERT INTO "events_trigram"("events_trigram") VALUES('rebuild');
COMMIT;
