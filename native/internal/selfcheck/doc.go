// Package selfcheck 是运行时自检（docs/规格-运行时自检.md，ADR-367）：桌面程序自己数几条守恒关系和卡住的东西，
// 每隔几分钟评估一次，把结果写进 <数据目录>/selfcheck/<日期>.json，状态行和巡检读它。
//
// 只读、只记：不补录、不重试、不清理、不重启、不发通知、不发消息。包里只拿得到一个只有 QueryContext 的 Querier，
// 不 import store、channel、app、turn、llm（deps_test.go 用 go list 守着）；计数器是叶子，channel、snowluma 往里数，包本身不反向依赖它们。
// 名字注意：app/desktop_selftest.go 的“自检”是 sumika-ctl chat 的开发对话，和这里无关；这里界面写“运行自检”。
package selfcheck
