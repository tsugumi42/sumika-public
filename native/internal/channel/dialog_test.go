package channel

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 对拍：testdata/dialog.json 是 Bun 版对话链（daemon/util/chain.ts）对编的群聊的输出（sumika-bun 仓库的 scripts/go-golden-dialog.ts 生成，2026-11-01 分仓前是这里的 testdata/gen-dialog.ts），Go 版要一样。
// 再加几条规格 2.1、2.2 里说得出名字的情形，单独写成断言，看得出每一条在测什么。

type dialogFixtureLink struct {
	Kind     string `json:"kind"`
	To       int64  `json:"to"`
	Strength string `json:"strength"`
	Why      string `json:"why"`
}

type dialogFixture struct {
	Seq       int64               `json:"seq"`
	ID        string              `json:"id"`
	Kind      string              `json:"kind"`
	Actor     string              `json:"actor"`
	Text      string              `json:"text"`
	At        int64               `json:"at"`
	ReplyTo   *string             `json:"replyTo"`
	Mentions  []string            `json:"mentions"`
	Notice    *string             `json:"notice"`
	Links     []dialogFixtureLink `json:"links"`
	Downgrade []struct {
		From int64  `json:"from"`
		Kind string `json:"kind"`
		Why  string `json:"why"`
	} `json:"downgrade"`
}

func TestDialogLinksMatchBun(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/dialog.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][]dialogFixture
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	downs := map[string]int{}
	for ci, c := range cases {
		var all []DialogEvent
		for i, f := range c {
			e := DialogEvent{Seq: f.Seq, ID: f.ID, Kind: f.Kind, ActorID: f.Actor, Text: f.Text, At: time.UnixMilli(f.At), Mentions: f.Mentions}
			if f.ReplyTo != nil {
				e.ReplyTo = *f.ReplyTo
			}
			if f.Notice != nil {
				e.Notice = *f.Notice
			}
			before := all
			if len(before) > 60 {
				before = before[len(before)-60:]
			}
			byID := func(id string) (DialogEvent, bool) {
				for _, x := range all {
					if x.ID == id {
						return x, true
					}
				}
				return DialogEvent{}, false
			}
			links, down := DialogLinksFor(e, before, byID)
			var got []dialogFixtureLink
			for _, l := range links {
				s := "weak"
				if l.Strong {
					s = "strong"
				}
				got = append(got, dialogFixtureLink{l.Kind, l.To, s, l.Why})
				kinds[l.Kind]++
			}
			if !reflect.DeepEqual(got, f.Links) && !(len(got) == 0 && len(f.Links) == 0) {
				t.Fatalf("第 %d 段第 %d 条（%q）的连接不一样：\nGo  %+v\nBun %+v", ci, i, f.Text, got, f.Links)
			}
			if len(down) != len(f.Downgrade) {
				t.Fatalf("第 %d 段第 %d 条的失效不一样：Go %+v，Bun %+v", ci, i, down, f.Downgrade)
			}
			for j, d := range down {
				w := f.Downgrade[j]
				if d.From != w.From || d.Kind != w.Kind || d.Why != w.Why {
					t.Fatalf("第 %d 段第 %d 条的失效不一样：Go %+v，Bun %+v", ci, i, d, w)
				}
				downs[d.Kind]++
			}
			all = append(all, e)
		}
	}
	// 每种连接、每种失效都出现过，对拍才算覆盖到了
	for _, k := range []string{LinkReply, LinkAt, LinkMerge, LinkWeak, LinkRecall, LinkRevise, LinkFix, LinkRepeat} {
		if kinds[k] == 0 {
			t.Errorf("对拍的样本里没有 %s 这种连接", k)
		}
	}
	if downs[LinkReply] == 0 || downs[LinkAt] == 0 {
		t.Errorf("对拍的样本里该有两种失效：%v", downs)
	}
}

// ---- 规格 2.1、2.2 里说得出名字的情形 ----

type dialogRoom struct {
	t      *testing.T
	evs    []DialogEvent
	now    time.Time
	nextID int
}

func newRoom(t *testing.T) *dialogRoom {
	return &dialogRoom{t: t, now: time.UnixMilli(1_790_000_000_000)}
}

func (r *dialogRoom) say(actor, text string, after time.Duration, mod func(*DialogEvent)) (DialogEvent, []DialogLink, []DialogDowngrade) {
	r.t.Helper()
	r.now = r.now.Add(after)
	r.nextID++
	e := DialogEvent{Seq: int64(r.nextID), ID: "e" + string(rune('a'+r.nextID)), Kind: "message", ActorID: actor, Text: text, At: r.now}
	if mod != nil {
		mod(&e)
	}
	byID := func(id string) (DialogEvent, bool) {
		for _, x := range r.evs {
			if x.ID == id {
				return x, true
			}
		}
		return DialogEvent{}, false
	}
	l, d := DialogLinksFor(e, r.evs, byID)
	r.evs = append(r.evs, e)
	return e, l, d
}

func hasLink(ls []DialogLink, kind string, to int64, strong bool) bool {
	for _, l := range ls {
		if l.Kind == kind && l.To == to && l.Strong == strong {
			return true
		}
	}
	return false
}

func TestDialogRules(t *testing.T) {
	t.Parallel()
	min := time.Minute
	t.Run("引用回复连到被引用的那条", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "今天吃什么", 0, nil)
		_, ls, _ := r.say("u2", "吃火锅", 30*time.Second, func(e *DialogEvent) { e.ReplyTo = a.ID })
		if !hasLink(ls, LinkReply, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("@ 某人（没有引用）连到那个人 5 分钟内最近的一条，超过 5 分钟不连", func(t *testing.T) {
		r := newRoom(t)
		a, _, _ := r.say("u1", "我看到一个很有意思的东西", 0, nil)
		_, ls, _ := r.say("u2", "是什么呀", time.Minute, func(e *DialogEvent) { e.Mentions = []string{"u1"} })
		if !hasLink(ls, LinkAt, a.Seq, true) {
			t.Fatalf("5 分钟内：%+v", ls)
		}
		r2 := newRoom(t)
		r2.say("u1", "我看到一个很有意思的东西", 0, nil)
		_, ls, _ = r2.say("u2", "是什么呀", 6*min, func(e *DialogEvent) { e.Mentions = []string{"u1"} })
		for _, l := range ls {
			if l.Kind == LinkAt {
				t.Fatalf("超过 5 分钟不该连：%+v", ls)
			}
		}
	})
	t.Run("@ 的那条已经被别人接过、内容又对不上：弱连接，不硬连", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "今天吃什么", 0, nil)
		r.say("u3", "吃火锅", 10*time.Second, func(e *DialogEvent) { e.ReplyTo = a.ID })
		_, ls, _ := r.say("u2", "天气真好", 20*time.Second, func(e *DialogEvent) { e.Mentions = []string{"u1"} })
		if !hasLink(ls, LinkAt, a.Seq, false) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("同一个人 10 秒内连发合成一句", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "我跟你说", 0, nil)
		_, ls, _ := r.say("u1", "昨天那个事", 5*time.Second, nil)
		if !hasLink(ls, LinkMerge, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
		_, ls, _ = r.say("u1", "后来呢", 15*time.Second, nil)
		for _, l := range ls {
			if l.Kind == LinkMerge {
				t.Fatalf("超过 10 秒不合：%+v", ls)
			}
		}
	})
	t.Run("发了又撤回：撤回通知连到被撤回的那条", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "发错了的话", 0, nil)
		n := DialogEvent{Seq: 99, ID: "n1", Kind: "notice", Notice: "recall", ReplyTo: a.ID, At: r.now.Add(5 * time.Second)}
		ls, _ := DialogLinksFor(n, r.evs, func(id string) (DialogEvent, bool) { return a, id == a.ID })
		if len(ls) != 1 || ls[0].Kind != LinkRecall || ls[0].To != a.Seq {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("撤回后重发（改了几个字）：以新的为准", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "明天八点出发吧", 0, nil)
		r.evs = append(r.evs, DialogEvent{Seq: 50, ID: "n1", Kind: "notice", Notice: "recall", ReplyTo: a.ID, At: r.now.Add(10 * time.Second)})
		_, ls, _ := r.say("u1", "明天八点出发吧！", 20*time.Second, nil)
		if !hasLink(ls, LinkRevise, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("回错了：本人说回错了，上一句的引用和 @ 失效", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "火锅好吃", 0, nil)
		_, ls, down := r.say("u1", "回错了", 10*time.Second, nil)
		if !hasLink(ls, LinkFix, a.Seq, true) || len(down) != 2 || down[0].From != a.Seq || down[0].Kind != LinkReply || down[1].Kind != LinkAt {
			t.Fatalf("%+v %+v", ls, down)
		}
	})
	t.Run("打错字后补一句：*正确的字", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "我今天去了超是", 0, nil)
		_, ls, _ := r.say("u1", "*超市", 10*time.Second, nil)
		if !hasLink(ls, LinkFix, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("@ 错人：一个人 30 秒内 @ 了别人、说的差不多，以后一个为准", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "@甲 在吗", 0, func(e *DialogEvent) { e.Mentions = []string{"u2"} })
		_, ls, down := r.say("u1", "@乙 在吗", 10*time.Second, func(e *DialogEvent) { e.Mentions = []string{"u3"} })
		if !hasLink(ls, LinkRevise, a.Seq, true) || len(down) != 1 || down[0].From != a.Seq || down[0].Kind != LinkAt {
			t.Fatalf("%+v %+v", ls, down)
		}
	})
	t.Run("复读：别人 2 分钟内说过一样的话（至少两个字），连到这一串的第一条；占位不算复读", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "笑死我了", 0, nil)
		r.say("u2", "笑死我了", 5*time.Second, nil)
		_, ls, _ := r.say("u3", "笑死我了", 5*time.Second, nil)
		if !hasLink(ls, LinkRepeat, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
		r2 := newRoom(t)
		r2.say("u1", "[表情包]", 0, nil)
		_, ls, _ = r2.say("u2", "[表情包]", 5*time.Second, nil)
		for _, l := range ls {
			if l.Kind == LinkRepeat {
				t.Fatalf("两个占位不是复读：%+v", ls)
			}
		}
	})
	t.Run("回复错了消息：引用的是一小时前的、内容对不上，却和最近的话对得上", func(t *testing.T) {
		r := newRoom(t)
		old, _, _ := r.say("u1", "周末去爬山吧", 0, nil)
		near, _, _ := r.say("u2", "看什么电影", 70*min, nil)
		_, ls, _ := r.say("u3", "看电影好", 20*time.Second, func(e *DialogEvent) { e.ReplyTo = old.ID })
		if !hasLink(ls, LinkReply, old.Seq, false) || !hasLink(ls, LinkWeak, near.Seq, false) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("补录的旧消息（seq 大、时间早）不算刚刚", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		r.say("u1", "今天吃什么", 0, nil)
		_, ls, _ := r.say("u1", "后来呢", -time.Hour, nil) // 时间在前一条之前
		for _, l := range ls {
			if l.Kind == LinkMerge {
				t.Fatalf("时间在前的不能合并：%+v", ls)
			}
		}
	})
	t.Run("@ 错人只认 30 秒内；31 秒以后的重发不算取代", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		r.say("u1", "@甲 在吗", 0, func(e *DialogEvent) { e.Mentions = []string{"u2"} })
		_, ls, down := r.say("u1", "@乙 在吗", 31*time.Second, func(e *DialogEvent) { e.Mentions = []string{"u3"} })
		for _, l := range ls {
			if l.Kind == LinkRevise {
				t.Fatalf("31 秒了不算：%+v", ls)
			}
		}
		if len(down) != 0 {
			t.Fatalf("也不该让前面的连接失效：%+v", down)
		}
	})
	t.Run("几乎一样：较短那句的相邻两字正好一半在另一句里算像，不到一半不算", func(t *testing.T) {
		t.Parallel()
		// 今天吃火锅：今天、天吃、吃火、火锅；今天吃面条：今天、天吃、吃面、面条；重合 2 / 4 = 0.5
		if !DialogSimilar("今天吃火锅", "今天吃面条") {
			t.Fatal("正好一半该算像")
		}
		// 今天吃火锅 和 今天去爬山：只有“今天”重合 1 / 4
		if DialogSimilar("今天吃火锅", "今天去爬山") {
			t.Fatal("四分之一不该算像")
		}
		if DialogSimilar("", "今天") || DialogSimilar("！！", "。。") {
			t.Fatal("去掉标点以后是空的，不算像")
		}
		if !DialogSimilar("好的！", "好的") {
			t.Fatal("去掉标点以后一样算像")
		}
	})
	t.Run("引用的是一小时前的才算很久以前：正好一小时不算", func(t *testing.T) {
		for _, c := range []struct {
			gap    time.Duration
			strong bool
		}{{60 * min, true}, {60*min + 30*time.Second, false}} {
			r := newRoom(t)
			old, _, _ := r.say("u1", "周末去爬山吧", 0, nil)
			r.say("u2", "看什么电影", c.gap-time.Minute, nil)
			_, ls, _ := r.say("u3", "看电影好", time.Minute, func(e *DialogEvent) { e.ReplyTo = old.ID })
			if !hasLink(ls, LinkReply, old.Seq, c.strong) {
				t.Fatalf("隔 %v：%+v", c.gap, ls)
			}
		}
	})
	t.Run("上一句按时间找：时间在这一条之后的不算它的上一句", func(t *testing.T) {
		t.Parallel()
		r := newRoom(t)
		a, _, _ := r.say("u1", "我跟你说", 0, nil)
		r.say("u2", "什么事", 100*time.Second, nil) // 后来补录进来的、时间在后面的一条
		// seq 最大、时间却是 5 秒（补录来的旧消息）：上一句是 a，不是时间在它之后的 u2
		_, ls, _ := r.say("u1", "昨天那个事", -95*time.Second, nil)
		if !hasLink(ls, LinkMerge, a.Seq, true) {
			t.Fatalf("%+v", ls)
		}
	})
	t.Run("MentionsOfSegs", func(t *testing.T) {
		t.Parallel()
		got := MentionsOfSegs("qq", []types.Seg{{Kind: types.SegAt, Target: "123"}, {Kind: types.SegAt, Target: types.AtAll}, {Kind: types.SegText, Text: "x"}, {Kind: types.SegAt}})
		if !reflect.DeepEqual(got, []string{"qq:123", "all"}) {
			t.Fatalf("%v", got)
		}
	})
}
