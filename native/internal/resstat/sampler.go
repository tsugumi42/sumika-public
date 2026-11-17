// Package resstat 采集运行页资源标签的数据（规格-运行页 第四节）：进程表（CPU、内存、显存）、主程序内部（Go 运行时）、后台队列。
// 只读，不结束、不操作任何进程，不持有数据库连接，没有协程和定时器：Sample 只在页面或命令行要数据的那一刻跑一次，
// 两次采样之差算 CPU，前一次的数据只是 Sampler 里的一份小表。
// Windows 的系统调用在 *_windows.go；别的平台是空实现，能编译，进程表只剩主程序自己。
package resstat

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/procreg"
	"sumika-native/internal/runmetrics"
)

// 采样间隔相关的常数
const (
	// MinGap：距上一次采样不到这么久就直接返回上一份结果，不重采。两个读者（界面、命令行）撞在一起，
	// 或者界面连点刷新时，CPU 的差值只有几毫秒，数是噪声
	MinGap = 200 * time.Millisecond
	// MaxAge：上一次采样比这更早就当没有前值。页面关了又开，前值是几分钟前的，算出来的是那段时间的平均 CPU，不是即时值
	MaxAge = 30 * time.Second
	// warmGap：命令行没有前值时，自己隔这么久采第二次，不给"—"
	warmGap = 250 * time.Millisecond
	// listEvery：进程列表（Toolhelp 快照）最多这么久重列一次。实测列一次要 20 多毫秒（进程多的时候），
	// 每次采样都列就超了"页面打开时单核 1%"的预算；两次重列之间只读已知进程的数，新起的登记进程会强制重列，
	// 没登记的新子进程最多晚这么久才出现在表里
	listEvery = 5 * time.Second
)

// procInfo 是一个进程这一刻的读数。各个 has 字段为假表示系统没给（没权限或老系统），不是 0
type procInfo struct {
	created    int64         // 创建时间，纳秒（procreg.CreationTime 同口径）
	cpu        time.Duration // 内核态加用户态的累计 CPU 时间
	hasCPU     bool
	privateWS  int64
	hasPrivate bool
	workingSet int64
	hasWS      bool
	commit     int64
	hasCommit  bool
}

// errGone 是 info 的一种错误：这个 pid 已经没有进程了（和"没权限读"区分开：没权限的行留着，数值留空）
var errGone = errors.New("进程已经退出")

// procEntry 是进程列表里的一项（Windows 里来自 Toolhelp 快照）
type procEntry struct {
	pid, ppid int
	name      string
}

// system 是读进程的系统调用，测试里换成假的
type system interface {
	list() ([]procEntry, error)
	info(pid int) (procInfo, error)
	creation(pid int) int64
}

// GPUReading 是显存和 GPU 引擎的读数，按 pid 汇总
type GPUReading struct {
	Memory map[int]int64   // 专用显存字节数
	Util   map[int]float64 // 引擎利用率百分比；第一次采样没有值，map 里就没有这个 pid
}

// GPUSource 读显存。取不到（计数器被禁用、权限不足、系统没有）时返回错误，页面整列写"不可用"和原因，不写 0。
// Close 放掉占着的系统句柄，之后再 Read 会重新打开
type GPUSource interface {
	Read() (GPUReading, error)
	Close()
}

// Options 是 Sampler 的选项，零值可用
type Options struct {
	Registry *procreg.Registry    // nil 用 procreg.Default
	Queues   *runmetrics.Registry // nil 用 runmetrics.Default
	GPU      GPUSource            // nil 用本机的实现（Windows 是 PDH）
	Now      func() time.Time     // 测试里固定
	Self     int                  // 主程序的 pid；0 用本进程
	Cores    int                  // 逻辑核数；0 用 runtime.NumCPU
	sys      system               // 测试里换成假的
}

// Sampler 保存上一次采样的 CPU 时间，让两次采样之差能算出 CPU 占用。可以被界面和命令行同时用
type Sampler struct {
	mu       sync.Mutex
	reg      *procreg.Registry
	queues   *runmetrics.Registry
	gpu      GPUSource
	now      func() time.Time
	self     int
	cores    int
	sys      system
	prev     map[procKey]time.Duration
	prevAt   time.Time
	procs    []procEntry // 上一次列出来的进程（listEvery 内复用）
	procsAt  time.Time
	gpuWarm  bool // 性能计数器是不是已经打开过：第一次采样不去开它（要 400 多毫秒），下一次再开
	last     ctlproto.ResourcesReport
	lastAt   time.Time
	lastGPU  bool
	haveLast bool
}

type procKey struct {
	pid     int
	created int64
}

// New 建采样器。不起任何协程
func New(o Options) *Sampler {
	s := &Sampler{reg: o.Registry, queues: o.Queues, gpu: o.GPU, now: o.Now, self: o.Self, cores: o.Cores, sys: o.sys}
	if s.reg == nil {
		s.reg = procreg.Default
	}
	if s.queues == nil {
		s.queues = runmetrics.Default
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.self == 0 {
		s.self = selfPID()
	}
	if s.cores <= 0 {
		s.cores = runtime.NumCPU()
	}
	if s.sys == nil {
		s.sys = newSystem()
	}
	if s.gpu == nil {
		s.gpu = newGPU()
	}
	return s
}

// Forget 丢掉前值和缓存，并放掉性能计数器句柄：离开资源标签时调，下次打开从"没有前值"开始
func (s *Sampler) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prev, s.haveLast, s.procs, s.gpuWarm = nil, false, nil, false
	if s.gpu != nil {
		s.gpu.Close()
	}
}

// Sample 采一次。gpu 为真才读显存（GPU 列不可见时不采，规格第四节第 5 条）。
// 没有可用的前值时 CPU 全是 nil、IntervalMS 是 0；有前值时按两次采样的间隔算。
// 距上一次采样不到 MinGap 就直接给上一份（拷了一层，调用方改不到缓存）
func (s *Sampler) Sample(ctx context.Context, gpu bool) (ctlproto.ResourcesReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sampleLocked(ctx, gpu, false)
}

// SampleWarm 给命令行用：没有前值时自己隔一小会儿采两次，CPU 不给"—"
func (s *Sampler) SampleWarm(ctx context.Context, gpu bool) (ctlproto.ResourcesReport, error) {
	s.mu.Lock()
	warm := s.prev == nil || s.now().Sub(s.prevAt) > MaxAge
	if warm {
		if _, err := s.sampleLocked(ctx, gpu, true); err != nil {
			s.mu.Unlock()
			return ctlproto.ResourcesReport{}, err
		}
	}
	s.mu.Unlock()
	if warm {
		select {
		case <-time.After(warmGap):
		case <-ctx.Done():
			return ctlproto.ResourcesReport{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sampleLocked(ctx, gpu, true)
}

// sampleLocked 采一次。eager 为假时，性能计数器还没打开过就先不开：打开要 400 多毫秒，页面第一次采样不该为它多等，
// 这一次 GPU 列写"还在准备"（Pending），下一次采样再开；命令行（eager）当场开
func (s *Sampler) sampleLocked(ctx context.Context, wantGPU, eager bool) (ctlproto.ResourcesReport, error) {
	if err := ctx.Err(); err != nil {
		return ctlproto.ResourcesReport{}, err
	}
	now := s.now()
	if gap := now.Sub(s.lastAt); s.haveLast && gap >= 0 && gap < MinGap && (s.lastGPU || !wantGPU) {
		return cloneReport(s.last), nil
	}
	rep := ctlproto.ResourcesReport{SampledAtMS: now.UnixMilli(), Cores: s.cores}
	rep.Processes, rep.MemoryKind = s.processes(now, &rep)
	pending := false
	switch {
	case wantGPU && !eager && !s.gpuWarm:
		s.gpuWarm, pending = true, true
		rep.GPU = ctlproto.GPUStatus{Pending: true}
	case wantGPU:
		s.gpuWarm = true
		s.applyGPU(&rep)
	default:
		rep.GPU = ctlproto.GPUStatus{Reason: "没有采样"}
	}
	rep.Runtime = readRuntime()
	if rep.MemoryKind == ctlproto.MemPrivate && len(rep.Processes) > 0 && rep.Processes[0].Memory != nil {
		// 第一行永远是主程序：其他 = 主程序专用工作集 - Go 运行时总量；差为负说明两个数不是同一时刻读的，不编
		if other := *rep.Processes[0].Memory - rep.Runtime.Total; other >= 0 {
			rep.Runtime.Other = &other
		}
	}
	rep.Queues = s.queues.Rows()
	s.last, s.lastAt, s.lastGPU, s.haveLast = cloneReport(rep), now, wantGPU && !pending, true
	return rep, nil
}

func cloneReport(r ctlproto.ResourcesReport) ctlproto.ResourcesReport {
	r.Processes = append([]ctlproto.ProcessRow(nil), r.Processes...)
	r.Queues = append([]ctlproto.QueueRow(nil), r.Queues...)
	return r
}

type procNode struct {
	cat, label string
	info       procInfo
	infoOK     bool
}

// processes 列出主程序、它的后代和登记过的子进程，算 CPU 和内存。返回的表第一行是主程序，其余按树和 CPU 降序排；
// 第二个返回值是内存列的口径
func (s *Sampler) processes(now time.Time, rep *ctlproto.ResourcesReport) ([]ctlproto.ProcessRow, string) {
	regs := s.reg.Snapshot()
	all := s.procs
	refresh := all == nil || now.Sub(s.procsAt) >= listEvery || now.Before(s.procsAt)
	if !refresh { // 有新登记的进程（上次列表里没有）也要重列，不然最多晚 listEvery 才看得到
		known := make(map[int]bool, len(all))
		for _, e := range all {
			known[e.pid] = true
		}
		for _, r := range regs {
			if !known[r.PID] {
				refresh = true
				break
			}
		}
	}
	if refresh {
		all, _ = s.sys.list() // 列不出来就只剩主程序加登记的；下面对"不在列表里"的登记项一律丢
		s.procs, s.procsAt = all, now
	}
	byPID := make(map[int]procEntry, len(all)+1)
	kids := map[int][]int{}
	for _, e := range all {
		byPID[e.pid] = e
		kids[e.ppid] = append(kids[e.ppid], e.pid)
	}
	if _, ok := byPID[s.self]; !ok {
		byPID[s.self] = procEntry{pid: s.self, name: selfName()}
	}
	nodes := map[int]*procNode{s.self: {cat: ctlproto.ProcMain}}
	// 后代：按父进程号往下找，登记簿里没有的显示成"未登记"，让漏登记的地方自己暴露
	var walk func(pid int)
	walk = func(pid int) {
		for _, c := range kids[pid] {
			if _, ok := nodes[c]; ok {
				continue
			}
			nodes[c] = &procNode{cat: ctlproto.ProcUnregistered}
			walk(c)
		}
	}
	walk(s.self)
	// 登记的：核对创建时间，pid 被系统复用了的作废；进程已经不在列表里的也不显示
	for _, r := range regs {
		if _, alive := byPID[r.PID]; !alive || r.PID == s.self {
			continue
		}
		if r.Created != 0 {
			if cur := s.sys.creation(r.PID); cur != 0 && cur != r.Created {
				continue
			}
		}
		nodes[r.PID] = &procNode{cat: mapCategory(r.Category), label: r.Label}
	}
	for pid, n := range nodes {
		var err error
		if n.info, err = s.sys.info(pid); err == nil {
			n.infoOK = true
		} else if errors.Is(err, errGone) && pid != s.self {
			delete(nodes, pid) // 复用的旧列表里已经退出的进程
		}
	}
	// 内存口径：主程序读得到专用工作集就整列用它，否则整列退回工作集，不混用
	kind := ctlproto.MemWorkingSet
	if root := nodes[s.self]; root.infoOK && root.info.hasPrivate {
		kind = ctlproto.MemPrivate
	}
	elapsed := now.Sub(s.prevAt)
	usePrev := s.prev != nil && elapsed > 0 && elapsed <= MaxAge
	if usePrev {
		rep.IntervalMS = elapsed.Milliseconds()
	}
	next := make(map[procKey]time.Duration, len(nodes))
	rows := make(map[int]*ctlproto.ProcessRow, len(nodes))
	for pid, n := range nodes {
		row := &ctlproto.ProcessRow{PID: pid, Name: byPID[pid].name, Category: n.cat, Label: n.label}
		if row.Name == "" {
			row.Name = "（名字没取到）"
		}
		if n.infoOK {
			if n.info.hasCPU {
				k := procKey{pid, n.info.created}
				next[k] = n.info.cpu
				if before, ok := s.prev[k]; ok && usePrev && n.info.cpu >= before {
					v := float64(n.info.cpu-before) / float64(elapsed) / float64(s.cores) * 100
					row.CPU = &v
				}
			}
			switch {
			case kind == ctlproto.MemPrivate && n.info.hasPrivate:
				v := n.info.privateWS
				row.Memory = &v
			case kind == ctlproto.MemWorkingSet && n.info.hasWS:
				v := n.info.workingSet
				row.Memory = &v
			}
			if n.info.hasCommit {
				v := n.info.commit
				row.Commit = &v
			}
		}
		rows[pid] = row
	}
	s.prev, s.prevAt = next, now
	// 树：父进程不在表里的（登记的进程被换了父亲之类）挂在主程序下
	children := map[int][]int{}
	for pid, row := range rows {
		if pid == s.self {
			continue
		}
		p := byPID[pid].ppid
		if _, ok := rows[p]; !ok || p == pid {
			p = s.self
		}
		row.Parent = p
		children[p] = append(children[p], pid)
	}
	less := func(a, b int) bool { // CPU 降序，没有 CPU 的排在后面，再按 pid
		ca, cb := rows[a].CPU, rows[b].CPU
		switch {
		case ca != nil && cb != nil && *ca != *cb:
			return *ca > *cb
		case (ca != nil) != (cb != nil):
			return ca != nil
		}
		return a < b
	}
	out := make([]ctlproto.ProcessRow, 0, len(rows))
	var emit func(pid, depth int)
	emit = func(pid, depth int) {
		rows[pid].Depth = depth
		out = append(out, *rows[pid])
		ks := children[pid]
		sort.Slice(ks, func(i, j int) bool { return less(ks[i], ks[j]) })
		for _, c := range ks {
			emit(c, depth+1)
		}
	}
	emit(s.self, 0)
	return out, kind
}

// mapCategory 把登记簿的类别键换成协议里的；不认识的当"未登记"，不丢
func mapCategory(c string) string {
	switch c {
	case procreg.CatModels:
		return ctlproto.ProcModels
	case procreg.CatAnalyzer:
		return ctlproto.ProcAnalyzer
	case procreg.CatMCP:
		return ctlproto.ProcMCP
	case procreg.CatShell:
		return ctlproto.ProcShell
	case procreg.CatMedia:
		return ctlproto.ProcMedia
	}
	return ctlproto.ProcUnregistered
}

// applyGPU 把显存和 GPU 引擎利用率并进进程表。读不到时整列标不可用并写原因，行里保持 nil，不写 0
func (s *Sampler) applyGPU(rep *ctlproto.ResourcesReport) {
	g, err := s.gpu.Read()
	if err != nil {
		rep.GPU = ctlproto.GPUStatus{Reason: err.Error()}
		return
	}
	rep.GPU = ctlproto.GPUStatus{Available: true}
	for i := range rep.Processes {
		pid := rep.Processes[i].PID
		if v, ok := g.Memory[pid]; ok {
			rep.Processes[i].GPUMemory = &v
		}
		if v, ok := g.Util[pid]; ok {
			rep.Processes[i].GPU = &v
		}
	}
}
