package selfcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/fileatomic"
	"sumika-native/internal/filelog"
)

const (
	// StartDelay 启动后等这么久才开始第一次评估，让启动期的积压（补录、重连）先走完
	StartDelay = 2 * time.Minute
	// CheckTimeout 是一条检查的评估时限，超了那一条标 unknown
	CheckTimeout = 2 * time.Second
	stateFile    = "state.json"
	baselinePre  = "baseline."
)

// Config 是 Runner 的配置
type Config struct {
	Dir       string // <数据目录>/selfcheck：自检只写这个目录
	Counters  *Counters
	Checks    []Check
	Trace     Querier
	Events    func() map[string]Querier
	Running   func() []string
	Pending   func() int                                        // 入库队列里排着、没做完的活的个数（S1 的静默点）
	Backup    func() BackupState                                // 备份调度器缓存的状态（S9）
	Drill     func() DrillState                                 // 备份抽验的状态（S12）
	Faults    func() []KernelFault                              // 回合内核没解除的故障（S10）
	StrayEnds func() int                                        // 没人认领的活动退出记录条数（S10，不挡受理）
	MCP       func(ctx context.Context) ([]MCPIsolation, error) // 被隔离的 MCP 服务（S11）
	Embed     func() EmbedState                                 // 本机嵌入的状态（S13）
	// StoreUnavailable 是打不开、已经停掉的平台库（S14）；nil = 没接，没有就是空
	StoreUnavailable func() []StoreUnavailable
	Classify         func(errText string) string
	// Warnf、Infof 记日志（状态从 ok 变 bad、从 bad 变回 ok 才记，同一个状态不重复）；nil 就不记
	Warnf, Infof func(format string, args ...any)
	// Redact 给例子里的字段值打码（通道号、会话号）；nil 原样
	Redact     func(string) string
	Now        func() time.Time
	Interval   time.Duration // 0 = 5 分钟
	StartDelay time.Duration // 0 = StartDelay；负数 = 不等
}

// CheckRow 是当天文件里一条检查的一行
type CheckRow struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	State     State             `json:"state"`
	Value     int64             `json:"value"`
	Threshold int64             `json:"threshold"`
	Since     string            `json:"since"`
	Example   map[string]string `json:"example,omitempty"`
	Note      string            `json:"note,omitempty"`
	Next      string            `json:"next,omitempty"`
}

// DayFile 是 <目录>/<日期>.json 的内容（字段名定死，巡检和状态行按它读）
type DayFile struct {
	Day      string           `json:"day"`
	Updated  string           `json:"updated"`
	Running  bool             `json:"running"`
	Checks   []CheckRow       `json:"checks"`
	Counters map[string]int64 `json:"counters"`
	Baseline map[string]int64 `json:"baseline"`
}

// Runner 周期评估各条检查，把结果写进当天的文件
type Runner struct {
	cfg Config
	kv  *KV

	mu       sync.Mutex // 评估、刷新、关闭串行
	day      string
	carry    [numCounters]int64 // 今天文件里已有的数（重启前的）
	mark     [numCounters]int64 // 本进程在今天开始时的计数器值
	rows     map[string]*CheckRow
	reported map[string]State // 日志里最近一次报的是 bad 还是 ok

	// 生命周期单独一把锁：Close 要先取消循环再等评估收尾，不能先等 mu（评估持着 mu 最坏要十几秒，关机会被拖慢）
	life    sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	closing bool

	closed bool // 受 mu 保护：Close 收尾以后 Evaluate、Start 不再做事
}

// New 建一个 Runner：读今天的文件把计数和状态续上（崩溃重启后当天的数不从零开始），读坏了当没有
func New(cfg Config) *Runner {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.Counters == nil {
		cfg.Counters = NewCounters()
	}
	r := &Runner{cfg: cfg, rows: map[string]*CheckRow{}, reported: map[string]State{}, kv: &KV{m: map[string]int64{}}}
	now := cfg.Now()
	r.day = now.Format(filelog.DayLayout)
	// mark 起点是零：进程里 New 之前数到的也算今天的（计数器比 Runner 先建，启动期的消息不能漏）
	if f, err := readDayFile(cfg.Dir, r.day); err == nil {
		for i := Counter(0); i < numCounters; i++ {
			r.carry[i] = f.Counters[i.Name()]
		}
		for i := range f.Checks {
			row := f.Checks[i]
			r.rows[row.ID] = &row
			if row.State == Bad || row.State == Ok {
				r.reported[row.ID] = row.State
			}
		}
	}
	r.loadState()
	return r
}

func (r *Runner) log(warn bool, format string, args ...any) {
	f := r.cfg.Infof
	if warn {
		f = r.cfg.Warnf
	}
	if f != nil {
		f(format, args...)
	}
}

// Start 先写一份"启动中"的文件（状态行马上有东西可读），再开后台循环：等 StartDelay 以后第一次评估，之后每个 Interval 一次。
func (r *Runner) Start(ctx context.Context) {
	r.life.Lock()
	if r.cancel != nil || r.closing {
		r.life.Unlock()
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	done := make(chan struct{})
	r.done = done
	r.life.Unlock()
	r.mu.Lock()
	if !r.closed {
		r.flushLocked(r.cfg.Now(), true)
	}
	r.mu.Unlock()
	go r.loop(ctx, done)
}

func (r *Runner) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	delay := r.cfg.StartDelay
	if delay == 0 {
		delay = StartDelay
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	tk := time.NewTicker(r.cfg.Interval)
	defer tk.Stop()
	for {
		r.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

// Close 停下循环，把当天的文件最后刷新一次（running 记成 false）
func (r *Runner) Close() {
	// 先取消、再等：评估正持着 mu 查库（最坏 7 条各 2 秒加 S8 的 5 秒），取消以后它在下一条检查前就收手
	r.life.Lock()
	if r.closing {
		r.life.Unlock()
		return
	}
	r.closing = true
	cancel, done := r.cancel, r.done
	r.life.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	r.mu.Lock()
	r.closed = true
	r.flushLocked(r.cfg.Now(), false)
	r.mu.Unlock()
}

// Evaluate 评估一轮：每条检查各有时限，出错、超时、panic 都记成 unknown；评估完刷新文件
func (r *Runner) Evaluate(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	now := r.cfg.Now()
	r.rolloverLocked(now)
	env := &Env{Now: now, Live: r.cfg.Counters, Day: r.dayCountsLocked(), Trace: r.cfg.Trace, Events: r.cfg.Events, Running: r.cfg.Running, Pending: r.cfg.Pending, Backup: r.cfg.Backup, Drill: r.cfg.Drill, Faults: r.cfg.Faults, MCP: r.cfg.MCP, Embed: r.cfg.Embed, StoreUnavailable: r.cfg.StoreUnavailable, StrayEnds: r.cfg.StrayEnds, Classify: r.cfg.Classify, KV: r.kv}
	for _, c := range r.cfg.Checks {
		if ctx.Err() != nil {
			break // 关机或调用方取消了：后面的不评估了，已有的行保持原样
		}
		to := CheckTimeout
		if t, ok := c.(interface{ Timeout() time.Duration }); ok {
			to = t.Timeout() // 个别检查（S8 第一次数历史基线）要更久
		}
		cctx, cancel := context.WithTimeout(ctx, to)
		res := safeEval(cctx, c, env)
		if ctx.Err() != nil {
			cancel()
			break // 整轮被打断：这一条的结果不可信（可能是被打断才失败），丢掉，不把它记成 unknown 覆盖上一次的判定
		}
		if cctx.Err() != nil && res.State != Skip {
			res = Result{State: Unknown, Note: "评估超时或被打断"}
		}
		cancel()
		r.applyLocked(c, res, now)
	}
	r.flushLocked(now, true)
}

func safeEval(ctx context.Context, c Check, env *Env) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Result{State: Unknown, Note: fmt.Sprintf("评估时出了意外：%v", p)}
		}
	}()
	return c.Eval(ctx, env)
}

func (r *Runner) applyLocked(c Check, res Result, now time.Time) {
	if res.State == Skip {
		if _, ok := r.rows[c.ID()]; !ok {
			// 还从来没判过：占一行 unknown，不能让它看起来像没这条检查
			r.rows[c.ID()] = &CheckRow{ID: c.ID(), Name: c.Name(), State: Unknown, Threshold: res.Threshold, Note: "还没有判定过（等静默点）"}
		}
		return
	}
	row, ok := r.rows[c.ID()]
	if !ok {
		row = &CheckRow{ID: c.ID()}
		r.rows[c.ID()] = row
	}
	prev := row.State
	row.Name = c.Name()
	row.State, row.Value, row.Threshold, row.Note, row.Next = res.State, res.Value, res.Threshold, res.Note, res.Next
	row.Example = r.redact(res.Example)
	switch res.State {
	case Bad:
		if prev != Bad || row.Since == "" {
			row.Since = now.Format(time.RFC3339)
		}
		if r.reported[c.ID()] != Bad {
			r.reported[c.ID()] = Bad
			r.log(true, "自检异常：%s %s（值 %d，阈值 %d%s）", c.ID(), c.Name(), res.Value, res.Threshold, exampleText(row.Example))
		}
	case Ok:
		row.Since = ""
		if r.reported[c.ID()] == Bad {
			r.reported[c.ID()] = Ok
			r.log(false, "自检恢复：%s", c.ID())
		}
	default:
		row.Since = ""
	}
}

func (r *Runner) redact(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if r.cfg.Redact != nil {
			v = r.cfg.Redact(v)
		}
		out[k] = v
	}
	return out
}

func exampleText(ex map[string]string) string {
	if len(ex) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ex))
	for k := range ex {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + ex[k]
	}
	return "，例子 " + strings.Join(parts, " ")
}

// rolloverLocked 过了零点：先把昨天的文件按现在的数最后写一次（零点到这次评估之间数到的归给昨天，宁可差几分钟也不丢），
// 再新建当天的文件，计数从零开始，不带昨天的；各条检查的状态接着用
func (r *Runner) rolloverLocked(now time.Time) {
	day := now.Format(filelog.DayLayout)
	if day == r.day {
		return
	}
	r.writeDayLocked(now, true)
	r.day = day
	r.carry = [numCounters]int64{}
	r.mark = r.cfg.Counters.snapshot()
}

func (r *Runner) dayCountsLocked() map[string]int64 {
	cur := r.cfg.Counters.snapshot()
	out := make(map[string]int64, numCounters)
	for i := Counter(0); i < numCounters; i++ {
		out[i.Name()] = r.carry[i] + cur[i] - r.mark[i]
	}
	return out
}

func (r *Runner) flushLocked(now time.Time, running bool) {
	r.rolloverLocked(now)
	r.writeDayLocked(now, running)
}

// writeDayLocked 把当前的状态写进 r.day 那一天的文件
func (r *Runner) writeDayLocked(now time.Time, running bool) {
	f := DayFile{Day: r.day, Updated: now.Format(time.RFC3339), Running: running, Counters: r.dayCountsLocked(), Baseline: map[string]int64{}, Checks: []CheckRow{}}
	// 检查的顺序按配置里给的顺序，不按 map 的随机顺序，巡检把表原样贴进报告
	for _, c := range r.cfg.Checks {
		if row, ok := r.rows[c.ID()]; ok {
			f.Checks = append(f.Checks, *row)
		}
	}
	kv, dirty := r.kv.copyOut()
	for k, v := range kv {
		if strings.HasPrefix(k, baselinePre) {
			f.Baseline[strings.TrimPrefix(k, baselinePre)] = v
		}
	}
	if err := writeJSON(filepath.Join(r.cfg.Dir, r.day+".json"), f); err != nil {
		r.log(true, "写当天的自检文件失败：%v", err)
	}
	if dirty {
		if err := writeJSON(filepath.Join(r.cfg.Dir, stateFile), kv); err != nil {
			r.log(true, "写自检状态文件失败：%v", err)
		}
	}
}

func (r *Runner) loadState() {
	b, err := os.ReadFile(filepath.Join(r.cfg.Dir, stateFile))
	if err == nil {
		var m map[string]int64
		if json.Unmarshal(b, &m) == nil && m != nil {
			r.kv.m = m
			return
		}
	}
	r.kv.Fresh = true
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := fileatomic.Replace(tmp, path, time.Second); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func readDayFile(dir, day string) (DayFile, error) {
	var f DayFile
	b, err := os.ReadFile(filepath.Join(dir, day+".json"))
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return DayFile{}, err
	}
	return f, nil
}

// Summary 是 sumika-ctl status 里"自检："后面那句。读今天的文件（刚过零点还没写新文件时读昨天的）：
// 正常 / N 项异常（id、id），详见 <文件> / 未更新（文件超过 15 分钟没动，桌面程序却在运行）。只读文件，不依赖 Runner。
func Summary(dir string, now time.Time) string {
	day := now.Format(filelog.DayLayout)
	f, err := readDayFile(dir, day)
	if err != nil && os.IsNotExist(err) {
		yday := now.AddDate(0, 0, -1).Format(filelog.DayLayout)
		if y, e2 := readDayFile(dir, yday); e2 == nil {
			f, err, day = y, nil, yday
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return "未更新（没有当天的文件）"
		}
		return "读取失败（文件读不了或格式不对）"
	}
	upd, perr := time.Parse(time.RFC3339, f.Updated)
	if perr != nil || now.Sub(upd) > 15*time.Minute {
		return "未更新（文件超过 15 分钟没动）"
	}
	var bad []string
	unknown := 0
	for _, c := range f.Checks {
		switch c.State {
		case Bad:
			bad = append(bad, c.ID)
		case Unknown:
			unknown++
		}
	}
	file := filepath.Join(dir, day+".json")
	switch {
	case len(bad) > 0:
		s := fmt.Sprintf("%d 项异常（%s），详见 %s", len(bad), strings.Join(bad, "、"), file)
		if unknown > 0 {
			s += fmt.Sprintf("；另有 %d 项无法判定", unknown)
		}
		return s
	case len(f.Checks) == 0:
		return "启动中，还没有评估"
	case unknown > 0:
		return fmt.Sprintf("没有异常，但有 %d 项无法判定，详见 %s", unknown, file)
	}
	return "正常"
}
