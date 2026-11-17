package main

// 运行页的数据流（规格-运行页 第六、八节）：画页面在 runpage_view.go，用量、存储两个标签在 runpage_usage.go、runpage_storage.go。
// 只认 api.StatsReader（和 sumika-ctl stats 读同一份结构）：查库、扫目录都在读服务的后台线程，界面线程只并结果。
// 每次请求号加一，晚到的旧结果对不上就丢；换了读服务整页清回空。
// 订阅只在"运行页的用量或存储标签在前台"时活着：离开标签、切到别的页、窗口隐藏或最小化，定时器和在途查询全部取消，
// 没有订阅者时没有任何定时器和常驻协程（规格第八节第 5 条，测试用协程数钉住）；回来立刻补读一次。
// 读失败时保留上一次的数据并标"已过期"，没有旧数据才显示出错。

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/storagestat"
)

// 运行页的标签，顺序就是标签栏上的顺序（资源放在状态后面）。不放占位：没做的标签不进这张表
const (
	runTabStatus    = iota // 原来的"后台状态"：托盘"查看后台状态"落在这里
	runTabResources        // 资源：进程表、主程序内部、后台队列
	runTabUsage            // 用量
	runTabStorage          // 存储
	runTabQuality          // 质量
)

var runTabNames = []string{"状态", "资源", "用量", "存储", "质量"}

// 自动刷新的间隔：用量范围含"今天"时、存储读增量时 30 秒一次（规格第八节第 4 条）；存储扫描进行中读进度更勤。测试里改小
var (
	runRefresh  = 30 * time.Second
	runScanPoll = 700 * time.Millisecond
)

// 资源标签的采样间隔选项（规格第四节第 5 条：1 / 2 / 5 秒和暂停；0 就是暂停，不起定时器）。默认第二项 2 秒。测试里改小
var runResIntervals = [4]time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 0}

var runResLabels = []string{"1 秒", "2 秒", "5 秒", "暂停"}

// 用量的时间范围选项。键稳定，标签是界面用的整词
var runRanges = []struct{ Key, Label string }{
	{"today", "今天"}, {"7d", "最近 7 天"}, {"30d", "最近 30 天"}, {"month", "本月"}, {"all", "全部"},
}

// errRunUnavailable 是"这个读数现在读不了，而且不是临时出错"：后台没起来、数据目录没就绪、库还没建。
// 界面写"不可用"和原因，不当出错重试
type errRunUnavailable struct{ Reason string }

func (e *errRunUnavailable) Error() string { return e.Reason }

// runBlock 是一个读数块：最近一份能用的数据，加上最近一次读的结果。
// 读失败时 data 不动、err 有值：有旧数据就是"已过期"，没有就是出错
type runBlock[T any] struct {
	has     bool
	data    T
	loading bool
	load    loadstate.Loader // 什么时候该画骨架、"刷新中…"（延迟出现、出现后留够；见 loadstate）
	err     error
	at      time.Time // 数据是什么时候读到的
	req     uint64    // 请求号
	ver     uint64    // 数据版本：每读到一份新数据加一，build 里的聚合结果按它判断要不要重算
}

func (b *runBlock[T]) stale() bool { return b.has && b.err != nil }

func (b *runBlock[T]) unavailable() bool {
	var u *errRunUnavailable
	return b.err != nil && errors.As(b.err, &u)
}

// runPage 是运行页的状态，整个窗口一份
type runPage struct {
	src  api.StatsReader
	scan api.StorageScanner // 没有就不给"重新统计"
	gen  uint64             // 代次：换读服务、离开页面都加一，在途的旧结果对不上就丢

	tab    int
	cancel context.CancelFunc // 订阅和所有在途查询的总开关；nil = 现在没有订阅
	life   context.Context
	subTab int // 订阅属于哪个标签；切标签就换订阅

	now func() time.Time // 测试里固定；nil 用 time.Now

	// 用量
	rangeKey  string // runRanges 的键；空 = 最近 7 天
	byWeek    bool
	usage     runBlock[ctlproto.UsageReport]
	usageTbl  usageTable
	dayList   ui.ListState
	taskPick  string // 类别筛选；空 = 全部
	modelPick string // 模型筛选；空 = 全部

	// 存储
	store    runBlock[ctlproto.StorageReport]
	storeTbl storageTable
	pollStop context.CancelFunc // 扫描进度的轮询；nil = 没在轮询

	// 质量
	quality    runBlock[qualityData]
	qualityTbl qualityState

	// 资源：只在标签在前台时采样；离开时数据留着（先显示旧的再补读），采样器的前值由读服务丢掉
	res     runBlock[ctlproto.ResourcesReport]
	resTbl  resourceState
	resPick int // 采样间隔选项的下标加一（0 = 没选过，用默认的 2 秒；见 resIdx）
}

// usageTable、storageTable 是两张表的展开和排序状态（OutlineTable：第一列带展开箭头，行可以点开到按模型、按平台）
type usageTable struct {
	out   ui.OutlineState[string]
	sort  ui.SortOrder
	cache usageModel
}

type storageTable struct {
	out   ui.OutlineState[string]
	sort  ui.SortOrder
	cache storageModel
}

// resIdx 是资源标签采样间隔选项的下标：没选过就是默认的 2 秒（零值不能直接当下标，不然默认成了 1 秒）
func (p *runPage) resIdx() int {
	if p.resPick == 0 {
		return 1
	}
	return p.resPick - 1
}

func (p *runPage) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// attachStats 换读服务（界面线程）：旧订阅、旧请求全取消，整页清回空；nil = 后台没起来
func (a *app) attachStats(src api.StatsReader, scan api.StorageScanner) {
	p := &a.run
	if p.src == src && p.scan == scan {
		return
	}
	a.runStop()
	*p = runPage{gen: p.gen + 1, src: src, scan: scan, tab: p.tab, rangeKey: p.rangeKey, byWeek: p.byWeek, now: p.now, resPick: p.resPick}
	a.runSync()
}

// runWant 现在要不要订阅：读服务在、用量或存储标签在前台、窗口看得见
func (a *app) runWant() bool {
	p := &a.run
	return p.src != nil && a.rail == 3 && p.tab != runTabStatus && !a.away.Load()
}

// runSync 让订阅跟着"运行页的用量或存储标签在前台"走（界面线程；view 每帧、窗口显示 / 隐藏 / 最小化 / 还原、换读服务时调）
func (a *app) runSync() {
	p := &a.run
	want := a.runWant()
	if want && p.cancel != nil && p.subTab != p.tab {
		a.runStop() // 切了标签：换一份订阅
	}
	if want == (p.cancel != nil) {
		return
	}
	if want {
		a.runStart()
	} else {
		a.runStop()
	}
}

func (a *app) runStart() {
	p := &a.run
	ctx, cancel := context.WithCancel(a.base())
	p.life, p.cancel, p.subTab = ctx, cancel, p.tab
	switch p.tab {
	case runTabResources:
		a.runFetchResources()
	case runTabUsage:
		a.runFetchUsage()
	case runTabStorage:
		a.runFetchStorage()
	case runTabQuality:
		a.runFetchQuality()
	}
	period := runRefresh
	if p.tab == runTabResources {
		period = runResIntervals[p.resIdx()]
	}
	if period > 0 { // 资源标签选了"暂停"：订阅还在（显示最后一次的时间），但不起定时器
		go a.runTicker(ctx, p.gen, p.tab, period)
	}
}

// runStop 离开：订阅和在途查询全取消，代次加一让晚到的结果作废。数据留着，回来先显示它再补读
func (a *app) runStop() { a.runStopKeep(false) }

// runStopKeep 同 runStop；keepSampler 为真时不让采样器丢前值。换采样间隔时订阅马上重起，
// 丢掉前值会让紧接着的那次采样算不出 CPU / GPU，整个周期显示"—"（晚到的 Forget 还会清掉新采样记下的前值）
func (a *app) runStopKeep(keepSampler bool) {
	p := &a.run
	if p.cancel == nil {
		return
	}
	p.cancel()
	leftRes := p.subTab == runTabResources
	p.cancel, p.life, p.pollStop = nil, nil, nil
	p.gen++
	p.usage.loading, p.store.loading, p.quality.loading, p.res.loading = false, false, false, false
	p.res.load.Cancel()
	p.usage.load.Cancel()
	p.store.load.Cancel()
	p.quality.load.Cancel()
	if f, ok := p.src.(resourceForgetter); ok && leftRes && !keepSampler {
		go f.ForgetResources() // 采样器丢掉前值、放掉性能计数器句柄；可能要等正在跑的那次采样收尾，所以不在界面线程
	}
}

// runLife 页面在前台这段时间的 ctx：查询都挂在它下面，离开页面时一起取消
func (a *app) runLife() context.Context {
	if a.run.life != nil {
		return a.run.life
	}
	return a.base()
}

// runTicker 是订阅期间唯一的定时器：到点只往界面线程发一个小函数，由它按当时的状态决定读不读
func (a *app) runTicker(ctx context.Context, gen uint64, tab int, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.post(func() {
				p := &a.run
				if p.gen != gen || ctx.Err() != nil {
					return
				}
				switch tab {
				case runTabResources:
					if !p.res.loading { // 上一次还没回来就不再叠一次
						a.runFetchResources()
					}
				case runTabUsage:
					if a.runRangeHasToday() {
						a.runFetchUsage()
					}
				case runTabStorage:
					a.runFetchStorage()
				case runTabQuality:
					if a.runRangeHasToday() {
						a.runFetchQuality()
					}
				}
			})
		}
	}
}

// runRange 把选中的范围算成具体的一段时间。今天、最近 N 天、全部交给读服务的 RangeOf（和命令行同一个算法），本月在这里按本地零点算
func (a *app) runRange() (ctlproto.UsageRange, error) {
	p := &a.run
	switch p.rangeKey {
	case "today":
		return p.src.RangeOf(ctlproto.StatsArgs{Days: 1})
	case "30d":
		return p.src.RangeOf(ctlproto.StatsArgs{Days: 30})
	case "all":
		return p.src.RangeOf(ctlproto.StatsArgs{All: true})
	case "month":
		now := p.clock().In(time.Local)
		y, m, _ := now.Date()
		from := time.Date(y, m, 1, 0, 0, 0, 0, time.Local)
		to := time.Date(y, m+1, 1, 0, 0, 0, 0, time.Local)
		return ctlproto.UsageRange{Kind: "custom", Label: "本月", FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Zone: time.Local.String()}, nil
	}
	return p.src.RangeOf(ctlproto.StatsArgs{Days: ctlproto.StatsDefaultDays})
}

// runRangeHasToday 当前范围含不含"现在"：含才自动刷新，其余范围只在手动刷新时查
func (a *app) runRangeHasToday() bool {
	if a.run.usage.has {
		r := a.run.usage.data.Range
		now := a.run.clock().UnixMilli()
		return r.ToMS == math.MaxInt64 || (r.FromMS <= now && now < r.ToMS)
	}
	return true
}

// runFetchUsage 读一次用量（界面线程发起，后台线程查，结果回界面线程）。有旧数据时不显示加载，旧的先留着
func (a *app) runFetchUsage() {
	p := &a.run
	if p.src == nil {
		return
	}
	p.usage.req++
	req, gen, src, ctx := p.usage.req, p.gen, p.src, a.runLife()
	p.usage.loading = true
	p.usage.load.Begin()
	rng, err := a.runRange()
	go func() {
		var rep ctlproto.UsageReport
		if err == nil {
			rep, err = src.Usage(ctx, rng)
		}
		a.post(func() {
			if p.gen != gen || p.usage.req != req {
				return
			}
			p.usage.loading = false
			if err != nil {
				p.usage.err = err
				p.usage.load.Fail()
				return
			}
			p.usage.data, p.usage.has, p.usage.err, p.usage.at = rep, true, nil, p.clock()
			p.usage.ver++
			p.usage.load.Succeed()
		})
	}()
}

// runFetchStorage 读一次存储（快照加增量，不遍历大目录）。扫描在进行就接着轮询进度
func (a *app) runFetchStorage() {
	p := &a.run
	if p.src == nil {
		return
	}
	p.store.req++
	req, gen, src, ctx := p.store.req, p.gen, p.src, a.runLife()
	p.store.loading = true
	p.store.load.Begin()
	go func() {
		rep, err := src.Storage(ctx)
		a.post(func() {
			if p.gen != gen || p.store.req != req {
				return
			}
			p.store.loading = false
			if err != nil {
				p.store.err = err
				p.store.load.Fail()
				a.runPoll(false)
				return
			}
			p.store.data, p.store.has, p.store.err, p.store.at = rep, true, nil, p.clock()
			p.store.ver++
			p.store.load.Succeed()
			a.runPoll(rep.Scan.Running)
		})
	}()
}

// runPoll 扫描进行中每隔一会儿读一次进度，结束了就停。轮询挂在订阅的 ctx 下，离开页面就没了（扫描本身不取消）
func (a *app) runPoll(running bool) {
	p := &a.run
	if !running {
		if p.pollStop != nil {
			p.pollStop()
			p.pollStop = nil
		}
		return
	}
	if p.pollStop != nil || p.life == nil {
		return
	}
	ctx, cancel := context.WithCancel(p.life)
	p.pollStop = cancel
	gen := p.gen
	go func() {
		t := time.NewTicker(runScanPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.post(func() {
					if p.gen == gen && ctx.Err() == nil && !p.store.loading {
						a.runFetchStorage()
					}
				})
			}
		}
	}()
}

// runRescan 点"重新统计"：后台开扫，马上返回；进度靠轮询
func (a *app) runRescan() {
	p := &a.run
	if p.scan == nil {
		return
	}
	scan, gen, ctx := p.scan, p.gen, a.runLife()
	go func() {
		err := scan.Rescan(ctx)
		a.post(func() {
			if p.gen != gen {
				return
			}
			if err != nil && !errors.Is(err, storagestat.ErrScanning) { // 已经在统计不算错，读一次进度就行
				p.store.err = err
				return
			}
			a.runFetchStorage()
		})
	}()
}

func (a *app) runCancelScan() {
	if a.run.scan != nil {
		a.run.scan.CancelScan()
		a.runFetchStorage()
	}
}
