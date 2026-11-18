package main

// 第二步的测试：T-block（段 → 块）、T-场合（四种场合的左右和名字）、撤回、窗口宽度。
// 数据全是编的；不连 7720，不读 data/。

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

func describeBlock(b segview.Block) string {
	switch b.Kind {
	case segview.Inline:
		return "行内:" + b.Plain()
	case segview.Images:
		return fmt.Sprintf("图×%d", len(b.Imgs))
	case segview.StickerNote:
		return "表情块:" + b.Text
	}
	return "占位:" + b.Text
}

func descr(bs []segview.Block) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, describeBlock(b))
	}
	return out
}

func img(h string) seg {
	return segFromDisplay(api.DisplaySegment{Kind: "image", Media: &api.MediaRef{Hash: h}})
}

func displaySegs(ds ...api.DisplaySegment) []seg {
	out := make([]seg, len(ds))
	for i, d := range ds {
		out[i] = segFromDisplay(d)
	}
	return out
}

func segmentViews(segs []seg) []segview.Segment {
	if segs == nil {
		return nil
	}
	out := make([]segview.Segment, len(segs))
	for i, s := range segs {
		out[i] = segmentView(s, i)
	}
	return out
}

// ───────────── T-block ─────────────

func TestBlocksOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		segs []seg
		want []string
	}{
		{"文字、小黄脸、文字是一个行内块",
			displaySegs(api.DisplaySegment{Kind: "text", Text: "你好"}, api.DisplaySegment{Kind: "face", Ref: "14"}, api.DisplaySegment{Kind: "text", Text: "世界"}),
			[]string{"行内:你好[表情14]世界"}},
		// 旧代码先画完所有图再画文字，这里三块的顺序不变
		{"文字、图、文字是三个块",
			[]seg{segFromDisplay(api.DisplaySegment{Kind: "text", Text: "前"}), img("h1"), segFromDisplay(api.DisplaySegment{Kind: "text", Text: "后"})},
			[]string{"行内:前", "图×1", "行内:后"}},
		{"图之间只隔换行时还是一组",
			[]seg{img("h1"), segFromDisplay(api.DisplaySegment{Kind: "text", Text: "\n"}), img("h2"), segFromDisplay(api.DisplaySegment{Kind: "text", Text: " \n"}), img("h3")},
			[]string{"图×3"}},
		{"图之间隔着占位就是两组",
			[]seg{img("h1"), segFromDisplay(api.DisplaySegment{Kind: "voice"}), img("h2")},
			[]string{"图×1", "占位:[语音]", "图×1"}},
		{"二十一张图仍保持原序", manyImgs(21), []string{"图×21"}},
		{"商城表情单独成块，灰字 [名字]",
			displaySegs(api.DisplaySegment{Kind: "text", Text: "看"}, api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[挥手]"}, api.DisplaySegment{Kind: "text", Text: "吧"}),
			[]string{"行内:看", "表情块:[挥手]", "行内:吧"}},
		{"商城表情缺名字", displaySegs(api.DisplaySegment{Kind: "emoji", Ref: "market"}), []string{"表情块:[表情包]"}},
		{"@ 用名字，取不到打码，全体成员单独写",
			displaySegs(api.DisplaySegment{Kind: "at", Target: "1001", Name: "豆沙包"}, api.DisplaySegment{Kind: "at", Target: "7"}, api.DisplaySegment{Kind: "at", Target: "all"}, api.DisplaySegment{Kind: "text", Text: "来"}),
			[]string{"行内:@豆沙包@***@全体成员来"}},
		{"小黄脸：空编号、超级表情",
			displaySegs(api.DisplaySegment{Kind: "face"}, api.DisplaySegment{Kind: "superface", Ref: "333"}),
			[]string{"行内:[表情][超级表情333]"}},
		{"没下载下来的图", displaySegs(api.DisplaySegment{Kind: "image", MediaState: "too-big"}, api.DisplaySegment{Kind: "image", MediaState: "expired"}, api.DisplaySegment{Kind: "image", MediaState: "failed"}),
			[]string{"占位:[图片太大，没有下载]", "占位:[图片已过期]", "占位:[图片没下载下来]"}},
		{"引用这一步不画", displaySegs(api.DisplaySegment{Kind: "reply", Ref: "x"}, api.DisplaySegment{Kind: "text", Text: "好"}), []string{"行内:好"}},
		{"只有空白的文字没有块", displaySegs(api.DisplaySegment{Kind: "text", Text: " \n "}), []string{}},
		{"行内块两头的空白去掉，中间的留着", displaySegs(api.DisplaySegment{Kind: "text", Text: "\n 你\n好 \n"}), []string{"行内:你\n好"}},
		{"没有段", nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := descr(segview.BlocksOf(segmentViews(c.segs))); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("得 %q，应为 %q", got, c.want)
			}
		})
	}
}

func manyImgs(n int) []seg {
	var out []seg
	for i := range n {
		out = append(out, img(fmt.Sprint("h", i)))
	}
	return out
}

func TestBlocksOfBoldAt(t *testing.T) {
	t.Parallel()
	bs := segview.BlocksOf(segmentViews(displaySegs(api.DisplaySegment{Kind: "text", Text: "嗨 "}, api.DisplaySegment{Kind: "at", Target: "9"}, api.DisplaySegment{Kind: "text", Text: " 来"})))
	want := []segview.Run{{Text: "嗨 "}, {Text: "@***", Bold: true}, {Text: " 来"}}
	if len(bs) != 1 || !reflect.DeepEqual(bs[0].Runs, want) {
		t.Fatalf("得 %+v，应为 %+v", bs, want)
	}
}

// 没有段（本地消息、老数据）和解析失败都按 text 显示；有段时显示只认段，不看 text 列
func TestDocOf(t *testing.T) {
	t.Parallel()
	key := "qq:private:1"
	ch := func(string) (character, bool) { return character{}, false }
	local := buildMsg(key, event{ID: "a", Text: "纯文字", CreatedAt: 1}, ch, nil)
	if got := descr(docOf(local)); !reflect.DeepEqual(got, []string{"行内:纯文字"}) {
		t.Fatalf("没有段：%q", got)
	}
	bad := buildMsg(key, event{ID: "b", Text: "退回来的字", SegmentsJSON: strp(`{"segs":[{"type":"text","text":`)}, ch, nil)
	if got := descr(docOf(bad)); !reflect.DeepEqual(got, []string{"行内:退回来的字"}) {
		t.Fatalf("解析失败：%q", got)
	}
	// events.text 把小黄脸写成 [表情]，显示不能读它
	seg := buildMsg(key, event{ID: "c", Text: "你好[表情]", SegmentsJSON: strp(`{"segs":[{"type":"text","text":"你好"},{"type":"face","id":"14"}]}`)}, ch, nil)
	if got := descr(docOf(seg)); !reflect.DeepEqual(got, []string{"行内:你好[表情14]"}) {
		t.Fatalf("有段：%q", got)
	}
}

// ───────────── 状态小字、撤回 ─────────────

func TestNotesOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		m        msg
		recalled bool
		want     []string
	}{
		{"没有标记", msg{}, false, nil},
		{"没发出去", msg{unsent: "send failed"}, false, []string{"没发出去"}},
		{"unsent 的原因含已撤回", msg{unsent: "自检：已撤回"}, false, []string{"已撤回"}},
		{"partial 时不写没发出去", msg{unsent: "timeout", partial: true}, false, nil},
		{"partial 但已撤回还是写已撤回", msg{unsent: "自检：已撤回", partial: true}, false, []string{"已撤回"}},
		{"被打断", msg{aborted: true}, false, []string{"被打断"}},
		{"撤回通知指向它", msg{}, true, []string{"已撤回"}},
		{"撤回通知加 unsent 只写一次", msg{unsent: "自检：已撤回"}, true, []string{"已撤回"}},
		{"被打断又被撤回", msg{aborted: true}, true, []string{"被打断", "已撤回"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notesOf(&c.m, c.recalled); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("得 %q，应为 %q", got, c.want)
			}
		})
	}
}

// 本地行只有根上的 aborted、没有 segs，也要带上标记（规格场合表：本地私聊）
func TestBuildMsgLocalAborted(t *testing.T) {
	t.Parallel()
	ch := func(id string) (character, bool) { return character{ID: id, Name: "小葵"}, true }
	m := buildMsg("local:aoi", event{ID: "l1", Origin: "character", ActorID: "aoi", Text: "说到一半", SegmentsJSON: strp(`{"aborted":true}`), CreatedAt: 5}, ch, nil)
	if !m.aborted || !m.md || len(m.segs) != 0 {
		t.Fatalf("%+v", m)
	}
	if got := notesOf(m, false); !reflect.DeepEqual(got, []string{"被打断"}) {
		t.Fatalf("小字 %q", got)
	}
	if got := descr(docOf(m)); !reflect.DeepEqual(got, []string{"行内:说到一半"}) {
		t.Fatalf("正文 %q", got)
	}
}

func notice(id, replyTo, kind string) event {
	return event{ID: id, Kind: "notice", ReplyTo: replyTo, SegmentsJSON: strp(fmt.Sprintf(`{"notice":%q}`, kind))}
}

// 撤回要同时满足：kind=notice、有 reply_to、根上的 notice 含 recall。贴表情、戳一戳的通知也有 reply_to，不能算撤回
func TestRecalledIDs(t *testing.T) {
	t.Parallel()
	es := []event{
		notice("n1", "m1", "friend_recall"),
		notice("n2", "m2", "group_recall"),
		notice("n3", "m3", "group_msg_emoji_like"),                                            // 贴表情
		{ID: "n4", Kind: "notice", SegmentsJSON: strp(`{"notice":"group_recall"}`)},           // 没有 reply_to（原消息没找到）
		{ID: "m5", Kind: "message", ReplyTo: "m1", SegmentsJSON: strp(`{"notice":"recall"}`)}, // 消息的 reply_to 是引用
		{ID: "n6", Kind: "notice", ReplyTo: "m6"},                                             // 没有段
		{ID: "n7", Kind: "notice", ReplyTo: "m7", SegmentsJSON: strp(`{"notice":`)},           // 解析失败
	}
	got := recalledIDs(es)
	if !reflect.DeepEqual(got, map[string]bool{"m1": true, "m2": true}) {
		t.Fatalf("得 %v", got)
	}
	if recalledIDs(nil) != nil {
		t.Fatal("没有通知时应当没有表")
	}
}

// ⑬ 乱序：通知所在的新页先落地，被撤回的原消息在后来取到的老页里，也要显示已撤回；换会话后清空
func TestRecalledAcrossPages(t *testing.T) {
	t.Parallel()
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}}
	newPage := []*msg{{id: "m9", seq: 9, actor: "p1"}}
	a.takePage(newPage, recalledIDs([]event{notice("n10", "m3", "group_recall")}), false)
	oldPage := []*msg{{id: "m3", seq: 3, actor: "p1"}, {id: "m4", seq: 4, actor: "p1"}}
	a.takePage(oldPage, nil, true)
	var order []string
	for _, m := range a.msgs {
		order = append(order, m.id)
	}
	if !reflect.DeepEqual(order, []string{"m3", "m4", "m9"}) {
		t.Fatalf("顺序 %v", order)
	}
	if got := notesOf(a.msgs[0], a.recalled[a.msgs[0].id]); !reflect.DeepEqual(got, []string{"已撤回"}) {
		t.Fatalf("老页里的原消息：%q", got)
	}
	if got := notesOf(a.msgs[1], a.recalled[a.msgs[1].id]); got != nil {
		t.Fatalf("没被撤回的不该有小字：%q", got)
	}
}

// ───────────── T-场合 ─────────────

var testChars = map[string]character{
	"aoi":  {ID: "aoi", Name: "小葵", Color: "#5fb7c2"},
	"sumi": {ID: "sumi", Name: "阿澄", Color: "#6a9fd8"},
}

func charOf(id string) (character, bool) { c, ok := testChars[id]; return c, ok }

func TestPlaces(t *testing.T) {
	t.Parallel()
	people := map[string]person{"qq:42": {Name: "豆沙包"}, "qq:43": {Name: "半糖"}}
	ev := func(kind, origin, actor string) event {
		return event{ID: "e", ActorKind: kind, Origin: origin, ActorID: actor, Text: "嗯", CreatedAt: 1}
	}
	cases := []struct {
		name      string
		key       string
		e         event
		mine      bool
		md        bool
		who       string
		group     bool
		several   bool
		wantName  bool // 一组的第一条上面写不写名字
		wantNameT string
	}{
		{"QQ 私聊·她说的在右", "qq:private:1", ev("character", "character", "aoi"), true, true, "小葵", false, false, false, ""},
		{"QQ 私聊·对方在左", "qq:private:1", ev("human", "member", "qq:42"), false, false, "豆沙包", false, false, false, ""},
		{"QQ 私聊·主人在左（不是原型的右）", "qq:private:1", ev("owner", "owner", "owner"), false, false, "", false, false, false, ""},
		{"QQ 私聊·她的号发的但不是她说的也在右", "qq:private:1", ev("character", "human", "aoi"), true, false, "小葵", false, false, false, ""},
		{"QQ 群·群友在左写名字", "qq:group:1", ev("human", "member", "qq:42"), false, false, "豆沙包", true, false, true, "豆沙包"},
		{"QQ 群·一个角色在右不写名字", "qq:group:1", ev("character", "character", "aoi"), true, true, "小葵", true, false, false, ""},
		{"QQ 群·两个角色在右写名字", "qq:group:1", ev("character", "character", "sumi"), true, true, "阿澄", true, true, true, "阿澄"},
		{"QQ 群·主人在左", "qq:group:1", ev("owner", "owner", "owner"), false, false, "", true, false, false, ""}, // 名字空：没有可写的
		{"本地私聊·主人在右", "local:aoi", ev("owner", "owner", "owner"), true, false, "", false, false, false, ""},
		{"本地私聊·角色在左", "local:aoi", ev("", "character", ""), false, true, "小葵", false, false, false, ""},
		{"本地群聊·主人在右不写名字", "home:cabin", ev("owner", "owner", "owner"), true, false, "", true, true, false, ""},
		{"本地群聊·角色在左写名字", "home:cabin", ev("character", "character", "sumi"), false, true, "阿澄", true, true, true, "阿澄"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := buildMsg(c.key, c.e, charOf, people)
			if m.mine != c.mine || m.md != c.md || m.who != c.who {
				t.Fatalf("mine=%v md=%v who=%q，应为 %v %v %q", m.mine, m.md, m.who, c.mine, c.md, c.who)
			}
			show := m.who != "" && showName(c.group, m.mine, m.isChar, c.several, true)
			if show != c.wantName {
				t.Fatalf("写名字 %v，应为 %v", show, c.wantName)
			}
			if showName(c.group, m.mine, m.isChar, c.several, false) {
				t.Fatal("不是一组的第一条不写名字")
			}
		})
	}
}

// 同一个角色的本地私聊和 QQ 私聊、同一个群里的两个角色：不混在一起
func TestSameCharacterTwoPlaces(t *testing.T) {
	t.Parallel()
	loc := buildMsg("local:aoi", event{ID: "a", Origin: "character", ActorKind: "character", ActorID: "aoi", Text: "x", CreatedAt: 1}, charOf, nil)
	qq := buildMsg("qq:private:1", event{ID: "b", Origin: "character", ActorKind: "character", ActorID: "aoi", Text: "x", CreatedAt: 2}, charOf, nil)
	if loc.mine || !qq.mine {
		t.Fatalf("同一个角色：本地在左（%v）、QQ 在右（%v）", !loc.mine, qq.mine)
	}
	a := buildMsg("qq:group:1", event{ID: "a", Origin: "character", ActorKind: "character", ActorID: "aoi", Text: "x", CreatedAt: 1}, charOf, nil)
	b := buildMsg("qq:group:1", event{ID: "b", Origin: "character", ActorKind: "character", ActorID: "sumi", Text: "x", CreatedAt: 2}, charOf, nil)
	if sameRun(a, b) {
		t.Fatal("两个角色不是同一组")
	}
	// 两个人显示名一样（或都没有名字）也不是同一组：按 id 分
	x := &msg{actor: "qq:1", who: "路人", at: 0}
	y := &msg{actor: "qq:2", who: "路人", at: 1000}
	if sameRun(x, y) {
		t.Fatal("同名的两个人不是同一组")
	}
	z := &msg{actor: "qq:1", at: 5*60_000 + 1}
	if sameRun(x, z) {
		t.Fatal("隔了 5 分钟以上另起一组")
	}
	if !sameRun(x, &msg{actor: "qq:1", at: 5*60_000 - 1}) {
		t.Fatal("5 分钟内同一个人是一组")
	}
}

// 说过话的角色数：翻页并起来，换会话清空
func TestSpeakers(t *testing.T) {
	t.Parallel()
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}}
	a.takePage([]*msg{{id: "1", isChar: true, actor: "aoi"}, {id: "2", actor: "p1"}}, nil, false)
	if len(a.speakers) != 1 {
		t.Fatalf("得 %v", a.speakers)
	}
	a.takePage([]*msg{{id: "0", isChar: true, actor: "sumi"}}, nil, true)
	if len(a.speakers) != 2 {
		t.Fatalf("更早的页里还有一个角色：%v", a.speakers)
	}
}

// ───────────── 渲染测试的共用视图 ─────────────

func testerOf(w, h int, view func(c *ui.Context)) *ui.Tester {
	tt := ui.NewTester(view, w, h)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true}) // 入场动画直接到位
	tt.Frame()
	return tt
}

func page(c *ui.Context, children func()) {
	ui.Column(c).Fill().Padding(20).Background(c.Theme().Background).Children(children)
}

// ───────────── 窗口宽度、按原序 ─────────────

// 取一个只含聊天列表的视图。图一律"取不到"（不连后台；没有窗口，回界面线程的活被丢掉，图停在加载中的样子）
// chatApp 渲染测试用的 app：照原型的布局（演示模式，带输入框）；正式数据源和本地收发另测。
func chatApp(msgs []*msg, group bool) *app {
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}, started: time.Now(), demoMode: true}
	a.imgFetch = func(context.Context, api.MediaRef, int) ([]byte, error) { return nil, errNotFound }
	a.msgs, a.group, a.title = msgs, group, "测试"
	a.list = ui.ListState{FollowEnd: true}
	for _, m := range msgs {
		if m.at == 0 {
			m.at = time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local).UnixMilli()
		}
	}
	return a
}

func msgFrom(key string, e event, people map[string]person) *msg {
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local).UnixMilli()
	}
	return buildMsg(key, e, charOf, people)
}

// 规格 T-block 的顺序：文字、图、文字按原序排。图占位的高度来自存好的宽高（240×120），后一段文字必须在它下面
func TestOrderOnScreen(t *testing.T) {
	seg := `{"segs":[{"type":"text","text":"前面的话"},{"type":"image","hash":"h1","width":240,"height":120},{"type":"text","text":"后面的话"},{"type":"record"},{"type":"text","text":"最后一句"}]}`
	m := msgFrom("qq:private:1", event{ID: "a", ActorKind: "human", ActorID: "qq:42", Text: "前面的话[图片]后面的话", SegmentsJSON: &seg}, map[string]person{"qq:42": {Name: "豆沙包"}})
	a := chatApp([]*msg{m}, false)
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	r1, ok1 := tt.Find("前面的话")
	r2, ok2 := tt.Find("后面的话")
	r3, ok3 := tt.Find("[语音]")
	r4, ok4 := tt.Find("最后一句")
	if !(ok1 && ok2 && ok3 && ok4) {
		t.Fatalf("文字没画出来：%q", tt.Texts())
	}
	if !(r2.Y >= r1.Y+r1.H+120 && r3.Y > r2.Y && r4.Y > r3.Y) {
		t.Fatalf("顺序不对：前 %+v 后 %+v 语音 %+v 最后 %+v", r1, r2, r3, r4)
	}
}

// 窄窗口（规格 6.2 第 5 条的 821、845、900）：气泡不伸出窗口，文字都在，撤回小字在
func TestNarrowWindows(t *testing.T) {
	long := strings.Repeat("今天也是摸鱼的一天，", 12)
	seg := `{"segs":[{"type":"text","text":"` + long + `"},{"type":"face","id":"14"},{"type":"at","qq":"42"}],"unsent":"自检：已撤回"}`
	for _, w := range []int{821, 845, 900} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			people := map[string]person{"qq:42": {Name: "豆沙包"}}
			mine := msgFrom("qq:group:1", event{ID: "a", Origin: "character", ActorKind: "character", ActorID: "aoi", SegmentsJSON: &seg}, people)
			other := msgFrom("qq:group:1", event{ID: "b", ActorKind: "human", ActorID: "qq:42", Text: "短的", CreatedAt: 1}, people)
			other.at = mine.at + 60_000
			a := chatApp([]*msg{mine, other}, true)
			tt := testerOf(w, 900, func(c *ui.Context) { a.chatView(c) })
			if !tt.HasText("已撤回") || !tt.HasText("@豆沙包") || !tt.HasText("[表情14]") {
				t.Fatalf("内容缺了：%q", tt.Texts())
			}
			for _, s := range tt.Texts() {
				if r, ok := tt.Find(s); ok && r.X+r.W > float32(w)+0.5 {
					t.Fatalf("%q 伸出了窗口：%+v（宽 %d）", s, r, w)
				}
			}
		})
	}
}

// 角色回复的 Markdown、图片和占位保持原序，时间占位不属于正文。
func TestMarkdownReplyKeepsOrder(t *testing.T) {
	seg := `{"segs":[{"type":"text","text":"先说这个"},{"type":"image","hash":"h9","width":240,"height":120},{"type":"text","text":"再说那个"}]}`
	m := msgFrom("qq:private:1", event{ID: "a", Origin: "character", ActorKind: "character", ActorID: "aoi", SegmentsJSON: &seg}, nil)
	if !m.md || !m.mine {
		t.Fatalf("应是她说的、在右：%+v", m)
	}
	a := chatApp([]*msg{m}, false)
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	r1, ok1 := tt.Find("先说这个")
	r2, ok2 := tt.Find("再说那个￼")
	if !ok1 || !ok2 || r2.Y < r1.Y+r1.H+120 {
		t.Fatalf("顺序不对：%v %v %+v %+v", ok1, ok2, r1, r2)
	}
}

// 模拟数据里四种场合各取一页，整页画一遍：不 panic、有字。别的场合没有的段（撤回、占位）由上面的用例管
func TestMockPlacesRender(t *testing.T) {
	for _, key := range []string{"local:aoi", "home:cabin", "qq:group:1001", "qq:private:2001"} {
		t.Run(key, func(t *testing.T) {
			var es []event
			var people map[string]person
			var group bool
			if strings.HasPrefix(key, "local:") {
				es, _ = mockLocal(strings.TrimPrefix(key, "local:"), 60, 0)
			} else {
				p, err := mockQQ(key, 60, 0)
				if err != nil {
					t.Fatal(err)
				}
				es, people, group = p.Events, p.People, p.Kind == "group"
			}
			var msgs []*msg
			for _, e := range es {
				msgs = append(msgs, buildMsg(key, e, charOf, people))
			}
			a := chatApp(msgs, group)
			a.takePage(msgs, nil, false)
			tt := testerOf(1000, 800, func(c *ui.Context) { a.chatView(c) })
			if len(tt.Texts()) < 5 {
				t.Fatalf("几乎什么都没画：%q", tt.Texts())
			}
		})
	}
}

func TestMarkdownExpandSurvivesRowRecycle(t *testing.T) {
	v := view("md", "character", "小葵", ">^ "+strings.Repeat("很长的编造引用内容，", 60), t0)
	m := msgFromView(v.Ref.Conversation, v, charOf)
	a := chatApp([]*msg{m}, false)
	p := newProbes(a)
	visible := true
	tt := testerOf(600, 800, func(c *ui.Context) {
		p.ctx = c
		if visible {
			a.messageBody(c, a.msgs[0], true, true, time.UnixMilli(t0))
		}
	})
	before := p.box(t, "body:md:0").H
	click(t, tt, "展开引用")
	expanded := p.box(t, "body:md:0").H
	if expanded <= before {
		t.Fatalf("引用没有展开：%.2f→%.2f", before, expanded)
	}
	visible = false
	tt.Frame()
	visible = true
	v.Version = 2
	a.msgs, _ = mergeLive(a.msgs, []*msg{msgFromView(v.Ref.Conversation, v, charOf)})
	tt.Frame()
	if after := p.box(t, "body:md:0").H; !approx(after, expanded, 0.5) {
		t.Fatalf("虚拟行重挂或版本更新丢失展开：%.2f→%.2f", expanded, after)
	}
}
