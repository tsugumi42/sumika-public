# qq-frames：协议端的一帧原文翻成什么

Go 的跑法：`native/internal/channel/snowluma/shared_frames_test.go`，直接调 `translate`（纯函数）。

## 文件

一个文件一组帧：`{id, about, source_tests[], cases[]}`，`cases[]` 里每项 `{name, frame, expect}`。

- `frame`：协议端推来的**原文字符串**（保留大整数、坏 JSON）。不要先解析再重新序列化。
- 号码、昵称、文字全是编的；登录号 100，对方 200，群 400。

## expect（逐字比）

```
dropped      真 = 这一帧不交出任何东西（无效 JSON、不是对象、认不出的 post_type、认不出对方的私聊戳一戳等）
inbound      消息：kind（message 或 self）、account、conv（qq:private:<登录号>:<对方> 或 qq:group:<群>）、platform_id（原始数字串，不转数字）、
             from_id、from_nick、from_card、time_unix、segs[]
notice       通知：kind（poke、recall、reaction、kicked、other）、account、conv（被踢是空串）、time_unix、actor、target、msg_id、ref、raw
status       状态：kind（connected、heartbeat、kicked 等）、account、time_unix、interval_ms、reason、conv
```

`segs[]` 的段字段和 Go 的 `types.Seg` 的 JSON 一致（`kind`、`text`、`target`、`hash`、`ref`、`raw`、`url`、`file`、`card_*` 等，空值省略）。

`time_unix`：帧里的 `time`（秒）。**0 = 帧里没有时间**——翻译这一步给零值，接收端另外用收到的时间补，所以不在这里比。

**不比**：无。

## 没覆盖

- 转发展开、文件 / 视频的 id 与大小、卡片显示字段（`card_*`）：只在 Go 的 `snowluma/fileseg_test.go`、`card_test.go`、`expand_shapes_test.go`；
- 接收循环本身（心跳看门狗、断线重连、登录号核验、回包分类）。
