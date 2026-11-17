package chatui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

type whisperCall struct {
	sel  Selected
	ids  []string
	all  bool
	keep bool
}

type whisperRig struct {
	bar     SelectionBar
	calls   []whisperCall
	multi   []string
	targets []WhisperTarget
	tt      *ui.Tester
	m       *Msg
}

// newWhisperRig 是悄悄话会话（本地群聊等）的一条消息：宿主给了对象表、OnWhisper 和多选。
func newWhisperRig(t *testing.T, md bool, targets []WhisperTarget) *whisperRig {
	t.Helper()
	r := &whisperRig{targets: targets}
	host := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Selection: &r.bar, MD: NewMD(),
		Media:          &blockMedia{state: MediaResult{State: MediaLoading}},
		WhisperTargets: func(string) []WhisperTarget { return r.targets },
		OnWhisper: func(sel Selected, ids []string, all, keep bool) {
			r.calls = append(r.calls, whisperCall{sel, ids, all, keep})
		},
		OnMultiSelect: func(id string) { r.multi = append(r.multi, id) },
		// 本地私聊才有的"回复"也给上：悄悄话会话里不能出现它
		OnReply: func(Selected, bool) { t.Fatal("悄悄话会话里走了回复") },
	}
	r.m = &Msg{ID: "m1", Sender: "小B", Name: "小B", Text: selMsgText, Markdown: md, Blocks: segview.TextBlocks(selMsgText), At: time.UnixMilli(1000)}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(100, 24, 24, 24).Children(func() { host.Message(c, r.m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

var threeTargets = []WhisperTarget{{ID: "a", Name: "小A"}, {ID: "b", Name: "小B", Spoke: true}, {ID: "c", Name: "小C"}}

func (r *whisperRig) rightClickBody(t *testing.T) {
	t.Helper()
	rect, ok := findText(r.tt, "第一句在这")
	if !ok {
		t.Fatalf("找不到正文：%v", r.tt.Texts())
	}
	r.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	r.tt.Frame()
}

func (r *whisperRig) selectMiddle(t *testing.T) (float32, float32) {
	t.Helper()
	rect, ok := findText(r.tt, "第一句在这")
	if !ok {
		t.Fatalf("找不到正文：%v", r.tt.Texts())
	}
	y := rect.Y + rect.H/2
	r.tt.Press(rect.X+rect.W*0.2, y)
	r.tt.Move(rect.X+rect.W*0.4, y)
	r.tt.Move(rect.X+rect.W*0.6, y)
	r.tt.Release(rect.X+rect.W*0.6, y)
	r.tt.Frame()
	r.tt.Advance(selectionSettle + 10*time.Millisecond)
	r.tt.Frame()
	return rect.X + rect.W*0.4, y
}

// 整条右键：复制、多选、引用（子菜单）三项顺序固定；子菜单说这句话的排第一并标出来，分隔线后是所有人。
func TestWhisperMessageMenu(t *testing.T) {
	for _, md := range []bool{false, true} {
		r := newWhisperRig(t, md, threeTargets)
		r.rightClickBody(t)
		if got := r.tt.Menu(); !slices.Equal(got, []string{"复制", "多选", "引用"}) {
			t.Fatalf("md=%v 菜单 %q，要 复制/多选/引用", md, got)
		}
		if err := r.tt.ChooseMenuItem("引用", "小B（说这句话的）"); err != nil {
			t.Fatalf("md=%v %v", md, err)
		}
		if len(r.calls) != 1 || !slices.Equal(r.calls[0].ids, []string{"b"}) || r.calls[0].all || r.calls[0].sel.Text != selMsgText {
			t.Fatalf("md=%v 引用整条给小B：%+v", md, r.calls)
		}
		r.rightClickBody(t)
		if err := r.tt.ChooseMenuItem("引用", "所有人"); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 2 || !r.calls[1].all || len(r.calls[1].ids) != 0 {
			t.Fatalf("md=%v 所有人：%+v", md, r.calls)
		}
		r.rightClickBody(t)
		if err := r.tt.ChooseMenuItem("多选"); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.multi, []string{"m1"}) {
			t.Fatalf("md=%v 多选从这一条开始：%v", md, r.multi)
		}
	}
}

// 子菜单的内容和顺序：说这句话的第一，其余照宿主给的顺序，分隔线，所有人。
func TestWhisperSubmenuOrder(t *testing.T) {
	r := newWhisperRig(t, false, threeTargets)
	r.rightClickBody(t)
	m, _ := r.tt.Menu(), 0
	if len(m) != 3 {
		t.Fatalf("菜单 %q", m)
	}
	// 用键盘：↓ 两次到"引用"，→ 打开子菜单，看画出来的子菜单项
	r.tt.Key(0, ui.KeyDown)
	r.tt.Key(0, ui.KeyDown)
	r.tt.Key(0, ui.KeyDown)
	r.tt.Key(0, ui.KeyRight)
	r.tt.Frame()
	texts := strings.Join(r.tt.Texts(), "|")
	ia, ib, ic, iall := strings.Index(texts, "小A"), strings.Index(texts, "小B（说这句话的）"), strings.Index(texts, "小C"), strings.Index(texts, "所有人")
	if ib < 0 || ia < 0 || ic < 0 || iall < 0 || !(ib < ia && ia < ic && ic < iall) {
		t.Fatalf("子菜单顺序不对（说这句话的、小A、小C、所有人）：%s", texts)
	}
	// Esc 一层层退：先关子菜单，菜单还在；再 Esc 才整个关掉
	r.tt.Key(0, ui.KeyEscape)
	r.tt.Frame()
	if texts := strings.Join(r.tt.Texts(), "|"); strings.Contains(texts, "所有人") || !strings.Contains(texts, "多选") {
		t.Fatalf("第一次 Esc 应只关子菜单：%s", texts)
	}
	r.tt.Key(0, ui.KeyEscape)
	r.tt.Frame()
	if texts := strings.Join(r.tt.Texts(), "|"); strings.Contains(texts, "多选") {
		t.Fatalf("第二次 Esc 应关掉菜单：%s", texts)
	}
	if len(r.calls) != 0 {
		t.Fatalf("只是看看，不该加卡：%+v", r.calls)
	}
}

// 选中文字后右键：只有复制、引用两项（没有多选，也没有回复）。
func TestWhisperSelectionMenuHasTwoItems(t *testing.T) {
	for _, md := range []bool{false, true} {
		r := newWhisperRig(t, md, threeTargets)
		x, y := r.selectMiddle(t)
		sel := r.bar.Current().Text
		if sel == "" {
			t.Fatalf("md=%v 没选中", md)
		}
		r.tt.RightClickAt(x, y)
		r.tt.Frame()
		if got := r.tt.Menu(); !slices.Equal(got, []string{"复制", "引用"}) {
			t.Fatalf("md=%v 选区菜单 %q，要 复制/引用", md, got)
		}
		if err := r.tt.ChooseMenuItem("引用", "小A"); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 1 || r.calls[0].sel.Text != sel || !slices.Equal(r.calls[0].ids, []string{"a"}) {
			t.Fatalf("md=%v 引的应是选中的字：%+v，选区 %q", md, r.calls, sel)
		}
	}
}

// 选区条：复制、引用两个钮，没有回复；点引用弹出对象列表，选一位就加卡、条收起。
func TestWhisperSelectionBar(t *testing.T) {
	r := newWhisperRig(t, false, threeTargets)
	r.selectMiddle(t)
	if !r.bar.Visible() {
		t.Fatal("选区条没弹出来")
	}
	if _, ok := r.tt.Find("回复"); ok {
		t.Fatalf("悄悄话会话的选区条不该有回复：%v", r.tt.Texts())
	}
	if err := r.tt.Click("引用"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	if _, ok := r.tt.Find("所有人"); !ok {
		t.Fatalf("点引用应弹出对象列表：%v", r.tt.Texts())
	}
	if err := r.tt.Click("小C"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	if len(r.calls) != 1 || !slices.Equal(r.calls[0].ids, []string{"c"}) {
		t.Fatalf("选区条引用：%+v", r.calls)
	}
	if r.bar.Visible() {
		t.Fatal("选了对象后选区条应收起")
	}
}

// 只有一位能被唤起（QQ 私聊、一位在观察的 QQ 群）：菜单里直接是"引用"，没有子菜单、没有所有人；选区条点"引用"直接加卡。
func TestWhisperSingleTargetNoSubmenu(t *testing.T) {
	one := []WhisperTarget{{ID: "a", Name: "小A"}}
	r := newWhisperRig(t, false, one)
	r.rightClickBody(t)
	if err := r.tt.ChooseMenuItem("引用"); err != nil {
		t.Fatalf("单角色时引用应是普通项：%v", err)
	}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0].ids, []string{"a"}) || r.calls[0].all {
		t.Fatalf("单角色引用：%+v", r.calls)
	}
	r.selectMiddle(t)
	if err := r.tt.Click("引用"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	if _, ok := r.tt.Find("所有人"); ok {
		t.Fatal("单角色时不该出现所有人")
	}
	if len(r.calls) != 2 || !slices.Equal(r.calls[1].ids, []string{"a"}) {
		t.Fatalf("选区条单角色直接加卡：%+v", r.calls)
	}
}

// 没有文字（只有图）或没有能指的角色：引用灰掉。
func TestWhisperQuoteDisabledWithoutText(t *testing.T) {
	r := newWhisperRig(t, false, nil)
	r.rightClickBody(t)
	if err := r.tt.ChooseMenuItem("引用"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("没有能指的角色时引用应灰掉：%v", err)
	}
	if len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
}

func TestComposerWhisperTargets(t *testing.T) {
	p := NewComposer()
	p.Whisper = true
	q := Quoted{MessageID: "m1", Name: "小B", Text: "第一句"}
	b := p.AddWhisper(q, []string{"a"}, false, true)
	if !p.Sendable() {
		t.Fatal("悄悄话卡没写字也能发")
	}
	// 同一段再引一次：不重复加卡，对象并进去
	if b2 := p.AddWhisper(q, []string{"b", "a"}, false, true); b2 != b || len(p.Cards) != 1 || !slices.Equal(b.To, []string{"a", "b"}) {
		t.Fatalf("重复引用：卡 %d 张，对象 %v", len(p.Cards), b.To)
	}
	p.SetTarget(b, "a", false)
	p.SetTarget(b, "b", false)
	if !NoTarget(b) {
		t.Fatalf("全取消后应没有对象：%v %v", b.To, b.All)
	}
	if p.Check() || b.Err != "至少选一位" {
		t.Fatalf("对象为空不能发：%q", b.Err)
	}
	p.SetTarget(b, "", true)
	if !p.Check() || b.sig() == b.errFor {
		t.Fatal("改了对象以后能发，旧的错误不再算数")
	}
	// 本地私聊的卡（不是悄悄话）照旧：没字不能发
	p2 := NewComposer()
	p2.AddQuote(q, true)
	if p2.Check() {
		t.Fatal("私聊引用卡没字不能发")
	}
}

// 卡头：引用 名字 → 对象；对象为空时标签变红写"选对象"；点标签勾选列表能改对象；单一对象时标签不能点。
func TestComposerWhisperTag(t *testing.T) {
	r := newComposerRig(t)
	r.p.Whisper = true
	r.host.Targets = threeTargets
	b := r.p.AddWhisper(Quoted{MessageID: "m1", Name: "小B", Text: "第一句"}, []string{"a", "c"}, false, false)
	r.tester.Frame()
	if _, ok := r.tester.Find("→ 小A、小C"); !ok {
		t.Fatalf("卡头应写对象：%v", r.tester.Texts())
	}
	if _, ok := r.tester.Find("引用 小B"); !ok {
		t.Fatalf("卡头应写引用谁：%v", r.tester.Texts())
	}
	if err := r.tester.Click("引用 1 的对象：小A、小C"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if err := r.tester.Click("小B"); err != nil {
		t.Fatalf("勾选列表：%v %v", err, r.tester.Texts())
	}
	r.tester.Frame()
	if !slices.Equal(b.To, []string{"a", "c", "b"}) {
		t.Fatalf("勾上小B：%v", b.To)
	}
	b.To = nil
	r.tester.Frame()
	if _, ok := r.tester.Find("→ 选对象"); !ok {
		t.Fatalf("没对象时标签写选对象：%v", r.tester.Texts())
	}
	r.host.Targets = threeTargets[:1]
	b.To = []string{"a"}
	r.tester.Frame()
	if err := r.tester.Click("→ 小A"); err == nil {
		r.tester.Frame()
		if _, ok := r.tester.Find("所有人"); ok {
			t.Fatal("单一对象的标签不能改")
		}
	}
}

// QQ 场合（交给她）：没有主输入框；键写"交给她"；卡下固定一行说明；Enter 在卡里只换行不交出；只有点键才交。
func TestComposerHandoff(t *testing.T) {
	r := newComposerRig(t)
	r.p.Whisper = true
	r.host.Handoff = true
	r.host.Targets = threeTargets[:1]
	r.p.AddWhisper(Quoted{MessageID: "m1", Name: "群友", Text: "一句话"}, []string{"a"}, false, true)
	r.tester.Frame()
	r.tester.Frame()
	if _, ok := r.tester.Find("发一条消息"); ok {
		t.Fatalf("QQ 场合不该有主输入框：%v", r.tester.Texts())
	}
	for _, want := range []string{HandoffLabel, HandoffNote, "→ 小A"} {
		if !r.tester.HasText(want) {
			t.Fatalf("缺 %q：%v", want, r.tester.Texts())
		}
	}
	if r.tester.HasText("发送") {
		t.Fatal("键不能叫发送")
	}
	if err := r.tester.Click("回复引用 1"); err != nil {
		t.Fatal(err)
	}
	r.tester.Type("帮我看看")
	r.tester.Key(0, ui.KeyEnter)
	r.tester.Frame()
	r.tester.Frame()
	if r.sends != 0 || !strings.Contains(r.p.Cards[0].Text, "\n") {
		t.Fatalf("Enter 应只换行不交出：交了 %d 次，文字 %q", r.sends, r.p.Cards[0].Text)
	}
	if err := r.tester.Click(HandoffLabel); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	if r.sends != 1 {
		t.Fatalf("点交给她应交一次：%d", r.sends)
	}
}

// 键盘：Tab 能走到消息正文上，Shift+F10 和菜单键打开和右键一样的菜单。
func TestWhisperMessageKeyboardMenu(t *testing.T) {
	for _, key := range []struct {
		name string
		mods ui.Modifiers
		key  ui.Key
	}{{"Shift+F10", ui.Shift, ui.KeyF10}, {"菜单键", 0, ui.KeyContextMenu}} {
		r := newWhisperRig(t, false, threeTargets)
		r.tt.Key(0, ui.KeyTab)
		r.tt.Frame()
		r.tt.Key(key.mods, key.key)
		r.tt.Frame()
		if got := r.tt.Menu(); !slices.Equal(got, []string{"复制", "多选", "引用"}) {
			t.Fatalf("%s：Tab 一下再开菜单得到 %q，要 复制/多选/引用", key.name, got)
		}
		if err := r.tt.ChooseMenuItem("引用", "小A"); err != nil {
			t.Fatalf("%s：%v", key.name, err)
		}
		if len(r.calls) != 1 || !slices.Equal(r.calls[0].ids, []string{"a"}) || r.calls[0].sel.Text != selMsgText {
			t.Fatalf("%s：键盘打开的菜单应引用整条给小A：%+v", key.name, r.calls)
		}
	}
}

// 引用卡的卡头写被引消息的时间，写法和气泡右下角一样（时:分）。
func TestComposerWhisperCardTime(t *testing.T) {
	r := newComposerRig(t)
	r.p.Whisper = true
	r.host.Targets = threeTargets
	at := time.Date(2026, 11, 9, 21, 5, 0, 0, time.Local)
	r.p.AddWhisper(Quoted{MessageID: "m1", Name: "小B", Text: "第一句", At: at}, []string{"a"}, false, false)
	r.p.AddWhisper(Quoted{MessageID: "m2", Name: "小C", Text: "第二句"}, []string{"a"}, false, false)
	r.tester.Frame()
	if _, ok := r.tester.Find("21:05"); !ok {
		t.Fatalf("卡头应写被引消息的时间：%v", r.tester.Texts())
	}
	n := 0
	for _, s := range r.tester.Texts() {
		if strings.Contains(s, "00:00") || strings.Contains(s, "0001") {
			n++
		}
	}
	if n != 0 {
		t.Fatalf("没有时间的卡不能编一个：%v", r.tester.Texts())
	}
}

// 对象勾选列表：可以连着勾几位，点外面或按 Esc 才收起。
func TestComposerWhisperTagStaysOpen(t *testing.T) {
	for _, closeBy := range []string{"Esc", "点外面"} {
		r := newComposerRig(t)
		r.p.Whisper = true
		r.host.Targets = threeTargets
		b := r.p.AddWhisper(Quoted{MessageID: "m1", Name: "小B", Text: "第一句"}, []string{"a"}, false, false)
		r.tester.Frame()
		if err := r.tester.Click("引用 1 的对象：小A"); err != nil {
			t.Fatal(err)
		}
		r.tester.Frame()
		for _, name := range []string{"小B", "小C"} {
			if err := r.tester.Click(name); err != nil {
				t.Fatalf("%s：勾 %s：%v %v", closeBy, name, err, r.tester.Texts())
			}
			r.tester.Frame()
			r.tester.Frame()
		}
		if !slices.Equal(b.To, []string{"a", "b", "c"}) {
			t.Fatalf("%s：连勾两位后对象 %v，要 a b c", closeBy, b.To)
		}
		if len(r.tester.Menu()) == 0 {
			t.Fatalf("%s：勾完列表不该收起", closeBy)
		}
		// 再点一下取消
		if err := r.tester.Click("小B"); err != nil {
			t.Fatal(err)
		}
		r.tester.Frame()
		r.tester.Frame()
		if !slices.Equal(b.To, []string{"a", "c"}) || len(r.tester.Menu()) == 0 {
			t.Fatalf("%s：取消小B后对象 %v、列表开着=%v", closeBy, b.To, len(r.tester.Menu()) > 0)
		}
		if closeBy == "Esc" {
			r.tester.Key(0, ui.KeyEscape)
		} else {
			r.tester.ClickAt(5, 5)
		}
		r.tester.Frame()
		r.tester.Frame()
		if m := r.tester.Menu(); len(m) != 0 {
			t.Fatalf("%s：应当收起，还剩 %q", closeBy, m)
		}
		if !slices.Equal(b.To, []string{"a", "c"}) {
			t.Fatalf("%s：收起不该改对象：%v", closeBy, b.To)
		}
	}
}
