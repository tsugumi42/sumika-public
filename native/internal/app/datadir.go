package app

// P2-1：只初始化数据目录（规格-不带QQ也能用 设计第 3 条）。只看历史从不初始化；这里只给宿主的启动用。

import (
	"context"
	"fmt"

	"sumika-native/internal/applog"
	"sumika-native/internal/channel/snowluma"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// openInitStores 是建空库的出口，测试注入失败用；生产就是 store.OpenGoOpts
var openInitStores = store.OpenGoOpts

// halfInitialized 把“身份证已经写了、库没建完”的错误说清楚：下一次启动看到身份证不会再建库，
// 用户要先决定里面有没有要留的东西，删掉身份证重新来。
func halfInitialized(p paths.Paths, err error) error {
	return fmt.Errorf("数据目录初始化到一半（%s）：确认里面没有要留的东西后，删掉 %s 再打开（%w）", p.Root, paths.MetaFile, err)
}

// InitDataDir 只做初始化：目录还没初始化时写身份证、建空的平台库和 trace，然后关掉；已经初始化的目录什么都不做。
// 旧布局、没恢复完、身份证读坏、有数据没身份证的目录照旧报原来的错（paths.OpenOrInit 只认 ErrUninitialized）。
// 返回的 initialized 表示这次真的初始化了，调用方据此决定要不要复制示例卡、写日志。
func InitDataDir(ctx context.Context, opt Options) (paths.Paths, bool, error) {
	if err := ctx.Err(); err != nil {
		return paths.Paths{}, false, err
	}
	location, err := ResourceRoot(opt.Root)
	if err != nil {
		return paths.Paths{}, false, err
	}
	cfg, err := config.LoadIn(location.Dir, opt.DataDir)
	if err != nil {
		return paths.Paths{}, false, fmt.Errorf("读配置：%w", err)
	}
	dataDir := opt.DataDir
	if dataDir == "" {
		dataDir = cfg.DataDirIn(location.Dir)
	}
	p, initialized, err := paths.OpenOrInit(dataDir)
	if err != nil {
		return paths.Paths{}, false, err
	}
	if !initialized {
		return p, false, nil
	}
	for _, pl := range store.DefaultRegistry.Platforms() {
		if err := snowluma.CheckDBPath(p.ChatDB(pl.Name)); err != nil {
			return p, false, halfInitialized(p, err)
		}
	}
	if err := snowluma.CheckDBPath(p.GoTraceDB()); err != nil {
		return p, false, halfInitialized(p, err)
	}
	var logf func(string, ...any)
	if opt.Logs != nil {
		logf = opt.Logs.Logf(applog.Store)
	}
	set, err := openInitStores(p, store.DefaultRegistry, store.OpenOptions{Logf: logf})
	if err != nil {
		return p, false, halfInitialized(p, fmt.Errorf("初始化空库：%w", err))
	}
	if err := set.Close(); err != nil {
		return p, false, halfInitialized(p, fmt.Errorf("初始化空库后关闭：%w", err))
	}
	if opt.Logs != nil {
		opt.Logs.Logf(applog.App)("新建了数据目录 %s（写了身份证）", p.Root)
	}
	return p, true, nil
}
