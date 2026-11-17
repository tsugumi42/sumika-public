-- events.db 的基线：2026-10-25 从真实库 sqlite_master 原样导出（30 项），和 Bun 版各模块建的表一模一样。
-- 只给 Go 新建的库用（测试的临时库、全新安装）；Bun 管着的真实库不跑这个（规格-Go后台 四点五）。
-- 以后改表另写一个迁移文件，不改这个。

-- +goose Up
CREATE TABLE blob_refs ( hash TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY (hash, owner_kind, owner_id));
CREATE TABLE blobs ( hash TEXT PRIMARY KEY, size INTEGER NOT NULL, mime TEXT NOT NULL, kind TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
CREATE TABLE chain_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL);
CREATE TABLE context_epochs (id TEXT PRIMARY KEY, conversation TEXT NOT NULL, character_id TEXT NOT NULL, from_seq INTEGER NOT NULL, summary TEXT NOT NULL, kind TEXT NOT NULL, trigger TEXT NOT NULL, tokens_before INTEGER NOT NULL, tokens_after INTEGER NOT NULL, dropped INTEGER NOT NULL, kept INTEGER NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL, error TEXT, ms INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE event_links (from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, kind TEXT NOT NULL, strength TEXT NOT NULL, why TEXT NOT NULL, PRIMARY KEY (from_seq, to_seq, kind));
CREATE TABLE events ( seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, channel TEXT NOT NULL, conversation TEXT NOT NULL, actor_kind TEXT NOT NULL, actor_id TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, segments_json TEXT, reply_to TEXT, created_at INTEGER NOT NULL , origin TEXT);
CREATE TABLE local_topics (id TEXT PRIMARY KEY, character_id TEXT NOT NULL, title TEXT, title_by TEXT, pinned_at INTEGER, archived_at INTEGER, created_at INTEGER NOT NULL, forked_from TEXT, forked_seq INTEGER);
CREATE TABLE media_checks (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, ok INTEGER NOT NULL, brief TEXT, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE media_descriptions (hash TEXT NOT NULL, model TEXT NOT NULL, prompt_version TEXT NOT NULL, prep_version INTEGER NOT NULL, note_id INTEGER NOT NULL DEFAULT 0, ok INTEGER NOT NULL, category TEXT, brief TEXT, result_json TEXT, error TEXT, ms INTEGER, input_tokens INTEGER, output_tokens INTEGER, at INTEGER NOT NULL, PRIMARY KEY (hash, model, prompt_version, prep_version, note_id));
CREATE TABLE media_notes (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, note TEXT NOT NULL, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE observations ( character_id TEXT NOT NULL, event_seq INTEGER NOT NULL REFERENCES events(seq), PRIMARY KEY (character_id, event_seq) );
CREATE TABLE qq_alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, kind TEXT NOT NULL, account TEXT NOT NULL, text TEXT NOT NULL, detail TEXT);
CREATE TABLE qq_avatars (key TEXT PRIMARY KEY, hash TEXT, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_forwards (id TEXT PRIMARY KEY, tree_json TEXT NOT NULL, fetched_at INTEGER NOT NULL);
CREATE TABLE qq_groups (gid TEXT PRIMARY KEY, name TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE qq_msgs (conversation TEXT NOT NULL, message_id TEXT NOT NULL, event_seq INTEGER NOT NULL, PRIMARY KEY (conversation, message_id));
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
CREATE INDEX ix_local_topics_character ON local_topics(character_id);
CREATE INDEX ix_obs_seq ON observations(event_seq, character_id);
CREATE INDEX media_checks_hash ON media_checks (hash, id);
CREATE INDEX media_notes_hash ON media_notes (hash, id);
