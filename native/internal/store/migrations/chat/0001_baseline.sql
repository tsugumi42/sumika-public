-- Go 各平台聊天库的通用表基线（规格-聊天记录按平台分库 第三节）。平台自己的表在 migrations/local/、migrations/qq/ 里。
-- 建表语句和索引从 migrations/events/0001_baseline.sql 原样抄来。不复用那一份：它是 Bun 的镜像，混着 qq_* 和 local_topics，
-- 每个平台库都跑的话本地库里会有 QQ 的表。通用表的表结构在各个平台库里一样，读写代码只有一套。
-- 只给 Go 自己的库用：Bun 管着的真实库不跑这个（OpenReadOnly 只核对表结构）。
-- 以后改通用表另写一个迁移文件（0004 起），不改这个。

-- +goose Up
CREATE TABLE blob_refs ( hash TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, PRIMARY KEY (hash, owner_kind, owner_id));
CREATE TABLE blobs ( hash TEXT PRIMARY KEY, size INTEGER NOT NULL, mime TEXT NOT NULL, kind TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
CREATE TABLE chain_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL);
CREATE TABLE context_epochs (id TEXT PRIMARY KEY, conversation TEXT NOT NULL, character_id TEXT NOT NULL, from_seq INTEGER NOT NULL, summary TEXT NOT NULL, kind TEXT NOT NULL, trigger TEXT NOT NULL, tokens_before INTEGER NOT NULL, tokens_after INTEGER NOT NULL, dropped INTEGER NOT NULL, kept INTEGER NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL, error TEXT, ms INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE event_links (from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, kind TEXT NOT NULL, strength TEXT NOT NULL, why TEXT NOT NULL, PRIMARY KEY (from_seq, to_seq, kind));
CREATE TABLE events ( seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, channel TEXT NOT NULL, conversation TEXT NOT NULL, actor_kind TEXT NOT NULL, actor_id TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, segments_json TEXT, reply_to TEXT, created_at INTEGER NOT NULL , origin TEXT);
CREATE TABLE media_checks (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, ok INTEGER NOT NULL, brief TEXT, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE media_descriptions (hash TEXT NOT NULL, model TEXT NOT NULL, prompt_version TEXT NOT NULL, prep_version INTEGER NOT NULL, note_id INTEGER NOT NULL DEFAULT 0, ok INTEGER NOT NULL, category TEXT, brief TEXT, result_json TEXT, error TEXT, ms INTEGER, input_tokens INTEGER, output_tokens INTEGER, at INTEGER NOT NULL, PRIMARY KEY (hash, model, prompt_version, prep_version, note_id));
CREATE TABLE media_notes (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, note TEXT NOT NULL, by TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE observations ( character_id TEXT NOT NULL, event_seq INTEGER NOT NULL REFERENCES events(seq), PRIMARY KEY (character_id, event_seq) );
CREATE INDEX event_links_to ON event_links (to_seq, kind);
CREATE INDEX ix_blob_refs_owner ON blob_refs(owner_kind, owner_id);
CREATE INDEX ix_context_epochs_conv ON context_epochs(conversation, created_at);
CREATE INDEX ix_events_conv ON events(conversation, seq);
CREATE INDEX ix_events_conv_time ON events(conversation, created_at, seq);
CREATE INDEX ix_obs_seq ON observations(event_seq, character_id);
CREATE INDEX media_checks_hash ON media_checks (hash, id);
CREATE INDEX media_notes_hash ON media_notes (hash, id);
