package main

// 引用块的测试（数字说话）：块在气泡里离上、左、右三边的距离和圆角同心、点击跳转、原消息不在本地、跳转时往上翻页找。数据全是编的。

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/api"
)

// ───────────── 展示 DTO → 引用 ─────────────

func TestQuoteOf(t *testing.T) {
	t.Parallel()
	ready := func(who, text string) api.Result[api.QuoteView] {
		return api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{DisplayName: who, Text: text}}
	}
	ref := &api.EventRef{Event: api.EventKey{Platform: "qq", EventID: "o"}}
	cases := []struct {
		name string
		v    api.MessageView
		want *quote
	}{
		{"没有引用", api.MessageView{}, nil},
		{"读到了原消息", api.MessageView{ReplyTo: ref, Reply: ready("豆沙包", "今天\n也是  摸鱼")}, &quote{target: "o", who: "豆沙包", text: "今天 也是 摸鱼", ok: true}},
		{"原消息没有文字（只有图之类）", api.MessageView{ReplyTo: ref, Reply: ready("豆沙包", " \n ")}, &quote{target: "o", who: "豆沙包", text: quoteNoText, ok: true}},
		{"原消息没有发言人名字", api.MessageView{ReplyTo: ref, Reply: ready("", "嗯")}, &quote{target: "o", who: "某人", text: "嗯", ok: true}},
		{"后台读不到原消息", api.MessageView{ReplyTo: ref, Reply: api.Result[api.QuoteView]{State: api.Unsupported}}, &quote{target: "o"}},
		{"只有引用段、reply_to 没连上", api.MessageView{Segments: []api.DisplaySegment{{Kind: "reply", Target: "5001"}}}, &quote{}},
		{"控制字符去掉", api.MessageView{ReplyTo: ref, Reply: ready("甲", "a\x00b")}, &quote{target: "o", who: "甲", text: "ab", ok: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := quoteOf(c.v)
			if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
				t.Fatalf("得 %+v，应为 %+v", got, c.want)
			}
		})
	}
	long := quoteOf(api.MessageView{ReplyTo: ref, Reply: ready("甲", strings.Repeat("长", 500))})
	if n := len([]rune(long.text)); n != quoteMaxRunes {
		t.Fatalf("长文字应截到 %d 个字，实际 %d", quoteMaxRunes, n)
	}
}

// msgFromView 把引用带进 msg；没有 reply_to 的没有引用
func TestQuoteInMsg(t *testing.T) {
	t.Parallel()
	v := quoting(view("b", "human", "某人", "好", t0), "a", "甲", "原话", true)
	if m := msgFromView(demoKey("qq:group:1001"), v, charOf); m.quote == nil || !m.quote.ok || m.quote.target != "a" {
		t.Fatalf("%+v", m.quote)
	}
	if m := msgFromView(demoKey("qq:group:1001"), view("c", "human", "某人", "好", t0), charOf); m.quote != nil {
		t.Fatalf("没引用却有引用块：%+v", m.quote)
	}
}

// ───────────── 几何：四边等距、圆角同心 ─────────────

// fitRadius 在放大后的图里量一个色块左上角的圆角（DIP）：逐行找第一个"更像块的颜色"的像素，
// 和半径 0～16 DIP 的圆弧逐个比，取误差最小的。inside 是块里的颜色，outside 是块外面（壳）的颜色
func fitRadius(img *image.RGBA, scale, x0, y0 float32, inside, outside [3]uint8) float32 {
	px := func(x, y int) [3]uint8 { c := img.RGBAAt(x, y); return [3]uint8{c.R, c.G, c.B} }
	dist := func(a, b [3]uint8) int {
		d := 0
		for i := range a {
			d += (int(a[i]) - int(b[i])) * (int(a[i]) - int(b[i]))
		}
		return d
	}
	X0, Y0 := int(math.Round(float64(x0*scale))), int(math.Round(float64(y0*scale)))
	rows := int(20 * scale)
	off := make([]float64, rows)
	for dy := range rows {
		off[dy] = -1
		for x := X0 - 2; x < X0+int(20*scale); x++ {
			p := px(x, Y0+dy)
			if dist(p, inside) < dist(p, outside) {
				off[dy] = float64(x - X0)
				break
			}
		}
		if off[dy] < 0 {
			return -1
		}
	}
	best, bestErr := float32(-1), math.MaxFloat64
	for r := float32(0); r <= 16; r += 0.25 {
		R := float64(r * scale)
		e := 0.0
		for dy := range rows {
			want := 0.0
			if float64(dy) < R {
				d := R - float64(dy) - 0.5
				want = R - math.Sqrt(math.Max(R*R-d*d, 0))
			}
			e += math.Abs(off[dy] - want)
		}
		if e < bestErr {
			best, bestErr = r, e
		}
	}
	return best
}

func TestQuoteGeometry(t *testing.T) {
	const scale = 4
	for _, mine := range []bool{false, true} {
		t.Run(fmt.Sprint("mine=", mine), func(t *testing.T) {
			ms := msgsOf(t,
				view("a", "human", "豆沙包", "原来的话：今天也是摸鱼的一天", t0),
				quoting(view("b", "human", "半糖", "我也这么觉得，所以今晚一起打游戏吧，顺便聊聊周末的安排", t0+60_000), "a", "豆沙包", "原来的话：今天也是摸鱼的一天", true))
			ms[1].mine, ms[1].md = mine, false
			a := chatApp(ms, true)
			p := newProbes(a)
			tt := ui.NewTester(func(c *ui.Context) { p.ctx, p.theme = c, *c.Theme(); a.chatView(c) }, 900, 700)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.SetScale(scale)
			tt.Frame()
			tt.Frame()
			q, body := p.box(t, "quote:b"), p.box(t, "body:b:0")
			if !approx(q.X, body.X, 0.5) || !approx(q.W, body.W, 0.5) || q.Y+q.H > body.Y+0.5 {
				t.Fatalf("引用应与正文共用内边线并在正文前：引用 %+v 正文 %+v", q, body)
			}
			// 测量仍从渲染像素取：换成主题的消息背景与文字色，小圆角还受外层可用空间约束。
			img := tt.Image()
			tokens := theme.Light()
			shellBg := tokens.Materials().MessageIn().Background()
			base, tint := tokens.Colors().Quote().In().Background(), tokens.Metrics().Quote().BackgroundOpacity()
			if mine {
				shellBg, base = tokens.Materials().MessageOut().Background(), tokens.Colors().Quote().Out().Background()
			}
			mix := func(bg, fg ui.Color, a float32) [3]uint8 {
				f := func(b, g uint8) uint8 { return uint8(math.Round(float64(b)*(1-float64(a)) + float64(g)*float64(a))) }
				return [3]uint8{f(bg.R, fg.R), f(bg.G, fg.G), f(bg.B, fg.B)}
			}
			got := fitRadius(img, scale, q.X, q.Y, mix(shellBg, base, tint), [3]uint8{shellBg.R, shellBg.G, shellBg.B})
			want := tokens.Metrics().Quote().Radius()
			t.Logf("块的圆角量出来 %.2f，外层圆角 %.1f，小圆角 %.1f，应为 %.1f", got, tokens.Metrics().Message().RadiusLarge(), tokens.Metrics().Quote().Radius(), want)
			if got < 0 || math.Abs(float64(got-want)) > 1 {
				t.Fatalf("块的圆角量出来 %.2f，应为 %.1f（小圆角不能越过壳边）", got, want)
			}
		})
	}
}

// ───────────── 点击跳转 ─────────────

// chain 编 n 条连续的消息，第 quoter 条引用第 target 条
func chain(t *testing.T, n, quoter, target int) []*msg {
	t.Helper()
	var vs []api.MessageView
	for i := range n {
		v := view(fmt.Sprint("m", i), "human", "某人", fmt.Sprintf("第 %d 条", i), t0+int64(i)*600_000)
		if i == quoter {
			v = quoting(v, fmt.Sprint("m", target), "某人", fmt.Sprintf("第 %d 条", target), true)
		}
		vs = append(vs, v)
	}
	return msgsOf(t, vs...)
}

func centerOf(r ui.Rect) (float32, float32) { return r.X + r.W/2, r.Y + r.H/2 }

func TestQuoteClickJumps(t *testing.T) {
	a := chatApp(chain(t, 60, 59, 2), true)
	a.list.FollowEnd = true
	tt, p := chatTester(a, 900, 700)
	first, last := a.list.Visible()
	if first <= 2 {
		t.Fatalf("起点不对：一开始就该在底部，第 2 条还没进视野（可见 %d～%d）", first, last)
	}
	q := p.box(t, "quote:m59")
	x, y := q.X+q.W/2, q.Y+q.H-1
	tt.ClickAt(x, y)
	tt.Frame()
	tt.Frame()
	first, last = a.list.Visible()
	t.Logf("点引用后可见 %d～%d，闪的行 %q", first, last, a.flashID)
	if !(first <= 2 && 2 <= last) {
		t.Fatalf("点引用没有滚到第 2 条：可见 %d～%d", first, last)
	}
	if a.flashID != "m2" {
		t.Fatalf("应该闪第 2 条（m2），实际 %q", a.flashID)
	}
	// 选中态只作用于气泡；时间到期恢复普通背景。
	body := p.box(t, "body:m2:0")
	probe := func() color.RGBA { return tt.Image().RGBAAt(int(body.X-5), int(body.Y+body.H/2)) }
	lit := probe()
	tt.Advance(flashSpan + 500*time.Millisecond)
	tt.Frame()
	if after := probe(); after == lit {
		t.Fatalf("跳转高亮没有恢复：%v", after)
	}
}

// 读不到原消息：写"原消息不在本地"，不能点，点了什么也不发生
func TestQuoteGoneNotClickable(t *testing.T) {
	ms := msgsOf(t,
		view("a", "human", "甲", "没引用的话", t0),
		quoting(view("b", "human", "乙", "回一句", t0+60_000), "x", "", "", false))
	a := chatApp(ms, true)
	tt, p := chatTester(a, 900, 700)
	if !tt.HasText(quoteGone) {
		t.Fatalf("没有写 %q：%q", quoteGone, tt.Texts())
	}
	x, y := centerOf(p.box(t, "quote:b"))
	tt.ClickAt(x, y)
	tt.Frame()
	if a.flashID != "" || a.jump.id != "" || a.status != "" {
		t.Fatalf("不能点的引用块点了有反应：flash=%q jump=%q status=%q", a.flashID, a.jump.id, a.status)
	}
}

// jumpApp 一个读演示数据的 app：只加载了会话最新的 50 条，更早的在后台；回界面线程的活进队列，由测试取出来执行
func jumpApp(t *testing.T, conv string) (*app, func(want func() bool)) {
	t.Helper()
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}, started: time.Now(), demoMode: true}
	a.imgFetch = func(context.Context, api.MediaRef, int) ([]byte, error) { return nil, errNotFound }
	queue := make(chan func(), 16)
	a.postFn = func(f func()) { queue <- f }
	a.reader = demoReader{}
	k := demoKey(conv)
	a.keys = map[string]api.ConversationKey{conv: k}
	a.sel, a.title, a.group, a.list = conv, "测试", true, a.newList()
	a.firstDone = true
	res, err := a.reader.ReadMessages(context.Background(), api.ConversationRequest{Key: k, Limit: 50}, "")
	if err != nil || !res.Value.HasMore {
		t.Fatalf("演示数据读不出一页或没有更早的：%v %+v", err, res.State)
	}
	page := a.build(k, res.Value.Items, charOf)
	a.takePage(page.msgs, page.gone, false)
	a.cursor, a.hasMore = res.Value.NextCursor, res.Value.HasMore
	pump := func(want func() bool) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for !want() {
			select {
			case f := <-queue:
				f()
			case <-deadline:
				t.Fatalf("等了 20 秒还没好：status=%q jump=%+v loading=%v", a.status, a.jump, a.loading)
			}
		}
	}
	return a, pump
}

// 原消息在更早的几页里：点一下，往上翻页（一次 500 条）找到再跳。800 条的会话，第 10 条要翻两页才到
func TestQuoteJumpLoadsOlder(t *testing.T) {
	t.Parallel()
	const conv = "qq:group:1002"
	a, pump := jumpApp(t, conv)
	oldID := conv + "#10"
	if a.indexOf(oldID) >= 0 {
		t.Fatalf("起点不对：%s 不该在第一页里", oldID)
	}
	a.jumpTo(oldID)
	if a.jump.id != oldID || !a.loading || a.jump.left != jumpPages-1 {
		t.Fatalf("点引用后应开始往上翻：jump=%+v loading=%v", a.jump, a.loading)
	}
	pump(func() bool { return a.jump.id == "" && !a.loading })
	n := len(a.msgs)
	if a.indexOf(oldID) < 0 || a.flashID != oldID || a.status != "" {
		t.Fatalf("翻页以后应找到并闪它：index=%d flash=%q status=%q", a.indexOf(oldID), a.flashID, a.status)
	}
	if n != 800 || a.hasMore {
		t.Fatalf("800 条翻了两页应全部加载完：%d 条，hasMore=%v", n, a.hasMore)
	}
	// 一条不存在的：翻到头了照实写
	a.flashID = ""
	a.jumpTo(conv + "#99999")
	pump(func() bool { return a.jump.id == "" && !a.loading })
	if a.flashID != "" || !strings.Contains(a.status, "不在已有的记录里") {
		t.Fatalf("找不到应写明：flash=%q status=%q", a.flashID, a.status)
	}
}

// 翻了 jumpPages 页还没找到就停下，写明翻了多少、可以继续点加载更早；不一直翻下去
func TestQuoteJumpStopsAfterPages(t *testing.T) {
	t.Parallel()
	const conv = "qq:group:1001" // 一万条
	a, pump := jumpApp(t, conv)
	a.jumpTo(conv + "#5")
	pump(func() bool { return a.jump.id == "" && !a.loading })
	want := 50 + jumpPages*500
	if len(a.msgs) != want || !a.hasMore || a.flashID != "" || !strings.Contains(a.status, fmt.Sprint(jumpPages*500)) {
		t.Fatalf("应翻 %d 页停下（共 %d 条）：实际 %d 条 hasMore=%v flash=%q status=%q", jumpPages, want, len(a.msgs), a.hasMore, a.flashID, a.status)
	}
	// 换了会话，没翻完的找法作废
	a.jump = jumpState{id: "x", left: 3}
	a.switchTo("qq:group:1003")
	if a.jump.id != "" || a.flashID != "" {
		t.Fatalf("换会话应清掉：%+v", a.jump)
	}
}

func TestQuoteMentionNamesAcrossScenes(t *testing.T) {
	for _, conv := range []string{"console:a", "home:room", "qq:private:10003:10004", "qq:group:10005"} {
		t.Run(conv, func(t *testing.T) {
			key := demoKey(conv)
			if conv == "console:a" {
				key = api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}
			}
			segs := []api.DisplaySegment{{Kind: "at", Target: "10001", Name: "编的群名片"}, {Kind: "text", Text: " 红气泡 "}, {Kind: "at", Target: "10002"}, {Kind: "text", Text: " "}, {Kind: "at", Target: "all"}, {Kind: "text", Text: " "}, {Kind: "at", Target: "伪造名字"}, {Kind: "text", Text: " "}, {Kind: "at", Target: "0"}}
			body := "@10001 红气泡 @10002"
			want := body
			if key.Platform == "qq" {
				want = "@编的群名片 红气泡 @***02 @全体成员 @某人 @***"
			}
			v := api.MessageView{Ref: api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "reply"}}, ActorKind: "human", Text: body}
			if key.Platform == "qq" {
				v.Segments = segs
			}
			v.ReplyTo = &api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "original"}}
			v.Reply = api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{DisplayName: "编的甲", Text: body, Segments: segs}}
			m := msgFromView(key, v, charOf)
			a := chatApp([]*msg{m}, true)
			tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
			if m.quote.text != want || !tt.HasText(want) {
				t.Fatalf("引用显示=%q，要=%q；界面=%v", m.quote.text, want, tt.Texts())
			}
			last := convFromView(api.ConversationView{Key: key, Last: &v})
			if last.Last.Text != want {
				t.Fatalf("最后一句=%q", last.Last.Text)
			}
			if key.Platform == "qq" {
				if strings.Contains(strings.Join(tt.Texts(), "|"), "10001") || strings.Contains(strings.Join(tt.Texts(), "|"), "10002") {
					t.Fatal("界面仍显示完整号码")
				}
				v.Reply.Value.Segments = nil
				if got := quoteOf(v).text; got != "@***01 红气泡 @***02" {
					t.Fatalf("旧引用原文=%q", got)
				}
				forward := forwardNodeMsg(api.ForwardRequest{Source: v.Ref, ID: "outer"}, api.ForwardNode{Segments: []api.DisplaySegment{{Kind: "reply", Summary: body}}}, 0)
				if forward.quote.text != "@***01 红气泡 @***02" {
					t.Fatal(forward.quote.text)
				}
			}
		})
	}
}
