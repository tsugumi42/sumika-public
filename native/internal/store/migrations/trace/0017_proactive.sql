-- +goose Up
-- 主动联系的导演（规格-主动联系 第七节）：每个角色一行状态，每次醒、被拦、续醒、收线各一行日志。
-- inputs 只放数字和布尔（距上次接触、系数、今天次数、是否睡觉时段、主人最后活跃距今、窗口前台），不放会话名和文字。
-- count 是"同样的错误只记两次，之后合并成一行计数"用的：这一行代表发生了几次。
CREATE TABLE proactive_state (
 character_id TEXT PRIMARY KEY,
 factor REAL NOT NULL DEFAULT 1,
 quiet_until INTEGER NOT NULL DEFAULT 0,
 cold_streak INTEGER NOT NULL DEFAULT 0,
 next_wake_at INTEGER NOT NULL DEFAULT 0,
 last_wake_at INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL
);
CREATE TABLE proactive_decisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 at INTEGER NOT NULL,
 character_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 reason TEXT NOT NULL,
 inputs TEXT,
 next_at INTEGER,
 thread TEXT,
 model_note TEXT,
 count INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_proactive_decisions_char ON proactive_decisions(character_id, at);
CREATE INDEX ix_proactive_decisions_at ON proactive_decisions(at);

-- +goose Down
DROP INDEX ix_proactive_decisions_at;
DROP INDEX ix_proactive_decisions_char;
DROP TABLE proactive_decisions;
DROP TABLE proactive_state;
