package app

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/taskrun"
)

// sceneText 是提醒里的场合说法（词汇表：QQ 私聊、QQ 群），这里只会是 QQ 的两种
func sceneText(scene string) string {
	if scene == types.ScenePrivate {
		return "QQ 私聊"
	}
	return "QQ 群"
}

// qqSendMode 执行发送上线 5.1。首次要健康；启用后由适配器停发，不因健康变化改档位。
// 复读有没有听众函数不在这里管（2026-11-07 改）：复读没有听众时自己拦下不发（repeat.go 的 "守卫没有听众"），
// 不该因为一个功能没接好就钳住整个 QQ 发送（notes/盘点-插件解耦-2026-11-07.md，ADR-289）。
func (a *App) qqSendMode(raw func(types.Conversation) string, out *channel.OutletConfig) func(types.Conversation) string {
	var mu sync.Mutex
	var enabled bool
	lastReason := map[string]string{}
	intentWarned := false // 按场合分开记：私聊不钳、群钳时两边的原因不互相顶掉
	return func(c types.Conversation) string {
		m := raw(c)
		if c.Channel != "qq" || m != channel.ModeSpeak {
			return m
		}
		mu.Lock()
		defer mu.Unlock()
		reason := ""
		// 主人的私聊：听的人就是主人，守卫没到完整能力也不钳（ADR-311 第 1 条）；群照旧钳
		ownerPrivate := c.Scene == types.ScenePrivate && a.cfg.QQ.Owner != "" && c.ID == string(a.cfg.QQ.Owner)
		in, intentErr := sendlock.ReadIntent(a.paths.Root)
		if intentErr == nil && in.Holder == sendlock.HolderBun && a.sendLock.CanSend() {
			if _, err := os.Stat(sendlock.IntentFile(a.paths.Root)); errors.Is(err, os.ErrNotExist) {
				// 运行中意图文件被删了：锁在自己手里就是 Go 接管着，照常发并提醒（ADR-311 第 3 条），不悄悄当成 Bun 持有
				in = sendlock.Intent{Holder: sendlock.HolderGo, Account: a.account}
				if !intentWarned {
					intentWarned = true
					a.logf("QQ 发送权：意图文件不见了，发送锁仍在 Go 手里，按 Go 持有继续发")
					a.alerts.Raise(alert.Alert{Key: "qq:" + a.account + ":intent", Title: "发送权意图文件不见了", Body: "运行中意图文件被删了；发送锁仍在 Go 手里，按 Go 持有继续发。下次启动会按交接记录补回"})
				}
			}
		}
		switch {
		case !a.cfg.QQ.GoSend:
			reason = "qq.goSend 没开"
		case intentErr != nil || in.Holder != sendlock.HolderGo || in.Account != a.account:
			reason = "意图没有交给这个号的 Go"
		case !a.sendLock.CanSend():
			reason = "没有持锁或没有可接过的 protocol 1 记录"
		case !ownerPrivate && !a.guardFull(a.character):
			reason = "守卫没有 full 能力"
		case out.Check == nil:
			reason = "没接发送检查"
		case !enabled && (a.adapter == nil || !a.adapter.Ready()):
			reason = "登录核验尚未通过或当前不健康"
		}
		if reason != "" {
			enabled = false
			if reason != lastReason[c.Scene] {
				a.logf("QQ 发送钳制保持（%s）：%s", c.Scene, reason)
				lastReason[c.Scene] = reason
				// 钳制从无到有、换了原因时记一行并提醒（同场合同键去重）：只在变化时，不逐条消息记
				class := taskrun.ClassClamped
				if intentErr != nil || in.Holder != sendlock.HolderGo || in.Account != a.account {
					class = taskrun.ClassIntentMissing
				}
				a.degrade.Degrade(taskrun.ModuleSend, class, errors.New(reason), taskrun.Opts{
					Alert: &alert.Alert{Key: "qq:" + a.account + ":clamp:" + c.Scene, Class: "QQ 发送", Title: "QQ 发送被钳成只存不回（" + sceneText(c.Scene) + "）", Body: reason}})
			}
			return channel.ModeDryRun
		}
		enabled = true
		if lastReason[c.Scene] != "" {
			a.alerts.Clear("qq:"+a.account+":clamp:"+c.Scene, alert.Note{Title: "QQ 发送恢复正常", Body: sceneText(c.Scene) + "不再被钳制"})
		}
		lastReason[c.Scene] = ""
		return m
	}
}

func (a *App) checkQQSend(character string, origin string, conv types.Conversation, parts []types.Part) error {
	if conv.Channel == "qq" {
		if why, paused := a.gate.Paused("qq"); paused {
			err := fmt.Errorf("QQ 发送已暂停：%s", why)
			if origin == linkOrigin {
				return &linkplugin.Blocked{Why: "闸门：QQ 发送已暂停", Err: err} // 链接插件的日志记成“没发”，不当发送失败
			}
			return err
		}
	}
	if origin == linkOrigin {
		return a.checkLinkParts(character, conv, parts)
	}
	return nil
}

func (a *App) onQQPause(ch string, paused bool, reason string) {
	if ch == "qq" && paused {
		a.alerts.Raise(alert.Alert{Key: "qq:" + a.account + ":send", Class: "QQ 发送", Title: "暂停了 QQ 发送", Body: reason})
	}
}

func (a *App) onQQResume(ch string) {
	if ch == "qq" {
		a.alerts.Clear("qq:"+a.account+":send", alert.Note{Title: "QQ 发送恢复了", Body: "通道门已恢复"})
	}
}

func (a *App) onQQPart(p channel.PartResult) {
	a.sendResultMu.Lock()
	defer a.sendResultMu.Unlock()
	if !p.Confirmed && p.Origin == linkOrigin && p.Class == taskrun.ClassRejected {
		// 链接展开按形式一级级试图（直接发、合并转发、转 180°），每一级被拒都是内容的事，不是通道坏了；
		// 不数进通道门的连续失败（否则一张图三级都被拒就暂停整个 QQ，文字版也发不出去），也不弹“发不出去”的提醒。
		// 整个阶梯都不成时，链接插件自己写文字版并上报（ADR-333）；核实没进群的也归这一类
		return
	}
	a.gate.PartEnded(p.Conv.Channel, p.Confirmed, !p.Confirmed)
	if p.Conv.Channel != "qq" {
		return
	}
	key := "qq:" + a.account + ":send"
	if p.Confirmed {
		a.alerts.Clear(key, alert.Note{Title: "QQ 消息发出去了", Body: "已经确认"})
		return
	}
	body := "一段消息用完重试仍未发出"
	switch p.Class {
	case taskrun.ClassUnsupported:
		body = "这种消息在这里发不出去（适配器不支持），没有重试" // 不支持和重试用完分开说（盘点 第一类第 3 条）
	case taskrun.ClassRejected:
		body = "协议端明确拒绝了这条消息，没有原样重试（ADR-333）"
	}
	if _, paused := a.gate.Paused("qq"); paused {
		return
	} // OnPause 已将同键标题换成暂停。
	for _, x := range a.alerts.Open() {
		if x.Key == key {
			return
		}
	}
	a.alerts.Raise(alert.Alert{Key: key, Class: "QQ 发送", Title: "有消息发不出去", Body: body})
}
