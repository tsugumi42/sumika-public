package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type composerRig struct {
	p      *Composer
	host   ComposerHost
	sends  int
	stops  int
	tester *ui.Tester
}

func newComposerRig(t *testing.T) *composerRig { return newComposerRigSized(t, 640, 480) }

func newComposerRigSized(t *testing.T, w, h float32) *composerRig {
	t.Helper()
	r := &composerRig{p: NewComposer()}
	r.host = ComposerHost{T: Derive(DefaultSettings()), OnSend: func() { r.sends++ }, OnStop: func() { r.stops++ }}
	r.host.C = ComposerColors{Fill: ui.RGB(255, 255, 255), Line: ui.RGB(200, 200, 200), Text: ui.RGB(20, 20, 20), Muted: ui.RGB(120, 120, 120),
		Accent: ui.RGB(40, 90, 200), OnAccent: ui.RGB(255, 255, 255), Danger: ui.RGB(200, 40, 40)}
	r.tester = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(w, h).Justify(ui.End).Padding(24).Children(func() { r.host.Draw(c, r.p) })
	}, int(w), int(h))
	r.tester.Frame()
	return r
}

func TestComposerEnterSendsAndShiftEnterBreaksLine(t *testing.T) {
	r := newComposerRig(t)
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Type("第一行")
	r.tester.Key(ui.Shift, ui.KeyEnter)
	r.tester.Type("第二行")
	r.tester.Frame()
	if r.sends != 0 || r.p.Main.Text != "第一行\n第二行" {
		t.Fatalf("Shift+Enter 应当换行不发送：发送 %d 次，文字 %q", r.sends, r.p.Main.Text)
	}
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if r.sends != 1 {
		t.Fatalf("Enter 应当发送一次，实际 %d", r.sends)
	}
	if r.p.Main.Text != "第一行\n第二行" {
		t.Fatalf("发送由宿主决定清不清，输入区不能自己清：%q", r.p.Main.Text)
	}
}

func TestComposerSendButtonNeedsText(t *testing.T) {
	r := newComposerRig(t)
	_ = r.tester.Click("发送")
	r.tester.Frame()
	if r.sends != 0 {
		t.Fatal("空输入框也能点发送")
	}
	r.p.Main.Text = "有字了"
	r.tester.Frame()
	if err := r.tester.Click("发送"); err != nil {
		t.Fatal(err)
	}
	if r.sends != 1 {
		t.Fatalf("有字后点发送应当发出一次，实际 %d", r.sends)
	}
}

func TestComposerBusySwapsSendForStop(t *testing.T) {
	r := newComposerRig(t)
	r.p.Main.Text = "再来一条"
	r.host.Busy = true
	r.tester.Frame()
	if _, ok := r.tester.Find("发送"); ok {
		t.Fatal("她在回复时还显示发送键")
	}
	rect, ok := r.tester.Find("停止生成")
	if !ok || rect.W < 24 || rect.H < 24 {
		t.Fatalf("停止键不可达或小于 24：%+v %v", rect, ok)
	}
	if err := r.tester.Click("停止生成"); err != nil {
		t.Fatal(err)
	}
	if r.stops != 1 {
		t.Fatalf("停止没有触发：%d", r.stops)
	}
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	if r.sends != 0 {
		t.Fatal("她在回复时按 Enter 不该发送")
	}
}

func quoted(id, text string) Quoted { return Quoted{MessageID: id, Name: "小明", Text: text} }

func TestComposerCardsKeepAddedOrderAndDedupe(t *testing.T) {
	p := NewComposer()
	a := p.AddQuote(quoted("m1", "第一段"), true)
	b := p.AddQuote(quoted("m2", "第二段"), true)
	if again := p.AddQuote(quoted("m1", "第一段"), true); again != a || len(p.Cards) != 2 {
		t.Fatalf("同一段不该再加一张卡：%d 张", len(p.Cards))
	}
	if p.focus != a.ID {
		t.Fatal("重复加同一段应当把焦点给已有的那张卡")
	}
	if got := p.Blocks(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("空的主输入框不该进发送块：%d 块", len(got))
	}
	p.Main.Text = "另外的话"
	if got := p.Blocks(); len(got) != 3 || got[2] != p.Main {
		t.Fatalf("发送顺序应当是各张卡再主输入框：%d 块", len(got))
	}
	if x := p.AddQuote(quoted("m1", "同一条里的另一段"), false); x == a || len(p.Cards) != 3 {
		t.Fatal("同一条消息的另一段应当是另一张卡")
	}
}

func TestComposerEscRemovesLastEmptyCardOnly(t *testing.T) {
	p := NewComposer()
	a := p.AddQuote(quoted("m1", "甲"), false)
	b := p.AddQuote(quoted("m2", "乙"), false)
	c := p.AddQuote(quoted("m3", "丙"), false)
	a.Text = "写好的回复"
	if !p.Esc() || len(p.Cards) != 2 || p.Cards[1] != b {
		t.Fatalf("Esc 应当先删最后一张空卡：%d 张", len(p.Cards))
	}
	if !p.Esc() || len(p.Cards) != 1 || p.Cards[0] != a {
		t.Fatal("Esc 应当接着删前一张空卡")
	}
	if p.Esc() || len(p.Cards) != 1 {
		t.Fatal("有字的卡不能被 Esc 删掉")
	}
	_ = c
	if p.focus != a.ID {
		t.Fatalf("删卡后焦点该落在前一张卡上：%d", p.focus)
	}
	p.Remove(a.ID)
	if p.focus != p.Main.ID {
		t.Fatal("没有卡了焦点该回主输入框")
	}
}

func TestComposerAcceptKeepsCardsAddedWhileSending(t *testing.T) {
	p := NewComposer()
	a := p.AddQuote(quoted("m1", "甲"), false)
	a.Text = "回复甲"
	p.Main.Text = "另外"
	sent := p.Blocks()
	p.Begin(sent)
	late := p.AddQuote(quoted("m2", "乙"), true)
	late.Text = "发送期间写的"
	if !a.Sending || !p.Main.Sending || late.Sending {
		t.Fatal("只有交出去的块才只读")
	}
	p.Accept(sent)
	if len(p.Cards) != 1 || p.Cards[0] != late || late.Text != "发送期间写的" {
		t.Fatalf("发送期间新加的卡丢了：%d 张", len(p.Cards))
	}
	if p.Main.Text != "" || p.Main.Sending {
		t.Fatal("收下后主输入框该清空")
	}
}

func TestComposerRejectKeepsEverythingAndMarksTheBlock(t *testing.T) {
	p := NewComposer()
	a := p.AddQuote(quoted("m1", "甲"), false)
	b := p.AddQuote(quoted("m2", "乙"), false)
	a.Text, b.Text, p.Main.Text = "甲的回复", "乙的回复", "另外"
	sent := p.Blocks()
	p.Begin(sent)
	p.Reject(sent)
	p.SetError(b, "引用的消息已经看不到了")
	if len(p.Cards) != 2 || a.Text != "甲的回复" || b.Text != "乙的回复" || p.Main.Text != "另外" || a.Sending || b.Sending || p.Main.Sending {
		t.Fatal("被拒时所有块都要原样留着并解除只读")
	}
	if a.Err != "" || b.Err == "" || p.Main.Err != "" {
		t.Fatalf("原因只该标在出错的那一块：%q %q %q", a.Err, b.Err, p.Main.Err)
	}
}

func TestComposerCheckFlagsEmptyCardAndClearsWhenEdited(t *testing.T) {
	r := newComposerRig(t)
	empty := r.p.AddQuote(quoted("m1", "甲"), false)
	filled := r.p.AddQuote(quoted("m2", "乙"), false)
	filled.Text = "有字"
	r.tester.Frame()
	if r.p.Check() || empty.Err == "" || filled.Err != "" {
		t.Fatalf("应当只标没写字的那张卡：%q %q", empty.Err, filled.Err)
	}
	r.tester.Frame()
	if !r.tester.HasText(empty.Err) {
		t.Fatal("原因没有画在卡上")
	}
	empty.Text = "补上了"
	r.tester.Frame()
	if empty.Err != "" || r.tester.HasText("写点针对这一段的话再发") {
		t.Fatal("改了文字以后原因该消失")
	}
}

func TestComposerCardsDrawAndRemoveByButtonAndEsc(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "小明", Text: "被引的第一句"}, false)
	r.p.AddQuote(Quoted{MessageID: "m2", Name: "小红", Text: "被引的第二句"}, false)
	r.tester.Frame()
	for _, want := range []string{"回复 小明", "被引的第一句", "回复 小红", "被引的第二句"} {
		if !r.tester.HasText(want) {
			t.Fatalf("卡上缺 %q：%v", want, r.tester.Texts())
		}
	}
	rect, ok := r.tester.Find("删除引用 2")
	if !ok || rect.W < 24 || rect.H < 24 {
		t.Fatalf("删除键不可达或小于 24：%+v %v", rect, ok)
	}
	if err := r.tester.Click("删除引用 2"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if len(r.p.Cards) != 1 || r.tester.HasText("被引的第二句") {
		t.Fatalf("点删除没删掉第二张：%d 张", len(r.p.Cards))
	}
	if err := r.tester.Click("回复引用 1"); err != nil {
		t.Fatal(err)
	}
	r.tester.Key(0, ui.KeyEscape)
	r.tester.Frame()
	if len(r.p.Cards) != 0 {
		t.Fatalf("焦点在空卡里按 Esc 应当删掉它：%d 张", len(r.p.Cards))
	}
}

func TestComposerFocusGoesToNewCardAndTypingLandsThere(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "小明", Text: "引的这句"}, true)
	r.tester.Frame()
	r.tester.Frame()
	r.tester.Type("针对这句的回复")
	r.tester.Frame()
	if got := r.p.Cards[0].Text; got != "针对这句的回复" {
		t.Fatalf("新卡该拿到焦点，字落在了别处：卡 %q 主 %q", got, r.p.Main.Text)
	}
	if r.p.Main.Text != "" {
		t.Fatalf("主输入框不该有字：%q", r.p.Main.Text)
	}
}

func TestComposerLongSnippetStaysInsideCard(t *testing.T) {
	r := newComposerRig(t)
	long := strings.Repeat("很长很长的一段话没有空格", 80) + strings.Repeat("a", 300)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "名字特别特别特别特别特别特别特别特别特别特别长的人", Text: long}, false)
	r.tester.Frame()
	rect, ok := r.tester.Find("删除引用 1")
	if !ok || rect.X+rect.W > 640 || rect.Y+rect.H > 480 {
		t.Fatalf("长片段把删除键挤出了窗口：%+v %v", rect, ok)
	}
	if send, ok := r.tester.Find("发送"); !ok || send.X+send.W > 640 {
		t.Fatalf("长片段把发送键挤出了窗口：%+v %v", send, ok)
	}
}

func TestComposerAddQuoteWithoutFocusReturnsFocusToTheActiveBlock(t *testing.T) {
	p := NewComposer()
	first := p.AddQuote(quoted("m1", "甲"), true)
	p.active = first.ID
	p.focus = 0
	second := p.AddQuote(quoted("m2", "乙"), false)
	if p.focus != first.ID || p.focus == second.ID {
		t.Fatalf("不抢焦点：该把焦点还给原来有焦点的卡 %d，实际 %d", first.ID, p.focus)
	}
	p.focus = 0
	p.AddQuote(quoted("m2", "乙"), false)
	if p.focus != first.ID {
		t.Fatal("重复加同一段时也不该抢焦点")
	}
}

func TestComposerManyCardsScrollInsideAndKeepTheInputReachable(t *testing.T) {
	r := newComposerRig(t)
	for i := range 20 {
		c := r.p.AddQuote(Quoted{MessageID: fmt.Sprintf("m%d", i), Name: "小明", Text: strings.Repeat("被引的一段话", 6)}, false)
		c.Text = fmt.Sprintf("回复 %d", i)
	}
	r.tester.Frame()
	r.tester.Frame()
	box, ok := r.tester.Find("输入区")
	if !ok || box.H > 400+r.host.T.BodyLine*cardsMaxLines {
		t.Fatalf("二十张卡把输入区撑得太高：%+v", box)
	}
	for _, label := range []string{"发一条消息", "发送"} {
		rect, ok := r.tester.Find(label)
		if !ok || rect.Y < 0 || rect.Y+rect.H > 480 {
			t.Fatalf("%s 被挤出窗口：%+v %v", label, rect, ok)
		}
	}
}

func TestComposerNarrowWindowKeepsEverythingInside(t *testing.T) {
	r := newComposerRigSized(t, 320, 480)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "名字很长很长很长很长很长的人", Text: strings.Repeat("很长的一段话", 20)}, false)
	for i := range 5 {
		r.p.Attach(r.p.Main, attOf(AttFile, fmt.Sprintf("文件名称%d.txt", i)))
	}
	r.p.Main.Text = "窄窗口里的字"
	r.tester.Frame()
	r.tester.Frame()
	for _, label := range []string{"发送", "删除引用 1", "移除附件 文件名称4.txt"} {
		rect, ok := r.tester.Find(label)
		if !ok || rect.X < 0 || rect.X+rect.W > 320 || rect.Y < 0 || rect.Y+rect.H > 480 {
			t.Fatalf("窄窗口里 %s 出了窗口：%+v %v", label, rect, ok)
		}
	}
}

func TestComposerSmallButtonsShowHoverAndPressedStates(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "小明", Text: "被引"}, false)
	r.p.Main.Text = "有字"
	r.tester.Frame()
	for _, label := range []string{"删除引用 1", "发送"} {
		rect, _ := r.tester.Find(label)
		cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
		// 悬停、按下的底 140 毫秒淡变：每换一个状态走够一秒再取样
		r.tester.Move(10, 10)
		r.tester.Advance(time.Second)
		r.tester.Frame()
		rest := shotPixel(r.tester, cx, cy)
		r.tester.Move(cx, cy)
		r.tester.Frame()
		r.tester.Advance(time.Second)
		r.tester.Frame()
		hover := shotPixel(r.tester, cx-rect.W/2+3, cy)
		r.tester.Press(cx, cy)
		r.tester.Frame()
		r.tester.Advance(time.Second)
		r.tester.Frame()
		down := shotPixel(r.tester, cx-rect.W/2+3, cy)
		r.tester.Release(cx, cy)
		r.tester.Move(10, 10)
		r.tester.Frame()
		r.tester.Advance(time.Second)
		r.tester.Frame()
		edge := shotPixel(r.tester, cx-rect.W/2+3, cy)
		if hover == edge || down == hover {
			t.Fatalf("%s 的悬停（%v）、按下（%v）和静止（%v）该各有样子（中心 %v）", label, hover, down, edge, rest)
		}
	}
}

func shotPixel(tt *ui.Tester, x, y float32) [4]uint8 {
	img := tt.Image()
	px := img.RGBAAt(int(x*1.0), int(y*1.0))
	return [4]uint8{px.R, px.G, px.B, px.A}
}

func TestComposerShortWindowStillShowsTheInputAndSend(t *testing.T) {
	r := newComposerRigSized(t, 640, 560)
	for i := range 6 {
		r.p.AddQuote(Quoted{MessageID: fmt.Sprintf("m%d", i), Name: "小明", Text: strings.Repeat("被引的一段话", 8)}, false)
	}
	r.p.Main.Text = "矮窗口里的字"
	r.tester.Frame()
	r.tester.Frame()
	for _, label := range []string{"发一条消息", "发送"} {
		rect, ok := r.tester.Find(label)
		if !ok || rect.Y < 0 || rect.Y+rect.H > 560 {
			t.Fatalf("560 高的窗口里 %s 出了窗口：%+v %v", label, rect, ok)
		}
	}
	if box, _ := r.tester.Find("输入区"); box.H > 560*0.8 {
		t.Fatalf("输入区占了窗口的 %.0f%%，消息列表没地方了", box.H/560*100)
	}
}

func TestComposerTabMovesFromCardToMainInputThenSend(t *testing.T) {
	r := newComposerRig(t)
	r.p.AddQuote(Quoted{MessageID: "m1", Name: "小明", Text: "被引"}, false)
	r.p.Main.Text = "有字"
	r.tester.Frame()
	if err := r.tester.Click("回复引用 1"); err != nil {
		t.Fatal(err)
	}
	r.tester.Key(0, ui.KeyTab)
	r.tester.Frame()
	if !r.tester.Focused("发一条消息") {
		t.Fatal("Tab 该从卡里的回复框走到主输入框")
	}
	r.tester.Key(0, ui.KeyTab)
	r.tester.Frame()
	if !r.tester.Focused("发送") {
		t.Fatal("再按 Tab 该走到发送键")
	}
}

func TestComposerBusySteerQueueAndStopButton(t *testing.T) {
	r := newComposerRig(t)
	steers, queues := 0, 0
	r.host.OnSteer, r.host.OnQueue = func() { steers++ }, func() { queues++ }
	r.host.Busy = true
	r.tester.Frame()
	// 没字：键是停止，Enter 什么都不发
	if _, ok := r.tester.Find("停止生成"); !ok {
		t.Fatal("回复中没字时应当是停止键")
	}
	if _, ok := r.tester.Find("发送（插话）"); ok {
		t.Fatal("没字时不该显示发送键")
	}
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	if steers != 0 || queues != 0 || r.sends != 0 {
		t.Fatalf("没字时 Enter 不该发：插话 %d 排队 %d 发送 %d", steers, queues, r.sends)
	}
	// 有字：键换成发送；点它是插话，位置和大小不变
	stop, _ := r.tester.Find("停止生成")
	r.tester.Type("改成先写测试")
	r.tester.Frame()
	send, ok := r.tester.Find("发送（插话）")
	if !ok || send != stop {
		t.Fatalf("有字时应当在同一位置换成发送键：%+v → %+v %v", stop, send, ok)
	}
	if _, ok := r.tester.Find("停止生成"); ok {
		t.Fatal("有字时不该还有停止键")
	}
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if steers != 1 || queues != 0 || r.sends != 0 {
		t.Fatalf("Enter 应当插话一次：插话 %d 排队 %d 发送 %d", steers, queues, r.sends)
	}
	if err := r.tester.Click("发送（插话）"); err != nil {
		t.Fatal(err)
	}
	if steers != 2 {
		t.Fatalf("点发送键也是插话：%d", steers)
	}
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Key(ui.Alt, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if steers != 2 || queues != 1 || r.sends != 0 {
		t.Fatalf("Alt+Enter 应当排队一次：插话 %d 排队 %d 发送 %d", steers, queues, r.sends)
	}
	if strings.Contains(r.p.Main.Text, "\n") {
		t.Fatalf("Alt+Enter 不该插进换行：%q", r.p.Main.Text)
	}
	// 停止已发出：键回到不可点的停止，Enter 不再发
	r.host.Stop = true
	r.tester.Frame()
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if steers != 2 || queues != 1 {
		t.Fatalf("停止中不该再发：插话 %d 排队 %d", steers, queues)
	}
}

func TestComposerIdleAltEnterSendsAndEscInInputKeepsOldWay(t *testing.T) {
	r := newComposerRig(t)
	r.host.OnSteer, r.host.OnQueue = func() {}, func() {}
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Type("空闲时")
	r.tester.Key(ui.Alt, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if r.sends != 1 {
		t.Fatalf("没在回复时 Alt+Enter 等于发送：%d", r.sends)
	}
	r.host.Busy = true
	r.tester.Frame()
	r.tester.Key(0, ui.KeyEscape)
	r.tester.Frame()
	if r.stops != 0 {
		t.Fatalf("输入框里的 Esc 保持老规矩（清空卡、退出回忆），不停回复：%d", r.stops)
	}
}
