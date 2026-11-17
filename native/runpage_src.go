package main

// 运行页读数的正式来源（规格-运行页 第三节）：把桌面外壳里的 runstats、storagestat 接成界面用的 api.StatsReader。
// 和 sumika-ctl stats 读同一批结构；每次读用量都只读打开一次 trace.db（只读连接，不抢写连接），用完就关。
// 数据目录没就绪、运行记录库还没建这类"读不了而且不是临时出错"的，回 errRunUnavailable，界面写"不可用"和原因，不当出错重试。

import (
	"context"
	"errors"
	"os"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/paths"
	"sumika-native/internal/runstats"
)

var (
	_ api.StatsReader    = (*shellStats)(nil)
	_ api.StorageScanner = (*shellStats)(nil)
)

type shellStats struct{ s *shell }

// RangeOf 只做时间运算，不碰库，所以不用开 trace.db
func (x *shellStats) RangeOf(a ctlproto.StatsArgs) (ctlproto.UsageRange, error) {
	return runstats.New(nil, runstats.Options{}).RangeOf(a)
}

// openTrace 只读打开运行记录库；用量和质量都走它，调用方负责 Close
func (x *shellStats) openTrace() (*runstats.Service, error) {
	ds := x.s.settings.Load()
	if ds == nil {
		return nil, &errRunUnavailable{"数据目录还没就绪"}
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return nil, &errRunUnavailable{"数据目录打不开"}
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return nil, &errRunUnavailable{"还没有运行记录库，没有可统计的"}
	}
	svc, err := runstats.OpenFile(file, runstats.Options{})
	if err != nil {
		return nil, errors.New("打不开运行记录库")
	}
	return svc, nil
}

func statsErr(err error) error {
	if errors.Is(err, runstats.ErrTimeout) {
		return errors.New("统计超时")
	}
	return errors.New("读运行记录库出错")
}

func (x *shellStats) Usage(ctx context.Context, r ctlproto.UsageRange) (ctlproto.UsageReport, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.UsageReport{}, err
	}
	defer svc.Close()
	rep, err := svc.Usage(ctx, r)
	if err != nil {
		return ctlproto.UsageReport{}, statsErr(err)
	}
	return rep, nil
}

func (x *shellStats) Quality(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.QualityReport{}, err
	}
	defer svc.Close()
	rep, err := svc.Quality(ctx, r, a)
	if err != nil {
		return ctlproto.QualityReport{}, statsErr(err)
	}
	return rep, nil
}

func (x *shellStats) Spans(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.SpansReport, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.SpansReport{}, err
	}
	defer svc.Close()
	rep, err := svc.Spans(ctx, r, a)
	if err != nil {
		return ctlproto.SpansReport{}, statsErr(err)
	}
	return rep, nil
}

func (x *shellStats) Rebuild(ctx context.Context, r ctlproto.UsageRange) (ctlproto.RebuildReport, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.RebuildReport{}, err
	}
	defer svc.Close()
	rep, err := svc.Rebuild(ctx, r)
	if err != nil {
		return ctlproto.RebuildReport{}, statsErr(err)
	}
	return rep, nil
}

func (x *shellStats) ConvTurns(ctx context.Context, conversation string, maxRuns int) (ctlproto.ConvTurnsReport, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.ConvTurnsReport{}, err
	}
	defer svc.Close()
	rep, err := svc.ConvTurns(ctx, conversation, maxRuns)
	if err != nil {
		return ctlproto.ConvTurnsReport{}, statsErr(err)
	}
	return rep, nil
}

// Resources 采一次资源。不开 trace.db：资源采样不持有任何数据库连接（规格-运行页 第四节第 5 条）。
// 采样很快（本进程实测见规格），但仍在读服务的后台线程里跑，界面线程只拿结果
func (x *shellStats) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	rep, err := x.s.sampler().Sample(ctx, true)
	if err != nil {
		return ctlproto.ResourcesReport{}, errors.New("采样资源出错")
	}
	return rep, nil
}

func (x *shellStats) RequestDetail(ctx context.Context, id string) (ctlproto.RequestDetail, error) {
	svc, err := x.openTrace()
	if err != nil {
		return ctlproto.RequestDetail{}, err
	}
	defer svc.Close()
	rep, err := svc.RequestDetail(ctx, id)
	if errors.Is(err, runstats.ErrNoRequest) {
		return ctlproto.RequestDetail{}, err
	}
	if err != nil {
		return ctlproto.RequestDetail{}, statsErr(err)
	}
	return rep, nil
}

// ForgetResources 离开资源标签时调：采样器丢掉前值、放掉性能计数器句柄（runpage.go 的 runStop）
func (x *shellStats) ForgetResources() { x.s.sampler().Forget() }

func (x *shellStats) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	svc, err := x.s.storageService()
	if err != nil {
		return ctlproto.StorageReport{}, &errRunUnavailable{err.Error()}
	}
	rep, err := svc.Storage(ctx)
	if err != nil {
		return ctlproto.StorageReport{}, errors.New("读存储统计出错")
	}
	return rep, nil
}

func (x *shellStats) Rescan(ctx context.Context) error {
	svc, err := x.s.storageService()
	if err != nil {
		return &errRunUnavailable{err.Error()}
	}
	return svc.Rescan(ctx)
}

func (x *shellStats) CancelScan() {
	if svc, err := x.s.storageService(); err == nil {
		svc.CancelScan()
	}
}
