package main

// 桌面程序的日志文件：-H windowsgui 编出来的程序没有控制台，后台的日志（app.Options.Logf，nil 时是 log.Printf）
// 一行都留不下。这里把它接到 <数据目录>/logs/desk-<日期>.log（internal/filelog，和诊断入口 cmd/sumika-go 的 go-<日期>.log 同一口径，
// 只是前缀不同、不打命令行窗口）。
//
// 后台日志和宿主日志、状态共用 app.RedactLog；宿主在发布状态前抹掉已知令牌、会话号和长数字，
// status / receive 和界面只拿脱敏后的快照。filelog 只负责落盘和换行，不读取配置或密钥。
// 演示数据（-demo）不碰任何数据目录，也就不开日志。

import (
	"time"

	backend "sumika-native/internal/app"
	"sumika-native/internal/applog"
	"sumika-native/internal/config"
	"sumika-native/internal/filelog"
)

// deskLogPrefix 是桌面程序日志文件名的前缀
const deskLogPrefix = "desk"

// openDeskLog 开桌面程序的日志。数据目录的定法和 app.Open 一样（-data，或配置里的 dataDir）；读不到配置就不开，
// 宿主随后读同一份配置时会把原因写进状态区。数据目录还没有身份证时 filelog 不建 logs/，等 receive 初始化了目录再写。
func openDeskLog(opt backend.Options, now func() time.Time) (*applog.Root, error) {
	location, err := backend.ResourceRoot(opt.Root)
	if err != nil {
		return nil, err
	}
	dir, err := resolveDataDir(opt)
	if err != nil {
		return nil, err
	}
	file, err := config.FileIn(location.Dir, opt.DataDir)
	if err != nil {
		return nil, err
	}
	root := applog.New(filelog.New(dir, deskLogPrefix, nil, now), now)
	// 下面几行启动行直接走 root：先挂上不靠令牌的打码（路径、会话号、长数字）；外壳随后换成宿主那份带令牌的
	root.SetRedact(func(s string) string { return backend.RedactLog(s, nil) })
	desk := root.Logf(applog.Desk)
	desk("桌面程序启动，数据目录 %s，配置文件 %s", dir, file)
	if crashLogEnabled {
		if _, err := openCrashLog(dir, now()); err != nil {
			desk("崩溃记录打不开：%v（程序崩溃时出错信息会丢）", err)
		}
	}
	return root, nil
}

// resolveDataDir 数据目录的定法和 app.Open 一样：-data，或配置里的 dataDir。日志和桌面开关（desktop-settings.json）都放在它里面
func resolveDataDir(opt backend.Options) (string, error) {
	location, err := backend.ResourceRoot(opt.Root)
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadIn(location.Dir, opt.DataDir)
	if err != nil {
		return "", err
	}
	if opt.DataDir != "" {
		return opt.DataDir, nil
	}
	return cfg.DataDirIn(location.Dir), nil
}
