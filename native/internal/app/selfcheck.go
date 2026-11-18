package app

import (
	"context"
	"database/sql"
	"time"

	"sumika-native/internal/applog"
	"sumika-native/internal/embed"
	"sumika-native/internal/llm"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// selfcheckState 是运行自检（规格-运行时自检）在 App 里的那一份：计数器给入库一站、适配器、出口往里数；
// 评估循环用只读连接读 trace 库和各平台聊天库，结果写 <数据目录>/selfcheck/<日期>.json。
// 自检只读只记，不补录、不重试、不发消息；配置 selfcheck.enabled=false 就整个不建（计数器仍建着，往里数是空操作之外的一次原子加）。
type selfcheckState struct {
	counters *selfcheck.Counters
	runner   *selfcheck.Runner
	dbs      []*sql.DB // 自检自己开的只读连接，关机时先关
}

func (a *App) openSelfcheck() {
	if !a.cfg.Selfcheck.Enabled || a.opt.Mode == Archive {
		return
	}
	var trace selfcheck.Querier
	var traceDB *sql.DB
	if db, err := store.OpenReadOnlyFile(a.paths.GoTraceDB()); err != nil {
		a.warnFor(applog.Selfcheck)("打不开运行记录库的只读连接，依赖它的检查会标无法判定：%v", err)
	} else {
		a.selfcheck.dbs = append(a.selfcheck.dbs, db)
		trace, traceDB = db, db
	}
	// S8：每个平台聊天库一个只读连接
	events := map[string]selfcheck.Querier{}
	for _, name := range a.set.Platforms() {
		db, err := store.OpenReadOnlyFile(a.paths.ChatDB(name))
		if err != nil {
			a.warnFor(applog.Selfcheck)("打不开平台 %s 聊天库的只读连接，序号断号检查会标无法判定：%v", name, err)
			continue
		}
		a.selfcheck.dbs = append(a.selfcheck.dbs, db)
		events[name] = db
	}
	sc := a.cfg.Selfcheck
	a.selfcheck.runner = selfcheck.New(selfcheck.Config{
		Dir:      a.paths.SelfcheckDir(),
		Counters: a.selfcheck.counters,
		Checks:   selfcheck.Checks(a.selfcheckThresholds()),
		Trace:    trace,
		Backup:   a.backupSelfcheck,
		Drill:    a.drillSelfcheck,
		// S10：回合内核没解除的故障（停止受理的会话或整个内核）
		Faults: a.kernelFaultsSelfcheck,
		StrayEnds: func() int {
			if a.hub == nil {
				return 0
			}
			return a.hub.StrayEndFailures()
		},
		MCP:   a.mcpSelfcheck(traceDB),
		Embed: a.embedSelfcheck,
		// S14：平台库打不开、已经整个停掉的平台（规格第 8 条）
		StoreUnavailable: a.storeUnavailableSelfcheck,
		Events:           func() map[string]selfcheck.Querier { return events },
		// 旧行没有错误类别时按原文现算（和运行页质量标签同一个函数）
		Classify: llm.ClassifyText,
		// S4：进程内正在跑的回合 id（含内部运行），不在其中又没结局的回合才算卡住
		Running: func() []string {
			if a.hub == nil {
				return nil
			}
			return a.hub.RunningTurnIDs()
		},
		// S1：入库队列里还有排着、没做完的活，就不是静默点
		Pending: func() int {
			if a.intake == nil {
				return 0
			}
			return a.intake.Pending()
		},
		Warnf: a.warnFor(applog.Selfcheck), Infof: a.logfFor(applog.Selfcheck),
		Redact:   func(s string) string { return RedactLog(s, a.tokens) },
		Now:      a.now,
		Interval: time.Duration(sc.IntervalSeconds) * time.Second,
	})
}

// selfcheckThresholds 把配置里的自检阈值翻成 selfcheck 的写法；Runner 和环境快照的 EvalNow 用同一份。
func (a *App) selfcheckThresholds() selfcheck.Thresholds {
	sc := a.cfg.Selfcheck
	return selfcheck.Thresholds{
		ReceiptUnknownPerDay: sc.ReceiptUnknownPerDay, StuckTurn: time.Duration(sc.StuckTurnMinutes) * time.Minute,
		ReconnectCount: sc.ReconnectCount, ReconnectWindow: time.Duration(sc.ReconnectWindowMinutes) * time.Minute,
		CompactFailStreak: sc.CompactFailStreak, StageFailStreak: sc.StageFailStreak,
	}
}

// embedSelfcheck 把本机嵌入现在的状态交给自检（S13）：关着、启动中、就绪、起不来，加上最近一次的原因（缺模型时带完整路径）。
// 还没建嵌入器（archive 不建）按 off 算；自检只在 receive 跑
func (a *App) embedSelfcheck() selfcheck.EmbedState {
	s := selfcheck.EmbedState{Enabled: a.cfg.Embed.Enabled, Backend: a.cfg.Embed.Backend, Status: string(embed.StatusOff)}
	if a.embedding == nil {
		return s
	}
	status, why := a.embedding.Status()
	s.Status, s.Reason = string(status), why
	return s
}

// kernelFaultsSelfcheck 把回合内核现在没解除的故障交给自检（S10）；内核还没建起来就没有故障
func (a *App) kernelFaultsSelfcheck() []selfcheck.KernelFault {
	if a.hub == nil {
		return nil
	}
	return kernelFaultsOf(a.hub.Faults())
}

func kernelFaultsOf(faults []turn.Fault) []selfcheck.KernelFault {
	out := make([]selfcheck.KernelFault, 0, len(faults))
	for _, f := range faults {
		out = append(out, selfcheck.KernelFault{Kernel: f.Kernel(), CharacterID: f.CharacterID, Conversation: f.Conversation,
			Kind: string(f.Kind), At: f.At, Detail: f.Detail, Tries: f.Tries})
	}
	return out
}

// runSelfcheck 随后台起评估循环（Run 里调）。关机时由 closeSet 收尾，不用另停
func (a *App) runSelfcheck() {
	if a.selfcheck.runner != nil {
		a.selfcheck.runner.Start(context.Background())
	}
}

// closeSelfcheck 停评估循环、最后刷新一次当天的文件、关只读连接。可重复调
func (a *App) closeSelfcheck() {
	if a.selfcheck.runner != nil {
		a.selfcheck.runner.Close()
	}
	for _, db := range a.selfcheck.dbs {
		db.Close()
	}
	a.selfcheck.dbs = nil
}

// SelfcheckLine 是 sumika-ctl status 里"自检："后面那句；没开自检是"已关闭"
func (a *App) SelfcheckLine() string {
	if a == nil || a.selfcheck.runner == nil {
		return "已关闭"
	}
	return selfcheck.Summary(a.paths.SelfcheckDir(), a.now())
}
