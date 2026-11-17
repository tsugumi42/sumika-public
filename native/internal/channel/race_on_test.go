//go:build race

package channel

// raceBuild：带竞态检测编译的。-race 下纯 Go 的 SQLite 慢一个数量级，放大十倍的量法按比例缩小，免得只是慢就红
const raceBuild = true
