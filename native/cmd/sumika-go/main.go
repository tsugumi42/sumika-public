// Command sumika-go 是无界面的 Go 后台入口（规格-Go后台装配 第二节、第八节第 5 步）：照 app.Options 传给 app.Open，
// 再把 ctx 交给 App.Run；Ctrl+C 或关掉命令行窗口就让 ctx 取消，按第六节的顺序停。
//
// 日志每一行同时打到命令行窗口和 <数据目录>/logs/go-<日期>.log（internal/filelog，桌面程序共用，前缀是 desk）；启动时打一行要连的号（打码）、
// 数据目录、配置文件路径，不打令牌（app.newLogf 另外会把令牌原文抹掉）。
// media-check 在装配前分流，只读核对已有媒体；只向终端输出计数，不创建日志。
//
//	go run ./cmd/sumika-go -account 100            # 在 native/ 里跑；数据目录照配置（项目根 + dataDir）
//	go run ./cmd/sumika-go -account 100 -data <仓库>\data
//	go run ./cmd/sumika-go -mode archive -root <资源根> -data <数据目录>   # 只看历史，不连 QQ
//	go run ./cmd/sumika-go -mode media-check -data <数据目录>           # 停后台后只读核对媒体
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sumika-native/internal/app"
	"sumika-native/internal/applog"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/config"
	"sumika-native/internal/filelog"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/paths"
)

// 退出码：2 是参数不对（没给 -account），1 是启动或运行出错
const (
	exitUsage = 2
	exitError = 1
)

const usage = "用法：sumika-go [-root <资源根>] [-data <目录>] [-mode receive|archive|media-check] -account <号> | -local-only（receive 必须指定账号或只开本地）"

func main() {
	// Ctrl+C 是 os.Interrupt；Windows 上关掉命令行窗口走 SIGTERM，系统只给几秒，所以关机的活要在 ctx 取消以后马上做
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}

// run 是 main 的全部内容：退出码由它给，日志写在 stdout。ctx 取消就照 App.Run 的顺序关（测试里直接取消它）
// cliRoot 定诊断入口的资源根：给了 -root 就用它；不给照旧从当前目录往上找项目根。
// 不用桌面的 app.ResourceRoot：go run 的 exe 在临时编译目录里，诊断入口仍按当前目录定位。
func cliRoot(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	root, err := config.FindRoot(".")
	if err != nil {
		return "", fmt.Errorf("找不到项目根：%w", err)
	}
	return root, nil
}

// failureText 把启动或运行失败的原文过统一脱敏再写日志：令牌、会话号、长号码打码；
// 取不到令牌（配置读不出、密钥库打不开）时不给自由详情，和桌面宿主一致（#20）。
func failureText(opts app.Options, err error) string {
	var redactor app.LogRedactor
	redactor.Refresh(opts)
	return redactor.Redact(err.Error())
}

// logsRouted 在日志出口接好 slog 和标准库 log 之后调；生产里什么也不做，测试用它在 run 的这一段时间里往 slog 和 log 写，
// 看打码接线有没有接上（run 起的是整个后台，从外面碰不到这个窗口）
var logsRouted = func() {}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, dial app.DialContextFunc) int {
	fs := flag.NewFlagSet("sumika-go", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var account, data, resourceRoot, modeValue string
	var firstTakeover, watchdogStopped, localOnly bool
	fs.BoolVar(&firstTakeover, "first-takeover", false, "首次接管 QQ 发送权；核对并写交接后退出，不启动后台")
	fs.BoolVar(&watchdogStopped, "confirm-bun-watchdog-stopped", false, "明确确认看门脚本和计划任务不会拉起 Bun")
	fs.StringVar(&account, "account", "", "要连的 QQ 小号（receive 必填）")
	fs.BoolVar(&localOnly, "local-only", false, "只开本地，不连 QQ（receive 用；不能和 -account 一起给）")
	fs.StringVar(&data, "data", "", "数据目录；不给就照配置")
	fs.StringVar(&resourceRoot, "root", "", "资源根；不给就从当前目录往上找项目根")
	fs.StringVar(&modeValue, "mode", string(app.Receive), "启动模式：archive、receive 或 media-check（诊断入口默认 receive）")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// 媒体核对不加载配置、不建日志或锁文件；只认显式指定的已有目录。
	if modeValue == "media-check" {
		if localOnly || strings.TrimSpace(data) == "" || firstTakeover || watchdogStopped {
			fmt.Fprintln(stderr, "媒体核对需要显式 -data，不能与 -local-only 或首次接管一起运行")
			return exitUsage
		}
		counts, err := mediastore.CheckMedia(ctx, data)
		for _, c := range counts {
			if _, err := fmt.Fprintf(stdout, "%s 段数=%d 读到=%d 缺文件=%d 内容不符=%d hash为空=%d 读取出错=%d\n", c.Platform, c.Segments, c.Read, c.Missing, c.ContentMismatch, c.EmptyHash, c.ReadErrors); err != nil {
				return exitError
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, "媒体核对失败")
			return exitError
		}
		return 0
	}
	mode, err := app.ParseMode(modeValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	account = strings.TrimSpace(account)
	if localOnly && (mode != app.Receive || account != "") {
		fmt.Fprintln(stderr, "只开本地只能和 receive 一起用，而且不能同时指定 -account")
		return exitUsage
	}
	if firstTakeover && (mode != app.Receive || account == "" || data == "" || !watchdogStopped) {
		fmt.Fprintln(stderr, "首次接管需要 -mode receive、-account、显式 -data 和 -confirm-bun-watchdog-stopped")
		return exitUsage
	}
	if firstTakeover {
		if err := sendlock.FirstTakeover(ctx, data, account, watchdogStopped); err != nil {
			fmt.Fprintln(stderr, "首次接管失败："+err.Error())
			return exitError
		}
		fmt.Fprintln(stdout, "首次接管核对通过，已记录 first-takeover；未启动后台、未发送")
		return 0
	}
	if mode == app.Receive && account == "" && !localOnly {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	root, err := cliRoot(resourceRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	// 日志写在数据目录里，所以得先知道数据目录：照 app.Open 的路子，-data 空时用配置里的 dataDir
	cfg, err := config.LoadIn(root, data)
	if err != nil {
		fmt.Fprintln(stderr, "读配置："+err.Error())
		return exitError
	}
	dir := data
	if dir == "" {
		dir = cfg.DataDirIn(root)
	}
	file, err := config.FileIn(root, data)
	if err != nil {
		fmt.Fprintln(stderr, "读配置："+err.Error())
		return exitError
	}
	// 还没初始化的目录先写身份证：只有 paths.ErrUninitialized（不存在，或顶层只有 config/、characters/、models/）才 Init；
	// 旧布局、没恢复完、身份证读坏、有数据没身份证的目录照旧报错。filelog 也要有身份证才建 logs/。
	// 只看历史从不初始化：目录不存在或还没初始化就直接报错，不然留下的空目录会让之后的 receive 认不出身份证
	initialized := false
	var opened paths.Paths
	if mode == app.Archive {
		if _, err := paths.Open(dir); err != nil {
			fmt.Fprintln(stderr, "只看历史需要已有的数据目录："+err.Error())
			return exitError
		}
	} else if opened, initialized, err = paths.OpenOrInit(dir); err != nil {
		fmt.Fprintln(stderr, "建数据目录："+err.Error())
		return exitError
	}
	if localOnly && initialized {
		if err := app.EnsureExampleCharacter(opened, app.Options{Root: root}, true); err != nil {
			fmt.Fprintln(stderr, "准备角色卡："+err.Error())
			return exitError
		}
	}

	logs := applog.New(filelog.New(dir, "go", stdout, time.Now), time.Now)
	defer logs.Close()
	opts := app.Options{Root: root, DataDir: data, Mode: mode, Account: account, LocalOnly: localOnly, Logs: logs, DialContext: dial}
	// slog 和标准库 log 的行（checkpoint 的 slog.Warn、没注入日志的模块）从 misc 进来，调用方没有机会打码：在这里注入（规格-日志分级 4.2）
	var miscRedactor app.LogRedactor
	miscRedactor.Refresh(opts)
	defer app.RouteLogs(logs, miscRedactor.Redact)() // 还原 slog 和 log 的默认值：run 返回后别再往已关掉的 logs 写
	logsRouted()
	goLog := logs.Logf(applog.Go)
	if initialized {
		goLog("新建了数据目录 %s（写了身份证）", dir)
	}
	if localOnly {
		goLog("只开本地（不连 QQ），数据目录 %s，配置文件 %s", dir, file)
	} else {
		goLog("要连的号 %s，数据目录 %s，配置文件 %s", config.MaskID(account), dir, file)
	}

	a, err := app.Open(ctx, opts)
	if err != nil {
		goLog("启动失败：%s", failureText(opts, err))
		return exitError
	}
	if err := a.Run(ctx); err != nil {
		goLog("退出：%s", failureText(opts, err))
		return exitError
	}
	return 0
}
