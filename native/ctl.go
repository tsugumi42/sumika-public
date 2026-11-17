package main

// 本机控制接口（规格-本机控制接口，ADR-227 第 4 条）接进桌面程序：sumika-ctl 经命名管道对开着的程序做托盘能做的事。
// 每个命令调的都是托盘用的同一个函数（host.startReceive / stopReceive、shell.showMain、setAutoReceive、退出走 beforeQuit），不另写一套；
// 碰窗口的操作回界面线程（a.post）；服务端不缓存状态，status 每次现查宿主。
// 服务端在日志开好、数据目录定下来以后起（shell.ready 里的后台线程），退出流程（beforeQuit）第一步就关。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/applog"
	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

// quickSettle 是 receive start 不带 --wait 时多等一会儿：很快就做完的（已经在接收、没有小号、数据锁被占）能直接回结果，
// 慢的先回"已开始"
var quickSettle = 200 * time.Millisecond // 测试里按需要调：机器忙时“很快做完”的那一路要留足余量，不靠墙钟碰运气

// uiCallLimit 是等界面线程回话的上限：开机自启的读写要等主循环（mygo 的 OpenAtLogin 是同步的），主循环不转了就别卡死 status
var uiCallLimit = 3 * time.Second // 测试里缩短

// ctlBackend 把控制接口的操作接到外壳和宿主上
type ctlBackend struct{ s *shell }

func (b *ctlBackend) host() (*host, error) {
	if b.s.host == nil {
		return nil, ctlproto.Errorf(ctlproto.CodeDemo, "这是演示数据模式，没有后台")
	}
	return b.s.host, nil
}

func (b *ctlBackend) Status(ctx context.Context) (ctlproto.Status, error) {
	s := b.s
	st := ctlproto.Status{Alerts: []ctlproto.Alert{}, Pid: os.Getpid(), Version: buildVersion(), Demo: s.demo,
		AutoReceive: s.autoRecv.Load(), DataDir: s.opt.DataDir}
	if ds := s.settings.Load(); ds != nil {
		st.DataDir = ds.Dir()
	}
	if on, ok := withLimit(ctx, uiCallLimit, s.openAtLogin); ok {
		st.Autostart = &on
	}
	if s.demo {
		st.Phase, st.Reason = "demo", "演示数据，全是编的，不碰任何库"
		return st, nil
	}
	st.Watchdog = s.watchdogInfo()
	hs := s.host.current()
	st.Mode, st.Phase, st.Reason, st.Account, st.NoRecv, st.Accounts = string(hs.Mode), hs.Phase, hs.Reason, hs.Account, hs.NoRecv, hs.Accounts
	if st.Phase == "" {
		st.Phase = phaseStarting
	}
	if hs.Reader != nil {
		st.Connection, st.Alerts = runtimeOf(ctx, hs.Reader)
		st.RunningTurns = runningTurnsOf(hs)
		if sc, ok := hs.Reader.(interface{ SelfcheckLine() string }); ok {
			st.Selfcheck = sc.SelfcheckLine()
		}
		if wb, ok := hs.Reader.(interface{ WebBalances() []string }); ok {
			st.WebBalances = wb.WebBalances()
		}
	}
	if s.host != nil {
		st.Env = envStatusLine(s.host.EnvSnapshot())
	}
	return st, nil
}

var longDigits = regexp.MustCompile(`[0-9]{5,}`)

// runtimeOf 取一份宿主状态快照里的连接和没恢复的提醒（后台状态页读的是同一份）。号码照界面的打码规则；取不到就空
func runtimeOf(ctx context.Context, r api.Reader) (string, []ctlproto.Alert) {
	alerts := []ctlproto.Alert{}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sub, err := r.WatchRuntime(ctx)
	if err != nil {
		return "", alerts
	}
	defer sub.Close()
	select {
	case snap, ok := <-sub.Updates():
		if !ok {
			return "", alerts
		}
		v := snap.Result.Value
		for _, al := range v.Alerts.Items {
			alerts = append(alerts, ctlproto.Alert{Title: maskDigits(al.Title), Body: maskDigits(al.Body)})
		}
		return v.Connection, alerts
	case <-ctx.Done():
		return "", alerts
	}
}

func maskDigits(s string) string { return longDigits.ReplaceAllStringFunc(s, config.MaskID) }

// buildVersion 程序版本：仓库里没有版本号，就用编译时带进来的提交号
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "未知"
	}
	v := bi.Main.Version
	if v == "" {
		v = "(devel)"
	}
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			v += " " + kv.Value[:min(len(kv.Value), 8)]
		case "vcs.modified":
			if kv.Value == "true" {
				v += "+改动未提交"
			}
		}
	}
	return v
}

func receiveOf(hs hostState, settled bool) ctlproto.Receive {
	return ctlproto.Receive{Phase: hs.Phase, Mode: string(hs.Mode), Reason: hs.Reason, Receiving: hs.Phase == phaseReceiving, Settled: settled}
}

func (b *ctlBackend) ReceiveStart(ctx context.Context, wait time.Duration) (ctlproto.Receive, error) {
	h, err := b.host()
	if err != nil {
		return ctlproto.Receive{}, err
	}
	done := make(chan struct{})
	go func() { h.startReceive(); close(done) }() // 和托盘一样放后台线程，调的是同一个函数
	settled := false
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-done:
			settled = true
		case <-t.C:
			r := receiveOf(h.current(), false)
			return r, &ctlproto.Error{Code: ctlproto.CodeTimeout, Msg: fmt.Sprintf("等了 %v，阶段还没落定（现在是「%s」）", wait, phaseText(r.Phase)), Data: r}
		case <-ctx.Done():
			return ctlproto.Receive{}, ctx.Err()
		}
	} else {
		select {
		case <-done:
			settled = true
		case <-time.After(quickSettle):
		case <-ctx.Done():
			return ctlproto.Receive{}, ctx.Err()
		}
	}
	r := receiveOf(h.current(), settled)
	if settled && !r.Receiving { // 做完了却没在接收：失败，原因就是宿主写在状态区的那句
		return r, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: r.Reason, Data: r}
	}
	return r, nil
}

func (b *ctlBackend) ReceiveStop(ctx context.Context) (ctlproto.Receive, error) {
	h, err := b.host()
	if err != nil {
		return ctlproto.Receive{}, err
	}
	before := h.current().Phase
	var note string
	done := make(chan struct{})
	go func() { note = h.stopReceive(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctlproto.Receive{}, ctx.Err()
	}
	r := receiveOf(h.current(), true)
	if note != "" { // 本地可用阶段没有可停的 QQ 后台：原样把这句交给命令行
		r.Reason = note
	}
	// 停是停了，可是连只看历史也没回得去：失败。但停之前就是受阻（后台没起来、本来就没在接收），stopReceive 什么也没做，
	// "本来就没在接收也算成功"（文档），阶段和原因照实交给调用方
	if r.Phase == phaseBlocked && before != phaseBlocked {
		return r, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: r.Reason, Data: r}
	}
	return r, nil
}

func (b *ctlBackend) ShowWindow(context.Context) error {
	b.s.a.post(b.s.showMain) // 碰窗口回界面线程；回复不等它
	return nil
}

func (b *ctlBackend) Logs(_ context.Context, n int, f ctlproto.LogFilter) (ctlproto.Logs, error) {
	lg := b.s.lg.Load()
	if lg == nil {
		return ctlproto.Logs{}, errors.New("没有日志文件：演示数据不写日志，或者日志还没开成")
	}
	if f.Level != "" || f.Module != "" {
		return b.filteredLogs(lg, n, f)
	}
	lines, err := lg.Tail(n)
	if err != nil {
		return ctlproto.Logs{}, fmt.Errorf("读日志：%w", err)
	}
	if lines == nil {
		lines = []string{}
	}
	return ctlproto.Logs{Lines: lines}, nil
}

// filteredLogs 按级别、模块过滤（规格第八节）：先验证过滤值，再在最近 scanLimit 行里过滤、取最后 n 行
func (b *ctlBackend) filteredLogs(lg *applog.Root, n int, f ctlproto.LogFilter) (ctlproto.Logs, error) {
	if _, ok := applog.ParseLevel(f.Level); f.Level != "" && !ok {
		return ctlproto.Logs{}, fmt.Errorf("没有这个级别：%s（可用：debug、info、warn、error）", f.Level)
	}
	if _, ok := applog.ByName(f.Module); f.Module != "" && !ok {
		return ctlproto.Logs{}, fmt.Errorf("没有这个模块：%s（可用：%s）", f.Module, strings.Join(applog.Names(), "、"))
	}
	res, err := lg.Query(n, applog.Filter{Level: f.Level, Module: f.Module})
	if err != nil {
		return ctlproto.Logs{}, fmt.Errorf("读日志：%w", err)
	}
	lines := res.Lines
	if lines == nil {
		lines = []string{}
	}
	return ctlproto.Logs{Lines: lines, Scanned: res.Scanned, Matched: res.Matched, Old: res.Old}, nil
}

func (b *ctlBackend) GateList(ctx context.Context) (ctlproto.GateList, error) {
	h, err := b.host()
	if err != nil {
		return ctlproto.GateList{}, err
	}
	rows, err := h.gateList(ctx)
	if err != nil {
		return ctlproto.GateList{}, err
	}
	out := ctlproto.GateList{Groups: make([]ctlproto.GateRow, 0, len(rows))}
	for _, r := range rows {
		out.Groups = append(out.Groups, ctlproto.GateRow{Conversation: r.Conversation, Name: r.Name, Mode: r.Mode, Source: r.Source, Actual: r.Actual})
	}
	return out, nil
}

func (b *ctlBackend) GateSet(_ context.Context, conversation, mode string) (ctlproto.GateChange, error) {
	h, err := b.host()
	if err != nil {
		return ctlproto.GateChange{}, err
	}
	ch, err := h.gateSet(conversation, mode)
	switch {
	case errors.Is(err, backend.ErrGateReadOnly):
		return ctlproto.GateChange{}, ctlproto.Errorf(ctlproto.CodeReadOnly, err.Error())
	case errors.Is(err, backend.ErrGateNotAllowed), errors.Is(err, backend.ErrGateBadGroup), errors.Is(err, backend.ErrGateUnknownGroup):
		return ctlproto.GateChange{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	case err != nil:
		return ctlproto.GateChange{}, err
	}
	return ctlproto.GateChange{Conversation: ch.Conversation, Before: ch.Before, After: ch.After}, nil
}

func (b *ctlBackend) SetAutoReceive(_ context.Context, on bool) error { return b.s.setAutoReceive(on) }

func (b *ctlBackend) SetAutostart(ctx context.Context, on bool) error {
	err, ok := withLimit(ctx, uiCallLimit, func() error { return b.s.setOpenAtLogin(on) })
	if !ok {
		// 不能说"没改成"：mygo 的 SetOpenAtLogin 已经排进界面线程的队列，撤不回，线程缓过来以后照样会写注册表
		return errors.New("界面线程没有及时回话，开机自动启动是否生效还不确定（已经排着的修改缓过来后仍会执行），请稍后用 status 看")
	}
	return err
}

// Quit 和托盘的"退出 Sumika"一样：请求退出，由界面线程触发 beforeQuit（唯一的退出流程）。服务端在"正在退出"的回复发完以后才调它
func (b *ctlBackend) Quit() { b.s.quitApp() }

// withLimit 在另一个线程里调 f，最多等 d。超时返回零值和 false（那个线程还会跑完，结果丢掉）
func withLimit[T any](ctx context.Context, d time.Duration, f func() T) (T, bool) {
	ch := make(chan T, 1)
	go func() { ch <- f() }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-t.C:
	case <-ctx.Done():
	}
	var zero T
	return zero, false
}

// ───────────── 起和关 ─────────────

// startCtl 开管道、收连接，直到 closeCtl。开不成只记日志，不影响桌面程序（比如另一个进程占了这个管道名）
func (s *shell) startCtl() {
	name := s.ctlPipe
	if name == "" {
		var err error
		if name, err = ctlproto.PipeName(s.demo); err != nil {
			s.logf("本机控制接口没开成：%v", err)
			return
		}
	}
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		s.logf("本机控制接口没开成：%v", err)
		// 日志之外也要让主人看得到：管道名可能被别的本地账号抢先占了，这时 sumika-ctl 连上的不是我们。
		// 状态区的字不带管道名（里面有用户 SID），原因在日志里
		s.a.post(func() {
			s.a.status = "本机控制接口没开成，sumika-ctl 暂时用不了：管道名可能被别的程序占了，原因见桌面日志"
		})
		return
	}
	srv := ctlserver.New(&ctlBackend{s: s}, ctlserver.Options{Demo: s.demo, Logf: s.logf, HandleKey: s.handleKey})
	s.ctlMu.Lock()
	if s.ctlClosed { // 开管道的时候退出流程已经走过第一步了
		s.ctlMu.Unlock()
		ln.Close()
		return
	}
	s.ctl = srv
	s.ctlMu.Unlock()
	s.logf("本机控制接口已开（当前用户专用的命名管道，不开网络端口）")
	if err := srv.Serve(ln); err != nil {
		s.logf("本机控制接口停了：%v", err)
	}
}

// handleKey 是 gate list / gate set 给群编句柄用的本机密钥：存在数据目录的桌面设置文件里，第一次要用时生成。
// 在控制接口的请求线程里调（读写小文件），不在界面线程
func (s *shell) handleKey() ([]byte, error) {
	st := s.settings.Load()
	if st == nil {
		return nil, errors.New("没找到数据目录")
	}
	return st.HandleKey()
}

// closeCtl 退出流程第一步：关管道，不再收新命令；在途的收到"正在退出"。不等在途的做完，可以重复调用，界面线程里调也不会卡
func (s *shell) closeCtl() {
	s.ctlMu.Lock()
	s.ctlClosed = true
	srv := s.ctl
	s.ctlMu.Unlock()
	if srv != nil {
		srv.Close()
	}
}
