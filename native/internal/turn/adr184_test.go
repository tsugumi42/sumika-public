package turn

// ADR-184 那几条（规格-渠道中间件第九节第 1、5、7 条）：句子随"回合定了"交出（Settled.Said）、渠道回合不由内核写库、
// 中间件写完库用 Commit 交回来以后实例里换成库里那一行、"对面是谁"由中间件给。对着写明的预期测

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

var groupAud = audience.Audience{Channel: "qq", Conversation: groupConv, Scene: "group", PersonIDs: []string{"p1"}}

// characterRows 是这个会话里她说的、存进库的有几条
func (h *harness) characterRows(conv string) int {
	var n int
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_kind = 'character'`, conv).Scan(&n)
	return n
}

// 第 5 条：每一步说出口的那句随回合定了交出，按先后、带步号；连同听众、场合段。主窗口的也带，提前出错的是空的
func TestSaidInSettled(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["第一步说的"],` + usage + `},{"content":["第二步说的"],` + usage + `},` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) { once.Do(func() { h.hub.Steer(h.c.CharacterID, "再说一句") }) }
		},
	})
	got := h.watchSettled()
	r := h.send("你好")
	s := settledFor(t, got, r.TurnID)
	want := []Said{{0, "第一步说的"}, {1, "第二步说的"}}
	if !reflect.DeepEqual(s.Said, want) || !reflect.DeepEqual(r.Said, want) {
		t.Fatalf("主窗口两步的句子：%+v，应该是 %+v", s.Said, want)
	}
	if !reflect.DeepEqual(s.Audience, audience.Console(consoleConv(h.c.CharacterID))) || s.Scenes != nil {
		t.Fatalf("主窗口的回合定了带的听众、场合不对：%+v %v", s.Audience, s.Scenes)
	}
	if n := h.characterRows(consoleConv(h.c.CharacterID)); n != 2 {
		t.Fatalf("主窗口照旧由内核存：她说的应该存了 2 条，实际 %d", n)
	}
	g := wait(t, h.inGroup("群里这句", []string{"scene-group"}))
	gs := settledFor(t, got, g.TurnID)
	if len(gs.Said) != 1 || gs.Said[0].Text != "好的" || !reflect.DeepEqual(gs.Audience, groupAud) || !reflect.DeepEqual(gs.Scenes, []string{"scene-group"}) {
		t.Fatalf("群里那一轮的回合定了：%+v", gs)
	}
	bad := <-h.inGroup("又一句", []string{"no-such-scene"})
	if bs := settledFor(t, got, bad.TurnID); bs.Said != nil || bs.Outcome != OutcomeError {
		t.Fatalf("提前出错的回合定了不该有句子：%+v", bs)
	}
}

// 第 1 条：渠道回合她说的不由内核写库（模型调用照记）；中间件改写、写库以后用 Commit 交回，实例里那一句换成库里那一行
func TestChannelTurnCommit(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	r := wait(t, h.inGroup("群里第一句", nil))
	if n := h.characterRows(groupConv); n != 0 {
		t.Fatalf("渠道回合她说的不该由内核写库，库里有 %d 条", n)
	}
	var runs int
	h.st.Trace.QueryRow(`SELECT count(*) FROM model_runs WHERE conversation = ?`, groupConv).Scan(&runs)
	if runs != 1 {
		t.Fatalf("模型调用照记：model_runs 应该有 1 条，实际 %d", runs)
	}
	// 交回来之前的几种错：不是这个回合、没有这一步、没有 seq，都报错、什么都不改
	if err := h.hub.Commit(h.c.CharacterID, groupConv, "no-such-turn", nil); err == nil {
		t.Fatal("没有这个回合，Commit 应该报错")
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{Step: 5, Seq: 1, Text: "x"}}); err == nil {
		t.Fatal("没有第 5 步，Commit 应该报错")
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{Step: 0, Text: "x"}}); err == nil {
		t.Fatal("没有 seq，Commit 应该报错")
	}
	lines := h.commitSaid(groupConv, r, func(string) string { return "改写以后的那句" })
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, lines); err == nil {
		t.Fatal("同一个回合交回两次，第二次应该报错")
	}
	h.hub.mu.Lock()
	done := h.hub.done[convKey(h.c.CharacterID, groupConv)][lines[0].Seq]
	h.hub.mu.Unlock()
	if !done {
		t.Fatal("交回来的那一行应该记成跑完的回合用过的")
	}
	wait(t, h.inGroup("群里第二句", nil))
	b := h.body(1)
	if strings.Count(b, "改写以后的那句") != 1 || strings.Contains(b, `"content":"好的"`) {
		t.Fatalf("下一轮的请求里应该是改写以后的那句（一次），不是原文：%s", b)
	}
	// 换成的那句在原来的位置：第一批之后、第二批之前
	m1 := strings.Join(h.messages(1), "\n")
	if a, c, d := strings.Index(m1, "群里第一句"), strings.Index(m1, "改写以后的那句"), strings.Index(m1, "群里第二句"); !(a < c && c < d) {
		t.Fatalf("换字以后那句应该在第一批和第二批之间（%d %d %d）", a, c, d)
	}
	// 别的档新建的实例从库里读，读到的也是改写以后的那一行（一次）
	seq := h.storeGroup("别的档问一句")
	wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{seq}, Text: "别的档问一句",
		Audience: groupAud, Addressed: true, Scope: "other"}))
	if c := strings.Count(h.body(2), "改写以后的那句"); c != 1 {
		t.Fatalf("别的档的实例里改写以后的那句应该出现一次，实际 %d：%s", c, h.body(2))
	}
}

// 第 1 条：只交回一部分步（别的被拦下、试运行）——没交回的从实例里拿掉
func TestCommitDropsUncommittedSteps(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["被拦下的那句"],` + usage + `},{"content":["留下的那句"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				once.Do(func() {
					h.hub.SteerIn(h.c.CharacterID, groupConv, "群里插一句", []int64{h.storeGroup("群里插一句")})
				})
			}
		},
	})
	r := wait(t, h.inGroup("群里第一句", nil))
	if len(r.Said) != 2 {
		t.Fatalf("应该说了两步：%+v", r.Said)
	}
	seq := h.storeHers("留下的那句（改写）") // 只把第二步写进库
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{Step: 1, Seq: seq, Text: "留下的那句（改写）"}}); err != nil {
		t.Fatal(err)
	}
	wait(t, h.inGroup("群里第二句", nil))
	b := h.body(2)
	if strings.Contains(b, "被拦下的那句") || strings.Count(b, "留下的那句（改写）") != 1 {
		t.Fatalf("没交回的那步应该拿掉、交回的换成改写以后的：%s", b)
	}
}

// 第 1 条：中间件没交回来（没调 Commit），下一轮开始时那几句从实例里拿掉、记一行日志——没进库的不能进历史
func TestUncommittedDroppedNextTurn(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["没进库的那句"],` + usage + `},` + textReply + `]`})
	wait(t, h.inGroup("群里第一句", nil))
	wait(t, h.inGroup("群里第二句", nil))
	if strings.Contains(h.body(1), "没进库的那句") {
		t.Fatalf("没交回来的那句不该进下一轮的请求：%s", h.body(1))
	}
	if !h.logged("没交回写库的结果") {
		t.Fatalf("应该记一行日志：%v", h.logs)
	}
}

// 第 1 条：结局不是 done（被停下）的渠道回合，中间件整轮丢掉、不会来 Commit；这几句当场拿掉，不等下一轮、不记日志
func TestStoppedChannelTurnDropped(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var h *harness
	h = newHarnessOpts(t, harnessOpts{
		hold:    1,
		replies: `[{"content":["说了一半","后半句"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			var once sync.Once
			c.OnDelta = func(string) {
				once.Do(func() {
					close(entered)
					go h.hub.StopIn(h.c.CharacterID, groupConv)
				})
			}
		},
	})
	r := wait(t, h.inGroup("群里第一句", nil))
	<-entered
	if !r.Stopped || len(r.Said) != 1 || r.Said[0].Text != "说了一半" {
		t.Fatalf("被停下的那一轮：%+v", r)
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, nil); err == nil {
		t.Fatal("被停下的回合不等交回，Commit 应该报错")
	}
	wait(t, h.inGroup("群里第二句", nil))
	if strings.Contains(h.body(1), "说了一半") || h.logged("没交回写库的结果") {
		t.Fatalf("被停下的那几句应该当场拿掉、不记日志：%s %v", h.body(1), h.logs)
	}
}

// 第 7 条："对面是谁"由中间件给：同一个实例里只给一次、变了才再给；平台群不给；主窗口不填照旧是"对面是主人"
func TestRelationFromInput(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`})
	const conv = "qq:private:p2"
	friend := "对面是小明：主人允许你私聊的朋友，不是主人。只说可以让别人知道的事，主人的私事不要说。"
	owner := "对面是主人。这里只有你们两个。"
	say := func(rel string) {
		s, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p2", Origin: "human", Kind: "message", Text: "你好", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
		if err != nil {
			t.Fatal(err)
		}
		r := wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{s}, Text: "你好",
			Audience: audience.Audience{Channel: "qq", Scene: "private", PersonIDs: []string{"p2"}}, Addressed: true, Scope: "p2", Relation: rel}))
		h.commitSaid(conv, r, nil)
	}
	say(friend)
	say(friend)
	if strings.Count(h.body(0), "对面是小明") != 1 || strings.Count(h.body(1), "对面是小明") != 1 {
		t.Fatalf("同一个实例里对面是谁只给一次（第二轮的请求里是历史里那一次）：\n%s\n%s", h.body(0), h.body(1))
	}
	say(owner)
	if !strings.Contains(h.lastUserBefore(2, "你好"), "对面是主人") {
		t.Fatalf("对面换了要再给：%s", h.body(2))
	}
	wait(t, h.inGroup("群里这句", nil))
	if strings.Contains(h.body(3), "对面是") {
		t.Fatalf("平台群不给对面是谁：%s", h.body(3))
	}
	h.send("主窗口这句")
	if !strings.Contains(h.body(4), owner) {
		t.Fatalf("主窗口不填照旧是对面是主人：%s", h.body(4))
	}
}

// 审查查出：新建实例从库里读进来的历史要登记成见过——B 档的那批排着队时 A 档跑完、交回了一句，B 档新建实例已经读到这句，
// 复用时不能再追一次
func TestNewInstanceSeenFromHistory(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["A 档那句"],` + usage + `},{"content":["B 档那句"],` + usage + `},` + textReply + `]`})
	bAud := audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"p9"}}
	// B 那一批先存进库（seq 小），A 那一批后存、先跑：B 档建实例时，A 那一批（比 B 这批晚、跑完的回合用过）已经从库里读进来了
	bSeq := h.storeGroup("B 档那一批")
	aOut := h.inGroup("A 档那一批", nil)
	bOut := h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{bSeq}, Text: "B 档那一批", Audience: bAud, Addressed: true, Scope: "b"})
	h.commitSaid(groupConv, wait(t, aOut), nil) // 排在 B 档那一轮后面做
	h.commitSaid(groupConv, wait(t, bOut), nil)
	seq := h.storeGroup("B 档再说一句")
	wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{seq}, Text: "B 档再说一句", Audience: bAud, Addressed: true, Scope: "b"}))
	for _, s := range []string{"A 档那一批", "A 档那句"} {
		if c := strings.Count(h.body(2), s); c != 1 {
			t.Fatalf("B 档复用实例时「%s」应该只出现一次，实际 %d：%s", s, c, h.body(2))
		}
	}
}

// 审查查出：乱码那一步只拿掉它自己——后面又说了真话不算乱码轮；最后一步乱码时，前面说过的话、工具结果都留着（规格第七节 A39）
func TestJunkStepOnlyDropsItself(t *testing.T) {
	t.Parallel()
	t.Run("乱码之后又说了真话", func(t *testing.T) {
		t.Parallel()
		var h *harness
		var once sync.Once
		h = newHarnessOpts(t, harnessOpts{
			replies: `[{"content":["】【"],` + usage + `},{"content":["真话"],` + usage + `}]`,
			mutate: func(c *Config) {
				c.OnDelta = func(string) { once.Do(func() { h.hub.Steer(h.c.CharacterID, "说话呀") }) }
			},
		})
		r := h.send("你好")
		if !reflect.DeepEqual(r.Said, []Said{{1, "真话"}}) {
			t.Fatalf("句子应该只有第二步的真话：%+v", r.Said)
		}
		if !strings.Contains(strings.Join(instanceTexts(h), "|"), "真话") {
			t.Fatalf("后面说的真话不该被当成乱码轮拿掉：%v", instanceTexts(h))
		}
	})
	t.Run("乱码以后插话、最后一步没说话", func(t *testing.T) {
		t.Parallel()
		// 复盘 2026-10-26 第 7 条查出原来说"标记打上不撤和撤了等价"不对：只看最后一步时，最后一步什么都没说（不是乱码）
		// 那条空回复留着；标记不撤的话收尾会把它当乱码拿掉
		var h *harness
		var once sync.Once
		h = newHarnessOpts(t, harnessOpts{
			replies: `[{"content":["】【"],` + usage + `},{"content":[],` + usage + `}]`,
			mutate: func(c *Config) {
				c.OnDelta = func(string) { once.Do(func() { h.hub.Steer(h.c.CharacterID, "说话呀") }) }
			},
		})
		h.send("你好")
		msgs := h.hub.liveOf(h.c.CharacterID).msgs
		if m := msgs[len(msgs)-1]; m.Role != "assistant" || m.Text != "" || len(m.Calls) != 0 {
			t.Fatalf("最后一步没说话不算乱码，那条空回复照旧留着：%+v", m)
		}
	})
	t.Run("说了一句又调工具最后乱码", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{
			replies: `[{"content":["我看看"],"toolCalls":[{"id":"r1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},{"content":["】【"],` + usage + `}]`,
			mutate: func(c *Config) {
				c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
					return tools.Result{Text: "列好了"}, nil
				}
			},
		})
		r := h.send("你好")
		if !reflect.DeepEqual(r.Said, []Said{{0, "我看看"}}) {
			t.Fatalf("句子应该是第一步那句：%+v", r.Said)
		}
		texts := strings.Join(instanceTexts(h), "|")
		if !strings.Contains(texts, "我看看") || !strings.Contains(texts, "列好了") {
			t.Fatalf("前面说过的话、工具结果都该留着：%v", instanceTexts(h))
		}
		last := h.hub.liveOf(h.c.CharacterID).msgs
		if m := last[len(last)-1]; m.Role == "assistant" && strings.TrimSpace(m.Text) == "" && len(m.Calls) == 0 {
			t.Fatal("末尾那条乱码回复应该拿掉")
		}
	})
}

func instanceTexts(h *harness) []string {
	var out []string
	for _, m := range h.hub.liveOf(h.c.CharacterID).msgs {
		out = append(out, m.Text)
	}
	return out
}

// 审查查出：同一步交回两次报错，什么都不改
func TestCommitDuplicateStep(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	r := wait(t, h.inGroup("群里第一句", nil))
	a, b := h.storeHers("一"), h.storeHers("二")
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, a, "一"}, {0, b, "二"}}); err == nil {
		t.Fatal("同一步交回两次应该报错")
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, a, "一"}}); err != nil {
		t.Fatalf("报错以后什么都没改，应该还能正常交回：%v", err)
	}
}

// 以库为准：交回的那一行要在库里、在这个会话、是她说出口的、字一样，不然报错、什么都不改
func TestCommitChecksDB(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	r := wait(t, h.inGroup("群里第一句", nil))
	hers := h.storeHers("库里的那句")
	other, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:g2", ActorKind: "character", ActorID: h.c.CharacterID, Origin: "character", Kind: "message", Text: "库里的那句", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Committed{
		"不在库里":    {0, hers + 100, "库里的那句"},
		"别的会话的":   {0, other, "库里的那句"},
		"群友说的":    {0, h.storeGroup("库里的那句"), "库里的那句"},
		"字和库里不一样": {0, hers, "交回时说的另一句"},
	} {
		if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{c}); err == nil {
			t.Fatalf("%s：应该报错", name)
		}
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, hers, "库里的那句"}}); err != nil {
		t.Fatalf("前面报错都没改东西，应该还能正常交回：%v", err)
	}
}

// 渠道线选的 c（规格-渠道中间件第九节第 1 条）：交回以后以库为准——接着用的实例、别的档追上来的、别的档新建的，这一回合那一段
// 逐条一样：照 seq 排（回合中途来的插话在她的句子前面）、照库里的写法（改写以后的字、插话不带前缀）；
// 插件给插话追加的参考信息排在那几句插话最早的那条前面；她的思考不带回来，和从库里重建的一样（规格-提示词前缀稳定第五节）
func TestCommitMatchesDB(t *testing.T) {
	t.Parallel()
	var h *harness
	var armed atomic.Bool
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,{"reasoning":["想了一下"],"content":["第一步"],` + usage + `},{"content":["第二步"],` + usage + `},` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				if armed.Load() {
					once.Do(func() {
						a, b := h.storeGroup("插话一"), h.storeGroup("插话二")
						h.hub.SteerIn(h.c.CharacterID, groupConv, "插话一\n插话二", []int64{a, b})
					})
				}
			}
		},
	})
	h.use(contextPlugin("recall", func(_ context.Context, in ContextIn) (*Contribution, error) {
		if in.Steer {
			return &Contribution{Lines: []string{"插话时召回的"}}, nil
		}
		return nil, nil
	}))
	ask := func(scope, text string) {
		seq := h.storeGroup(text)
		r := wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{seq}, Text: text,
			Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"p9"}}, Addressed: true, Scope: scope}))
		h.commitSaid(groupConv, r, nil)
	}
	ask("b", "B 档先说一句") // B 档先建好实例，之后靠追上来
	armed.Store(true)
	r := wait(t, h.inGroup("群里第一句", nil))
	if len(r.Said) != 2 {
		t.Fatalf("应该说了两步：%+v", r.Said)
	}
	lines := h.commitSaid(groupConv, r, func(s string) string { return s + "（改）" })
	from := lines[0].Seq - 3 // 那一批、两句插话、她的两句是连着存的
	pick := func(l *Live) []string {
		var out []string
		for _, m := range l.msgs {
			if m.Seq >= from && m.Seq <= lines[1].Seq {
				out = append(out, m.Role+"|"+m.Text)
			}
		}
		return out
	}
	reused := pick(h.liveIn(groupConv, "public"))
	if len(reused) != 5 || !strings.Contains(reused[0], "群里第一句") || !strings.Contains(reused[1], "插话一") || !strings.Contains(reused[2], "插话二") ||
		reused[3] != "assistant|第一步（改）" || reused[4] != "assistant|第二步（改）" || strings.Contains(strings.Join(reused, "\n"), steerPrefixChannel) {
		t.Fatalf("接着用的实例里这一回合应该照库排、照库写：%q", reused)
	}
	ask("b", "B 档再问一句")
	ask("c", "C 档问一句")
	if b, c := pick(h.liveIn(groupConv, "b")), pick(h.liveIn(groupConv, "c")); !reflect.DeepEqual(reused, b) || !reflect.DeepEqual(reused, c) {
		t.Fatalf("三个实例里这一回合应该逐条一样：\n接着用 %q\n追上来 %q\n新建的 %q", reused, b, c)
	}
	ms := h.liveIn(groupConv, "public").msgs
	for i, m := range ms {
		if m.Kind == KindSaid && strings.Contains(m.Text, "插话一") && !strings.Contains(ms[i-1].Text, "插话时召回的") {
			t.Fatalf("插件给插话追加的参考信息应该紧挨着排在插话一前面：%q", ms[i-1].Text)
		}
		if m.Text == "第一步（改）" && (m.Thinking != "" || m.Replay.Codec != "" || len(m.Replay.Data) > 0 || m.HasThinking) {
			t.Fatalf("交回以后她那一步的思考不该带回来（重建出来的也没有）：%q", m.Thinking)
		}
		if m.Kind == KindSaid && strings.Contains(m.Text, "群里第一句") && ms[i-1].Kind != KindContext {
			t.Fatalf("这一轮开头的参考信息不在重写的那一段里，应该留着：%q", ms[i-1].Text)
		}
	}
}

// 审查查出：交回的要是这一回合定了以后新写的那一行——以前交回过的同字句、两步共用一行都报错、什么都不改
func TestCommitRejectsStaleOrSharedSeq(t *testing.T) {
	t.Parallel()
	t.Run("以前交回过的同字句", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[{"content":["好的"],` + usage + `},{"content":["好的"],` + usage + `},` + textReply + `]`})
		first := h.commitSaid(groupConv, wait(t, h.inGroup("群里第一句", nil)), nil)
		r := wait(t, h.inGroup("群里第二句", nil))
		if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, first[0].Seq, "好的"}}); err == nil {
			t.Fatal("以前交回过的那一行不能当成这一回合的交回")
		}
		h.commitSaid(groupConv, r, nil) // 报错以后什么都没改，照常交回
		if c := strings.Count(strings.Join(instanceTextsIn(h, "public"), "|"), "好的"); c != 2 {
			t.Fatalf("两回合各一句「好的」，实例里应该两条，实际 %d", c)
		}
	})
	t.Run("两步共用一行", func(t *testing.T) {
		t.Parallel()
		var h *harness
		var once sync.Once
		h = newHarnessOpts(t, harnessOpts{
			replies: `[{"content":["第一句"],` + usage + `},{"content":["第二句"],` + usage + `},` + textReply + `]`,
			mutate: func(c *Config) {
				c.OnDelta = func(string) {
					once.Do(func() {
						h.hub.SteerIn(h.c.CharacterID, groupConv, "群里插一句", []int64{h.storeGroup("群里插一句")})
					})
				}
			},
		})
		r := wait(t, h.inGroup("群里第一句", nil))
		seq := h.storeHers("两步同字")
		if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, seq, "两步同字"}, {1, seq, "两步同字"}}); err == nil {
			t.Fatal("两步交回同一行应该报错")
		}
		h.commitSaid(groupConv, r, nil)
	})
}

// 审查查出：挂步骤记录出错时报错、实例什么都不改（原来先改了实例再挂，报错以后中间件再交一次只会拿到"没有等着写库的句子"）
func TestCommitAttachFailureChangesNothing(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	r := wait(t, h.inGroup("群里第一句", nil))
	seq := h.storeHers(r.Said[0].Text)
	if _, err := h.st.Trace.Exec(`ALTER TABLE steps RENAME TO steps_away`); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, seq, r.Said[0].Text}}); err == nil {
		t.Fatal("挂不上步骤记录应该报错")
	}
	if _, err := h.st.Trace.Exec(`ALTER TABLE steps_away RENAME TO steps`); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, r.TurnID, []Committed{{0, seq, r.Said[0].Text}}); err != nil {
		t.Fatalf("报错以后实例没改，应该还能再交一次：%v", err)
	}
}

func instanceTextsIn(h *harness, scope string) []string {
	var out []string
	for _, m := range h.liveIn(groupConv, scope).msgs {
		out = append(out, m.Text)
	}
	return out
}

// 一句都没说的渠道回合不会来 Commit：当场照库重写（那一步拿掉，不等下一轮），下一轮开始时不记"没交回"
func TestSilentChannelTurnSettled(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[{"content":[],` + usage + `},` + textReply + `]`})
	if r := wait(t, h.inGroup("群里第一句", nil)); len(r.Said) != 0 {
		t.Fatalf("这一轮应该一句都没说：%+v", r.Said)
	}
	l := h.liveIn(groupConv, "public")
	if l.pending != nil {
		t.Fatal("一句都没说的回合不等交回")
	}
	for _, m := range l.msgs {
		if m.Role == "assistant" {
			t.Fatalf("没说话的那一步应该照库拿掉：%+v", m)
		}
	}
	wait(t, h.inGroup("群里第二句", nil))
	if h.logged("没交回写库的结果") {
		t.Fatalf("一句都没说的回合不该记成没交回：%v", h.logs)
	}
}

func (h *harness) liveIn(conv, scope string) *Live {
	h.t.Helper()
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	for k, l := range h.hub.live {
		if strings.HasPrefix(k, convKey(h.c.CharacterID, conv)+"|") && l.in.Scope == scope {
			return l
		}
	}
	h.t.Fatalf("没有 %s 档的实例", scope)
	return nil
}

// 审查查出：中途出错的渠道回合（说过一句以后出错）当场拿掉，不等 Commit，也不等下一轮
func TestErroredChannelTurnDropped(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["说过的一句"],` + usage + `},{"content":[],"finish":"content_filter",` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) {
				once.Do(func() {
					h.hub.SteerIn(h.c.CharacterID, groupConv, "群里插一句", []int64{h.storeGroup("群里插一句")})
				})
			}
		},
	})
	o := <-h.inGroup("群里第一句", nil)
	if o.Err == nil || o.Result == nil || len(o.Result.Said) != 1 {
		t.Fatalf("第二步被内容过滤，这一轮应该出错、带着第一步那句：%+v", o)
	}
	if err := h.hub.Commit(h.c.CharacterID, groupConv, o.TurnID, nil); err == nil {
		t.Fatal("出错的回合不等交回，Commit 应该报错")
	}
	wait(t, h.inGroup("群里第二句", nil))
	if strings.Contains(h.body(2), "说过的一句") || h.logged("没交回写库的结果") {
		t.Fatalf("出错那一轮说的应该当场拿掉、不记日志：%s %v", h.body(2), h.logs)
	}
}

// 审查查出："没字的步不算"：只调工具、没说话的那步不进 Said，步号照样空出来
func TestSaidSkipsSilentSteps(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"r1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},{"content":["看完了"],` + usage + `}]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				return tools.Result{Text: "列好了"}, nil
			}
		},
	})
	if r := h.send("列一下"); !reflect.DeepEqual(r.Said, []Said{{1, "看完了"}}) {
		t.Fatalf("只调工具的第 0 步不该进句子：%+v", r.Said)
	}
}

// lastUserBefore 是第 i 次请求里，最后一条内容是 text 的 user 之前那一条 user（参考信息那一段）
func (h *harness) lastUserBefore(i int, text string) string {
	ms := h.messages(i)
	for k := len(ms) - 1; k > 0; k-- {
		if strings.Contains(ms[k], `"content":"`+text+`"`) {
			return ms[k-1]
		}
	}
	return ""
}
