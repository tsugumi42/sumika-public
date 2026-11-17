# 四种日志格式

每个服务用哪种格式，见 config/services.json 的 format。

## iso
`2026-03-01T08:05:12Z ERROR auth-svc: invalid token for session 7f3a`

- 级别：第二个词（DEBUG / INFO / WARN / ERROR / FATAL）
- 消息：第一个冒号和空格之后的部分

## syslog
`Mar  1 08:10:21 pay-1 billing[311]: level=error msg="deadlock detected"`

- 级别：`level=` 后面的词（小写）
- 消息：`msg="` 和结尾引号之间的部分

## jsonl
`{"ts":"2026-03-01T08:00:00Z","level":"error","msg":"shard 3 timeout"}`

- 每行一个 JSON；级别是 level 字段（小写），消息是 msg 字段

## bracket
`[2026-03-01 08:03:15] (ERROR) gateway - upstream timeout after 30s`

- 级别：圆括号里的词
- 消息：第一个 ` - ` 之后的部分
