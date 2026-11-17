package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 斜杠命令补全：筛选、键盘选择、Esc、和回忆历史不打架、Enter 不误发。
// 命令名都是编的，只验证"名单由宿主给、界面按前缀筛"。

func slashRig(t *testing.T, past ...string) *composerRig {
	t.Helper()
	r := newComposerRig(t)
	r.host.Past = func() []string { return past }
	r.host.Commands = func() []SlashItem {
		return []SlashItem{{Name: "goal", Summary: "设定目标"}, {Name: "loop", Summary: "定期任务"}, {Name: "lookup", Summary: "查一下"}}
	}
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	return r
}

func (r *composerRig) typed(s string) {
	r.tester.Type(s)
	r.tester.Frame()
	r.tester.Advance(time.Second) // 命令列表有进场（高度从 0 长出来），放完再看
	r.tester.Frame()
}

func (r *composerRig) shows(name string) bool { return r.tester.HasText("/" + name) }

func TestSlashListFiltersByPrefix(t *testing.T) {
	r := slashRig(t)
	r.typed("/")
	if !r.shows("goal") || !r.shows("loop") || !r.shows("lookup") {
		t.Fatalf("只打 / 应当列出全部命令：%v", r.tester.Texts())
	}
	r.typed("lo")
	if r.shows("goal") || !r.shows("loop") || !r.shows("lookup") {
		t.Fatalf("/lo 只该留 loop、lookup：%v", r.tester.Texts())
	}
	r.typed("o")
	if !r.shows("lookup") || !r.shows("loop") {
		t.Fatalf("/loo 还有 loop、lookup：%v", r.tester.Texts())
	}
	r.typed("p")
	if r.shows("lookup") || !r.shows("loop") {
		t.Fatalf("/loop 该只剩 loop：%v", r.tester.Texts())
	}
	r.typed("x")
	if r.shows("loop") || r.shows("lookup") || r.shows("goal") {
		t.Fatalf("没有匹配就不该显示：%v", r.tester.Texts())
	}
}

func TestSlashListOnlyWhileTypingCommandName(t *testing.T) {
	for _, text := range []string{"你好", "看 /goal", "/goal 写参数", "/usr/bin"} {
		r := slashRig(t)
		r.typed(text)
		if r.tester.HasText("设定目标") {
			t.Fatalf("%q 不该弹出列表：%v", text, r.tester.Texts())
		}
	}
	r := slashRig(t)
	r.host.Commands = nil // 宿主没给命令（群聊、只看历史）：不弹
	r.typed("/")
	if r.tester.HasText("设定目标") {
		t.Fatal("没有命令名单也弹出了列表")
	}
}

func TestSlashTabAcceptsSelectedCommand(t *testing.T) {
	r := slashRig(t)
	r.typed("/")
	r.key(0, ui.KeyDown) // goal → loop
	r.key(0, ui.KeyTab)
	if got := r.p.Main.Text; got != "/loop " || r.sends != 0 {
		t.Fatalf("Tab 应采纳选中的命令并补空格：%q 发送 %d 次", got, r.sends)
	}
	if r.tester.HasText("设定目标") {
		t.Fatal("采纳后列表该收起来")
	}
	r.typed("参数")
	if got := r.p.Main.Text; got != "/loop 参数" {
		t.Fatalf("采纳后焦点该还在输入框、光标在末尾：%q", got)
	}
}

func TestSlashEnterAcceptsInsteadOfSending(t *testing.T) {
	r := slashRig(t)
	r.typed("/go")
	r.key(0, ui.KeyEnter)
	if got := r.p.Main.Text; got != "/goal " || r.sends != 0 {
		t.Fatalf("列表开着 Enter 只采纳不发送：%q 发送 %d 次", got, r.sends)
	}
	r.key(0, ui.KeyEnter) // 列表已经收起：Enter 照常发送
	r.tester.Frame()
	if r.sends != 1 {
		t.Fatalf("第二次 Enter 应当发送：%d", r.sends)
	}
}

func TestSlashNoMatchEnterStillSends(t *testing.T) {
	r := slashRig(t)
	r.typed("/zzz")
	r.key(0, ui.KeyEnter)
	r.tester.Frame()
	if r.sends != 1 {
		t.Fatalf("没有匹配时列表不存在，Enter 照常发送：%d", r.sends)
	}
}

func TestSlashArrowsWrapAndDoNotRecallHistory(t *testing.T) {
	r := slashRig(t, "旧的话")
	r.typed("/")
	r.key(0, ui.KeyHome) // 光标在最开头：平时 ↑ 会翻历史
	r.key(0, ui.KeyUp)   // 列表开着：↑ 只管选择，从第一条绕到最后一条
	if r.p.Main.Text != "/" {
		t.Fatalf("列表开着 ↑ 不该触发回忆历史：%q", r.p.Main.Text)
	}
	r.key(0, ui.KeyTab)
	if got := r.p.Main.Text; got != "/lookup " {
		t.Fatalf("↑ 应当从第一条绕到最后一条：%q", got)
	}
}

func TestSlashEscClosesKeepsTextAndTypingReopens(t *testing.T) {
	r := slashRig(t, "旧的话")
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "甲", Text: "一句"}, false) // 空引用卡：Esc 平时会删它
	r.tester.Frame()
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.typed("/l")
	r.key(0, ui.KeyEscape)
	if r.p.Main.Text != "/l" || r.tester.HasText("定期任务") {
		t.Fatalf("Esc 应关掉列表、不清文字：%q %v", r.p.Main.Text, r.tester.Texts())
	}
	if len(r.p.Cards) != 1 {
		t.Fatal("这一下 Esc 只该关列表，不该删空引用卡")
	}
	r.typed("o")
	if !r.tester.HasText("定期任务") {
		t.Fatalf("继续打字应当重新出现：%v", r.tester.Texts())
	}
	r.key(0, ui.KeyEscape)
	r.key(0, ui.KeyHome)
	r.key(0, ui.KeyUp) // 列表关着：回忆历史照旧
	if r.p.Main.Text != "旧的话" {
		t.Fatalf("列表关掉后 ↑ 该回忆历史：%q", r.p.Main.Text)
	}
}

func TestSlashClickAcceptsAndSelectionResetsOnTyping(t *testing.T) {
	r := slashRig(t)
	r.typed("/")
	r.key(0, ui.KeyDown)
	r.key(0, ui.KeyDown) // 选到第三条
	r.typed("l")         // 文字变了：选中项回到筛选后的第一条
	r.key(0, ui.KeyTab)
	if got := r.p.Main.Text; got != "/loop " {
		t.Fatalf("文字变了选中项该回到第一条：%q", got)
	}
	r2 := slashRig(t)
	r2.typed("/")
	if err := r2.tester.Click("命令 /lookup"); err != nil {
		t.Fatal(err)
	}
	r2.tester.Frame()
	if got := r2.p.Main.Text; got != "/lookup " || r2.sends != 0 {
		t.Fatalf("点一条应当采纳：%q", got)
	}
}

func TestSlashWindowKeepsSelectionVisible(t *testing.T) {
	r := slashRig(t)
	var many []SlashItem
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "b1"} {
		many = append(many, SlashItem{Name: n})
	}
	r.host.Commands = func() []SlashItem { return many }
	r.typed("/")
	if r.shows("b1") || !r.shows("a8") {
		t.Fatalf("最多显示 %d 行：%v", slashRows, r.tester.Texts())
	}
	r.key(0, ui.KeyUp) // 绕到最后一条，窗口跟着滚到它
	if !r.shows("b1") {
		t.Fatalf("选中项必须在窗口里：%v", r.tester.Texts())
	}
}

// 规格-过渡动画 C26：命令列表进场时高度从 0 长出来（输入框跟着平滑长高），放完是满高；减少动态时直接满高。
func TestSlashPanelGrowsIn(t *testing.T) {
	r := slashRig(t)
	r.tester.Type("/")
	r.tester.Frame()
	h := func() float32 {
		b, ok := r.tester.Find("斜杠命令")
		if !ok {
			t.Fatal("没找到命令列表")
		}
		return b.H
	}
	h0 := h()
	r.tester.Advance(60 * time.Millisecond)
	r.tester.Frame()
	h1 := h()
	r.tester.Advance(time.Second)
	r.tester.Frame()
	h2 := h()
	if !(h0 < h1 && h1 < h2) {
		t.Errorf("命令列表进场高度 %.1f → %.1f → %.1f，应逐步长到满高", h0, h1, h2)
	}
}
