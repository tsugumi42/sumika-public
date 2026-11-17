package app

// 用户钩子的装配（规格-用户钩子）。内核只提供两个挂点，这里把 userhooks 插件接上：
// 全局配置在数据目录 config/hooks.json；项目级走项目说明同一份信任根（a.projectTrust，没开项目说明就是 nil，项目钩子不跑）。
// hooks.enabled 关着时插件登记但不启用，行为和没有这个功能时完全一样。

import (
	"time"

	"sumika-native/internal/userhooks"
)

func (a *App) openUserHooks() error {
	h := a.cfg.Hooks
	o := userhooks.Options{
		GlobalFile:     a.paths.HooksConfigFile(),
		Degrade:        a.degrade,
		Timeout:        time.Duration(h.TimeoutSeconds) * time.Second,
		MaxOutputBytes: h.MaxOutputBytes,
		MaxParallel:    h.MaxParallel,
		StopMax:        h.StopMaxContinues,
	}
	if a.projectTrust != nil { // 接口里装着 nil 指针会让"信任根没开"判不出来，所以先判再赋
		o.Trust = a.projectTrust
	}
	// hooks.enabled 是默认：plugins.userhooks.enabled 没写就跟它走；关着时插件照样登记（设置页能打开它），只是不开
	return a.mountPlugin(userhooks.New(o), h.Enabled)
}
