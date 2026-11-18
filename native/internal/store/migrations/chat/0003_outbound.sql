-- 渠道中间件的出站：发送计划和计划里的段（规格-渠道中间件 第十节 10.3、10.4，第十五节第 2 步）。只加表，不改 0001、0002 里的任何一张旧表。
-- 从 events/0003_outbound.sql 抄来，Go 的平台库用这一套。
-- 只给 Go 自己的库用：Bun 管着的真实库没有它们（store.Baseline() 只到版本 1，不要求真实库有）。
-- 计划和段都是结构列，不存 JSON（盘点 4.4 第 3 条：Bun 的 segments_json 里的 unsent、partial 要靠正则去读，是要去掉的做法）。
--
-- 一次回复一条计划。kind：send（发）、dry-run（试运行）、blocked（拦下）、interrupted（已打断）、error（出错）；
-- 后三种没有段，也不存原文（它可能含私事，不进库）。state 的取值和怎么汇总，见 store/outbound.go。
-- event_seq 是这次回复写进 events 的那一行；拦下的、出错的、只有戳一戳的计划没有，为空。
-- 段 (plan_id, idx) 是主键。sent_at 是这一段第一次调 Send 的时刻（毫秒）——回声认领的 5 分钟窗口从这里算（10.4）。

-- +goose Up
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

-- 启动和关闭时扫"还没结束的计划"；回声认领只看最近 5 分钟内发过的段。表会一直长（每条回复一行），这两处不能每次扫全表
CREATE INDEX ix_outbound_plans_open ON outbound_plans(state) WHERE state = 'sending';
CREATE INDEX ix_outbound_parts_sent ON outbound_parts(sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX ix_outbound_plans_conv ON outbound_plans(conversation, created_at);
