package main

// 表情回应显示的测试：合并计数、认人按号、悬停看是谁、药丸在气泡下面且不压时间。数据全是编的。

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// rv 编一条回应
func rv(id, ref, kind, actor, name string) api.ReactionView {
	return api.ReactionView{ID: id, Ref: ref, ActorKind: kind, ActorID: actor, DisplayName: name, At: t0}
}

func TestGroupReactions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []api.ReactionView
		want []string // "表情×个数:名字,名字"
	}{
		{"没有回应", nil, nil},
		{"同一个表情合并，表情按第一次出现的先后排",
			[]api.ReactionView{rv("1", "76", "human", "qq:1", "甲"), rv("2", "5", "human", "qq:2", "乙"), rv("3", "76", "human", "qq:3", "丙")},
			[]string{"76×2:甲,丙", "5×1:乙"}},
		{"同一个人把同一个表情贴两次只算一次",
			[]api.ReactionView{rv("1", "76", "human", "qq:1", "甲"), rv("2", "76", "human", "qq:1", "甲")},
			[]string{"76×1:甲"}},
		{"同一个人贴两个不同的表情，各算一次",
			[]api.ReactionView{rv("1", "76", "human", "qq:1", "甲"), rv("2", "5", "human", "qq:1", "甲")},
			[]string{"76×1:甲", "5×1:甲"}},
		{"两个人同名各算一个（认人按号不按名字）",
			[]api.ReactionView{rv("1", "76", "human", "qq:1", "路人"), rv("2", "76", "human", "qq:2", "路人")},
			[]string{"76×2:路人,路人"}},
		{"一个人换了名字，号一样还是一个人",
			[]api.ReactionView{rv("1", "76", "human", "qq:1", "旧名"), rv("2", "76", "human", "qq:1", "新名")},
			[]string{"76×1:旧名"}},
		{"没有表情编号的认不出是哪个，不算", []api.ReactionView{rv("1", "", "human", "qq:1", "甲")}, nil},
		{"名字空的退回号码，去掉 qq: 前缀；都没有写某人",
			[]api.ReactionView{rv("1", "76", "human", "qq:90001", ""), rv("2", "76", "human", "qq:90001x", "qq:90002"), rv("3", "76", "human", "", "")},
			[]string{"76×3:90001,90002,某人"}},
		{"没有号的（协议端没说是谁）每条算一个人，不并成一个",
			[]api.ReactionView{rv("1", "76", "human", "", "甲"), rv("2", "76", "human", "", "乙")},
			[]string{"76×2:甲,乙"}},
		{"同号不同身份种类（人和主人）不并",
			[]api.ReactionView{rv("1", "76", "human", "x", "甲"), rv("2", "76", "owner", "x", "主人")},
			[]string{"76×2:甲,主人"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, g := range groupReactions(c.in) {
				got = append(got, fmt.Sprintf("%s×%d:%s", g.Ref, g.Count(), strings.Join(g.Who, ",")))
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("得 %q，应为 %q", got, c.want)
			}
		})
	}
}

func TestUnicodeReaction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref  string
		want string
		ok   bool
	}{
		{"128077", "👍", true},
		{"8192", " ", true}, // 0x2000：下界，含
		{"8191", "", false}, // 0x1FFF：小黄脸的编号走不到这里
		{"76", "", false},
		{"515", "", false}, // 小黄脸编号的最大值
		{"1114111", "\U0010FFFF", true},
		{"1114112", "", false},
		{"55295", "퟿", true},
		{"55296", "", false}, // 代理区
		{"57343", "", false},
		{"-5", "", false},
		{"abc", "", false},
		{"", "", false},
		{"0x1F44D", "", false},
	}
	for _, c := range cases {
		got, ok := unicodeReaction(c.ref)
		if got != c.want || ok != c.ok {
			t.Errorf("%q：得 %q %v，应为 %q %v", c.ref, got, ok, c.want, c.ok)
		}
	}
}

func TestReactionTip(t *testing.T) {
	t.Parallel()
	if got := reactionTip([]string{"甲", "乙"}); got != "甲、乙" {
		t.Fatal(got)
	}
	var many []string
	for i := range 30 {
		many = append(many, fmt.Sprint("人", i))
	}
	got := reactionTip(many)
	if !strings.HasSuffix(got, "，等 30 人") || strings.Contains(got, "人12") || !strings.Contains(got, "人11") {
		t.Fatalf("人多了只列前 12 个：%q", got)
	}
}

// withReactions 给一条展示 DTO 贴上回应
func withReactions(v api.MessageView, rs ...api.ReactionView) api.MessageView {
	v.Reactions = rs
	return v
}

func rectsOverlap(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// 回应的药丸在气泡下面（和气泡同一边），个数对，不压时间；一行、很多行、图片气泡、有引用块各量一次，左右各一次
func TestReactionPillsBelowBubble(t *testing.T) {
	long := strings.Repeat("今天也是摸鱼的一天，有人看了昨晚那集吗，晚上一起打游戏吗。", 6)
	rs := []api.ReactionView{rv("1", "76", "human", "qq:1", "甲"), rv("2", "76", "human", "qq:2", "乙"), rv("3", "128077", "human", "qq:3", "丙")}
	cases := []struct {
		name string
		mk   func(kind string) []api.MessageView
		want string
	}{
		{"一行", func(k string) []api.MessageView {
			return []api.MessageView{withReactions(view("m", k, "某人", "好耶", t0), rs...)}
		}, "shell:m:0"},
		{"很多行", func(k string) []api.MessageView {
			return []api.MessageView{withReactions(view("m", k, "某人", long, t0), rs...)}
		}, "shell:m:0"},
		{"有引用块", func(k string) []api.MessageView {
			return []api.MessageView{view("o", "human", "对方", "原话", t0-60_000), withReactions(quoting(view("m", k, "某人", "回一句", t0), "o", "对方", "原话", true), rs...)}
		}, "shell:m:0"},
		{"只有图", func(k string) []api.MessageView {
			return []api.MessageView{withReactions(withImage(view("m", k, "某人", "", t0), "mock:1:300:200"), rs...)}
		}, "pic:m:mock:1:300:200"},
	}
	for _, c := range cases {
		for _, mine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mine=%v", c.name, mine), func(t *testing.T) {
				kind := map[bool]string{false: "human", true: "owner"}[mine]
				ms := msgsOf(t, c.mk(kind)...)
				for _, m := range ms {
					if m.id == "m" {
						m.mine, m.md = mine, false
					}
				}
				a := chatApp(ms, true)
				tt, p := chatTester(a, 900, 700)
				box := p.box(t, c.want)
				if c.name == "只有图" {
					tt.Move(box.X+box.W/2, box.Y+box.H/2)
					tt.Frame()
				}
				p76, pU := p.box(t, "react:m:76"), p.box(t, "react:m:128077")
				stamp, ok := tt.Find("12:00")
				if !ok || rectsOverlap(p76, stamp) || rectsOverlap(pU, stamp) {
					t.Fatalf("回应压时间：%+v %+v %+v", p76, pU, stamp)
				}
				if !tt.HasText(" 2") || !tt.HasText("👍 1") {
					t.Fatalf("计数或表情没画出来：%v", tt.Texts())
				}
			})
		}
	}
}

// 没有回应的消息不画药丸
func TestNoReactionsNoPills(t *testing.T) {
	a := chatApp(msgsOf(t, view("m", "human", "某人", "好", t0)), true)
	_, p := chatTester(a, 900, 700)
	for n := range p.hs {
		if strings.HasPrefix(n, "react:") {
			t.Fatalf("没有回应却有药丸 %s", n)
		}
	}
}

// 合并计数跟着数据变：后来又有人贴了同一个表情，个数加一；换了版本的行替换旧的
func TestReactionCountUpdates(t *testing.T) {
	v := withReactions(view("m", "human", "某人", "好", t0), rv("1", "76", "human", "qq:1", "甲"))
	a := chatApp(msgsOf(t, v), true)
	tt, _ := chatTester(a, 900, 700)
	if hasExact(tt, " 2") {
		t.Fatal("起点不对")
	}
	v2 := withReactions(v, rv("1", "76", "human", "qq:1", "甲"), rv("2", "76", "human", "qq:2", "乙"))
	v2.Version = 2
	a.msgs, _ = mergeLive(a.msgs, msgsOf(t, v2))
	tt.Frame()
	if !hasExact(tt, " 2") {
		t.Fatalf("多了一个人贴同一个表情，个数应变 2：%q", tt.Texts())
	}
}

// 悬停药丸：看到是谁贴的（名字照后台给的显示名）
func TestReactionHoverShowsWho(t *testing.T) {
	v := withReactions(view("m", "human", "某人", "好", t0),
		rv("1", "76", "human", "qq:1", "豆沙包"), rv("2", "76", "human", "qq:2", "半糖"), rv("3", "5", "human", "qq:3", "路过的猫"))
	a := chatApp(msgsOf(t, v), true)
	tt, p := chatTester(a, 900, 700)
	if hasExact(tt, "豆沙包、半糖") {
		t.Fatal("没悬停就不该显示")
	}
	x, y := centerOf(p.box(t, "react:m:76"))
	tt.Move(x, y)
	tt.Advance(1500 * time.Millisecond)
	tt.Frame()
	if !hasExact(tt, "豆沙包、半糖") {
		t.Fatalf("悬停以后应看到谁贴的：%q", tt.Texts())
	}
	if hasExact(tt, "路过的猫") {
		t.Fatalf("悬停的是 76，不该看到别的表情的人：%q", tt.Texts())
	}
}

// 小黄脸编号的回应：图没到时占住位置，到了药丸的宽度不变（不跳），并且真的画出了图（红色的小黄脸）
func TestReactionFaceImageNoJump(t *testing.T) {
	n := &fakeNet{tables: []map[string]faceInfo{testTable}, png: map[string][]byte{"14": facePNG(40, 40, red)}}
	ms := msgsOf(t, withReactions(view("m", "human", "某人", "好", t0), rv("1", "14", "human", "qq:1", "甲")))
	a, q := faceApp(t, n, ms...)
	f := a.fs()
	f.start()
	f.wg.Wait()
	q.drain() // 名字表到了
	tt, p := chatTester(a, 900, 700)
	before := p.box(t, "react:m:14")
	if f.state("14") != faceLoading {
		t.Fatalf("起点不对：应在取图中，实际 %d", f.state("14"))
	}
	f.wg.Wait()
	q.drain()
	tt.Frame()
	tt.Frame()
	after := p.box(t, "react:m:14")
	if f.state("14") != faceOK {
		t.Fatalf("图应已取到：%d", f.state("14"))
	}
	if math.Abs(float64(after.W-before.W)) > 0.5 || math.Abs(float64(after.H-before.H)) > 0.5 {
		t.Fatalf("图到了药丸不该变大小：前 %+v 后 %+v", before, after)
	}
	redPx := 0
	img := tt.Image()
	for y := int(after.Y); y < int(after.Y+after.H); y++ {
		for x := int(after.X); x < int(after.X+after.W); x++ {
			if c := img.RGBAAt(x, y); c.R > 200 && c.G < 60 && c.B < 60 {
				redPx++
			}
		}
	}
	t.Logf("药丸 %+v，红色像素 %d", after, redPx)
	if redPx < 150 {
		t.Fatalf("药丸里应画出 16×16 的红色小黄脸（约 256 个像素），只数到 %d", redPx)
	}
}
