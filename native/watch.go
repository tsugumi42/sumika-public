package main

// 看门狗的接线（docs/规格-看门狗.md）：
//   - sumika.exe -watch 是守护进程：不碰 mygo、不开窗口，拉起一个不带 -watch 的自己当桌面程序，盯着它（internal/watchdog）；
//   - 桌面程序（真正的 runMain 路径）按配置每隔一会儿写 <数据目录>/run/heartbeat.json，退出流程开始时先在心跳里记一笔；
//   - sumika-ctl status 里的"守护进程"一栏读 <数据目录>/run/watchdog.json；
//   - 开机自启的 Run 项写成 `"<exe>" -watch`（autostart.go 说明了为什么不用 mygo 的）。

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	backend "sumika-native/internal/app"
	"sumika-native/internal/applog"
	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/filelog"
	"sumika-native/internal/watchdog"
)

// watchedByEnv 是守护进程给它拉起的桌面程序设的环境变量，值是守护进程的进程号；桌面程序启动时读完就删（见 runMain）
const watchedByEnv = "SUMIKA_WATCHED_BY"

// watchLogPrefix 是守护进程日志的文件名前缀：logs/watch-<日期>.log
const watchLogPrefix = "watch"

// heartbeatEnabled 只在真正的程序入口 runMain 里打开（和 crashLogEnabled 同理）：测试直接调 ready 时不写心跳
var heartbeatEnabled bool

// watchedBy 是启动时读到的 SUMIKA_WATCHED_BY：这个进程是不是被守护进程拉起的
var watchedBy string

// watchChildArgs 是守护进程拉起桌面程序用的命令行：自己的参数去掉 -watch
func watchChildArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if n := strings.TrimLeft(a, "-"); len(a)-len(n) <= 2 && len(a) > len(n) && (n == "watch" || n == "watch=true") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// watchDataDir 守护进程放记录的数据目录，和桌面程序用同一套算法（-data，或配置里的 dataDir）。
// 配置读不了时落在用户缓存目录下的 Sumika\watchdog：配置坏了也要能拉起桌面程序、留下记录，桌面程序自己会把配置的毛病报出来
func watchDataDir(opt backend.Options) (dir string, fallback bool, err error) {
	if d, err := resolveDataDir(opt); err == nil {
		return d, false, nil
	}
	base, cerr := os.UserCacheDir()
	if cerr != nil {
		return "", false, cerr
	}
	return filepath.Join(base, "Sumika", "watchdog"), true, nil
}

// watchLockName 一个数据目录一把锁：同一个数据目录只许一个守护进程；测试用别的数据目录互不影响
func watchLockName(dataDir string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(filepath.Clean(dataDir))))
	return fmt.Sprintf("SumikaWatchdog-%08x", h.Sum32())
}

// watchParams 读配置里的 watchdog 一节；读不了就用默认值，并说一声
func watchParams(opt backend.Options, logf func(string, ...any)) watchdog.Params {
	return watchParamsWith(opt, logf, nil)
}

// watchParamsWith 同 watchParams，读到配置时顺手交给 onConfig（守护进程用它刷新自己的日志级别：
// 它每次取参数都重读配置，改 logs.level 不用重启守护进程）
func watchParamsWith(opt backend.Options, logf func(string, ...any), onConfig func(config.Config)) watchdog.Params {
	w := config.DefaultWatchdog()
	if loc, err := backend.ResourceRoot(opt.Root); err != nil {
		logf("读不到资源根，看门狗用默认设置：%v", err)
	} else if cfg, err := config.LoadIn(loc.Dir, opt.DataDir); err != nil {
		logf("读不了配置，看门狗用默认设置：%v", err)
	} else {
		w = cfg.Watchdog
		if onConfig != nil {
			onConfig(cfg)
		}
	}
	return watchdog.ParamsFrom(w)
}

// openWatchLog 开守护进程的日志。它的行里没有令牌（只有路径、命令行参数里的号、退出原因），所以用不靠令牌的打码；
// 不读配置和密钥库：配置读不到时恰恰最需要它的日志，不能整段换成“诊断详情已隐藏”。
func openWatchLog(dir string) *applog.Root {
	root := applog.New(filelog.New(dir, watchLogPrefix, nil, time.Now), time.Now)
	root.SetRedact(func(s string) string { return backend.RedactLog(s, nil) })
	return root
}

// runWatchdog 是 sumika.exe -watch：返回进程退出码（0 正常，1 连续崩溃停止拉起，2 起不来）
func runWatchdog(opt backend.Options, childArgs []string) int {
	dir, fallback, err := watchDataDir(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "守护进程找不到放记录的地方：", err)
		return 2
	}
	root := openWatchLog(dir)
	defer root.Close()
	logf := root.Logf(applog.Watch)
	exe, err := os.Executable()
	if err != nil {
		logf("找不到自己的路径，守护进程退出：%v", err)
		return 2
	}
	release, ok, err := watchdog.AcquireLock(watchLockName(dir))
	if err != nil {
		logf("取守护进程的锁失败，退出：%v", err)
		return 2
	}
	if !ok {
		logf("已经有一个守护进程在守着这个数据目录，这个直接退出")
		return 0
	}
	defer release()
	if fallback {
		logf("读不到配置里的数据目录，守护进程的记录先放在 %s", dir)
	}
	logf("守护进程启动，进程 %d，拉起 %s %v", os.Getpid(), exe, childArgs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sup := &watchdog.Supervisor{
		DataDir:  dir,
		Notifier: watchdog.ToastNotifier{},
		Params: func() watchdog.Params {
			return watchParamsWith(opt, logf, func(cfg config.Config) { backend.ApplyLogSettings(root, cfg.Logs) })
		},
		Spawn: func(context.Context) (watchdog.Child, error) {
			return watchdog.StartExec(exe, childArgs, []string{fmt.Sprintf("%s=%d", watchedByEnv, os.Getpid())})
		},
		Probe:        watchdog.ProbeCtl,
		ShuttingDown: watchdog.SystemShuttingDown,
		Logf:         root.Logf(applog.Watchdog),
		Warnf:        root.Warnf(applog.Watchdog),
		Errorf:       root.Errorf(applog.Watchdog),
	}
	switch sup.Run(ctx) {
	case watchdog.ResultGaveUp:
		return 1
	default:
		return 0
	}
}

// ───────────── 桌面程序一侧：心跳、退出标记、状态、开机自启 ─────────────

// startHeartbeat 数据目录定下来以后开始写心跳（后台线程调）。演示、远端界面、测试里不写
func (s *shell) startHeartbeat() {
	if !heartbeatEnabled || s.demo || s.remote != "" {
		return
	}
	st := s.settings.Load()
	if st == nil {
		return // 读不到配置：没有数据目录，守护进程那边会靠控制接口探测
	}
	interval := time.Duration(config.DefaultWatchdog().HeartbeatSeconds) * time.Second
	if loc, err := backend.ResourceRoot(s.opt.Root); err == nil {
		if cfg, err := config.LoadIn(loc.Dir, s.opt.DataDir); err == nil {
			interval = time.Duration(cfg.Watchdog.HeartbeatSeconds) * time.Second
		}
	}
	version := buildVersion()
	b := watchdog.NewBeater(st.Dir(), interval, func() watchdog.Snapshot {
		hs := s.host.current()
		if hs.Phase == "" {
			hs.Phase = phaseStarting // 宿主还没 start，和 status 一样说"正在检查后台"
		}
		return watchdog.Snapshot{Version: version, Phase: hs.Phase, RunningTurns: runningTurnsOf(hs)}
	}, s.logf)
	s.beat.Store(b)
	go b.Run(s.a.base())
	who := "没有守护进程"
	if watchedBy != "" {
		who = "由守护进程拉起"
	}
	s.logf("心跳已开，每 %s 一次（%s）", interval, who)
}

// markQuitting 退出流程一开始就调（beforeQuit 里的后台线程）
func (s *shell) markQuitting() {
	if b := s.beat.Load(); b != nil {
		b.MarkQuitting()
	}
}

// runningTurnsOf 宿主状态里正在跑的回合数；只看历史或后台没开好是 0
func runningTurnsOf(hs hostState) int {
	if rt, ok := hs.Reader.(interface{ RunningTurns() int }); ok {
		return rt.RunningTurns()
	}
	return 0
}

// watchdogInfo 是 sumika-ctl status 里"守护进程"一栏：读 run/watchdog.json，并确认那个进程还在
func (s *shell) watchdogInfo() *ctlproto.WatchdogInfo {
	info := &ctlproto.WatchdogInfo{Supervised: watchedBy != ""}
	st := s.settings.Load()
	if st == nil {
		return info
	}
	ws, err := watchdog.ReadState(st.Dir())
	if err != nil {
		return info
	}
	info.Running = ws.Running(time.Now(), watchdog.ProcessAlive)
	info.State, info.Restarts = ws.State, ws.Restarts
	if info.Running {
		info.Pid = ws.Pid
	}
	if li := ws.LastIncident; li != nil {
		info.LastIncidentAt, info.LastReason, info.LastFile = li.Time.Format(time.RFC3339), li.Reason, li.File
	}
	return info
}

// useWatchedAutostart 把开机自启换成"启动守护进程"的写法（正式运行；演示和远端界面继续用 mygo 的）
func (s *shell) useWatchedAutostart() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	a := watchdog.Autostart{Store: watchdog.RegistryRunStore(), Name: mygo.App.Name(), Exe: exe}
	s.autostart = &a
	s.openAtLogin, s.setOpenAtLogin = a.On, a.Set
}

// migrateAutostart 开机自启还是 mygo 以前的写法（直接启动桌面程序）就改成守护进程的写法。后台线程调，日志开了以后
func (s *shell) migrateAutostart() {
	if s.autostart == nil {
		return
	}
	switch changed, err := s.autostart.Migrate(); {
	case err != nil:
		s.logf("把开机自启改成启动守护进程失败：%v", err)
	case changed:
		s.logf("开机自启原来直接启动桌面程序，已改成启动守护进程（sumika.exe -watch）")
	}
}
