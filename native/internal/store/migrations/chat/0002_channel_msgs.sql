-- 渠道中间件的平台消息号映射（规格-渠道中间件 第六节第 3 步、第十五节第 1 步）。只加表，不改 0001 里的任何一张旧表。
-- 从 events/0002_channel_msgs.sql 抄来，Go 的平台库用这一套。
-- 只给 Go 自己的库用：Bun 管着的真实库没有它，所以 store.Baseline() 只到版本 1（Bun 版的镜像），不要求真实库有这张表。
--
-- 和 Bun 的 qq_msgs 一样是去重用的：同一条平台消息（同一渠道、同一会话里的同一个平台消息号）只存一个事件，
-- 第二次到达（另一个号也看到了、补录和实时重叠、自己发的回声）只补"看到了"。
-- 比 qq_msgs 多一列 channel：中间件不认识哪个平台，键是（渠道、会话、平台消息号）。
-- 平台消息号是原始数字串，用 TEXT 存：超过 2^53 的号往返不变，也不会因为 "007" 和 "7" 被当成同一个号。
-- 号为空或全是 0 的消息不进这张表，也就不去重（清单⑯：不同的消息不能因为都没有号被当成同一条）。

-- +goose Up
CREATE TABLE channel_msgs (
  channel TEXT NOT NULL,
  conversation TEXT NOT NULL,
  platform_id TEXT NOT NULL,
  event_seq INTEGER NOT NULL REFERENCES events(seq),
  PRIMARY KEY (channel, conversation, platform_id)
);
