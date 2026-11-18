package catalog

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 聊天类例子共用的小件：宿主、卡片、造消息。人名、句子都是编的。

// chatBase 是固定样例的时间；活的例子用 c.Now()。
var chatBase = time.Date(2026, 11, 6, 21, 30, 0, 0, time.Local)

// ChatHost 照正式聊天区的写法建宿主：令牌、字体、markdown 状态、过程展开状态都来自 env。
// column 是消息列宽，气泡最宽是它的一部分。
func (e Env) ChatHost(column float32, enter bool) chatui.Host {
	return chatui.Host{
		T: e.Kit.ChatTokens(), Font: e.Kit.Font, Mono: e.Mono, SelfChar: chatui.CharColors[0], Column: column, NoEnter: !enter,
		MD: e.Store.MD, MenuTheme: e.Kit.MenuTheme(), DenyReason: true,
		StepOpen:    func(id string) *bool { return e.Open("step-" + id) },
		BubbleGlass: e.Kit.BubbleGlassBlur(e.Store.Look.Alpha(), e.Store.Look.BlurRadius()), SelfGlassTint: e.Store.Look.SelfTint(),
	}
}

// chatCardWidth 是并排摆几张聊天卡时每张的宽：窗口宽按 n 份分，不窄于能放下两三个字的气泡。
func (e Env) chatCardWidth(n int) float32 {
	g := e.Kit.G
	w := (e.Room - float32(n-1)*g.SpaceMedium) / float32(n)
	return max(g.ControlWide*9/8, min(w, g.ControlWide*2)) // 不窄于放得下审批卡三个按钮的宽（气泡最宽是列宽的 .8）；窗口不够就换行，不挤
}

// ChatCard 是一块聊天区底色的卡：气泡是照聊天区的底色设计的，放在舞台底色上颜色会不对。
// 卡片内距取面板内距，圆角和内距一样大（和设置页预览框同一个做法）。
func (e Env) ChatCard(c *ui.Context, name string, width float32, body func(column float32)) {
	g := e.Kit.G
	inset := g.SpacePanel
	ui.Column(c.Key(e.Key(name))).Width(width).Shrink(0).Padding(inset).Radius(inset).Clip().Background(e.Kit.T.C.ChatBg).Children(func() {
		body(width - 2*inset)
	})
}

// messages 把一串消息按“上一条、下一条”交给宿主画。
func messages(c *ui.Context, h chatui.Host, ms []*chatui.Msg) {
	for i, m := range ms {
		var prev, next *chatui.Msg
		if i > 0 {
			prev = ms[i-1]
		}
		if i+1 < len(ms) {
			next = ms[i+1]
		}
		h.Message(c, m, prev, next)
	}
}

func her(id string, sec int, text string) *chatui.Msg {
	return &chatui.Msg{ID: id, Sender: "r1", Name: "晴枝", Char: chatui.CharColors[0], At: chatBase.Add(time.Duration(sec) * time.Second), Text: text, Markdown: true}
}

func mine(id string, sec int, text string) *chatui.Msg {
	return &chatui.Msg{ID: id, Sender: "self", Self: true, Name: "我", At: chatBase.Add(time.Duration(sec) * time.Second), Text: text}
}

// bubbleLive 是聊天气泡试一试的状态：消息串、已发了几条、等重发的那条什么时候发出去。
type bubbleLive struct {
	Msgs    []*chatui.Msg
	Sent    int
	Draft   string
	RetryAt map[string]time.Time
}

// 重发要等多久才算发出去：模拟后台确认，不是动效。
const retryDelay = 700 * time.Millisecond // 行为计时：模拟网络往返，让“发送中”能被看见

var herReplies = []string{"收到啦，我记下了。", "这个想法不错，晚点我把它排进今天的事里。", "好，那就先从最简单的一步开始。", "我在，慢慢说。"}

// bubbleDemo：对方的、自己的、连续两条；试一试里能发消息、看发送失败再重发。
func bubbleDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	w := e.chatCardWidth(4)
	e.Section(c, "states", "状态排", func() {
		e.Cells(c, "row", func() {
			e.Cell(c, "both", "对方和自己并排：同一种浮起、都不描边", func() {
				e.ChatCard(c, "both-card", w, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{
						her("st-b1", 0, "窗边的纸灯亮起来了，今天想聊些什么？"),
						mine("st-b2", 20, "先把早上的小事慢慢说一遍。"),
						her("st-b3", 40, "好呀。"),
					})
				})
			})
			e.Cell(c, "her", "对方的", func() {
				e.ChatCard(c, "her-card", w, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{her("st-her", 0, "窗边的纸灯亮起来了，今天想聊些什么？")})
				})
			})
			e.Cell(c, "mine", "自己的", func() {
				e.ChatCard(c, "mine-card", w, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{mine("st-mine", 0, "先把早上的小事慢慢说一遍。")})
				})
			})
			e.Cell(c, "run", "连续两条", func() {
				e.ChatCard(c, "run-card", w, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{
						her("st-run1", 0, "我看了你昨天的笔记。"),
						her("st-run2", 3, "停在窗边那一段，要接着写吗？"),
					})
				})
			})
		})
	})

	live := Local(e, "live", func() *bubbleLive {
		return &bubbleLive{RetryAt: map[string]time.Time{}, Msgs: []*chatui.Msg{
			her("lv-0", 0, "早，今天想从哪件事开始？"),
			mine("lv-1", 10, "先把纸灯那篇笔记补完。"),
		}}
	})
	b := *live
	e.Section(c, "play", "试一试", func() {
		// 重发中的消息到点就算发出去
		now := c.Now()
		for _, m := range b.Msgs {
			if at, ok := b.RetryAt[m.ID]; ok {
				if now.Before(at) {
					c.After(at.Sub(now))
				} else {
					m.State = 0
					delete(b.RetryAt, m.ID)
				}
			}
		}
		send := func(text string, fail bool) {
			b.Sent++
			id := fmt.Sprintf("lv-s%d", b.Sent)
			m := &chatui.Msg{ID: id, Sender: "self", Self: true, Name: "我", At: now, Text: text}
			b.Msgs = append(b.Msgs, m)
			if fail {
				m.State = chatui.Failed
			} else {
				b.Msgs = append(b.Msgs, &chatui.Msg{ID: id + "-r", Sender: "r1", Name: "晴枝", Char: chatui.CharColors[0], At: now.Add(time.Second), Text: herReplies[b.Sent%len(herReplies)], Markdown: true})
			}
			if len(b.Msgs) > 6 { // 只留最近几条，舞台不滚动
				b.Msgs = b.Msgs[len(b.Msgs)-6:]
			}
		}
		ui.Row(c.Key(e.Key("play-row"))).FillWidth().Gap(g.SpaceMedium).AlignItems(ui.Start).Children(func() {
			e.ChatCard(c, "play-card", min(e.Room, g.ControlWide*2), func(col float32) {
				h := e.ChatHost(col, true)
				h.OnRetry = func(id string) {
					for _, m := range b.Msgs {
						if m.ID == id {
							m.State = chatui.Sending
							b.RetryAt[id] = c.Now().Add(retryDelay)
						}
					}
				}
				messages(c, h, b.Msgs)
			})
			ui.Column(c.Key(e.Key("play-side"))).Gap(g.SpaceSmall).Width(g.ControlWide).Shrink(0).Children(func() {
				k.TextField(c, e.Key("draft"), &b.Draft, "写一句话").FillWidth()
				ui.Row(c.Key(e.Key("play-btns"))).Gap(g.SpaceSmall).Wrap().Children(func() {
					k.Button(c, e.Key("send"), "发送", true, b.Draft == "", func() { send(b.Draft, false); b.Draft = "" })
					k.Button(c, e.Key("send-fail"), "发一条失败的", false, b.Draft == "", func() { send(b.Draft, true); b.Draft = "" })
				})
				e.Line(c, "play-out", "失败的那条点红色感叹号可以重发。")
			})
		})
	})
}
