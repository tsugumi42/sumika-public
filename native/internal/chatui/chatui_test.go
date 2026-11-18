package chatui

import (
	"fmt"
	"strings"
	"sumika-native/internal/assets"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 每种设置组合下令牌都满足几何和对比度规矩（规格九-1）。
func TestTokensAcrossSettings(t *testing.T) {
	for _, text := range []float32{TextMin, TextDefault, TextMax} {
		for _, radius := range []float32{RadiusMin, RadiusDefault, RadiusMax} {
			for _, density := range []float32{DensityMin, DensityDefault, DensityMax} {
				for _, neutral := range []Neutral{NeutralCool, NeutralPlain, NeutralWarm} {
					for _, dark := range []bool{false, true} {
						s := Settings{Text: text, Radius: radius, Density: density, Neutral: neutral, Dark: dark, Wallpaper: true}
						name := fmt.Sprintf("字%v圆%v疏%v底%v深%v", text, radius, density, neutral, dark)
						tk := Derive(s)
						if !(tk.RCtl < tk.R1 && tk.R1 < tk.R2 && tk.R2 < tk.RWin) {
							t.Errorf("%s 圆角档没有逐层变大：%v %v %v %v", name, tk.RCtl, tk.R1, tk.R2, tk.RWin)
						}
						if tk.RJoin < 4 || tk.RJoin > tk.R1 {
							t.Errorf("%s 相贴角 %v 不在 [4, r1=%v]", name, tk.RJoin, tk.R1)
						}
						if tk.Inner(tk.R1, tk.BubblePadV) < 4 {
							t.Errorf("%s 气泡内层圆角小于 4", name)
						}
						c := tk.C
						pairs := []struct {
							what   string
							fg, bg ui.Color
						}{
							{"正文/窗口底", c.Text, c.Bg}, {"正文/她的气泡", c.Text, c.Bubble}, {"次要/窗口底", c.Text2, c.Bg},
							{"第三级/窗口底", c.Text3, c.Bg}, {"第三级/她的气泡", c.Text3, c.Bubble},
							// 失败角标是实心危险色圆、感叹号挖空，画出来的是危险色压在聊天底上（有无壁纸两种底都要过）
							{"角标/窗口底", c.Danger, c.Bg}, {"角标/内容层", c.Danger, c.Surface}, {"角标悬停/窗口底", c.DangerHover, c.Bg}, {"角标悬停/内容层", c.DangerHover, c.Surface},
							{"撤回字/聊天底", c.Text3, c.ChatBg},
						}
						for i, ch := range CharColors {
							a := tk.AccentOf(ch)
							pairs = append(pairs, struct {
								what   string
								fg, bg ui.Color
							}{fmt.Sprintf("正文/你的气泡%d", i), c.Text, a.Soft}, struct {
								what   string
								fg, bg ui.Color
							}{fmt.Sprintf("名字%d/她的气泡", i), a.Text, c.Bubble}, struct {
								what   string
								fg, bg ui.Color
							}{fmt.Sprintf("强调色底上的字%d", i), a.On, a.Base})
						}
						for id := uint64(0); id < 7; id++ {
							pairs = append(pairs, struct {
								what   string
								fg, bg ui.Color
							}{fmt.Sprintf("群名字%d/她的气泡", id), tk.NameColor(id), c.Bubble})
						}
						for _, p := range pairs {
							if got := Contrast(p.fg, p.bg); got < 4.5 {
								t.Errorf("%s %s 对比度 %.2f < 4.5", name, p.what, got)
							}
						}
					}
				}
			}
		}
	}
}

func TestBubbleCorners(t *testing.T) {
	tk := Derive(DefaultSettings())
	r1, j := tk.R1, tk.RJoin
	h := tk.BubblePadV*2 + tk.BodyLine
	multi := tk.BubblePadV*2 + tk.BodyLine*3
	cases := []struct {
		name                     string
		self, prev, next, single bool
		height                   float32
		want                     Corners
	}{
		{"单行胶囊", false, false, false, true, h, Corners{h / 2, h / 2, h / 2, h / 2}},
		{"多行用 r1", false, false, false, false, multi, Corners{r1, r1, r1, r1}},
		{"别人组首", false, false, true, false, multi, Corners{r1, r1, r1, j}},
		{"别人组中", false, true, true, false, multi, Corners{j, r1, r1, j}},
		{"别人组末", false, true, false, false, multi, Corners{j, r1, r1, r1}},
		{"自己组首", true, false, true, false, multi, Corners{r1, r1, j, r1}},
		{"自己组中", true, true, true, false, multi, Corners{r1, j, j, r1}},
		{"自己组末单行", true, true, false, true, h, Corners{h / 2, j, h / 2, h / 2}},
	}
	for _, c := range cases {
		got := BubbleCorners(tk, c.self, c.prev, c.next, c.single, c.height)
		for i := range got {
			if !near(got[i], c.want[i]) {
				t.Errorf("%s 第 %d 角 = %.2f，期望 %.2f（全部 %v）", c.name, i, got[i], c.want[i], got)
			}
		}
	}
}

func TestJoined(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	a := &Msg{Sender: "r1", At: at}
	if !Joined(a, &Msg{Sender: "r1", At: at.Add(899 * time.Second)}, 900*time.Second) {
		t.Error("同人 899 秒应合组")
	}
	if Joined(a, &Msg{Sender: "r1", At: at.Add(900 * time.Second)}, 900*time.Second) {
		t.Error("同人 900 秒不应合组（Telegram 是小于 900）")
	}
	if Joined(a, &Msg{Sender: "r2", At: at}, 900*time.Second) {
		t.Error("换人不应合组")
	}
	if Joined(a, &Msg{Sender: "r1", At: at, Self: true}, 900*time.Second) {
		t.Error("自己和角色同 id 也不应合组")
	}
	if !Joined(a, &Msg{Sender: "r1", At: at, State: Recalled}, 900*time.Second) {
		t.Error("撤回的也要合组（原文还显示，名字头像不再出一次，主人 2026-11-06）")
	}
}

// 场景：群聊，晴枝连发三条（第三条带引用）、雾栞一条、我连发两条（第二条失败）、晴枝一条正在输入。
func scene() []*Msg {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	a, b := CharColors[0], CharColors[1]
	return []*Msg{
		{ID: "m1", Sender: "r1", Name: "晴枝", Char: a, At: at, Text: "窗边的纸灯亮起来了。"},
		{ID: "m2", Sender: "r1", Name: "晴枝", Char: a, At: at.Add(5 * time.Second), Text: "今天想聊些什么？可以从早上发生的小事说起，也可以随便聊聊别的。", Reactions: []Reaction{{Emoji: "👍", Count: 5}, {Emoji: "😂", Count: 1, Mine: true}}},
		{ID: "m3", Sender: "r1", Name: "晴枝", Char: a, At: at.Add(9 * time.Second), Text: "比如这个。", Quote: &Quote{Name: "雾栞", Text: "昨天说好的纸灯"}, Reactions: []Reaction{{Emoji: "😢", Count: 2, Mine: true, By: []Reactor{{ID: "r2", Name: "雾栞", Char: b}, {ID: "self", Name: "我", Char: a}}}}},
		{ID: "m4", Sender: "r2", Name: "雾栞", Char: b, At: at.Add(20 * time.Second), Text: "我也在。", State: Interrupted},
		{ID: "m5", Sender: "self", Self: true, Name: "我", At: at.Add(30 * time.Second), Text: "先把想到的慢慢说吧。", Reactions: []Reaction{{Emoji: "❤", Count: 1, By: []Reactor{{ID: "r1", Name: "晴枝", Char: a}}}}},
		{ID: "m6", Sender: "self", Self: true, Name: "我", At: at.Add(32 * time.Second), Text: "这是一条发送失败的消息。", State: Failed},
	}
}

func render(t *testing.T, s Settings, group bool, w, h int) (*ui.Tester, Host, []*Msg) {
	t.Helper()
	tk := Derive(s)
	host := Host{T: tk, SelfChar: CharColors[0], Group: group, Column: 720, NoEnter: true}
	msgs := scene()
	view := func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(float32(w)).Padding(16).Background(tk.C.Bg).Children(func() {
			ui.Column(c.Key("list")).Width(host.Column).Children(func() {
				for i, m := range msgs {
					var prev, next *Msg
					if i > 0 {
						prev = msgs[i-1]
					}
					if i+1 < len(msgs) {
						next = msgs[i+1]
					}
					host.Message(c, m, prev, next)
				}
			})
		})
	}
	tt := ui.NewTester(view, w, h)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	return tt, host, msgs
}

// 气泡几何（规格九-2）：四角、组内组间间距、名字和头像位置、引用块四边、失败角标贴底。
func TestBubbleGeometry(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			s := DefaultSettings()
			s.Dark = dark
			tt, host, msgs := render(t, s, group, 800, 900)
			root := tree(tt)
			tk := host.T
			scene := fmt.Sprintf("深%v群%v", dark, group)
			var prevRow *node
			for i, m := range msgs {
				row := root.find(m.ID)
				if row == nil {
					t.Fatalf("%s 找不到 %s", scene, m.ID)
				}
				bubble := row.find("bubble")
				var prev, next *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				if i+1 < len(msgs) {
					next = msgs[i+1]
				}
				jp, jn := Joined(prev, m, tk.JoinWindow), Joined(m, next, tk.JoinWindow)
				// 间距
				if prevRow != nil {
					gap := row.Box.Y - (prevRow.Box.Y + prevRow.Box.H)
					want := tk.GapGroups
					if jp {
						want = tk.GapInGroup
					}
					if !near(gap, want) {
						t.Errorf("%s %s 上间距 %.2f，期望 %.2f", scene, m.ID, gap, want)
					}
				}
				prevRow = row
				// 四角：相贴的角是 RJoin，其余角相等
				r := bubble.Radius
				top, bottom := 0, 3
				if m.Self {
					top, bottom = 1, 2
				}
				for k := 0; k < 4; k++ {
					isJoin := (jp && k == top) || (jn && k == bottom)
					if isJoin && !near(r[k], tk.RJoin) {
						t.Errorf("%s %s 第 %d 角应是相贴角 %.1f，实际 %.2f", scene, m.ID, k, tk.RJoin, r[k])
					}
					if !isJoin && r[k] <= tk.RJoin {
						t.Errorf("%s %s 第 %d 角不该是小角，实际 %.2f", scene, m.ID, k, r[k])
					}
				}
				// 单行胶囊 / 多行不超过高度四分之一（有表情排时左下角按同心规则放大，不受四分之一限制，单独由 TestChipCornerConcentric 量）
				full := r[0]
				for k, v := range r {
					if k == 3 && len(m.Reactions) > 0 {
						continue
					}
					full = max(full, v)
				}
				if bubble.Box.H < tk.BubblePadV*2+tk.BodyLine*1.5 && m.Quote == nil && !(group && !m.Self && !jp) {
					if !near(full, bubble.Box.H/2) {
						t.Errorf("%s %s 单行高 %.1f 应是胶囊，圆角 %.2f", scene, m.ID, bubble.Box.H, full)
					}
				} else if full > bubble.Box.H/4+0.75 {
					t.Errorf("%s %s 多行圆角 %.2f 超过高度 %.1f 的四分之一", scene, m.ID, full, bubble.Box.H)
				}
				// 名字：群里别人组首才有；头像：群里别人组末才有
				hasName := bubble.find("name") != nil
				wantName := group && !m.Self && !jp
				if hasName != wantName {
					t.Errorf("%s %s 名字 %v，期望 %v", scene, m.ID, hasName, wantName)
				}
				if hasName && m.Quote == nil {
					nm, txt := bubble.find("name"), bubble.find("text")
					if gap := txt.Box.Y - (nm.Box.Y + nm.Box.H); !near(gap, 0) {
						t.Errorf("%s %s 名字和正文之间多隔了 %.2f", scene, m.ID, gap)
					}
				}
				hasAvatar := row.find("avatar") != nil
				if hasAvatar != (group && !m.Self && !jn) {
					t.Errorf("%s %s 头像 %v", scene, m.ID, hasAvatar)
				}
				if m.Self && row.find("avatar-slot") != nil {
					t.Errorf("%s %s 自己的消息不该留头像位", scene, m.ID)
				}
				if hasAvatar {
					av := row.find("avatar")
					if !near(av.Box.Y+av.Box.H, bubble.Box.Y+bubble.Box.H) {
						t.Errorf("%s %s 头像没和气泡底对齐", scene, m.ID)
					}
				}
				// 引用块：离气泡上、左、右都是竖内边距
				if q := bubble.find("quote"); q != nil {
					d := edges(bubble.Box, q.Box)
					first := tk.BubblePadV
					if hasName {
						first = d[0] // 有名字时上边是名字，不量
					}
					for k, want := range [4]float32{first, tk.BubblePadV, -1, tk.BubblePadV} {
						if want >= 0 && !near(d[k], want) {
							t.Errorf("%s %s 引用块第 %d 边距 %.2f，期望 %.2f", scene, m.ID, k, d[k], want)
						}
					}
					// 四个角逐个量：引用块在最上面时上边两角各自和气泡同侧的角同心，其余角是最小内层圆角
					std := tk.RInnerMin
					want := [4]float32{std, std, std, std}
					if !hasName {
						want[0] = tk.Inner(bubble.Radius[0], tk.BubblePadV)
						want[1] = tk.Inner(bubble.Radius[1], tk.BubblePadV)
					}
					for k := range want {
						if !near(q.Radius[k], want[k]) {
							t.Errorf("%s %s 引用块第 %d 角 %.2f，期望 %.2f（气泡角 %v）", scene, m.ID, k, q.Radius[k], want[k], bubble.Radius)
						}
					}
					// 引用块的竖条和正文落在同一条竖线上
					if bar, txt := q.find("bar"), bubble.find("text"); bar != nil && txt != nil && !near(bar.Box.X, txt.Box.X+txt.Pad[3]) {
						t.Errorf("%s %s 竖条 x=%.2f 和正文 x=%.2f 没对齐", scene, m.ID, bar.Box.X, txt.Box.X+txt.Pad[3])
					}
					// 引用块下沿到正文：一个竖内边距
					if txt := bubble.find("text"); txt != nil && !near(txt.Box.Y-(q.Box.Y+q.Box.H), tk.BubblePadV) {
						t.Errorf("%s %s 引用块到正文 %.2f，期望 %.2f", scene, m.ID, txt.Box.Y-(q.Box.Y+q.Box.H), tk.BubblePadV)
					}
				}
				// 停下：她的气泡右边贴底一个停止图标，和失败角标同一个大小；不写"被打断了"
				if m.State == Interrupted {
					st := row.find("stopped")
					if st == nil {
						t.Fatalf("%s 停下的消息没有停止图标", scene)
					}
					ic := st.find("badge")
					if !near(ic.Box.Y+ic.Box.H, bubble.Box.Y+bubble.Box.H) || ic.Box.X < bubble.Box.X+bubble.Box.W || !near(ic.Box.W, tk.Badge) {
						t.Errorf("%s 停止图标 %+v 不在气泡 %+v 右边贴底、或大小不是 %.1f", scene, ic.Box, bubble.Box, tk.Badge)
					}
					if tt.HasText("被打断了") || tt.HasText("已停止") {
						t.Errorf("%s 停下写了字", scene)
					}
				}
				// 失败角标：在气泡左边、底边和气泡底边齐平；没有任何说明文字
				if m.State == Failed {
					badge := row.find("badge")
					if badge == nil {
						t.Fatalf("%s 失败消息没有角标", scene)
					}
					if !near(badge.Box.Y+badge.Box.H, bubble.Box.Y+bubble.Box.H) {
						t.Errorf("%s 角标底 %.2f ≠ 气泡底 %.2f", scene, badge.Box.Y+badge.Box.H, bubble.Box.Y+bubble.Box.H)
					}
					if badge.Box.X+badge.Box.W > bubble.Box.X {
						t.Errorf("%s 角标不在气泡左边", scene)
					}
					// 只看画出来的字（读屏标签"重试发送"不算）
					var visit func(*node)
					visit = func(n *node) {
						if n.Text == "发送失败" || n.Text == "重试发送" || n.Text == "这次没有发出去，请重试。" || n.Text == "正在发送" {
							t.Errorf("%s 出现了说明文字：%q", scene, n.Text)
						}
						for _, ch := range n.Children {
							visit(ch)
						}
					}
					visit(row)
				}
			}
		}
	}
}

// 拖滑块（规格九-3）：改设置后同一帧内字号和圆角就变了。
func TestSettingsApplyLive(t *testing.T) {
	s := DefaultSettings()
	tk := Derive(s)
	host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
	m := &Msg{ID: "x", Sender: "r1", Name: "晴枝", Char: CharColors[0], Text: "第一行\n第二行\n第三行"}
	view := func(c *ui.Context) { host.Message(c, m, nil, nil) }
	tt := ui.NewTester(view, 700, 300)
	tt.Frame()
	before := tree(tt).path("x", "bubble")
	r0, h0 := before.Radius[0], before.Box.H
	s.Radius, s.Text = RadiusMax, TextMax
	host.T = Derive(s)
	tt.Frame()
	after := tree(tt).path("x", "bubble")
	if !(after.Radius[0] > r0) {
		t.Errorf("圆角调大后没变：%.2f → %.2f", r0, after.Radius[0])
	}
	if !(after.Box.H > h0) {
		t.Errorf("字号调大后气泡没变高：%.2f → %.2f", h0, after.Box.H)
	}
}

// 多行正文按实际最长一行收紧（主人 11-06："为什么消息气泡左右内距不一"）：
// 让"二十个字 + 句号"刚好挤成两行，句号不能放行首，会带一个字下去，第一行右边空出一截；
// 正文宽度要等于最长一行，气泡左右内边距相等。
func TestBubbleFitsLongestLine(t *testing.T) {
	tk := Derive(DefaultSettings())
	host := Host{T: tk, SelfChar: CharColors[0], NoEnter: true, Column: 600}
	var charW float32
	var lay layout
	m := &Msg{ID: "w", Sender: "r1", Name: "晴枝", Char: CharColors[0], Text: strings.Repeat("字", 20) + "。"}
	view := func(c *ui.Context) {
		charW, _ = c.MeasureText(1e6, ui.Span{Text: "字", Size: tk.Body})
		lay = host.measure(c, m, nil, nil)
		host.Message(c, m, nil, nil)
	}
	tt := ui.NewTester(view, 1200, 400)
	tt.Frame()
	// 可用宽 = 20.5 个字：21 个字放不下
	host.Column = (charW*20.5 + tk.BubblePadH*2) / tk.BubbleShare
	tt.Frame()
	tt.Frame()
	if lay.Lines < 2 {
		t.Fatalf("没有换行（%d 行），这个测试没测到东西", lay.Lines)
	}
	if lay.Fit > lay.TextW-charW*0.5 {
		t.Fatalf("最长一行 %.2f 应明显短于可用宽 %.2f", lay.Fit, lay.TextW)
	}
	bubble := tree(tt).path("w", "bubble")
	txt := bubble.find("text")
	d := edges(bubble.Box, txt.Box)
	if !near(d[1], d[3]) {
		t.Errorf("正文左右到气泡边 %.2f / %.2f 不等", d[3], d[1])
	}
	if !near(bubble.Box.W, lay.Fit+tk.BubblePadH*2) {
		t.Errorf("气泡宽 %.2f，应等于最长一行 %.2f + 两边横内边距", bubble.Box.W, lay.Fit)
	}
}

// 右下角时间的位置（Telegram 占位块）：挂在最后一行 / 另起一行 / 挂在表情那排 / 表情满了另起一行。
func TestInfoPlacement(t *testing.T) {
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 13, 50, 0, 0, time.Local)
	many := []Reaction{{Emoji: "⭐", Count: 1}, {Emoji: "😢", Count: 3, Mine: true}, {Emoji: "👍", Count: 12}, {Emoji: "🎉", Count: 2}, {Emoji: "😂", Count: 5}, {Emoji: "❤", Count: 7}, {Emoji: "🔥", Count: 9}, {Emoji: "👀", Count: 4}}
	cases := []struct {
		name      string
		text      string
		reactions []Reaction
		inline    bool
	}{
		{"短句挂在同一行", "好，我在听。", nil, true},
		{"最后一行满了另起一行", strings.Repeat("字", 20), nil, false},
		{"表情少挂在表情排", "比如这个。", []Reaction{{Emoji: "⭐", Count: 1}, {Emoji: "😢", Count: 1, Mine: true}}, true},
		{"表情两排挂在第二排", strings.Repeat("字", 20), many, true},
		{"表情排满另起一行", strings.Repeat("字", 20), nil, false}, // 表情由下面逐个加到时间挂不上为止
	}
	for _, cs := range cases {
		host := Host{T: tk, SelfChar: CharColors[0], NoEnter: true, Column: 600}
		m := &Msg{ID: "i", Sender: "r1", Name: "晴枝", Char: CharColors[0], At: at, Text: cs.text, Reactions: cs.reactions}
		var charW float32
		var lay layout
		view := func(c *ui.Context) {
			charW, _ = c.MeasureText(1e6, ui.Span{Text: "字", Size: tk.Body})
			lay = host.measure(c, m, nil, nil)
			host.Message(c, m, nil, nil)
		}
		tt := ui.NewTester(view, 1200, 600)
		tt.Frame()
		// 可用宽 = 20.3 个字：20 个字放得下一行，但后面挂不下时间
		host.Column = (charW*20.3 + tk.BubblePadH*2) / tk.BubbleShare
		tt.Frame()
		tt.Frame()
		if cs.name == "表情排满另起一行" {
			emojis := []string{"👍", "😂", "🎉", "🔥", "👀", "❤", "😢", "⭐", "🙏", "😮"}
			for i := 0; i < 40; i++ {
				m.Reactions = append(m.Reactions, Reaction{Emoji: emojis[i%len(emojis)], Count: 4 + i%5})
				tt.Frame()
				if !lay.InfoInline {
					break
				}
			}
			cs.reactions = m.Reactions
			tt.Frame()
		}
		if lay.InfoInline != cs.inline {
			t.Errorf("%s：时间挂在同一行 = %v，期望 %v（%+v）", cs.name, lay.InfoInline, cs.inline, lay)
			continue
		}
		b := tree(tt).path("i", "bubble")
		tm := b.find("time")
		if tm == nil {
			t.Fatalf("%s：没画时间", cs.name)
		}
		// 右沿：时间挂在正文行时和正文右沿对齐（离气泡右边一个横内边距）
		if len(cs.reactions) == 0 && !near(b.Box.X+b.Box.W-(tm.Box.X+tm.Box.W), tk.BubblePadH) {
			t.Errorf("%s：时间右边距 %.2f，期望 %.2f", cs.name, b.Box.X+b.Box.W-(tm.Box.X+tm.Box.W), tk.BubblePadH)
		}
		switch {
		case cs.inline && len(cs.reactions) == 0:
			// 时间和最后一行的字之间至少隔一个 s2，不压字
			if gap := tm.Box.X - (b.Box.X + tk.BubblePadH + lay.LastW); gap < tk.S2-0.75 {
				t.Errorf("%s：时间离最后一行的字只有 %.2f", cs.name, gap)
			}
		case cs.inline:
			// 时间不压最后一排表情
			last := lay.Chips[len(lay.Chips)-1]
			r := b.find("reactions")
			if gap := tm.Box.X - (r.Box.X + last.X + last.W); gap < tk.S2-0.75 {
				t.Errorf("%s：时间离最后一个表情只有 %.2f", cs.name, gap)
			}
		default:
			// 时间不压在正文或表情上
			last := b.find("text")
			if r := b.find("reactions"); r != nil {
				last = r
			}
			if tm.Box.Y < last.Box.Y+last.Box.H-0.75 {
				t.Errorf("%s：时间压到了上面的内容（时间 y=%.2f h=%.2f，上一块 y=%.2f h=%.2f，气泡 y=%.2f h=%.2f）", cs.name, tm.Box.Y, tm.Box.H, last.Box.Y, last.Box.H, b.Box.Y, b.Box.H)
			}
		}
		// 高度：挂在同一行时不额外加高
		want := tk.BubblePadV*2 + lay.TextH
		if len(cs.reactions) > 0 {
			want += tk.BubblePadV + lay.ReactH
		}
		if !cs.inline {
			want += lay.InfoH
		}
		if !near(b.Box.H, want) {
			t.Errorf("%s：气泡高 %.2f，期望 %.2f", cs.name, b.Box.H, want)
		}
	}
}

// 表情回应：整条消息不超过 3 人显示头像、超过显示数字（Telegram）；表情框到按钮上、下、左等距，
// 头像到按钮右、上、下等距，圆角同心。
func TestReactionChips(t *testing.T) {
	a, b := CharColors[0], CharColors[1]
	if !ShowUserpics([]Reaction{{Emoji: "😢", Count: 2, By: []Reactor{{Name: "雾栞", Char: b}, {Name: "我", Char: a}}}, {Emoji: "❤", Count: 1, By: []Reactor{{Name: "晴枝", Char: a}}}}) {
		t.Error("一共 3 人应显示头像")
	}
	if ShowUserpics([]Reaction{{Emoji: "👍", Count: 2, By: []Reactor{{}, {}}}, {Emoji: "😂", Count: 2, By: []Reactor{{}, {}}}}) {
		t.Error("一共 4 人应显示数字（Telegram 按整条消息算，不按单个表情）")
	}
	if ShowUserpics([]Reaction{{Emoji: "👍", Count: 2, By: []Reactor{{}}}}) {
		t.Error("点了的人不全时应显示数字")
	}
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 13, 50, 0, 0, time.Local)
	EmojiDir = twemojiDir(t)
	defer func() { EmojiDir = "" }()
	for _, cs := range []struct {
		name string
		rs   []Reaction
		pics bool
	}{
		{"头像", []Reaction{{Emoji: "😊", Count: 2, Mine: true, By: []Reactor{{Name: "雾栞", Char: b}, {Name: "我", Char: a}}}}, true}, // 像素量中心要用左右对称的表情（😢 一侧有泪滴）
		{"数字", []Reaction{{Emoji: "❤", Count: 5}, {Emoji: "😂", Count: 1}}, false},
	} {
		host := Host{T: tk, SelfChar: a, NoEnter: true, Column: 600}
		m := &Msg{ID: "x", Sender: "r1", Name: "晴枝", Char: a, At: at, Text: "比如这个。", Reactions: cs.rs}
		view := func(c *ui.Context) { host.Message(c, m, nil, nil) }
		tt := ui.NewTester(view, 800, 300)
		tt.SetScale(4) // 放大画，像素量得准
		tt.Frame()
		tt.Frame()
		root := tree(tt)
		chip0 := root.path("x", "reactions").Children[0]
		emoji := chip0.find("e")
		d := edges(chip0.Box, emoji.Box)
		if !near(d[0], d[2]) || !near(d[0], d[3]) {
			t.Errorf("%s：表情框到按钮上 %.2f 下 %.2f 左 %.2f 不等", cs.name, d[0], d[2], d[3])
		}
		// 按像素量表情图形本身：竖直方向居中于按钮、水平方向居中于正方形框（瘦的表情如 👍 左右天然空得多，
		// 不要求左 = 上），差不超过 1
		ink := inkBox(tt, emoji.Box)
		if d := (ink.Y + ink.H/2) - (chip0.Box.Y + chip0.Box.H/2); d > 1 || d < -1 {
			t.Errorf("%s：表情图形竖直中心偏了 %.2f（上 %.2f 下 %.2f）", cs.name, d, ink.Y-chip0.Box.Y, chip0.Box.Y+chip0.Box.H-ink.Y-ink.H)
		}
		if d := (ink.X + ink.W/2) - (emoji.Box.X + emoji.Box.W/2); d > 1 || d < -1 {
			t.Errorf("%s：表情图形水平中心偏了 %.2f", cs.name, d)
		}
		pics := chip0.find("pics")
		if (pics != nil) != cs.pics {
			t.Fatalf("%s：显示头像 = %v，期望 %v", cs.name, pics != nil, cs.pics)
		}
		if pics != nil {
			in := pics.Children[len(pics.Children)-1].find("in")
			q := edges(chip0.Box, in.Box)
			if !near(q[0], d[0]) || !near(q[1], d[3]) || !near(q[2], d[2]) {
				t.Errorf("%s：头像到按钮 上 %.2f 右 %.2f 下 %.2f，应和表情框到上下左 %.2f 一样", cs.name, q[0], q[1], q[2], d[0])
			}
			p := edges(chip0.Box, pics.Box)
			if !near(p[0], p[1]) || !near(p[0], p[2]) {
				t.Errorf("%s：头像到按钮上 %.2f 右 %.2f 下 %.2f 不等", cs.name, p[0], p[1], p[2])
			}
			if !near(chip0.Radius[0]-p[0], tk.ChipAvatar/2) {
				t.Errorf("%s：头像和按钮圆角不同心（按钮 %.2f − 边距 %.2f ≠ 头像半径 %.2f）", cs.name, chip0.Radius[0], p[0], tk.ChipAvatar/2)
			}
		} else if chip0.find("n") == nil {
			t.Errorf("%s：没显示数字", cs.name)
		}
	}
}

// inkBox 在 Tester 的图里找表情框 area 里和底色明显不同的像素（表情图形）的外框，单位 DIP。
// 底色取框左上角那一点（图形比框小，角上是按钮底色；不取按钮边缘，主人点过的按钮边上有细边）。
func inkBox(tt *ui.Tester, area ui.Rect) ui.Rect {
	img := tt.Image()
	scale := float32(img.Bounds().Dx()) / 800
	br, bg, bb, _ := img.At(int((area.X+0.5)*scale), int((area.Y+0.5)*scale)).RGBA()
	x0, y0, x1, y1 := 1<<30, 1<<30, -1, -1
	for y := int(area.Y*scale) - 4; y < int((area.Y+area.H)*scale)+4; y++ {
		for x := int(area.X*scale) - 4; x < int((area.X+area.W)*scale)+4; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if absDiff(r, br)+absDiff(g, bg)+absDiff(b, bb) > 3*0x2000 {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	return ui.Rect{X: float32(x0) / scale, Y: float32(y0) / scale, W: float32(x1-x0+1) / scale, H: float32(y1-y0+1) / scale}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// twemojiDir 是本机 native/twemoji/ 里放着的 Twemoji（不进 git）；没有就跳过要用图的检查。
func twemojiDir(t *testing.T) string {
	d := assets.TwemojiDir()
	if d == "" {
		t.Skip("本机没有 Twemoji（native/twemoji/）")
	}
	return d
}

// Strong 档（会话列表选中行这类）：每个角色色、浅深两种模式，白字和 90% 白次要字都过 WCAG 4.5:1，
// 并且是过线的最亮一档（再亮 0.01 就不过；角色色本身够深时直接用它）。主人 11-06：换主题色要能按公式自动更新。
func TestStrongAccentContrast(t *testing.T) {
	white := ui.Color{R: 255, G: 255, B: 255, A: 255}
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		for i, ch := range append([]OKLCH{DefaultChar}, CharColors...) {
			a := tk.AccentOf(ch)
			name, muted := Contrast(a.OnStrong, a.Strong), Contrast(a.OnStrongMuted, a.Strong)
			if name < StrongMinContrast || muted < StrongMinContrast {
				t.Errorf("深=%v 色%d 选中底 %v：白字 %.2f、次要字 %.2f，要都 ≥ %.1f", dark, i, a.Strong, name, muted, StrongMinContrast)
			}
			if a.Strong != ch.Color() {
				brighter := OKLCH{ch.L, ch.C, ch.H, 1}
				for l := 0.0; l < 1; l += 0.0005 {
					if (OKLCH{l, ch.C, ch.H, 1}).Color() == a.Strong {
						brighter.L = l + 0.01
						break
					}
				}
				b := brighter.Color()
				if Contrast(white, b) >= StrongMinContrast && Contrast(over(white, strongMutedAlpha, b), b) >= StrongMinContrast {
					t.Errorf("深=%v 色%d 选中底不是过线的最亮一档：再亮 0.01 也过", dark, i)
				}
			}
			t.Logf("深=%v 色%d 选中底 %v 白字 %.2f 次要字 %.2f", dark, i, a.Strong, name, muted)
		}
	}
}

// 撤回消息的字要读得清（审查 A1）：Bun 那种整块 opacity 0.55 会把字压到约 2.3:1。
// 按实际画出来的像素量：字最深的那一点对气泡底色至少 4.5（浅深 × 有无壁纸 × 她的和我的）。
func TestRecalledReadable(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, wall := range []bool{false, true} {
			for _, self := range []bool{false, true} {
				s := DefaultSettings()
				s.Dark, s.Wallpaper = dark, wall
				tk := Derive(s)
				host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
				m := &Msg{ID: "r", Sender: "r1", Self: self, Name: "晴枝", Char: CharColors[0], State: Recalled}
				view := func(c *ui.Context) {
					c.Root().Background(tk.C.ChatBg)
					host.Message(c, m, nil, nil)
				}
				tt := ui.NewTester(view, 700, 200)
				tt.Frame()
				tt.Frame()
				n := tree(tt).path("r", "text")
				img := tt.Image()
				bg := img.RGBAAt(int(n.Box.X)-2, int(n.Box.Y+n.Box.H/2)) // 正文左边一点点，还在气泡里
				best := 0.0
				for y := int(n.Box.Y); y < int(n.Box.Y+n.Box.H); y++ {
					for x := int(n.Box.X); x < int(n.Box.X+n.Box.W); x++ {
						p := img.RGBAAt(x, y)
						best = max(best, Contrast(ui.Color{R: p.R, G: p.G, B: p.B, A: 255}, ui.Color{R: bg.R, G: bg.G, B: bg.B, A: 255}))
					}
				}
				if best < MinTextContrast {
					t.Errorf("深=%v 壁纸=%v 自己=%v 撤回字最深处对比 %.2f < 4.5", dark, wall, self, best)
				}
			}
		}
	}
}

// 相贴小角固定 6，圆角滑块从最小拖到最大都不变（ADR-285 第 3 条）。
func TestJoinRadiusFixed(t *testing.T) {
	for r := float32(RadiusMin); r <= RadiusMax; r++ {
		s := DefaultSettings()
		s.Radius = r
		tk := Derive(s)
		if tk.RJoin != JoinRadius {
			t.Errorf("圆角 %v 时相贴角 %v，期望固定 %v", r, tk.RJoin, JoinRadius)
		}
	}
}

// 别人消息上"我点过的"表情回应用我自己的强调色，不用发送人的角色色（ADR-285 第 8 条）。
func TestMineReactionUsesSelfAccent(t *testing.T) {
	tk := Derive(DefaultSettings())
	self, other := CharColors[0], CharColors[3]
	host := Host{T: tk, SelfChar: self, Column: 600, NoEnter: true}
	m := &Msg{ID: "x", Sender: "r4", Name: "雾栞", Char: other, Text: "好呀", Reactions: []Reaction{{Emoji: "👍", Count: 5, Mine: true}}}
	view := func(c *ui.Context) { host.Message(c, m, nil, nil) }
	tt := ui.NewTester(view, 700, 200)
	tt.Frame()
	tt.Frame()
	r := tree(tt).path("x", "reactions")
	if r == nil || len(r.Children) == 0 {
		t.Fatal("找不到表情回应按钮")
	}
	chip := r.Children[0]
	if chip == nil {
		t.Fatal("找不到表情回应按钮")
	}
	px := tt.Image().RGBAAt(int(chip.Box.X+chip.Box.W-3), int(chip.Box.Y+chip.Box.H/2)) // 按钮右端内侧，避开表情和数字
	want, wrong := tk.AccentOf(self).Base, tk.AccentOf(other).Base
	if px.R != want.R || px.G != want.G || px.B != want.B {
		t.Errorf("我点过的回应底色 %v，期望我的强调色 %v（发送人的是 %v）", px, want, wrong)
	}
}

// 有壁纸时自己的气泡也有 sh-1 投影（Bun chat.css:529；审查 B4）：气泡下沿往下 1 像素比底色深；没壁纸时不深。
func TestSelfBubbleShadowOnWallpaper(t *testing.T) {
	for _, wall := range []bool{true, false} {
		s := DefaultSettings()
		s.Wallpaper = wall
		tk := Derive(s)
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
		m := &Msg{ID: "s", Sender: "self", Self: true, Text: "好呀"}
		bg := ui.RGB(240, 242, 245)
		view := func(c *ui.Context) {
			c.Root().Background(bg)
			host.Message(c, m, nil, nil)
		}
		tt := ui.NewTester(view, 700, 200)
		tt.Frame()
		tt.Frame()
		b := tree(tt).path("s", "bubble")
		p := tt.Image().RGBAAt(int(b.Box.X+b.Box.W/2), int(b.Box.Y+b.Box.H)+1)
		darker := p.R < bg.R || p.G < bg.G || p.B < bg.B
		if wall && !darker {
			t.Errorf("有壁纸时自己的气泡下面没有投影：%v", p)
		}
		if !wall && darker {
			t.Errorf("没壁纸时自己的气泡下面不该有投影：%v", p)
		}
	}
}

// 段距照 Grok（审查 C1）：空行分段，段间隔一个正文字号，不是一整行；单个换行只换行不加空隙。
func TestParagraphGap(t *testing.T) {
	tk := Derive(DefaultSettings())
	host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
	for _, c := range []struct {
		text string
		want float32
	}{
		{"第一段\n\n第二段", 2*tk.BodyLine + tk.Body},
		{"第一段\n\n\n  \n第二段", 2*tk.BodyLine + tk.Body}, // 几个空行也只算一次段距
		{"第一行\n第二行", 2 * tk.BodyLine},
	} {
		m := &Msg{ID: "p", Sender: "r1", Name: "晴枝", Char: CharColors[0], Text: c.text}
		view := func(ctx *ui.Context) { host.Message(ctx, m, nil, nil) }
		tt := ui.NewTester(view, 700, 300)
		tt.Frame()
		tt.Frame()
		n := tree(tt).path("p", "text")
		if n == nil {
			t.Fatalf("%q 找不到正文", c.text)
		}
		if !near(n.Box.H, c.want) {
			t.Errorf("%q 正文高 %.1f，期望 %.1f", c.text, n.Box.H, c.want)
		}
	}
}

// 减少动态效果时消息进场不位移（R067；审查 C3）：这是 mygo 的 Transition 自己保证的（ui/transition.go:85-88），
// 这条测试钉住它，升级 mygo 后行为变了会红。进场第一帧看得见字时的位置就是停稳后的位置；平时第一帧偏下。
func TestEnterRespectsReduceMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		tk := Derive(DefaultSettings())
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600}
		show := false
		m := &Msg{ID: "e", Sender: "self", Self: true, Text: "进场"}
		view := func(c *ui.Context) {
			c.Root().Background(ui.RGB(255, 255, 255))
			if show {
				host.Message(c, m, nil, nil)
			}
		}
		tt := ui.NewTester(view, 400, 120)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		show = true
		first := float32(-1)
		for range 30 { // 逐帧往后找第一帧看得见字的位置（刚进场时字还是透明的）
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			if first = textTop(tt); first >= 0 {
				break
			}
		}
		tt.Advance(2 * time.Second)
		tt.Frame()
		settled := textTop(tt)
		moved := first - settled
		if reduce && moved > 0.5 {
			t.Errorf("减少动态效果时进场还在位移：第一帧比停稳低 %.1f", moved)
		}
		if !reduce && moved < 1 {
			t.Errorf("平时进场没有位移（第一帧比停稳低 %.1f），测试本身可能没量到", moved)
		}
	}
}

// textTop 是图里字（亮度很低的像素）最上沿的 y；进场还在淡入时字是浅的，用"比底色明显深"判断。
func textTop(tt *ui.Tester) float32 {
	img := tt.Image()
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p := img.RGBAAt(x, y)
			if int(p.R)+int(p.G)+int(p.B) < 3*160 {
				return float32(y)
			}
		}
	}
	return -1
}

// 引用块里面四边等距（审查 C5）：内距四边一样，竖条离引用块上、下、左相等；字离右边至少 s2；在三档外观下都成立。
func TestQuoteInnerEqual(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		tk := Derive(s)
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
		m := &Msg{ID: "q", Sender: "r1", Name: "晴枝", Char: CharColors[0], Text: "好", Quote: &Quote{Name: "雾栞", Text: strings.Repeat("很长的引用原文", 20)}}
		view := func(c *ui.Context) { host.Message(c, m, nil, nil) }
		tt := ui.NewTester(view, 700, 300)
		tt.Frame()
		tt.Frame()
		q := tree(tt).path("q", "quote")
		if q == nil || len(q.Children) < 2 {
			t.Fatal("找不到引用块")
		}
		for k := 1; k < 4; k++ {
			if !near(q.Pad[k], q.Pad[0]) {
				t.Errorf("外观 %+v 引用块内距不等：%v", s, q.Pad)
			}
		}
		bar, col := q.Children[0], q.Children[1]
		top, bottom, left := bar.Box.Y-q.Box.Y, q.Box.Y+q.Box.H-bar.Box.Y-bar.Box.H, bar.Box.X-q.Box.X
		if !near(top, left) || !near(bottom, left) {
			t.Errorf("外观 %+v 竖条离引用块 上 %.2f 下 %.2f 左 %.2f，应相等", s, top, bottom, left)
		}
		if right := q.Box.X + q.Box.W - col.Box.X - col.Box.W; right < tk.S2-0.5 {
			t.Errorf("外观 %+v 字离引用块右边 %.2f，少于 s2 %.2f", s, right, tk.S2)
		}
	}
}

// 时间用等宽数字（R046；审查 D2）：字体数字默认不等宽（11:11 比 08:08 窄 14），开了 tnum 时间块宽度不随数字变。
// 用仓库里的测试子集：数字字形和 tnum 与完整文渊圆体一样，整包不必为这条布局用例复制几十 MB 字体。
func TestTimeTabularDigits(t *testing.T) {
	font := registerTinyTestFont(t)
	tk := Derive(DefaultSettings())
	host := Host{T: tk, Font: font, SelfChar: CharColors[0], Column: 600, NoEnter: true}
	var w1, w2 float32
	view := func(c *ui.Context) {
		w1 = host.measure(c, &Msg{ID: "a", Sender: "r1", Text: "好", At: time.Date(2026, 11, 6, 11, 11, 0, 0, time.Local)}, nil, nil).InfoW
		w2 = host.measure(c, &Msg{ID: "b", Sender: "r1", Text: "好", At: time.Date(2026, 11, 6, 8, 8, 0, 0, time.Local)}, nil, nil).InfoW
	}
	tt := ui.NewTester(view, 200, 100)
	tt.Frame()
	if w1 != w2 {
		t.Errorf("时间块宽度随数字变：11:11 是 %.1f，08:08 是 %.1f", w1, w2)
	}
}

// 角色色阶梯有缓存：缓存命中的结果必须和重新推一遍完全一样，深浅和中性色不同的不能串。
func TestAccentOfMemoMatchesDerive(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, neutral := range []Neutral{NeutralCool, NeutralPlain, NeutralWarm} {
			s := DefaultSettings()
			s.Dark, s.Neutral = dark, neutral
			tk := Derive(s)
			for i, ch := range append([]OKLCH{DefaultChar}, CharColors...) {
				first, again := tk.AccentOf(ch), tk.AccentOf(ch)
				if want := tk.deriveAccent(ch); first != want || again != want {
					t.Fatalf("深=%v 中性=%v 色%d：缓存的色阶梯和重新推的不同", dark, neutral, i)
				}
			}
		}
	}
}
