package chatui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/fonts"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 设 CHATUI_SHOT=目录 时，把消息气泡族、过程组件族的每个状态浅色、深色各截一张小图（视觉验收并排对照用）；平时跳过。
// 名字都是编的，不含真实聊天。
func TestShotStates(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && registerTestFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	mono := ""
	for _, f := range ui.FontFamilies() {
		if mono == "" && strings.HasPrefix(f.Name, "Maple Mono") {
			mono = f.Name
		}
	}
	EmojiDir = assets.TwemojiDir()
	defer func() { EmojiDir = "" }()

	at := time.Date(2026, 11, 6, 21, 30, 0, 0, time.Local)
	a, b := CharColors[0], CharColors[1]
	her := func(id string, sec int, text string) *Msg {
		return &Msg{ID: id, Sender: "r1", Name: "晴枝", Char: a, At: at.Add(time.Duration(sec) * time.Second), Text: text, Markdown: true}
	}
	other := func(id string, sec int, text string) *Msg {
		return &Msg{ID: id, Sender: "r2", Name: "雾栞", Char: b, At: at.Add(time.Duration(sec) * time.Second), Text: text, Markdown: true}
	}
	self := func(id string, sec int, text string) *Msg {
		return &Msg{ID: id, Sender: "self", Self: true, Name: "我", At: at.Add(time.Duration(sec) * time.Second), Text: text}
	}
	with := func(m *Msg, f func(m *Msg)) *Msg { f(m); return m }
	proc := func(id string, sec int, steps ...*Step) *Msg {
		m := her(id, sec, "")
		m.Steps = steps
		return m
	}
	diff := "@@ -3,2 +3,3 @@\n 窗边的纸灯亮起来了。\n-昨天说到一半。\n+昨天说到窗边那段。\n+今天接着说。\n"
	type scene struct {
		name  string
		group bool
		live  bool // 思考进行中边想边展开（ADR-286 的设置）
		msgs  []*Msg
		open  map[string]bool
	}
	scenes := []scene{
		{name: "01-单条", msgs: []*Msg{her("a", 0, "好呀。")}},
		{name: "02-连续合组-私聊", msgs: []*Msg{her("a", 0, "窗边的纸灯亮起来了。"), her("b", 2, "今天想聊些什么？可以从早上发生的小事说起，也可以随便聊聊别的。"), her("c", 4, "我在听。")}},
		{name: "03-群聊-头像和名字", group: true, msgs: []*Msg{her("a", 0, "窗边的纸灯亮起来了。"), her("b", 2, "我在听。"), other("c", 20, "我也在。"), other("d", 22, "先把想到的慢慢说吧，不着急。")}},
		{name: "04-自己的-连续", msgs: []*Msg{self("a", 0, "先把想到的慢慢说吧。"), self("b", 2, "今天有点累，不过还想聊一会儿。")}},
		{name: "05-发送中和失败", msgs: []*Msg{with(self("a", 0, "这条正在发送。"), func(m *Msg) { m.State = Sending }), with(self("b", 30, "这条没有发出去。"), func(m *Msg) { m.State = Failed })}},
		{name: "06-撤回", group: true, msgs: []*Msg{with(her("a", 0, "撤回的"), func(m *Msg) { m.State = Recalled }), her("b", 30, "刚才那句说错了。")}},
		{name: "07-引用", group: true, msgs: []*Msg{with(her("a", 0, "比如这个。"), func(m *Msg) { m.Quote = &Quote{Name: "雾栞", Text: "昨天说好的纸灯"} }), with(self("b", 30, "对，就是那个。"), func(m *Msg) { m.Quote = &Quote{Name: "晴枝", Text: "比如这个。"} })}},
		{name: "08-表情回应", msgs: []*Msg{with(her("a", 0, "今天想聊些什么？"), func(m *Msg) {
			m.Reactions = []Reaction{{Emoji: "👍", Count: 5}, {Emoji: "😂", Count: 1, Mine: true}}
		}), with(self("b", 30, "先把想到的慢慢说吧。"), func(m *Msg) {
			m.Reactions = []Reaction{{Emoji: "❤", Count: 1, By: []Reactor{{ID: "r1", Name: "晴枝", Char: a}}}}
		})}},
		{name: "09-出字中", msgs: []*Msg{with(her("a", 0, "我收到你的小纸条了。我们可以从最想说的"), func(m *Msg) { m.State = Streaming })}},
		{name: "10-被停下", msgs: []*Msg{with(her("a", 0, "我收到你的小纸条了。我们可以从"), func(m *Msg) { m.State = Interrupted })}},
		{name: "11-带格式", msgs: []*Msg{her("a", 0, "好呀，我把**今天要做的**理一下：\n\n- 先把 `notes/纸灯.md` 补完\n- 再跑一遍测试\n\n```go\nfunc lamp() float64 { return 0.6 }\n```\n\n弄完我们接着聊。")}},
		{name: "12-多段长文", msgs: []*Msg{her("a", 0, "第一段比较短。\n\n第二段要长一些，长到会换行才好看出来段距和行高对不对得上，再多写几个字凑一凑。\n第二段里单个换行就是换行。")}},
		{name: "13-思考中-收着", msgs: []*Msg{proc("p", 0, &Step{ID: "t", Kind: StepThink, State: StepRunning, Thought: "她刚才提到纸灯，先想想昨天说到哪了。"})}},
		{name: "14-思考中-展开", live: true, msgs: []*Msg{proc("p", 0, &Step{ID: "t", Kind: StepThink, State: StepRunning, Thought: "她刚才提到纸灯，先想想昨天说到哪了。\n上次停在窗边那段。"})}},
		{name: "15-想完和读搜", open: map[string]bool{"p": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "想过了"}, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "notes/纸灯.md"}, &Step{ID: "s", Kind: StepSearch, State: StepDone, Target: "纸灯"}, &Step{ID: "r2", Kind: StepRead, State: StepRunning, Target: "notes/窗边.md"}), her("b", 4, "看完了。")}},
		{name: "16-命令运行中-收着", msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test ./lamp/...", Seconds: 3, Output: "=== RUN   TestLamp\n=== RUN   TestWindow"})}},
		{name: "17-命令完成-收着", msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "go test ./lamp/...", Seconds: 2, Output: "ok"}), her("b", 4, "测试都过了。")}},
		{name: "18-命令失败-展开", open: map[string]bool{"p": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepFailed, Target: "go test ./lamp/...", HasExit: true, ExitCode: 1, Output: "=== RUN   TestLamp\n--- FAIL: TestLamp (0.01s)\n    lamp_test.go:12: 亮度 0.4，期望 0.6\nFAIL"})}},
		{name: "19-命令被停", open: map[string]bool{"p": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepStopped, Target: "make all", Output: "编译 lamp\n编译 window"})}},
		{name: "20-改文件等批准", msgs: []*Msg{proc("p", 0, &Step{ID: "e", Kind: StepEdit, State: StepRunning, Target: "notes/纸灯.md", Diff: diff, Added: 2, Removed: 1, Approval: &Approval{State: ApprovalPending, Reason: "把昨天的笔记补完整。", Remaining: 272 * time.Second}})}},
		{name: "21-命令等批准-群里谁引起", group: true, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build", Approval: &Approval{State: ApprovalPending, Cause: "雾栞", Reason: "清掉旧的构建，下一步重新编译。", Remaining: 59 * time.Second}})}},
		{name: "22-审批提交中", msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build", Approval: &Approval{State: ApprovalSubmitting, Reason: "清掉旧的构建。", Remaining: 50 * time.Second}})}},
		{name: "23-审批结果四种", msgs: []*Msg{proc("p", 0,
			&Step{ID: "c1", Kind: StepCommand, State: StepDone, Target: "rm -rf build", Seconds: 1, Approval: &Approval{State: ApprovalAllowed}},
			&Step{ID: "c2", Kind: StepCommand, State: StepDenied, Target: "rm -rf /", Approval: &Approval{State: ApprovalDenied}},
			&Step{ID: "c3", Kind: StepCommand, State: StepStopped, Target: "make deploy", Approval: &Approval{State: ApprovalExpired}},
			&Step{ID: "c4", Kind: StepEdit, State: StepStopped, Target: "a.md", Approval: &Approval{State: ApprovalRevoked}})}},
		{name: "24-自动放行", msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "ls notes", Seconds: 1, Output: "纸灯.md", Approval: &Approval{State: ApprovalAuto, AutoReason: "只读命令"}})}},
		{name: "26-群聊两个角色的过程", group: true, msgs: []*Msg{
			proc("p1", 0, &Step{ID: "t1", Kind: StepThink, State: StepDone, Seconds: 2, Thought: "想过"}, &Step{ID: "r1", Kind: StepRead, State: StepDone, Target: "notes/纸灯.md"}),
			her("a1", 2, "我看了笔记，昨天停在窗边那段。"),
			with(proc("p2", 4, &Step{ID: "t2", Kind: StepThink, State: StepRunning, Thought: "想"}), func(m *Msg) { m.Sender, m.Name, m.Char = "r2", "雾栞", b }),
			with(proc("p3", 6, &Step{ID: "c3", Kind: StepCommand, State: StepDone, Target: "ls notes", Seconds: 1}), func(m *Msg) { m.Sender, m.Name, m.Char = "r2", "雾栞", b }),
			other("a2", 8, "我这边也找到了。")}},
		{name: "27-进行中展开过程", live: true, msgs: []*Msg{proc("p", 0, &Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "先看看笔记里写了什么。"}, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "notes/纸灯.md"}, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test ./...", Output: "ok  sumika/a\n=== RUN TestB"})}},
		{name: "28-进行中展开过程-做完收起", live: true, msgs: []*Msg{proc("p", 0, &Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "先看看笔记里写了什么。"}, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "notes/纸灯.md"}, &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "go test ./...", Seconds: 4}), her("m", 5, "测试都过了。")}},
		{name: "29-终端-短命令", open: map[string]bool{"p": true, "c": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "go vet ./...", Seconds: 2, Output: "ok"})}},
		{name: "30-终端-长命令", open: map[string]bool{"p": true, "c": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepDone, Seconds: 1, Target: "cat > notes/纸灯.md <<'EOF'\n# 纸灯\n\n窗边的纸灯亮起来了，光是暖黄色的，照在桌上那叠还没拆的信上。\n- 灯罩换成和纸\n- 底座刷一层清漆\nEOF", Output: ""})}},
		{name: "31-终端-很多行输出", open: map[string]bool{"p": true, "c": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "go test -v ./lamp/...", Seconds: 6, Output: logLines(128)})}},
		{name: "32-终端-失败", open: map[string]bool{"p": true}, msgs: []*Msg{proc("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepFailed, Target: "go test ./lamp/...", HasExit: true, ExitCode: 1, Output: "\x1b[1m=== RUN   TestLamp\x1b[0m\n--- FAIL: TestLamp (0.01s)\n    lamp_test.go:12: 亮度 0.4，期望 0.6，这一行特别长、长到在终端块里要折成两行才放得下\nFAIL"})}},
		{name: "25-卡片和气泡相贴", open: map[string]bool{"p1": true, "p2": true}, msgs: []*Msg{
			proc("p1", 0, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"}, &Step{ID: "c1", Kind: StepCommand, State: StepFailed, Target: "go test", HasExit: true, ExitCode: 1, Output: "FAIL"}),
			her("m", 2, "好了，我们接着聊。"),
			proc("p2", 4, &Step{ID: "e", Kind: StepEdit, State: StepDone, Target: "notes/纸灯.md", Added: 2, Removed: 1, Diff: diff}, &Step{ID: "c2", Kind: StepCommand, State: StepDone, Target: "go test", Seconds: 2})}},
	}
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		for _, sc := range scenes {
			open := map[string]*bool{}
			for k, v := range sc.open {
				open[k] = &v
			}
			h := Host{T: tk, Font: font, Mono: mono, SelfChar: a, Group: sc.group, Column: 720, NoEnter: true, MD: NewMD(), LiveOpen: sc.live,
				StepOpen: func(id string) *bool {
					if open[id] == nil {
						open[id] = new(bool)
					}
					return open[id]
				}}
			view := func(c *ui.Context) {
				c.Root().Background(tk.C.ChatBg)
				ui.Column(c.Key("list")).Width(h.Column).Padding(24).Children(func() {
					for i, m := range sc.msgs {
						var prev, next *Msg
						if i > 0 {
							prev = sc.msgs[i-1]
						}
						if i+1 < len(sc.msgs) {
							next = sc.msgs[i+1]
						}
						h.Message(c, m, prev, next)
					}
				})
			}
			tt := ui.NewTester(view, 800, 1400)
			tt.SetScale(1.25)
			tt.Frame()
			tt.Advance(2 * time.Second)
			list := tree(tt).find("list")
			img := tt.Image()
			const sc125 = 1.25
			r := image.Rect(0, 0, int(800*sc125), int((list.Box.Y+list.Box.H)*sc125)).Intersect(img.Bounds())
			mode := map[bool]string{false: "浅", true: "深"}[dark]
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%s.png", sc.name, mode)))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, img.SubImage(r))
			f.Close()
		}
	}
}

// logLines 造 n 组像测试日志的输出（截图用）。
func logLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "=== RUN   TestLamp/case_%03d\n--- PASS: TestLamp/case_%03d (0.00s)\n", i, i)
	}
	return b.String()
}
