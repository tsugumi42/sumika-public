package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 悬停操作条的测试台：一条消息放在一列里，量整行（Label "消息：<id>"）的高度，悬停、点击都走 Tester。
type barRig struct {
	tt      *ui.Tester
	m       *Msg
	replies []Selected
}

func newBarRig(t *testing.T, m *Msg, group, canReply bool) *barRig {
	t.Helper()
	r := &barRig{m: m}
	host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Group: group, MD: NewMD()}
	if canReply {
		host.OnReply = func(s Selected, _ bool) { r.replies = append(r.replies, s) }
	}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(100, 24, 24, 24).Children(func() { host.Message(c, r.m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func (r *barRig) rowH(t *testing.T) float32 {
	t.Helper()
	rect, ok := r.tt.Find("消息：" + r.m.ID)
	if !ok {
		t.Fatalf("找不到消息行：%v", r.tt.Texts())
	}
	return rect.H
}

// away 把指针移到消息行外面；over 移到气泡正文上。
func (r *barRig) away() { r.tt.Move(3, 3); r.tt.Frame(); r.tt.Frame() }
func (r *barRig) over(t *testing.T, word string) {
	t.Helper()
	rect, ok := findText(r.tt, word)
	if !ok {
		t.Fatalf("找不到正文：%v", r.tt.Texts())
	}
	r.tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	r.tt.Frame()
	r.tt.Frame()
}

// click 点一个按钮的中心（不先 Frame：模拟指针刚到就点）。
func (r *barRig) click(t *testing.T, label string) {
	t.Helper()
	rect, ok := r.tt.Find(label)
	if !ok {
		t.Fatalf("没有 %q：%v", label, r.tt.Texts())
	}
	r.tt.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	r.tt.Frame()
}

func plainMsg(self bool) *Msg {
	return &Msg{ID: "m1", Sender: "她", Name: "她", Self: self, Text: selMsgText, Blocks: segview.TextBlocks(selMsgText), At: time.UnixMilli(1000)}
}

// 默认不可点；悬停到行上才能点；点复制后剪贴板是整条原文，图标变对勾，过一会儿变回去。
func TestHoverBarOnlyClickableWhenHovered(t *testing.T) {
	r := newBarRig(t, plainMsg(false), false, true)
	r.away()
	if _, ok := r.tt.Find("复制消息"); !ok {
		t.Fatalf("操作条要始终占位，没悬停时也该在树里：%v", r.tt.Texts())
	}
	// 指针不在行上：两个按钮都是禁用的（不可点）；到了行上才启用
	for _, label := range []string{"复制消息", "回复消息"} {
		if info, ok := r.tt.Access(label); !ok || !info.Disabled {
			t.Fatalf("没悬停时 %q 该是禁用的：%+v %v", label, info, ok)
		}
	}
	r.away()
	r.over(t, "第一句")
	for _, label := range []string{"复制消息", "回复消息"} {
		if info, ok := r.tt.Access(label); !ok || info.Disabled {
			t.Fatalf("悬停后 %q 该能点：%+v %v", label, info, ok)
		}
	}
	r.click(t, "复制消息")
	if got := r.tt.Clipboard(); got != selMsgText {
		t.Fatalf("复制到 %q，应是整条 %q", got, selMsgText)
	}
	if _, ok := r.tt.Find("已复制"); !ok {
		t.Fatalf("复制后图标该变成对勾（标签已复制）：%v", r.tt.Texts())
	}
	r.tt.Advance(copiedFor + 50*time.Millisecond)
	r.tt.Frame()
	if _, ok := r.tt.Find("复制消息"); !ok {
		t.Fatalf("对勾没有按时变回复制：%v", r.tt.Texts())
	}
	r.over(t, "第一句")
	r.click(t, "回复消息")
	if len(r.replies) != 1 || r.replies[0].MessageID != "m1" || r.replies[0].Text != selMsgText {
		t.Fatalf("回复应引用整条：%+v", r.replies)
	}
}

// 行高不随悬停变，也不因为有没有操作条而变：单行、多行、自己和别人、群聊和私聊都量。
func TestHoverBarKeepsRowHeight(t *testing.T) {
	long := strings.Repeat("很长的一段话要折成好几行。", 12)
	for _, self := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			for name, text := range map[string]string{"单行": "短", "多行": long} {
				m := &Msg{ID: "m1", Sender: "她", Name: "她", Self: self, Text: text, Blocks: segview.TextBlocks(text), At: time.UnixMilli(1000)}
				with := newBarRig(t, m, group, true)
				bare := newBarRig(t, m, group, false) // 没有回复，操作条只剩复制
				recalled := *m
				recalled.State = Recalled // 撤回的没有操作条：拿它当"没有条"的对照
				without := newBarRig(t, &recalled, group, true)
				with.away()
				h0 := with.rowH(t)
				with.over(t, string([]rune(text)[:1]))
				if h1 := with.rowH(t); h1 != h0 {
					t.Fatalf("self=%v group=%v %s：悬停后行高 %v → %v", self, group, name, h0, h1)
				}
				if h := without.rowH(t); h != h0 || bare.rowH(t) != h0 {
					t.Fatalf("self=%v group=%v %s：有无操作条行高不同：有 %v、只有复制 %v、无 %v", self, group, name, h0, bare.rowH(t), h)
				}
			}
		}
	}
}

// 回复只在宿主给了 OnReply 时出现；撤回的消息没有复制也没有回复；还在出字的也不摆。
func TestHoverBarWhichButtons(t *testing.T) {
	r := newBarRig(t, plainMsg(false), false, false)
	if _, ok := r.tt.Find("复制消息"); !ok {
		t.Fatal("没有回复入口时复制仍该有")
	}
	if _, ok := r.tt.Find("回复消息"); ok {
		t.Fatal("宿主没给 OnReply，不该摆回复")
	}
	for name, st := range map[string]State{"撤回": Recalled, "出字中": Streaming} {
		m := plainMsg(false)
		m.State = st
		r := newBarRig(t, m, false, true)
		for _, label := range []string{"复制消息", "回复消息"} {
			if _, ok := r.tt.Find(label); ok {
				t.Fatalf("%s的消息不该有 %q", name, label)
			}
		}
	}
	img := &Msg{ID: "m1", Sender: "她", Name: "她", At: time.UnixMilli(1000), Blocks: []segview.Block{{Kind: segview.Images, Imgs: []segview.Segment{{Position: 2, Width: 100, Height: 100}}}}}
	if _, ok := newBarRig(t, img, false, true).tt.Find("复制消息"); ok {
		t.Fatal("只有图片的消息没有可复制的文字，不该摆操作条")
	}
}

// 分块消息按块的顺序拼；四种场合（本地私聊、本地群聊、QQ 私聊、QQ 群）在这一层只差 Group 和有没有 OnReply，都正常。
func TestHoverBarCopyPartsAndScenes(t *testing.T) {
	parts := []Part{{Text: "前一块"}, {Quote: &PartQuote{Target: "x", Name: "他", Snippet: "被引的话"}}, {Text: "后一块"}}
	scenes := []struct {
		name         string
		group, reply bool
	}{{"本地私聊", false, true}, {"本地群聊", true, false}, {"QQ 私聊", false, false}, {"QQ 群", true, false}}
	for _, sc := range scenes {
		m := &Msg{ID: "m1", Sender: "她", Name: "她", At: time.UnixMilli(1000), Text: "前一块 被引的话 后一块", Parts: parts}
		r := newBarRig(t, m, sc.group, sc.reply)
		rect, ok := r.tt.Find("消息：m1")
		if !ok {
			t.Fatalf("%s：没画出来", sc.name)
		}
		r.tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
		r.tt.Frame()
		r.tt.Frame()
		r.click(t, "复制消息")
		if got := r.tt.Clipboard(); got != "前一块\n后一块" {
			t.Fatalf("%s：分块消息复制到 %q", sc.name, got)
		}
		if _, ok := r.tt.Find("回复消息"); ok != sc.reply {
			t.Fatalf("%s：回复入口 %v，应为 %v", sc.name, ok, sc.reply)
		}
	}
}

// 过程气泡没有操作条；折叠头里的用时：做完且有命令用时才写，进行中和只有思考时不写。
func TestProcessElapsedOnlyWhenKnown(t *testing.T) {
	think := &Step{Kind: StepThink, State: StepDone, Seconds: 3}
	cmd := &Step{Kind: StepCommand, State: StepDone, Seconds: 40, Target: "ls"}
	if s := Summarize([]*Step{think}); s.Elapsed != 0 {
		t.Fatalf("只有思考时摘要已经写了想了几秒，不再重复用时：%d", s.Elapsed)
	}
	if s := Summarize([]*Step{think, cmd}); s.Elapsed != 43 {
		t.Fatalf("用时 %d，应是各步相加 43", s.Elapsed)
	}
	if s := Summarize([]*Step{think, {Kind: StepCommand, State: StepDone, Target: "ls"}}); s.Elapsed != 0 {
		t.Fatalf("命令没有用时数据，不该写：%d", s.Elapsed)
	}
	if s := Summarize([]*Step{think, {Kind: StepCommand, State: StepRunning, Seconds: 40, Target: "ls"}}); s.Elapsed != 0 {
		t.Fatalf("进行中不写用时：%d", s.Elapsed)
	}
	for sec, want := range map[int]string{5: "5 秒", 60: "1 分", 65: "1 分 5 秒"} {
		if got := elapsedText(sec); got != want {
			t.Fatalf("elapsedText(%d) = %q，应是 %q", sec, got, want)
		}
	}
	host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, StepOpen: func(string) *bool { return new(bool) }}
	m := &Msg{ID: "p1", Sender: "她", Name: "她", At: time.UnixMilli(1000), Steps: []*Step{think, cmd}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(100, 24, 24, 24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 480)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	if !tt.HasText("用时 43 秒") {
		t.Fatalf("折叠头没写用时：%v", tt.Texts())
	}
	if _, ok := tt.Find("复制消息"); ok {
		t.Fatal("过程气泡不该有复制")
	}
}

// 时间悬停显示完整日期时间。
func TestTimeTooltipFullDateTime(t *testing.T) {
	m := plainMsg(false)
	m.At = time.Date(2026, 11, 9, 15, 4, 5, 0, time.Local)
	r := newBarRig(t, m, false, true)
	rect, ok := r.tt.Find("15:04")
	if !ok {
		t.Fatalf("找不到时间：%v", r.tt.Texts())
	}
	r.tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	r.tt.Frame()
	r.tt.Advance(2 * time.Second)
	r.tt.Frame()
	if !r.tt.HasText("2026年11月9日 15:04:05") {
		t.Fatalf("悬停时间没有完整日期时间：%v", r.tt.Texts())
	}
}
