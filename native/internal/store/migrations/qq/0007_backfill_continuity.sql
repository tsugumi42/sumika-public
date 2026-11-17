-- +goose Up
-- qq_sync.through 仅作账号检查位置；连续覆盖按账号和会话另记，旧值不冒充证明。
CREATE TABLE qq_backfill_continuity (
    account TEXT NOT NULL,
    conversation TEXT NOT NULL,
    from_ms INTEGER NOT NULL,
    through_ms INTEGER NOT NULL,
    PRIMARY KEY(account, conversation)
);

-- 第七代之前的 filled 没有连续证明。先退检查位置，再恢复缺口，避免升级后漏信仍被过滤。
-- 群缺口尚无账号列，只能保守回退全部已有账号；运行时仍按原白名单准入。
UPDATE qq_sync
SET through = min(through, coalesce((
    SELECT min(from_ms) FROM channel_gaps
    WHERE channel = 'qq' AND state = 'filled'
      AND (instr(conversation, 'qq:private:' || qq_sync.account || ':') = 1
           OR conversation LIKE 'qq:group:%')
), through));

UPDATE channel_gaps
SET state = 'blocked', reason = 'unverified-history', tries = 0, last_try_ms = 0,
    updated_ms = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE channel = 'qq' AND state = 'filled';
