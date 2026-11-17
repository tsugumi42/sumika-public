package main

// 窗口外壳（规格-界面接Go后台 第三节）：主窗口、右下角托盘、关窗口只隐藏、唯一的退出流程。
// 照 mygo 官方示例 examples/menubar-native（托盘左右键、quitting 区分、退出时销毁托盘）和 examples/launcher
// （单实例唤醒、关闭隐藏），补上后台的异步收尾：OnBeforeQuit 第一次取消默认退出、在后台线程收后台，
// 收完回到界面线程再 App.Quit；第二次放行，OnClose 不再拦，OnWillQuit 销毁托盘。
// 托盘建不出来就不起后台（"有托盘和原生 GUI 再起 Go"，ADR-208），X 直接走退出。

import (
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	backend "sumika-native/internal/app"
	"sumika-native/internal/applog"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/envcheck"
	"sumika-native/internal/resstat"
	"sumika-native/internal/storagestat"
	"sumika-native/internal/watchdog"
	"sumika-native/internal/winshell"
)

// quitBudget 是整个退出的上限（规格 3.3，含界面消费者停止）；超了按异常退出，退出码非零
const quitBudget = 30 * time.Second

const (
	quitRunning = iota
	quitStopping
	quitStopped
)

type shell struct {
	a    *app
	opt  backend.Options
	demo bool
	// remote 不空：不开本地后台，只当界面连协议后台（remote.go）；rpcListen：本地后台开着时也开协议入口
	remote    string
	rpcListen bool
	// inprocRPC：主窗口默认经进程内协议客户端（-inproc-rpc，默认开）
	inprocRPC bool
	host      *host
	tray      *mygo.Tray
	lg        atomic.Pointer[applog.Root] // 日志出口（desklog.go）；演示数据、没开成时是 nil
	unroute   atomic.Pointer[func()]      // openLogAndRoute 接的 slog/log 默认值的还原函数；closeLog 先还原再关文件
	// noHostRedact 是没有宿主（托盘建不出来）时外壳自己的打码：照 s.opt 读一次令牌，别让令牌只靠正则猜
	noHostRedact     backend.LogRedactor
	noHostRedactOnce sync.Once
	// showNotice 弹系统通知（attention.go）；nil 用 mygo 的。测试换成假的
	showNotice func(id, title, body string) error

	// 桌面开关（autoreceive.go）：任何线程都能读；改的时候持 recvMu，文件和缓存一起换
	settings     atomic.Pointer[desksettings.Store] // 数据目录定下来以后才有；演示数据、读不到配置时是 nil
	autoRecv     atomic.Bool                        // "启动后自动接收 QQ" 的缓存，托盘菜单每次弹出读它，不在界面线程读文件
	recvMu       sync.Mutex
	themeMu      sync.Mutex // 主题写盘按修订号串行，旧任务不能盖回新选择
	themeWritten uint64

	// 开机自动启动的读写：默认是 mygo 的（它们要等界面线程的主循环转起来，所以不能在界面线程等自己）；测试里换掉
	openAtLogin    func() bool
	setOpenAtLogin func(bool) error
	// autostart 是正式运行时开机自启的实现（watch.go 的 useWatchedAutostart）：Run 项启动守护进程；演示、远端界面和测试是 nil
	autostart *watchdog.Autostart
	// beat 是写心跳的（watch.go 的 startHeartbeat）；没开时是 nil
	beat atomic.Pointer[watchdog.Beater]
	// quitApp 是请求退出（mygo 的 App.Quit：它回到界面线程，触发 beforeQuit），托盘的退出项、sumika-ctl 的 quit 和退出流程最后一步都走它；测试里换掉
	quitApp func()

	// 运行页存储统计（storage.go）：整个桌面程序一份，累计媒体新增字节；退出时写回
	storageMu  sync.Mutex
	storage    *storagestat.Service
	storageDir string

	// 运行页资源采样器（resstat_wire.go）：界面和 sumika-ctl 共用一个，让两次采样之差算 CPU；没有协程，用到才建
	resOnce sync.Once
	res     *resstat.Sampler

	// 本机控制接口（ctl.go）：管道服务端。退出流程第一步就关它；ctlPipe 非空时用这个管道名（只给测试：不连真实的管道）
	ctlMu     sync.Mutex
	ctl       *ctlserver.Server
	ctlClosed bool
	ctlPipe   string

	// 以下只在界面线程读写
	quit      int
	trayErr   error
	exitCode  int
	hostState hostState
}

func newShell(a *app, opt backend.Options, demo bool) *shell {
	return &shell{a: a, opt: opt, demo: demo, inprocRPC: true, openAtLogin: mygo.App.OpenAtLogin, setOpenAtLogin: mygo.App.SetOpenAtLogin, quitApp: mygo.App.Quit}
}

// fitWindow 把默认窗口大小缩进屏幕可用区：四周留一点，别贴满。
func fitWindow(w, h, workW, workH int) (int, int) {
	const margin = 48
	return min(w, max(workW-margin, 1)), min(h, max(workH-margin, 1))
}

func (s *shell) ready() {
	a := s.a
	// 窗口和任务栏的图标（ADR-230）：exe 带着图标资源就不用设（icons.go 的 setWindowIcon 说明了为什么），没带才设。
	// 设不上只记日志：没图标不该挡着程序起来，日志文件还没开，等开了再写
	iconErr := setWindowIcon()
	logIcon := func() {
		if iconErr != nil {
			s.logf("设置窗口图标失败：%v", iconErr)
		}
	}
	title := "Sumika"
	if s.demo {
		title = "Sumika（演示数据）"
	} else if s.remote != "" {
		title = "Sumika（远端后台）"
	}
	// 第一次打开的大小不超过屏幕（标题栏是自己画的，窗口比屏幕高就够不到顶行了）；之后记住上次的位置和大小
	w, h, stateKey := 1640, 900, "main"
	if work := mygo.Screen.PrimaryDisplay().WorkArea; work.Width > 0 {
		w, h = fitWindow(w, h, work.Width, work.Height)
	}
	if v := os.Getenv("SUMIKA_SIZE"); v != "" {
		fmt.Sscanf(v, "%dx%d", &w, &h)
		stateKey = "" // 指定了大小（测试、截图）就照它，不读上次的
	}
	// 隐藏系统标题栏和三个键：三个键自己画（winshell.WindowControls），边缘缩放、阴影、圆角、贴靠布局都还在；Title 照旧，任务栏和 Alt+Tab 用
	a.win = mygo.NewWindow(mygo.WindowOptions{Title: title, Width: w, Height: h, MinWidth: min(380, w), MinHeight: min(480, h), UseContentSize: true, TitleBarStyle: mygo.TitleBarHidden, HideWindowButtons: true, StateKey: stateKey, Vibrancy: mygo.VibrancyMica, Content: ui.View(a.view)})
	// mygo 的最小值按外框计；内容下限要加回原生边框，保持库的单栏断点可达。
	outer := a.win.Bounds()
	contentW, contentH := a.win.ContentSize()
	a.win.SetMinimumSize(min(380, w)+outer.Width-contentW, min(480, h)+outer.Height-contentH)
	a.look.choose = s.chooseTheme
	a.rm.logf = s.logf
	a.trackForeground() // 窗口在不在前台：标已读要看（readmark.go）
	// X、Alt+F4 只隐藏：窗口对象、选中的会话、订阅都留着（规格 3.1 第 4 条）。退出流程走完以后才放行
	a.win.OnClose(func(e *mygo.CloseEvent) {
		if s.quit == quitStopped {
			return
		}
		e.PreventDefault()
		if s.trayErr != nil || s.tray == nil {
			mygo.App.Quit() // 没有托盘就没有别的入口：X 走退出，不留看不见的常驻进程
			return
		}
		a.win.Hide()
	})
	// 窗口看不见的时候记忆页不订阅（规格-记忆页 4.5）；事件可能不在界面线程，设标记后交回界面线程再同步
	away := func(v bool) func() {
		return func() { a.away.Store(v); a.post(func() { a.memSync(); a.runSync() }) }
	}
	a.win.OnHide(away(true))
	a.win.OnMinimize(away(true))
	a.win.OnShow(away(false))
	a.win.OnRestore(away(false))
	a.winControls = &winshell.WindowControls{Minimize: a.win.Minimize, Toggle: a.win.ToggleMaximize, Close: a.win.Close}
	maximized := func(v bool) func() { return func() { a.post(func() { a.winControls.Maximized = v }) } }
	a.win.OnMaximize(maximized(true))
	a.win.OnUnmaximize(maximized(false))
	var err error
	s.tray, err = mygo.NewTray(mygo.TrayOptions{ID: "sumika-main", Icon: trayIcon(), ToolTip: "Sumika · 正在检查后台"})
	if err != nil {
		s.trayErr = err
		s.tray = nil
		a.status = "托盘建不出来，后台没有启动：" + err.Error()
		if !s.demo {
			go func() { s.openLogAndRoute(); logIcon(); s.logf("托盘建不出来，后台没有启动：%v", err) }() // 读配置不在界面线程
		} else {
			logIcon()
		}
		return
	}
	s.tray.OnClick(s.showMain)
	if !s.demo && s.remote == "" {
		st := &shellStats{s: s}
		a.attachStats(st, st) // 运行页读数：请求时才看数据目录就绪没有，没就绪回"不可用"
	}
	s.tray.OnRightClick(func() {
		// 照 menubar-native：菜单只为这一次弹出装上，弹完卸掉，左键才进得了 OnClick
		s.tray.SetMenu(s.menu())
		defer s.tray.SetMenu(nil)
		s.tray.PopUpMenu()
	})
	if s.demo {
		a.attach(demoReader{})
		a.attachMemory(newDemoMemory())
		ds := newDemoStats()
		a.attachStats(ds, ds)
		s.tray.SetToolTip("Sumika · 演示数据")
		logIcon()       // 演示不开日志文件，退回 log.Printf
		go s.startCtl() // 演示也开管道（名字带 -demo），receive、gate 回 demo
		return
	}
	if s.remote != "" {
		s.tray.SetToolTip("Sumika · 远端后台")
		go s.connectRemote()
		return
	}
	s.opt.Attention = s.attention                                                  // 需要主人看的事：窗口不在前台就弹系统通知（attention.go）
	s.opt.WindowForeground = func() bool { return s.a != nil && s.a.rm.fg.Load() } // 主动联系的导演：主人是不是正看着窗口
	s.host = newHost(s.opt, func(st hostState) { a.post(func() { s.applyHost(st) }) })
	s.host.onEnv = func(snap envcheck.Snapshot) { a.post(func() { s.a.host.Env = snap }) }
	s.host.rpcListen = s.rpcListen
	s.host.inprocRPC = s.inprocRPC
	go func() {
		s.initDataLocation()
		s.openLogAndRoute() // 先开日志、再起宿主：宿主和后台的第一行就进文件
		logIcon()
		s.initSettings()
		go s.startCtl() // 日志开了、数据目录定了就开管道；后台还在起也能回 status（阶段 starting）
		s.migrateAutostart()
		s.startHeartbeat()
		s.host.start()
		s.maybeAutoReceive() // 只看历史开好以后，设了自动接收就走和托盘"开始接收"同一个函数
	}()
}

// openLog 开日志文件并交给宿主（后台线程调；宿主还没 start，改它的 opt 不会和别的线程撞）。开不成就算了：宿主读同一份配置时会在状态区写原因
func (s *shell) openLog() {
	if s.demo || s.lg.Load() != nil {
		return
	}
	lg, err := openDeskLog(s.opt, time.Now)
	if err != nil {
		return
	}
	lg.SetRedact(s.redactLine) // Printf 形状的直调（外壳、宿主之外的入口）也要打码，不靠每个调用方记得
	s.lg.Store(lg)
	if s.host != nil {
		s.host.redactor.Refresh(s.host.opt)
		s.host.opt.Logs = lg
	}
}

// openLogAndRoute 只在程序里调（不在 openLog 里，测试会并行开多个日志，全局默认值会串）：
// 标准库 log 包和 slog（checkpoint 的 slog.Warn 等）的输出也进桌面日志，模块记为 misc（规格 4.4）
func (s *shell) openLogAndRoute() {
	s.openLog()
	if lg := s.lg.Load(); lg != nil {
		restore := backend.RouteLogs(lg, s.redactLine)
		s.unroute.Store(&restore)
	}
}

// redactLine 是外壳所有日志出口共用的打码：有宿主就用宿主的（带令牌），没有宿主（托盘建不出来）就用外壳自己照 s.opt 读来的那份
func (s *shell) redactLine(line string) string {
	if s.host != nil {
		return s.host.redactor.Redact(line)
	}
	s.noHostRedactOnce.Do(func() { s.noHostRedact.Refresh(s.opt) })
	return s.noHostRedact.Redact(line)
}

// logf 外壳的收尾错误也经过宿主脱敏；没有日志文件时退回 log.Printf。
func (s *shell) logf(format string, args ...any) {
	if lg := s.lg.Load(); lg != nil {
		if !lg.Enabled(applog.Desk, slog.LevelInfo) {
			return // 级别没开：不格式化也不打码
		}
		lg.Masked(applog.Desk, slog.LevelInfo)("%s", s.redactLine(fmt.Sprintf(format, args...)))
		return
	}
	log.Printf("%s", s.redactLine(fmt.Sprintf(format, args...)))
}

// closeLog 退出的最后一步：关日志文件，之后的行只剩 log.Printf
func (s *shell) closeLog() {
	if restore := s.unroute.Swap(nil); restore != nil {
		(*restore)()
	}
	if lg := s.lg.Swap(nil); lg != nil {
		lg.Close()
	}
}

// applyHost 宿主状态回到界面线程：换数据源、更新状态区和托盘提示
func (s *shell) applyHost(st hostState) {
	if s.quit == quitStopped {
		return
	}
	s.hostState = st
	s.a.host = st
	if st.Phase != phaseStopping && st.Phase != phaseStopped {
		s.a.attach(st.Reader)
		s.a.attachMemory(st.Memory)
	}
	if s.tray != nil {
		s.tray.SetToolTip("Sumika · " + phaseText(st.Phase))
	}
}

func phaseText(p string) string {
	switch p {
	case phaseStarting:
		return "正在检查后台"
	case phaseArchive:
		return "只看历史"
	case phaseLocal:
		return "本地可用（QQ 没开）"
	case phaseReceiving:
		return "接收中"
	case phaseSwitching:
		return "正在切换"
	case phaseBlocked:
		return "后台没在运行"
	case phaseStopping:
		return "正在退出"
	}
	return "已停"
}

// secondInstance 第二次启动只唤醒这个窗口（规格 3.1 第 2 条）：不换数据目录、不连账号；
// 它要的数据目录和这里不一样时，在状态区写明现在用的是哪个
func (s *shell) secondInstance(args []string, wd string) {
	s.showMain()
	fs := flag.NewFlagSet("second", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "")
	fs.String("root", "", "")
	fs.String("account", "", "")
	fs.Bool("demo", false, "")
	if fs.Parse(args) != nil || *data == "" {
		return
	}
	want := *data
	if !filepath.IsAbs(want) {
		want = filepath.Join(wd, want)
	}
	have, _ := filepath.Abs(s.opt.DataDir)
	if s.opt.DataDir == "" || !strings.EqualFold(filepath.Clean(want), filepath.Clean(have)) {
		cur := s.opt.DataDir
		if cur == "" {
			cur = "配置里的数据目录"
		}
		s.a.status = "已经开着一个 Sumika，用的是 " + cur + "；要换数据目录请先从托盘退出再开"
	}
}

// showMain 恢复主窗口：最小化的先还原，再显示、聚焦（同一个窗口对象）
func (s *shell) showMain() {
	w := s.a.win
	if w == nil {
		return
	}
	if w.IsMinimized() {
		w.Restore()
	}
	w.Show()
	w.Focus()
}

// menu 每次弹出前按当前状态重建（不跨线程改菜单项）
func (s *shell) menu() *mygo.Menu {
	st := s.hostState
	items := []*mygo.MenuItem{
		{Label: "打开主窗口", Click: func(*mygo.MenuItem, *mygo.Window) { s.showMain() }},
		{Label: "查看后台状态", Click: func(*mygo.MenuItem, *mygo.Window) { s.a.rail = 3; s.showMain() }},
	}
	if !s.demo && s.remote == "" {
		busy := s.quit != quitRunning || st.Phase == phaseStarting || st.Phase == phaseSwitching
		if st.Phase == phaseReceiving {
			items = append(items, &mygo.MenuItem{Label: "停止接收 QQ", Disabled: busy, Click: func(*mygo.MenuItem, *mygo.Window) { go s.host.stopReceive() }})
		} else {
			label := "开始接收 QQ"
			if st.Account != "" {
				label += "（" + st.Account + "）"
			}
			items = append(items, &mygo.MenuItem{Label: label, Disabled: busy || st.Account == "", ToolTip: st.NoRecv, Click: func(*mygo.MenuItem, *mygo.Window) { go s.host.startReceive() }})
		}
		// 启动后自动接收默认不勾；值在 desktop-settings.json 里，不碰用户的 sumika.yml。数据目录没找到（读不到配置）时点不了
		items = append(items, &mygo.MenuItem{Label: "启动后自动接收 QQ", Type: mygo.MenuItemCheckbox, Checked: s.autoRecv.Load(), Disabled: s.settings.Load() == nil,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { on := it.Checked; go s.setAutoReceive(on) }})
	}
	// 开机启动默认不勾（ADR-209）；只有用户点了才写系统设置
	items = append(items,
		&mygo.MenuItem{Label: "开机自动启动", Type: mygo.MenuItemCheckbox, Checked: s.openAtLogin(), Click: func(it *mygo.MenuItem, _ *mygo.Window) {
			if err := s.setOpenAtLogin(it.Checked); err != nil {
				s.a.status = "改开机启动失败：" + err.Error()
			}
		}},
		mygo.Separator(),
		&mygo.MenuItem{Label: "退出 Sumika（后台和界面一起退出）", Click: func(*mygo.MenuItem, *mygo.Window) { s.quitApp() }},
	)
	return mygo.NewMenu(items)
}

// beforeQuit 唯一的退出入口：菜单、系统退出请求都走这里（规格 3.3）
func (s *shell) beforeQuit(e *mygo.QuitEvent) {
	s.closeCtl() // 第一步：先关本机控制接口，不再收新命令，在途的收到"正在退出"；quit 命令的回复早在退出请求之前就发完了
	switch s.quit {
	case quitStopped:
		return // 收尾完了：放行
	case quitStopping:
		e.PreventDefault() // 正在收尾：不重复关
		return
	}
	e.PreventDefault()
	s.quit = quitStopping
	s.a.status = "正在退出：收好后台再关"
	h := s.host
	pend, st := s.a.pn.unsaved(), s.settings.Load() // 分栏的选择改了不到半秒就退出，防抖还没写的那一份
	appearance, revision := s.a.look.settings, s.a.look.revision
	go func() {
		s.markQuitting()     // 先让守护进程知道这是用户要退出：收尾超时按异常码退出也不会被拉起
		s.a.deskSaves.wait() // 已经排队的先写完，下面补写的才是最新的
		if pend != nil && st != nil {
			s.writePanes(st, *pend)
		}
		if revision > 0 && st != nil {
			s.writeAppearance(st, appearance, revision)
		}
		s.closeStorage() // 把媒体的"快照加增量"写回 maintenance_runs；先于关后台，趁库还开着
		done := make(chan error, 1)
		go func() {
			if h == nil {
				done <- nil
				return
			}
			done <- h.shutdown()
		}()
		select {
		case err := <-done:
			if err != nil {
				s.logf("退出时收后台出错：%v", err)
			}
			s.a.post(func() { // 回界面线程（a.post 就是 win.Update，测试里能换）
				s.quit = quitStopped
				s.a.cancel()
				s.quitApp()
			})
		case <-time.After(quitBudget):
			s.logf("退出超过 %v 后台还没收完，按异常退出", quitBudget)
			mygo.App.Exit(1)
		}
	}()
}

func (s *shell) destroyTray() {
	if s.tray != nil {
		s.tray.Destroy()
		s.tray = nil
	}
}
