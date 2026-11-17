package main

// 运行页统计（sumika-ctl stats，规格-运行页 第三节）接进桌面程序：和界面共用 runstats.Service，只读。
// 每次请求只读打开一次 trace.db（只读连接，不抢写连接），用完就关；服务端校验过参数，这里不接受路径、SQL。

import (
	"context"
	"errors"
	"os"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/runstats"
)

var (
	_ ctlserver.StatsBackend     = (*ctlBackend)(nil)
	_ ctlserver.StorageBackend   = (*ctlBackend)(nil)
	_ ctlserver.QualityBackend   = (*ctlBackend)(nil)
	_ ctlserver.ResourcesBackend = (*ctlBackend)(nil)
)

func (b *ctlBackend) Usage(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.UsageReport, error) {
	ds := b.s.settings.Load()
	if ds == nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录打不开")
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "还没有运行记录库（go/trace.db），没有可统计的")
	}
	svc, err := runstats.OpenFile(file, runstats.Options{})
	if err != nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "打不开运行记录库")
	}
	defer svc.Close()
	rng, err := svc.RangeOf(a)
	if err != nil {
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	rep, err := svc.Usage(ctx, rng)
	switch {
	case errors.Is(err, runstats.ErrTimeout):
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")
	case err != nil:
		return ctlproto.UsageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "读运行记录库出错")
	}
	return rep, nil
}

// Resources 采一次资源给命令行：没有前一次采样，自己隔一小会儿采两次，CPU 不给空。不开 trace.db
func (b *ctlBackend) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	rep, err := b.s.sampler().SampleWarm(ctx, true)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ctlproto.ResourcesReport{}, ctlproto.Errorf(ctlproto.CodeTimeout, "采样超时")
	case err != nil:
		return ctlproto.ResourcesReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "采样资源出错")
	}
	return rep, nil
}

// Storage 读存储标签（只读快照加增量，不触发扫描，要重扫去界面）。数据目录没就绪、演示模式由服务端先拦
func (b *ctlBackend) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	svc, err := b.s.storageService()
	if err != nil {
		return ctlproto.StorageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
	}
	rep, err := svc.Storage(ctx)
	if err != nil {
		return ctlproto.StorageReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "读存储统计出错")
	}
	return rep, nil
}

// Spans 读回合耗时时间线：只读打开 trace.db。表里只有环节名、插件名、工具名、模型名和时间，不用脱敏。
func (b *ctlBackend) Spans(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.SpansReport, error) {
	ds := b.s.settings.Load()
	if ds == nil {
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录打不开")
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "还没有运行记录库（go/trace.db）")
	}
	svc, err := runstats.OpenFile(file, runstats.Options{})
	if err != nil {
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "打不开运行记录库")
	}
	defer svc.Close()
	var rng ctlproto.UsageRange
	if a.Turn == "" {
		if rng, err = svc.RangeOf(a); err != nil {
			return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
		}
	}
	rep, err := svc.Spans(ctx, rng, a)
	switch {
	case errors.Is(err, runstats.ErrTimeout):
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")
	case err != nil:
		return ctlproto.SpansReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "读运行记录库出错")
	}
	return rep, nil
}

// Rebuild 读重建留痕：只读打开 trace.db。表里只有场合名、原因标识和条数，不带会话串，不用脱敏。
func (b *ctlBackend) Rebuild(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.RebuildReport, error) {
	ds := b.s.settings.Load()
	if ds == nil {
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录打不开")
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "还没有运行记录库（go/trace.db）")
	}
	svc, err := runstats.OpenFile(file, runstats.Options{})
	if err != nil {
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "打不开运行记录库")
	}
	defer svc.Close()
	rng, err := svc.RangeOf(a)
	if err != nil {
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	rep, err := svc.Rebuild(ctx, rng)
	switch {
	case errors.Is(err, runstats.ErrTimeout):
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")
	case err != nil:
		return ctlproto.RebuildReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "读运行记录库出错")
	}
	return rep, nil
}

// Quality 读质量标签：只读打开 trace.db，范围和 --task、--failures 按 StatsArgs 来。
// 失败摘要先过 backend.RedactLog（会话名、号码打码），再由 runstats 过 taskrun.Detail 兜底（URL、令牌、模型输出）
func (b *ctlBackend) Quality(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	ds := b.s.settings.Load()
	if ds == nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录打不开")
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "还没有运行记录库（go/trace.db），没有可统计的")
	}
	svc, err := runstats.OpenFile(file, runstats.Options{Redact: func(s string) string { return backend.RedactLog(s, nil) }})
	if err != nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "打不开运行记录库")
	}
	defer svc.Close()
	rng, err := svc.RangeOf(a)
	if err != nil {
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	rep, err := svc.Quality(ctx, rng, a)
	switch {
	case errors.Is(err, runstats.ErrTimeout):
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeTimeout, "统计超时")
	case err != nil:
		return ctlproto.QualityReport{}, ctlproto.Errorf(ctlproto.CodeFailed, "读运行记录库出错")
	}
	return rep, nil
}
