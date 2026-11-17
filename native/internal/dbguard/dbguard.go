// Package dbguard 在打开一个 SQLite 库之前判断它的迁移版本本程序能不能读、要不要迁移，
// 有待跑迁移时先拍快照再迁、失败换回，并维护库里的 sumika_schema 版本表
// （规格-库版本与升级护栏 第一到五节）。
//
// 对外是 Open 和只读的 Inspect：Open 探查、判定、快照、迁移、换回、保留都在里面走完，
// 返回的句柄已经过护栏；Inspect 只读看一眼、给出判定，给归档只读打开和角色记忆库用。
// 护栏包不 import store 或 memory，免得它们接线时形成环。
//
// 一个库文件可以有多条迁移轨（平台库是 chat 加平台自己的一条），判定、快照和换回都以文件为单位，
// 一次看全部轨。可写连接、剩余空间、时钟、改名由调用方通过 Options 传入，测试靠它们注入失败。
package dbguard

// DefaultTable 是 goose 没改名时用的版本表。
const DefaultTable = "goose_db_version"

// SchemaTable 是护栏自己的版本表：每条迁移轨一行，记现在第几版、最低要第几版的程序才能读。
const SchemaTable = "sumika_schema"

// Floors 是每条迁移轨的护栏起点号（规格第 2 条）：这个号及以前的存量迁移都按 breaking 算。
// events 是冻结的 Bun 线，不登记。接线的 Track.Floor 从这里取。
var Floors = map[string]int64{
	"chat":         17,
	"qq":           8,
	"local":        3,
	"trace":        21,
	"memory":       3,
	"memory-index": 1,
}
