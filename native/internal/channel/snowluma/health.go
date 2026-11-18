package snowluma

import (
	"bytes"
	"encoding/json"

	"sumika-native/internal/alert"
)

// 运行中的接收健康（拆解笔记 11.10、7.2）：协议端的 hook 会退化成"WS 通、心跳照来、收不到 QQ 数据"，
// 它自己在心跳的 status.good 里报。连接建立时 verify.go 调 get_status 看过一次，就绪之后这里接着看每一拍心跳：
// good（或 online）变假就 Raise 一条，变回真就 Clear；同一状态不重复提醒；心跳里没有 status、或 status 里
// 没有 good 和 online 的，不知道好坏，既不当成坏、也不当成恢复。
//
// 只提醒，不停发、不拆连接、不重启协议端（AGENTS「安全边界」：不自动部署或启动 SnowLuma）。
// 不另起轮询：心跳 30 秒一拍就是这个信号（bridge 另有 60 秒 get_status 兜底，以后看情况加，见 docs/subsystems/qq.md）。
// 只在就绪（phaseReady）时看：连接刚建立、核验没过的时候，状态由 get_status 那一路管（statusKey），不在这里重复提醒。

// heartbeatHealth 从一帧里读心跳报的接收状态。known 为假：不是心跳，或没有可判断的字段。
// 字段只认字面的 true / false：null、字符串、数字都当没有。
func heartbeatHealth(frame []byte) (known, healthy bool) {
	// 每条消息帧都经过这里，先用一个子串挡掉绝大多数；挡不住的（聊天里恰好有这个词）下面照样老实判断
	if !bytes.Contains(frame, []byte("heartbeat")) {
		return false, false
	}
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return false, false
	}
	if str(o, "post_type") != "meta_event" || str(o, "meta_event_type") != "heartbeat" {
		return false, false
	}
	var st object
	if json.Unmarshal(o["status"], &st) != nil || st == nil {
		return false, false
	}
	good, online := string(st["good"]), string(st["online"])
	switch {
	case good == "false" || online == "false":
		return true, false
	case good == "true" || online == "true":
		return true, true
	}
	return false, false
}

// noteHeartbeat 按这一帧更新"协议端自报收不到"的提醒。raised 记着上一次的结论，只在变化时 Raise、Clear。
func (a *Adapter) noteHeartbeat(frame []byte) {
	known, healthy := heartbeatHealth(frame)
	if !known {
		return
	}
	a.mu.Lock()
	if a.phase != phaseReady || a.alerts == nil || a.recvRaised == !healthy {
		a.mu.Unlock()
		return
	}
	a.recvRaised = !healthy
	a.mu.Unlock()
	if !healthy {
		a.alerts.Raise(alert.Alert{
			Key:    a.recvKey(),
			Class:  "QQ 接收",
			Title:  "SnowLuma 连着但收不到 QQ 消息",
			Body:   "协议端的心跳报它现在收不到 QQ 数据，群里和私聊的新消息进不来；需要去 SnowLuma 那边处理，Sumika 不会替你重启它",
			Detail: "",
		})
		a.warnf("心跳报接收不健康（status.good 或 online 为假），已提醒")
		return
	}
	a.alerts.Clear(a.recvKey(), alert.Note{Title: "SnowLuma 又能收到 QQ 消息了", Body: "心跳里的接收状态恢复了"})
	a.logf("心跳报接收恢复")
}

func (a *Adapter) recvKey() string { return "qq:" + a.cfg.Account + ":recv" }
