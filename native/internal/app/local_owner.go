package app

// 本地私聊交给 Bun（规格-本地私聊切到Bun 第三、八节）：配置 backend.local 是 bun 时，local.db 由 Bun 写，Go 后台
//   - 不拿 go/local.lock，只读打开 local.db（store.OpenGoOpts：连接上就写不了）；
//   - 不装本地群聊、悄悄话、目标、定期任务、提醒、主动联系：这些全是只写本地的，不装就不会到点触发、不会开轮；
//   - 本地审批、工作区、检查点、自检等写方法回“现在由 Bun 管”（local_owner 的 bunOwned）；
//   - 每秒查一次 local.db 的 PRAGMA data_version，Bun 写了就叫醒消息、总览的订阅；
//   - 启动收拾不碰 console 的在途行（store.foreignClauses）。
// QQ 的一切照旧。go 模式（默认）的 receive 阶段多拿一把 local.lock，防止有人手动起了一个 Bun 去写本地（archive 只读，不拿）。

import (
	"errors"
	"fmt"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/store"
)

// pickLocalOwner 定下这个 App 的本地私聊归谁：宿主给了（Options.LocalOwner）就照宿主的，没给照配置文件。
// App 里所有的判断都只看这一个结果，不再各自读 cfg.Backend（宿主和 App 各读一次，中途改配置会对不上）
func pickLocalOwner(host string, cfg config.Backend) (string, error) {
	switch host {
	case "":
		return cfg.Local, nil
	case config.LocalByGo, config.LocalByBun:
		return host, nil
	}
	return "", fmt.Errorf("%w：LocalOwner 只允许 go 或 bun，收到 %q", ErrArguments, host)
}

// localByBun 说本地私聊是不是交给了 Bun。Open 时定下，之后不变
func (a *App) localByBun() bool { return a.localOwner == config.LocalByBun }

// localReadOnlyPlatforms 是要只读打开的平台：local 交给 Bun 时就是它
func (a *App) localReadOnlyPlatforms() []string {
	if a.localByBun() {
		return []string{"local"}
	}
	return nil
}

// watchExternalLocal：local 交给 Bun 时开始查 data_version（archive 也要：它读的 local.db 同样是 Bun 在写）
func (a *App) watchExternalLocal() {
	if !a.localByBun() {
		return
	}
	st, ok := a.set.Platform("local")
	if !ok {
		return
	}
	every := a.opt.ExternalPollEvery
	if every <= 0 {
		every = store.ExternalPollInterval
	}
	a.stopExt = st.PollExternalEvery(every)
}

// closeSet 先停 data_version 的轮询，再关库
func (a *App) closeSet() error {
	a.closeBackup()    // 先停备份：取消正在做的那份，之后才关库
	a.closeSelfcheck() // 再收自检：它的只读连接也在这个库目录里，要在关库前关掉
	a.closeEnvInputs() // 环境快照的运行部分也有只读连接，同样要在关库前关掉
	if a.stopExt != nil {
		a.stopExt()
		a.stopExt = nil
	}
	return a.set.Close()
}

// closeLocks 放两把锁（local.lock 在 bun 模式下没拿，Close 对 nil 是空操作）
func (a *App) closeLocks() error {
	return errors.Join(a.dataLock.Close(), a.localLock.Close())
}

// bunOwned：local 交给 Bun 时，本地私聊的写方法一律回这个错，不去碰任何库
func (r *DesktopReader) bunOwned() error {
	if r == nil || r.desk == nil || r.desk.app == nil || !r.desk.app.localByBun() {
		return nil
	}
	return api.ErrLocalOwnedByBun
}
