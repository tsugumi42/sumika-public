package api

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// StatsReader 是运行页的读接口（规格-运行页 第三节）：界面和命令行（经 ctlserver）读的是同一份结构，定义在 ctlproto，不各写一份。
// 只读：实现用只读连接查运行记录库，不抢写连接，不 select 提示词那几列大文本。
// 用量、存储、质量、资源都已接入；资源不读运行记录库（采样不持有任何数据库连接）。
type StatsReader interface {
	// RangeOf 把 --days / --year / --all 这类参数算成一段具体的时间（本地时区）；参数不合法回错误
	RangeOf(a ctlproto.StatsArgs) (ctlproto.UsageRange, error)
	// Usage 查这段时间的用量：按任务类别、模型、天、周各一份，调用方用 UsageReport.Shape 裁
	Usage(ctx context.Context, r ctlproto.UsageRange) (ctlproto.UsageReport, error)
	// Quality 查这段时间的质量：每类任务和模块的成败、失败原因、最慢的环节、最近的失败、回合和工具。
	// a 里只认 Task（只看某类）和 Failures（最近的失败列几条，0 用默认）；旧行的失败原因读时现算，不回写
	Quality(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.QualityReport, error)
	// Spans 查这段时间每一步的耗时（规格-回合耗时时间线）；a 里只认 Turn（看某一轮）
	Spans(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.SpansReport, error)
	// Rebuild 查这段时间的重建留痕：每天、每个场合重建几次、为什么、丢了几条、有没有摘要覆盖（规格-提示词前缀稳定第八节）
	Rebuild(ctx context.Context, r ctlproto.UsageRange) (ctlproto.RebuildReport, error)
	// ConvTurns 查一个会话最近的模型请求，按轮分组（规格-右栏上下文）；maxRuns 是最多读多少次请求
	ConvTurns(ctx context.Context, conversation string, maxRuns int) (ctlproto.ConvTurnsReport, error)
	// RequestDetail 按请求 id 读一次请求的详情（系统提示词、注入段、工具表、用量）；只在界面点开时调，列表查询不碰这些大文本
	RequestDetail(ctx context.Context, id string) (ctlproto.RequestDetail, error)
	// Storage 给存储标签的数据：小目录现量，大目录用后台统计的快照加增量，不遍历大目录、不触发扫描
	Storage(ctx context.Context) (ctlproto.StorageReport, error)
	// Resources 采一次资源：进程表、主程序内部、后台队列。只读，不碰库；CPU 要两次采样之差，
	// 没有前一次采样（第一次、隔太久）时 CPU 是 nil，不是 0。调用方负责只在页面打开时调，采样器没有协程和定时器
	Resources(ctx context.Context) (ctlproto.ResourcesReport, error)
}

// StorageScanner 是存储标签的"重新统计"：后台低优先级扫描，马上返回，进度在 Storage 的 Scan 字段里，可以取消。
// 和 StatsReader 分开：命令行只读，不触发扫描
type StorageScanner interface {
	Rescan(ctx context.Context) error
	CancelScan()
}
