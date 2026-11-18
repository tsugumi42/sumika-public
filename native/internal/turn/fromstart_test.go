package turn

// P2-9 第 1 步：本地私聊和主人 QQ 私聊从上下文起点重建（规格-私聊从上下文起点重建 第一、二、三、六、七条；验收 1～5、8）。
// 群聊的锚定窗口不动，原有前缀契约测试照旧；这里只钉新场合。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/memory"
	"sumika-native/internal/runstate"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

const ownerQQConv = "qq:private:1001:2002"

func qqOwnerAud() audience.Audience {
	return audience.Audience{Channel: "qq", Conversation: ownerQQConv, Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true, OwnerAsked: true}
}

// newStartHarness 用一个能往前拨的钟，闲置 61 分钟重建（写法照 prefix_report_test.go 的 prefixClock）
func newStartHarness(t *testing.T, o harnessOpts) (*harness, *prefixClock) {
	t.Helper()
	clock := &prefixClock{}
	inner := o.mutate
	o.mutate = func(c *Config) {
		clock.now = c.Now()
		c.Now = clock.Now
		if inner != nil {
			inner(c)
		}
	}
	return newHarnessOpts(t, o), clock
}

func (h *harness) storeConv(conv, channel, actorKind, actorID, text string) int64 {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: channel, Conversation: conv, ActorKind: actorKind, ActorID: actorID,
		Origin: actorKind, Kind: "message", Text: text, CreatedAt: h.hub.Cfg.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return seq
}

// seedLocal 往本地私聊里放 n 条算数的消息（主人和她交替）
func (h *harness) seedLocal(n int) {
	h.t.Helper()
	conv := consoleConv(h.c.CharacterID)
	for i := range n {
		kind, id := "owner", "owner"
		if i%2 == 1 {
			kind, id = "character", h.c.CharacterID
		}
		h.storeConv(conv, "console", kind, id, fmt.Sprintf("旧话 %d", i))
	}
}

// seedQQ 往主人 QQ 私聊里放 n 条算数的消息（主人和她交替）
func (h *harness) seedQQ(n int) {
	h.t.Helper()
	for i := range n {
		kind, id := "owner", "owner"
		if i%2 == 1 {
			kind, id = "character", h.c.CharacterID
		}
		h.storeConv(ownerQQConv, "qq", kind, id, fmt.Sprintf("旧话 %d", i))
	}
}

// seedQQRounds 往主人 QQ 私聊里放 n 轮长话（每句 300 个字），第一轮是"甲"
func (h *harness) seedQQRounds(n int) {
	h.t.Helper()
	for i := range n {
		ch := string([]rune("甲乙丙丁戊己庚辛壬癸")[i%10])
		h.storeConv(ownerQQConv, "qq", "owner", "owner", strings.Repeat(ch, 300))
		h.storeConv(ownerQQConv, "qq", "character", h.c.CharacterID, strings.Repeat(ch, 300))
	}
}

// ownerQQ 是主人在 QQ 私聊里说了一句：存进库、请她回、按渠道中间件交回
func (h *harness) ownerQQ(text string) *Result {
	h.t.Helper()
	seq := h.storeConv(ownerQQConv, "qq", "owner", "owner", text)
	aud := qqOwnerAud()
	o := <-h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: ownerQQConv, Seqs: []int64{seq}, Text: text,
		Audience: aud, Addressed: true, Scope: memory.ScopeKey(aud)})
	if o.Err != nil {
		h.t.Fatal(o.Err)
	}
	h.commitSaid(ownerQQConv, o.Result, nil)
	return o.Result
}

type startTrace struct {
	Reason   string   `json:"reason"`
	Also     []string `json:"also"`
	Dropped  int      `json:"dropped"`
	Kept     int      `json:"kept"`
	Snapshot bool     `json:"snapshot"`
}

func (t startTrace) has(reason string) bool {
	return t.Reason == reason || slices.Contains(t.Also, reason)
}

func (h *harness) lastTrace(t *testing.T) startTrace {
	t.Helper()
	var cj string
	if err := h.st.Trace.QueryRow(`SELECT coalesce(context_json, '') FROM model_runs WHERE stage = 'reply' ORDER BY rowid DESC LIMIT 1`).Scan(&cj); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Rebuild startTrace `json:"rebuild"`
	}
	if err := json.Unmarshal([]byte(cj), &v); err != nil {
		t.Fatal(err)
	}
	return v.Rebuild
}

func bodyHasAll(t *testing.T, body string, n int, prefix string) {
	t.Helper()
	for i := range n {
		if !strings.Contains(body, fmt.Sprintf("%s %d", prefix, i)) {
			t.Fatalf("重建后的请求里少了第 %d 条：%s", i, body)
		}
	}
}

// 场合判断：本地私聊和主人 QQ 私聊从起点；群、别人的 QQ 私聊、话题子会话、自检、悄悄话、线、子任务照旧
func TestRebuildFromStartScenes(t *testing.T) {
	t.Parallel()
	c := "made-char"
	console := "console:" + c
	topic := console + ":topic:t1"
	selftest := audience.SelftestConversation(c, audience.SelftestTopicID(c, ""))
	whisper := audience.WhisperConversation(c, "qq:group:1")
	thread := audience.ThreadConversation(c, "t1")
	localGroup := "console:group:made-name"
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"本地私聊", Input{CharacterID: c, Conversation: console, Audience: audience.Console(console)}, true},
		{"主人QQ私聊", Input{CharacterID: c, Conversation: ownerQQConv, Audience: qqOwnerAud()}, true},
		{"QQ群", Input{CharacterID: c, Conversation: "qq:group:1", Audience: audience.Audience{Channel: "qq", Conversation: "qq:group:1", Scene: "group"}}, false},
		{"本地群聊", Input{CharacterID: c, Conversation: console + ":group:g1", Audience: audience.Audience{Channel: "console", Conversation: console + ":group:g1", Scene: "group", PrivateOK: true}}, false},
		{"生产形状的本地群聊", Input{CharacterID: c, Conversation: localGroup, Audience: audience.Audience{Channel: "console", Conversation: localGroup, Scene: "group", PrivateOK: true}}, false},
		{"本地群聊但 Scene 为空", Input{CharacterID: c, Conversation: localGroup, Audience: audience.Audience{Channel: "console", Conversation: localGroup, PrivateOK: true}}, false},
		{"别人的QQ私聊", Input{CharacterID: c, Conversation: ownerQQConv, Audience: audience.Audience{Channel: "qq", Conversation: ownerQQConv, Scene: "private", PersonIDs: []string{"stranger"}}}, false},
		{"话题子会话", Input{CharacterID: c, Conversation: topic, Audience: audience.Console(topic)}, false},
		{"话题没有结尾冒号", Input{CharacterID: c, Conversation: console + ":topic", Audience: audience.Console(console + ":topic")}, false},
		{"自检", Input{CharacterID: c, Conversation: selftest, Audience: audience.Console(selftest)}, false},
		{"悄悄话", Input{CharacterID: c, Conversation: whisper, Audience: audience.Console(whisper)}, false},
		{"格式不合格的悄悄话", Input{CharacterID: c, Conversation: console + ":whisper:abc", Audience: audience.Console(console + ":whisper:abc")}, false},
		{"群标识没有名字", Input{CharacterID: c, Conversation: console + ":group", Audience: audience.Console(console + ":group")}, false},
		{"主动联系线", Input{CharacterID: c, Conversation: thread, Audience: audience.Thread(thread)}, false},
		{"子任务线程", Input{CharacterID: c, Conversation: console, Audience: audience.Console(console), Identity: kernel.Invocation{Kind: kernel.KindChild}}, false},
	}
	for _, tc := range cases {
		if got := rebuildFromStart(Config{}, tc.in); got != tc.want {
			t.Errorf("%s：rebuildFromStart=%v，应该 %v", tc.name, got, tc.want)
		}
	}
	// 评测开关：同一格场合关掉从起点读
	if rebuildFromStart(Config{NoStartRebuild: true}, Input{CharacterID: c, Conversation: console, Audience: audience.Console(console)}) {
		t.Error("NoStartRebuild 打开时不该从起点读")
	}
}

// 验收 1：本地私聊两百条、闲置 61 分钟，第二轮请求里 200 条都在，原因 idle、dropped 0
func TestIdleRebuildFromStartLocal(t *testing.T) {
	t.Parallel()
	h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	h.seedLocal(200)
	h.send("开始")
	clock.add(61 * time.Minute)
	h.send("回来")
	bodyHasAll(t, h.body(1), 200, "旧话")
	if tr := h.lastTrace(t); tr.Reason != RebuildIdle || tr.Dropped != 0 || tr.Kept < 200 {
		t.Fatalf("闲置重建应该从起点带全：%+v", tr)
	}
}

// 验收 1：主人 QQ 私聊同样两百条、闲置 61 分钟
func TestIdleRebuildFromStartOwnerQQ(t *testing.T) {
	t.Parallel()
	h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	h.seedQQ(200)
	h.ownerQQ("开始")
	clock.add(61 * time.Minute)
	h.ownerQQ("回来")
	bodyHasAll(t, h.body(1), 200, "旧话")
	if tr := h.lastTrace(t); tr.Reason != RebuildIdle || tr.Dropped != 0 || tr.Kept < 200 {
		t.Fatalf("闲置重建应该从起点带全：%+v", tr)
	}
}

// 验收 2：本地私聊纯聊天，重建后的历史段和上一轮逐字节相同（本地不存参考块，见规格 7.5，头一段参考信息不在比较里）
func TestRebuildPrefixFromStartLocal(t *testing.T) {
	t.Parallel()
	h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	h.seedLocal(12)
	h.send("第一句")
	clock.add(3 * time.Minute)
	h.send("第二句")
	clock.add(61 * time.Minute)
	h.send("第三句")
	// 本地回合不存参考块（规格-提示词前缀稳定 7.5）：参考信息那条本来就不在重建范围里，比的是库里有的事件
	persisted := func(ms []string) []string {
		var out []string
		for _, m := range ms {
			// ContextHeader 里有引号，请求体里被转义；按不含引号的开头认
			if !strings.Contains(m, "（以下是给你的参考信息") {
				out = append(out, m)
			}
		}
		return out
	}
	prev, cur := persisted(h.messages(1)), persisted(h.messages(2))
	if !strings.Contains(strings.Join(prev, "\n"), "旧话 0") || !strings.Contains(strings.Join(cur, "\n"), "旧话 0") {
		t.Fatalf("重建后的请求里没有从起点带来的第一句：\n%s", strings.Join(cur, "\n"))
	}
	if !isPrefix(prev, cur) {
		t.Fatalf("本地私聊重建后的历史段和上一轮不一样：\n上一轮 %v\n这一轮 %v", prev, cur)
	}
}

// 验收 2：主人 QQ 私聊纯聊天，重建后的请求整个以上一轮为前缀（参考块按档存库、插回原位）
func TestRebuildPrefixFromStartOwnerQQ(t *testing.T) {
	t.Parallel()
	h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	h.seedQQ(12)
	h.ownerQQ("第一句")
	clock.add(3 * time.Minute)
	h.ownerQQ("第二句")
	clock.add(61 * time.Minute)
	h.ownerQQ("第三句")
	prev, cur := h.messages(1), h.messages(2)
	if !strings.Contains(strings.Join(prev, "\n"), "旧话 0") || !strings.Contains(strings.Join(cur, "\n"), "旧话 0") {
		t.Fatalf("重建后的请求里没有从起点带来的第一句：\n%s", strings.Join(cur, "\n"))
	}
	if !isPrefix(prev, cur) {
		t.Fatalf("主人 QQ 私聊重建后不是以上一轮整个为前缀：\n上一轮 %v\n这一轮 %v", prev, cur)
	}
}

// 验收 3：群聊不变，QQ 群和本地群聊带上条数仍在 [n, 2n)
func TestGroupWindowStillAnchored(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		conv func(*harness) string
		run  func(*harness, string)
	}{
		{"QQ群", func(*harness) string { return groupConv }, func(h *harness, text string) {
			o := wait(h.t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{h.storeGroup(text)}, Text: text,
				Audience: groupAud, Addressed: true, Scope: "g"}))
			h.commitSaid(groupConv, o, nil)
		}},
		{"本地群聊", func(h *harness) string { return "console:" + h.c.CharacterID + ":group:g1" }, func(h *harness, text string) {
			conv := "console:" + h.c.CharacterID + ":group:g1"
			seq := h.storeConv(conv, "console", "owner", "owner", text)
			aud := audience.Audience{Channel: "console", Conversation: conv, Scene: "group", PrivateOK: true, OwnerAsked: true}
			wait(h.t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: text,
				Audience: aud, Addressed: true, Scope: memory.ScopeKey(aud)}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
			conv := tc.conv(h)
			for i := range 120 {
				h.storeConv(conv, strings.Split(conv, ":")[0], "human", "p1", fmt.Sprintf("旧话 %d", i))
			}
			tc.run(h, "先来一句")
			clock.add(61 * time.Minute)
			tc.run(h, "过了很久")
			tr := h.lastTrace(t)
			if tr.Reason != RebuildIdle || tr.Kept < 30 || tr.Kept >= 60 {
				t.Fatalf("群聊仍是锚定窗口，带上条数在 [30,60)：%+v", tr)
			}
			if tr.Dropped == 0 {
				t.Fatalf("锚定窗口应该丢掉窗口外的：%+v", tr)
			}
		})
	}
}

// 验收 4：开始新话题写进库，重建只带分隔之后的；检查点回退、渠道交回用的 RestartIn 不写
func TestStartNewTopicWritesBreak(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	h.seedLocal(3)
	h.send("旧话题最后一句")
	if h.liveCount(conv) == 0 {
		t.Fatal("旧话题这一轮应该已经建了实例")
	}
	var first int64
	if err := h.st.Events.QueryRow(`SELECT min(seq) FROM events WHERE conversation = ? AND kind = 'message'`, conv).Scan(&first); err != nil {
		t.Fatal(err)
	}
	// 旧话题的快照不能在新话题里当成摘要用（切点比新起点早）
	if err := h.st.AddEpoch(store.Epoch{ID: "made-old-epoch", Conversation: conv, CharacterID: h.c.CharacterID, FromSeq: first,
		Summary: "旧摘要不该再出现", Kind: "summary", Trigger: "manual", Status: "ok", CreatedAt: h.hub.Cfg.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.StartNewTopic(h.c.CharacterID, conv); err != nil {
		t.Fatal(err)
	}
	if brk, err := history.LastBreak(h.st.Events, conv); err != nil || brk == 0 {
		t.Fatalf("新话题应该写一条分隔：%d %v", brk, err)
	}
	// 这里不再补 RestartIn：丢实例要由 StartNewTopic 自己做到
	if n := h.liveCount(conv); n != 0 {
		t.Fatalf("新话题以后这个会话不该还有实例，还有 %d 个", n)
	}
	var notices int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND kind = 'notice'`, conv).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("只该有新话题这一条分隔，通知有 %d 条", notices)
	}
	h.send("新话题第一句")
	body := h.body(1)
	for i := range 3 {
		if strings.Contains(body, fmt.Sprintf("旧话 %d", i)) {
			t.Fatalf("新话题以后不该带旧话题第 %d 条：%s", i, body)
		}
	}
	if strings.Contains(body, "旧摘要不该再出现") {
		t.Fatalf("新话题不该用旧话题的快照：%s", body)
	}
	if !strings.Contains(body, "新话题第一句") {
		t.Fatalf("新话题这一句应该在：%s", body)
	}
	if tr := h.lastTrace(t); tr.Dropped != 0 {
		t.Fatalf("新话题之前结束的话不算丢，dropped 应为 0：%+v", tr)
	}
}

// 新话题的场合只有主人的本地私聊和主人的 QQ 私聊：群聊、话题、别人的私聊一律报错，不写分隔
func TestStartNewTopicOnlyOwnerPrivate(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{mutate: func(c *Config) {
		c.OwnerPrivate = func(conv string) bool { return conv == ownerQQConv }
	}})
	conv := consoleConv(h.c.CharacterID)
	for _, c := range []struct {
		name, conv string
	}{
		{"本地群聊", "console:group:g1"},
		{"本地话题", conv + ":topic:t1"},
		{"QQ群", "qq:group:1"},
		{"别人的QQ私聊", "qq:private:1001:9999"},
	} {
		if err := h.hub.StartNewTopic(h.c.CharacterID, c.conv); err == nil {
			t.Errorf("%s：开始新话题应该报错", c.name)
		}
		var n int
		if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND kind = 'notice'`, c.conv).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s：报错的调用不该写分隔，写了 %d 条", c.name, n)
		}
	}
	// 主人 QQ 私聊（宿主说它是主人）可以开
	if err := h.hub.StartNewTopic(h.c.CharacterID, ownerQQConv); err != nil {
		t.Fatalf("主人的 QQ 私聊应该可以开始新话题：%v", err)
	}
	if brk, err := history.LastBreak(h.st.Events, ownerQQConv); err != nil || brk == 0 {
		t.Fatalf("主人 QQ 私聊也要写分隔：%d %v", brk, err)
	}
}

// 有回合正在跑时开始新话题：先把这个回合停下，再写分隔；不能等它自己说完
func TestStartNewTopicStopsRunningTurn(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `]`, hold: 1, onRequest: func(n int) {
		if n == 1 {
			requested <- struct{}{}
		}
	}})
	conv := consoleConv(h.c.CharacterID)
	out := h.hub.Send(h.c.CharacterID, "说到一半")
	select {
	case <-requested:
	case <-time.After(2 * time.Minute):
		t.Fatal("第一轮没有发出请求")
	}
	if err := h.hub.StartNewTopic(h.c.CharacterID, conv); err != nil {
		t.Fatal(err)
	}
	o := wait(t, out)
	if o == nil || !o.Stopped {
		t.Fatalf("正在跑的回合应该先被停下：%+v", o)
	}
	if brk, err := history.LastBreak(h.st.Events, conv); err != nil || brk == 0 {
		t.Fatalf("停下以后要写分隔：%d %v", brk, err)
	}
}

// 排着的那批属于旧话题：开始新话题直接取消它们，不等它们说完；下一轮不带旧话题。
// 去掉“取消排着的批次”，这一批会照常回完（结局不是 stopped），测试红。
func TestStartNewTopicCancelsQueuedBatch(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, hold: 1, onRequest: func(n int) {
		if n == 1 {
			requested <- struct{}{}
		}
	}})
	conv := consoleConv(h.c.CharacterID)
	h.seedLocal(3)
	first := h.hub.Send(h.c.CharacterID, "旧话题第一句")
	recv(t, requested, "第一轮发出请求")
	seq := h.storeConv(conv, "console", "owner", "owner", "旧话题第二句")
	second := h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "旧话题第二句",
		Audience: audience.Console(conv), Addressed: true, Scope: ConsoleScope})
	// Respond 回来时这句已经排进队列；第一批还停在假网关上，StartNewTopic 取消的正是这一批
	if err := h.hub.StartNewTopic(h.c.CharacterID, conv); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]<-chan Outcome{"第一轮": first, "排队的一批": second} {
		o := recv(t, out, name+"收尾")
		if o.Err != nil {
			t.Fatalf("%s收尾出错：%v", name, o.Err)
		}
		if o.Result == nil || !o.Result.Stopped {
			t.Fatalf("%s属于旧话题，应该按被停下收尾：%+v", name, o)
		}
	}
	if brk, err := history.LastBreak(h.st.Events, conv); err != nil || brk == 0 {
		t.Fatalf("取消排着的批次以后也要写分隔：%d %v", brk, err)
	}
	if n := h.liveCount(conv); n != 0 {
		t.Fatalf("新话题以后这个会话不该还有实例，还有 %d 个", n)
	}
	h.send("新话题第一句")
	body := h.body(1)
	for _, text := range []string{"旧话题第一句", "旧话题第二句"} {
		if strings.Contains(body, text) {
			t.Fatalf("下一轮不该带分隔之前的「%s」：%s", text, body)
		}
	}
	if !strings.Contains(body, "新话题第一句") {
		t.Fatalf("新话题这一句应该在：%s", body)
	}
}

// 开新话题挡住输入的那段时间里，新来的输入收到 ErrInputBusy（maintenance 必须先设上）。
func TestStartNewTopicRejectsInputWhileWaiting(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	barrier, release := make(chan struct{}), make(chan struct{})
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `]`, hold: 1, onRequest: func(n int) {
		if n == 1 {
			requested <- struct{}{}
		}
	}})
	h.hub.testHook = func(point string) {
		if point == "newTopicBarrier" {
			close(barrier)
			<-release
		}
	}
	conv := consoleConv(h.c.CharacterID)
	out := h.hub.Send(h.c.CharacterID, "说到一半")
	recv(t, requested, "第一轮发出请求")
	done := make(chan error, 1)
	go func() { done <- h.hub.StartNewTopic(h.c.CharacterID, conv) }()
	recv(t, barrier, "开始新话题挡住输入")
	rejected := h.hub.Send(h.c.CharacterID, "这时候插一句")
	if o := recv(t, rejected, "被挡的输入回执"); !errors.Is(o.Err, ErrInputBusy) {
		t.Fatalf("开新话题期间的新输入应该被挡住：%+v", o)
	}
	if n := h.ownerSaid(conv, "这时候插一句"); n != 0 {
		t.Fatalf("被挡的输入不该落库，落了 %d 条", n)
	}
	close(release)
	if err := recv(t, done, "开始新话题返回"); err != nil {
		t.Fatal(err)
	}
	if o := recv(t, out, "被停下的回合"); o.Result == nil || !o.Result.Stopped {
		t.Fatalf("旧话题那一轮应该按被停下收尾：%+v", o)
	}
	if brk, err := history.LastBreak(h.st.Events, conv); err != nil || brk == 0 {
		t.Fatalf("挡住输入的这一轮应该写成分隔：%d %v", brk, err)
	}
}

// 写分隔失败：报错、不丢实例、maintenance 释放，下一条输入照常进来。
func TestStartNewTopicWriteFailureReleasesMaintenance(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	if _, err := h.st.Events.Exec(`CREATE TRIGGER injected_new_topic_failure BEFORE INSERT ON events
		WHEN NEW.actor_id = 'new-topic' BEGIN SELECT RAISE(ABORT, '注入：分隔写不进'); END`); err != nil {
		t.Fatal(err)
	}
	defer h.st.Events.Exec(`DROP TRIGGER IF EXISTS injected_new_topic_failure`)
	if err := h.hub.StartNewTopic(h.c.CharacterID, conv); err == nil {
		t.Fatal("写分隔失败应该返回错误")
	}
	h.hub.lifecycle.mu.Lock()
	busy := h.hub.lifecycle.maintenance[convKey(h.c.CharacterID, conv)]
	h.hub.lifecycle.mu.Unlock()
	if busy {
		t.Fatal("写分隔失败以后 maintenance 没释放")
	}
	var notices int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_id = 'new-topic'`, conv).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("写失败不该留下分隔，有 %d 条", notices)
	}
	if _, err := h.st.Events.Exec(`DROP TRIGGER IF EXISTS injected_new_topic_failure`); err != nil {
		t.Fatal(err)
	}
	h.send("还能接着说")
	if body := h.body(0); !strings.Contains(body, "还能接着说") {
		t.Fatalf("maintenance 释放以后下一句应该照常进来：%s", body)
	}
}

// 关 hub 时正在等旧话题收尾：StartNewTopic 退出并报已封口，不写分隔。
// 去掉 h.base.Done() 分支，它会等到超时（短上限）才报超时，测试红。
func TestStartNewTopicStopsWhenHubClosing(t *testing.T) {
	t.Parallel()
	entered, toolRelease := make(chan struct{}, 1), make(chan struct{})
	barrier, release := make(chan struct{}), make(chan struct{})
	h, _ := newStartHarness(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"t1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				entered <- struct{}{}
				<-toolRelease // 不看 ctx：关 hub 也停不住，只能靠 StartNewTopic 自己的出口
				return tools.Result{Text: "列好了"}, nil
			}
		}})
	h.hub.newTopicTimeout = time.Second // 改坏（少了 base.Done）时快些红
	h.hub.testHook = func(point string) {
		if point == "newTopicBarrier" {
			close(barrier)
			<-release
		}
	}
	conv := consoleConv(h.c.CharacterID)
	out := h.hub.Send(h.c.CharacterID, "跑个工具")
	recv(t, entered, "工具开始")
	done := make(chan error, 1)
	go func() { done <- h.hub.StartNewTopic(h.c.CharacterID, conv) }()
	recv(t, barrier, "开始新话题进入等待")
	h.cancel()
	close(release)
	if err := recv(t, done, "关 hub 时开始新话题返回"); !errors.Is(err, runstate.ErrSealed) {
		t.Fatalf("关 hub 时应该报已封口：%v", err)
	}
	var notices int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_id = 'new-topic'`, conv).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("关 hub 时不该写分隔，有 %d 条", notices)
	}
	close(toolRelease)
	recv(t, out, "被停下的回合收尾")
}

// 超时：旧话题的回合卡着不收尾，不要一直等。超时的不写分隔、maintenance 已释放。
func TestStartNewTopicTimeoutReleasesMaintenance(t *testing.T) {
	t.Parallel()
	entered, toolRelease := make(chan struct{}, 1), make(chan struct{})
	h, _ := newStartHarness(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"t1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				entered <- struct{}{}
				<-toolRelease
				return tools.Result{Text: "列好了"}, nil
			}
		}})
	h.hub.newTopicTimeout = 100 * time.Millisecond
	conv := consoleConv(h.c.CharacterID)
	out := h.hub.Send(h.c.CharacterID, "卡住的工具")
	recv(t, entered, "工具开始")
	if err := h.hub.StartNewTopic(h.c.CharacterID, conv); !errors.Is(err, ErrNewTopicTimeout) {
		t.Fatalf("回合不收尾时应该报超时：%v", err)
	}
	h.hub.lifecycle.mu.Lock()
	busy := h.hub.lifecycle.maintenance[convKey(h.c.CharacterID, conv)]
	h.hub.lifecycle.mu.Unlock()
	if busy {
		t.Fatal("超时以后 maintenance 没释放")
	}
	var notices int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_id = 'new-topic'`, conv).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("超时不该写分隔，有 %d 条", notices)
	}
	close(toolRelease)
	recv(t, out, "被取消的回合收尾")
}

// liveCount 是这个会话现在有几档实例
func (h *harness) liveCount(conv string) int {
	h.t.Helper()
	prefix := convKey(h.c.CharacterID, conv) + "|"
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	n := 0
	for k := range h.hub.live {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// 验收 5：本地私聊从起点读出来超过硬门槛，建实例时不裁，回复前 fitHard 压缩；压缩失败才裁并如实留痕
func TestOverHardFromStartLocal(t *testing.T) {
	t.Parallel()
	t.Run("压缩成了", func(t *testing.T) {
		t.Parallel()
		h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`})
		h.seedRounds(10)
		h.send("先建实例")
		h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) + 1000)
		h.seedRounds(10)
		h.seedRounds(10)
		h.hub.RestartIn(h.c.CharacterID, consoleConv(h.c.CharacterID))
		h.send("过了门槛")
		if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" {
			t.Fatalf("回复前应该压成一次：%+v", eps)
		}
		if b := h.body(1); !strings.Contains(b, compactMarker) || !strings.Contains(b, strings.Repeat("甲", 20)) {
			t.Fatalf("摘要请求应该带着从起点读出的最早那段（建实例时没裁）：%s", b)
		}
		msgs := h.messages(2)
		if !strings.Contains(msgs[1], "前面的聊天已经整理成下面的摘要") {
			t.Fatalf("压缩后换上新快照回复：%v", msgs)
		}
		if tr := h.lastTrace(t); !tr.has(RebuildOverLimitSnap) || !tr.Snapshot {
			t.Fatalf("留痕应该有 over_limit_compacted、带快照：%+v", tr)
		}
	})
	t.Run("压缩失败裁剪", func(t *testing.T) {
		t.Parallel()
		h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `]`})
		h.seedRounds(10)
		h.send("先建实例")
		h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) + 1000)
		h.seedRounds(10)
		h.seedRounds(10)
		h.hub.RestartIn(h.c.CharacterID, consoleConv(h.c.CharacterID))
		h.send("过了门槛")
		if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" {
			t.Fatalf("应该留一行失败的压缩：%+v", eps)
		}
		if b := h.body(2); strings.Contains(b, strings.Repeat("甲", 20)) || !strings.Contains(b, "过了门槛") {
			t.Fatalf("压不成才裁到 keepTokens，最早那段不该在：%s", b)
		}
		if tr := h.lastTrace(t); !tr.has(RebuildOverLimitTrim) || tr.Dropped == 0 || tr.Snapshot {
			t.Fatalf("留痕要如实记 over_limit_trimmed 和丢的条数：%+v", tr)
		}
	})
}

// 验收 5：主人 QQ 私聊第 1 步还是 trim，超过硬门槛只测裁剪兜底
func TestOverHardFromStartOwnerQQTrims(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	h.seedQQRounds(10)
	h.ownerQQ("先建实例")
	h.setHard(sizeOf(h.liveIn(ownerQQConv, memory.ScopeKey(qqOwnerAud()))) + 1000)
	h.seedQQRounds(20)
	h.hub.RestartIn(h.c.CharacterID, ownerQQConv)
	h.ownerQQ("过了门槛")
	if h.countBodies(compactMarker) != 0 {
		t.Fatalf("主人 QQ 私聊第 1 步不压缩")
	}
	if b := h.body(1); strings.Contains(b, strings.Repeat("甲", 20)) || !strings.Contains(b, "过了门槛") {
		t.Fatalf("只带最近的话：%s", b)
	}
	if tr := h.lastTrace(t); !tr.has(RebuildOverLimitTrim) || tr.Dropped == 0 {
		t.Fatalf("留痕要记 over_limit_trimmed：%+v", tr)
	}
}

// 有快照、切点比起点新时，参考块清理下限用切点：切点之前的块被摘要覆盖、清掉，切点之后的留着
func TestRefBlockBeforeSnapshotCutPruned(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	scope := memory.ScopeKey(qqOwnerAud())
	h.seedQQ(100)
	h.ownerQQ("第一句")
	seqOf := func(text string) int64 {
		t.Helper()
		var seq int64
		if err := h.st.Events.QueryRow(`SELECT seq FROM events WHERE conversation = ? AND text = ? LIMIT 1`, ownerQQConv, text).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	cut := seqOf("旧话 50")
	if err := h.st.AddEpoch(store.Epoch{ID: "made-cut", Conversation: ownerQQConv, CharacterID: h.c.CharacterID, FromSeq: cut,
		Summary: "切点之前的都收进摘要", Kind: "summary", Trigger: "manual", Status: "ok", CreatedAt: h.hub.Cfg.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	before, after := seqOf("旧话 20"), seqOf("旧话 80")
	if err := h.st.SaveRefBlocks([]store.RefBlock{
		{CharacterID: h.c.CharacterID, Conversation: ownerQQConv, ScopeKey: scope, BeforeSeq: before, CardHash: "made", MemoryRefsJSON: "[]", Text: "切点之前的块", CreatedAt: h.hub.Cfg.Now().UnixMilli()},
		{CharacterID: h.c.CharacterID, Conversation: ownerQQConv, ScopeKey: scope, BeforeSeq: after, CardHash: "made", MemoryRefsJSON: "[]", Text: "切点之后的块", CreatedAt: h.hub.Cfg.Now().UnixMilli()},
	}, h.c.CharacterID, ownerQQConv, 0); err != nil {
		t.Fatal(err)
	}
	// 再走一轮渠道回合：putRefBlocks 的清理按切点删
	h.ownerQQ("第二句")
	for text, want := range map[string]int{"切点之前的块": 0, "切点之后的块": 1} {
		var n int
		if err := h.st.Events.QueryRow(`SELECT count(*) FROM ref_blocks WHERE conversation = ? AND text = ?`, ownerQQConv, text).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%q 剩 %d 块，应该 %d", text, n, want)
		}
	}
}

// 验收 8：起点之后、旧窗口下限之前的参考块，清理和重建都还留着
func TestRefBlockBeforeOldWindowSurvives(t *testing.T) {
	t.Parallel()
	h, clock := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	scope := memory.ScopeKey(qqOwnerAud())
	h.seedQQ(100)
	h.ownerQQ("第一句")
	l := h.liveIn(ownerQQConv, scope)
	var seq20 int64
	if err := h.st.Events.QueryRow(`SELECT seq FROM events WHERE conversation = ? AND text = '旧话 20' LIMIT 1`, ownerQQConv).Scan(&seq20); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveRefBlocks([]store.RefBlock{{CharacterID: h.c.CharacterID, Conversation: ownerQQConv, ScopeKey: scope, BeforeSeq: seq20,
		CardHash: cardHashOf(l.system), MemoryRefsJSON: "[]", Text: "起点之后的参考块", CreatedAt: h.hub.Cfg.Now().UnixMilli()}},
		h.c.CharacterID, ownerQQConv, 0); err != nil {
		t.Fatal(err)
	}
	// 再走一轮渠道回合：putRefBlocks 的清理不能把起点之后的块删掉（旧锚定窗口下限在这块后面）
	h.ownerQQ("第二句")
	var left int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM ref_blocks WHERE conversation = ? AND text = '起点之后的参考块'`, ownerQQConv).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatal("清理把起点之后的参考块删了")
	}
	clock.add(61 * time.Minute)
	h.ownerQQ("第三句")
	if body := h.body(2); !strings.Contains(body, "起点之后的参考块") {
		t.Fatalf("重建后参考块应该插回原位：%s", body)
	}
}
