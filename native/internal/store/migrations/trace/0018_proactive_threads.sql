-- +goose Up
-- 主动联系的话题线（规格-主动联系 第四、五节）。线本身的消息是本地库里的会话事件（console:<角色>:topic:thread-<线号>），
-- 这里只存线的状态、每次发送的记录、被线认领的主人消息。所有时间是毫秒；0 表示没有。
-- 取号前查过 main 和所有分支：trace 迁移最大是 0017，不撞号。
CREATE TABLE proactive_threads (
 id TEXT PRIMARY KEY,
 character_id TEXT NOT NULL,
 conversation TEXT NOT NULL,
 state TEXT NOT NULL,                       -- thinking / awaiting / chatting / closed
 opened_at INTEGER NOT NULL,
 opened_reason TEXT NOT NULL DEFAULT '',    -- 第一次开口时的 reason，界面角标悬停显示，限 60 字
 first_send_at INTEGER NOT NULL DEFAULT 0,
 last_send_at INTEGER NOT NULL DEFAULT 0,
 last_wake_at INTEGER NOT NULL DEFAULT 0,   -- 最近一次叫醒（新线或续醒）
 awaiting_since INTEGER NOT NULL DEFAULT 0,
 followups INTEGER NOT NULL DEFAULT 0,
 sends INTEGER NOT NULL DEFAULT 0,          -- 真发出去的条数（含结果不明的）
 owner_replies INTEGER NOT NULL DEFAULT 0,  -- 主人回过几次（认领的和旁听的都算）
 first_reply_at INTEGER NOT NULL DEFAULT 0,
 owner_replied_at INTEGER NOT NULL DEFAULT 0,
 chat_last_at INTEGER NOT NULL DEFAULT 0,   -- 聊着时主人最后一句被认领的时间
 blocked_convs TEXT NOT NULL DEFAULT '',    -- hand_back 以后本线内不再认领的会话，换行分隔
 closed_at INTEGER NOT NULL DEFAULT 0,
 close_reason TEXT NOT NULL DEFAULT '',     -- quiet / done / no-reply / snoozed / cap / error
 conclusion TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ix_proactive_threads_char ON proactive_threads(character_id, opened_at);
CREATE INDEX ix_proactive_threads_open ON proactive_threads(character_id, state);

-- 先写后发：发送前写一行 pending，发完改成 sent / failed / unknown；重启时 pending 一律记 unknown，不自动重发。
-- plan_id 是线号加序号，当幂等键。event_seq 和 event_id 是写进目标会话的那条事件（失败或没发是 0 和空）。
CREATE TABLE proactive_sends (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 thread TEXT NOT NULL,
 plan_id TEXT NOT NULL UNIQUE,
 conversation TEXT NOT NULL,
 event_seq INTEGER NOT NULL DEFAULT 0,
 event_id TEXT NOT NULL DEFAULT '',
 target_label TEXT NOT NULL,
 scene TEXT NOT NULL,
 sent_state TEXT NOT NULL,
 summary TEXT NOT NULL DEFAULT '',          -- 一句话摘要，只本机显示，限 40 字
 at INTEGER NOT NULL
);
CREATE INDEX ix_proactive_sends_thread ON proactive_sends(thread, id);
CREATE INDEX ix_proactive_sends_event ON proactive_sends(conversation, event_seq);

-- 被线认领的主人消息：只用来防重复和跟踪"线有没有回"，不用来重放（规格第十三节）。
-- state：pending 线还没表态 / replied 线回了 / handed 交回给普通路径了 / lost 进程重启丢了
CREATE TABLE proactive_claims (
 conversation TEXT NOT NULL,
 seq INTEGER NOT NULL,
 thread TEXT NOT NULL,
 state TEXT NOT NULL,
 at INTEGER NOT NULL,
 PRIMARY KEY (conversation, seq)
);
CREATE INDEX ix_proactive_claims_thread ON proactive_claims(thread, state);

-- +goose Down
DROP INDEX ix_proactive_claims_thread;
DROP TABLE proactive_claims;
DROP INDEX ix_proactive_sends_event;
DROP INDEX ix_proactive_sends_thread;
DROP TABLE proactive_sends;
DROP INDEX ix_proactive_threads_open;
DROP INDEX ix_proactive_threads_char;
DROP TABLE proactive_threads;
