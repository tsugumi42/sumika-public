package app

// "需要主人看"的几件事（目标达成或暂停、定期任务有结果、审批卡出现）交给宿主，由宿主按"主窗口有没有在前台"决定弹不弹系统通知。
// 后台只管说"这里有件要看的事"，不知道窗口；每一轮结束都提醒会吵，所以只有这几类、同一会话同一类 20 秒内只说一次，
// 自检话题里的（sumika-ctl 开发用）不说。

import (
	"strings"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
)

// Attention 的种类
const (
	AttentionGoalDone   = "goal-done"   // 目标达成
	AttentionGoalPaused = "goal-paused" // 目标暂停了（要主人看一眼：卡住、预算到顶、出错、等回答）
	AttentionLoop       = "loop"        // 定期任务有内容要看，或出错
	AttentionApproval   = "approval"    // 有审批卡等主人点
	AttentionQuestion   = "question"    // 她有问题想问主人
)

// attentionGap 是同一会话同一类提醒的最短间隔
const attentionGap = 20 * time.Second

// Attention 是一件需要主人看的事；Title、Body 已经是给人看的中文
type Attention struct {
	Kind, Conversation, Character, Title, Body string
}

// attend 说一件需要主人看的事。没接宿主回调（测试、命令行）、自检话题、关机中、短时间内同一类重复，都不说。
func (a *App) attend(kind, conv, title, body string) {
	if a.opt.Attention == nil || a.closing.Load() || audience.IsSelftest(conv) {
		return
	}
	if !a.attentionEnabled(kind) {
		return
	}
	key := kind + "|" + conv
	now := a.now()
	a.attnMu.Lock()
	if a.attnLast == nil {
		a.attnLast = map[string]time.Time{}
	}
	if last, ok := a.attnLast[key]; ok && now.Sub(last) < attentionGap {
		a.attnMu.Unlock()
		return
	}
	a.attnLast[key] = now
	a.attnMu.Unlock()
	a.opt.Attention(Attention{Kind: kind, Conversation: conv, Character: characterOfConv(conv), Title: title, Body: clipText(body, 80)})
}

// attendApproval 在审批卡出现时说一声：只管本地私聊（主窗口能看到、能点的那些），别的场合的审批不在主窗口里批。
func (a *App) attendApproval(r approval.Request) {
	conv := a.cardConversation(r)
	if audience.IsWhisper(conv) { // 悄悄话那一轮的卡：通知指向来源会话（卡在那里的右栏对话日志里批）；找不到来源就不弹，没有窗口能看
		wh, _, ok := a.whisperApproval(conv)
		if !ok {
			return
		}
		conv = wh.Source.Conversation
	} else if !localPrivateConv(conv) {
		return
	}
	title := r.Title
	if title == "" {
		title = r.Tool
	}
	a.attend(AttentionApproval, conv, "有一项操作等你批准", title)
}

// cardConversation 是这张审批卡该显示在哪个会话。子任务跑在自己的内部会话里，主人没有那个窗口：
// 卡片放回派它的会话，不然它的每一步都没人看得到，只能等到过期。身份和授权照旧按子任务自己的算。
func (a *App) cardConversation(r approval.Request) string {
	if a.ecosystem == nil || a.ecosystem.Tasks == nil {
		return r.Conversation
	}
	return cardConversationOf(a.ecosystem.Tasks.Owner, r)
}

// cardConversationOf 是上面的判断本身：只认 internal: 开头、而且确实有这个正在跑的子任务的；
// 找不到派它的会话就原样不动（卡片留在内部会话里，主人看不到，等过期，不会跑到别的窗口去）。
func cardConversationOf(owner func(taskID string) (kernel.Scope, bool), r approval.Request) string {
	id, ok := strings.CutPrefix(r.Conversation, "internal:")
	if !ok {
		return r.Conversation
	}
	if s, ok := owner(id); ok && s.Conversation != "" {
		return s.Conversation
	}
	return r.Conversation
}

// attentionEnabled 是设置页“通知”里这一类开着没有。每次现读配置文件，设置页一改就生效、不用重启；
// 读不到（文件坏了）就当默认全开，宁可多弹不漏掉。
// 主动联系的通知有自己的开关（proactive.notify，在 proactivethread.go 里判），这里不管。
func (a *App) attentionEnabled(kind string) bool {
	n := config.DefaultNotifications()
	if saved, err := config.LoadIn(a.root, a.opt.DataDir); err == nil {
		n = saved.Notifications
	}
	return attentionKindOn(n, kind)
}

// attentionKindOn 把种类对到开关；提醒的种类后面带编号（reminder:12），不认识的种类照常通知
func attentionKindOn(n config.Notifications, kind string) bool {
	switch {
	case kind == AttentionGoalDone, kind == AttentionGoalPaused:
		return n.Goal
	case kind == AttentionLoop:
		return n.Loop
	case kind == AttentionApproval:
		return n.Approval
	case kind == AttentionQuestion:
		return n.Question
	case kind == AttentionReminder, strings.HasPrefix(kind, AttentionReminder+":"):
		return n.Reminder
	}
	return true
}
