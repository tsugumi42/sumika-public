package turn

// 规格 8.2 A8（压缩）的 C2b：回复前过了硬门槛现场压（去掉第七节 T2）、回合后提前压（A38）。对着写明的预期测

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// compact-chat 提示词里的那句：请求体里有它就是摘要请求
const compactMarker = "整理上下文的请求"

const shortSummary = `{"content":["好"],` + usage + `}`

// setHard 把硬门槛定成 hard 个 token（reserve 1、比例 1），keepTokens 50。要在没有回合在跑、也没有后台压缩时调（改 Cfg 不和谁抢）
func (h *harness) setHard(hard int) {
	h.hub.Cfg.Context = ContextLimits{KeepTokens: 50, Reserve: 1, HardRatio: 1, Window: hard + 1}
}

// sizeOf 是这个实例现在多大（固定前缀 + 历史：和看硬门槛的估算一样，假网关报的输入只有 100，比它小）
func sizeOf(l *Live) int { return l.b.Fixed + sumikaMessages(conversationTexts(l.msgs)...) }

// countBodies 是假网关回过的请求里有几次带着 sub
func (h *harness) countBodies(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, b := range h.bodies {
		if strings.Contains(b, sub) {
			n++
		}
	}
	return n
}

// lastRebuild 是最近一次回复记的重建原因
func (h *harness) lastRebuild() string {
	var r string
	h.st.Trace.QueryRow(`SELECT coalesce(rebuild, '') FROM model_runs WHERE stage = 'reply' ORDER BY rowid DESC LIMIT 1`).Scan(&r)
	return r
}

// seedGroup 往平台群里种 8 轮（群友一句、她一句，每句 300 个字），返回"群友说一句、请她回"
func (h *harness) seedGroup() func(text string) {
	h.t.Helper()
	for i := range 8 {
		ch := string([]rune("甲乙丙丁戊己庚辛")[i])
		h.storeGroup(strings.Repeat(ch, 300))
		h.storeHers(strings.Repeat(ch, 300))
	}
	return func(text string) {
		h.t.Helper()
		seq := h.storeGroup(text)
		wait(h.t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{seq}, Text: text, Audience: groupAud, Addressed: true, Scope: "g"}))
	}
}

// 种 8 轮、说一句建好实例，再把硬门槛收到"再来一句（加 600 的余量）就超"
func (h *harness) nearHard() {
	h.t.Helper()
	h.seedRounds(8)
	h.send("先建实例")
	h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) + 300)
}

// 过了硬门槛：先压（trigger rule，这一句还没给她看、不进摘要），压成了原地换上新快照再回；换上以后接着用这个实例（前缀不断）
func TestHardCompact(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `,` + textReply + `]`})
	h.nearHard()
	if r := h.send("过了门槛"); r.Stopped {
		t.Fatal("压完应该照常回")
	}
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "rule" {
		t.Fatalf("先压一次（rule）：%+v", eps)
	}
	if b := h.body(1); !strings.Contains(b, compactMarker) || strings.Contains(b, "过了门槛") {
		t.Fatalf("第二个请求是摘要请求，这一句不在里面：%s", b)
	}
	reply := h.messages(2)
	if !strings.Contains(reply[1], "前面的聊天已经整理成下面的摘要") || strings.Contains(h.body(2), strings.Repeat("甲", 10)) || !strings.Contains(reply[len(reply)-1], `"过了门槛"`) {
		t.Fatalf("回复的请求换上了新快照、末尾是这一句：\n%s", strings.Join(reply, "\n"))
	}
	if r := h.lastRebuild(); r != "compact" {
		t.Fatalf("记 rebuild compact：%q", r)
	}
	h.send("再说一句")
	if !isPrefix(h.messages(2), h.messages(3)) {
		t.Fatal("换上新快照以后接着用这个实例：下一轮的请求接在这一轮后面")
	}
}

// 压缩没成（摘要太短）：原地重建、只带最近 keepTokens 的话——重建出来的历史本身没超硬门槛也照样裁（Bun acquire 的 trim）
func TestHardCompactFailedTrims(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `]`})
	h.nearHard()
	h.send("过了门槛")
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" || eps[0].trigger != "rule" || !strings.Contains(eps[0].err, "太短") {
		t.Fatalf("记一行失败的：%+v", eps)
	}
	if b := h.body(2); strings.Contains(b, strings.Repeat("辛", 10)) || !strings.Contains(b, "先建实例") || !strings.Contains(b, "过了门槛") {
		t.Fatalf("只带最近的话（种下的都裁掉，最近一轮留着）：%s", b)
	}
	if r := h.lastRebuild(); r != "reset" || !h.logged("只带最近的话") {
		t.Fatalf("记 rebuild reset、记一行日志：%q %v", r, h.logs)
	}
}

// blockingCompactHook 挂一个会一直卡住的压缩前钩子（时限调成 2 秒：被调了也不至于等满 90 秒），返回它有没有被调过
func (h *harness) blockingCompactHook() *atomic.Bool {
	var called atomic.Bool
	h.use(fakePlugin{"mem", func(r *Registry) (func(), error) {
		r.OnBeforeCompact(func(ctx context.Context, _ BeforeCompact) (int64, bool, error) {
			called.Store(true)
			<-ctx.Done()
			return 0, false, nil
		})
		return nil, nil
	}})
	return &called
}

func shortCompactHook(c *Config) { c.Hooks.Compact = 2 * time.Second }

// 不压缩的（关了自动压缩的本地闲聊、渠道的会话）：不请求摘要、不等插件，直接原地只带最近的话
func TestHardTrimWithoutCompact(t *testing.T) {
	t.Parallel()
	t.Run("关了自动压缩", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, mutate: shortCompactHook})
		called := h.blockingCompactHook()
		h.nearHard()
		h.hub.Cfg.Context.NoAutoCompact = true
		h.send("过了门槛")
		if len(h.bodies) != 2 || len(h.epochs()) != 0 || called.Load() {
			t.Fatalf("不压缩、不等插件：请求了 %d 次，快照 %+v，钩子调过 %v", len(h.bodies), h.epochs(), called.Load())
		}
		if b := h.body(1); strings.Contains(b, strings.Repeat("辛", 10)) || !strings.Contains(b, "过了门槛") || h.lastRebuild() != "reset" {
			t.Fatalf("只带最近的话：%s", b)
		}
	})
	t.Run("渠道的会话", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, mutate: shortCompactHook})
		called := h.blockingCompactHook()
		respond := h.seedGroup()
		respond("先来一句")
		h.setHard(sizeOf(h.liveIn(groupConv, "g")) + 300)
		respond("过了门槛")
		if len(h.bodies) != 2 || len(h.epochs()) != 0 || called.Load() {
			t.Fatalf("渠道的不压缩、不等插件：请求了 %d 次，快照 %+v，钩子调过 %v", len(h.bodies), h.epochs(), called.Load())
		}
		if b := h.body(1); strings.Contains(b, strings.Repeat("辛", 10)) || !strings.Contains(b, "过了门槛") {
			t.Fatalf("只带最近的话：%s", b)
		}
	})
}

// A8"压缩被取消"：回复前压的时候按停止——这一轮算停下、不发回复的请求、实例照旧、不留"上一轮被停止"的提醒；
// 这一句已经存进库，下一轮接着用这个实例时追进来（不能因为这一轮没跑起来就从她的历史里消失）
func TestHardCompactStopped(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`,
		intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(body, compactMarker) {
				entered <- struct{}{}
				<-r.Context().Done()
				return true
			}
			return false
		}})
	h.nearHard()
	out := h.hub.Send(h.c.CharacterID, "被停下的那句")
	recv(t, entered, "回复前的摘要请求")
	if !h.stopWithin() {
		t.Fatal("压的时候按停止应该停得下")
	}
	if r := wait(t, out); !r.Stopped || len(r.Steps) != 0 || len(h.bodies) != 1 {
		t.Fatalf("这一轮算停下、不发回复的请求：%+v，请求了 %d 次", r, len(h.bodies))
	}
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" || !strings.Contains(eps[0].err, "被停下") {
		t.Fatalf("记一行被停下的：%+v", eps)
	}
	h.hub.Cfg.Context = ContextLimits{} // 放宽：下一轮不再压，接着用原来的实例
	h.send("接着说")
	b := h.body(1)
	if n := strings.Count(b, "被停下的那句"); n != 1 || strings.Contains(b, "上一轮被所有者手动停止") || strings.Contains(b, "前面的聊天已经整理成下面的摘要") {
		t.Fatalf("实例照旧、不提醒，被停下的那句追进来一次（%d）：%s", n, b)
	}
	if !isPrefix(h.messages(0), h.messages(1)) {
		t.Fatal("实例照旧：下一轮的请求接在上一轮后面")
	}
}

// holdCompact 拦住摘要请求：进来时往 entered 报一声，等 release 关上再照常回（她那边中止了就不等：测试红了以后收尾不卡住）
func holdCompact(entered chan<- struct{}, release <-chan struct{}) func(int, string, http.ResponseWriter, *http.Request) bool {
	return func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(body, compactMarker) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		return false
	}
}

// recv 等 ch 来一个，barrierLimit 没来就红（改坏以后不一直卡着）
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(barrierLimit):
		t.Fatalf("等不到：%s", what)
		var zero T
		return zero
	}
}

// stopWithin 按停止，barrierLimit 没停好就红
func (h *harness) stopWithin() bool {
	h.t.Helper()
	ok := make(chan bool, 1)
	go func() { ok <- h.hub.Stop(h.c.CharacterID) }()
	return recv(h.t, ok, "停止返回")
}

// A8"压缩和插话交错"：回复前压的时候插一句，收下、压完送进这一轮
func TestHardCompactSteer(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`, intercept: holdCompact(entered, release)})
	h.nearHard()
	out := h.hub.Send(h.c.CharacterID, "过了门槛")
	recv(t, entered, "回复前的摘要请求")
	if _, ok := h.hub.Steer(h.c.CharacterID, "压的时候插一句"); !ok {
		t.Fatal("压的时候插话应该收下")
	}
	close(release)
	wait(t, out)
	if b := h.body(2); !strings.Contains(b, "压的时候插一句") || !strings.Contains(b, "前面的聊天已经整理成下面的摘要") {
		t.Fatalf("插话送进压完以后的这一轮：%s", b)
	}
	if p := strings.Join(h.steerPhases("压的时候插一句"), ","); p != "accepted,delivered" {
		t.Fatalf("插话走过的几步：%s", p)
	}
	if h.ownerSaid(consoleConv(h.c.CharacterID), "压的时候插一句") != 1 {
		t.Fatal("插话存进库一次")
	}
}

// 种 8 轮、说一句建好实例，再把硬门槛定成现在大小的 1.25 倍：再说一句不超硬门槛，说完就过了八成（该提前压了）。返回现在多大
func (h *harness) nearEarly() int {
	h.t.Helper()
	h.seedRounds(8)
	h.send("先建实例")
	size := sizeOf(h.hub.liveOf(h.c.CharacterID))
	h.setHard(size * 5 / 4)
	return size
}

// compactBusy：后台正在提前压主窗口这个会话
func (h *harness) compactBusy() bool {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	return h.hub.compactJobs[convKey(h.c.CharacterID, consoleConv(h.c.CharacterID))] != nil
}

// onWaitCompact 在回合（或手动压缩）开始等后台那一次时关上返回的通道
func (h *harness) onWaitCompact() <-chan struct{} {
	waiting := make(chan struct{})
	var once sync.Once
	h.hub.testHook = func(p string) {
		if p == "waitCompact" {
			once.Do(func() { close(waiting) })
		}
	}
	return waiting
}

// A38：说完过了硬门槛的八成，后台提前压（trigger early，材料是这一轮说完的样子）；下一句换上新快照，不用等、也不再压
func TestEarlyCompact(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`})
	h.nearEarly()
	h.send("提前")
	h.hub.Idle()
	eps := h.epochs()
	if len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "early" {
		t.Fatalf("后台提前压了一次：%+v", eps)
	}
	// 最近两轮都是短话，keepTokens 50 留得下两轮：切在"先建实例"上；摘要请求里有这一轮
	var cut string
	h.st.Events.QueryRow(`SELECT text FROM events WHERE seq = ?`, eps[0].from).Scan(&cut)
	if cut != "先建实例" || !strings.Contains(h.body(2), `"提前"`) {
		t.Fatalf("材料是这一轮说完的样子：切在「%.20s」", cut)
	}
	h.send("接着说")
	if !strings.Contains(h.messages(3)[1], "前面的聊天已经整理成下面的摘要") || h.lastRebuild() != "compact" || h.countBodies(compactMarker) != 1 {
		t.Fatalf("下一句换上新快照、不再压：%s", h.body(3))
	}
}

// 后台还在提前压时下一句过了硬门槛：等它（不再压一次），压成了换上新快照再回
func TestEarlyInFlightThenHard(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`, intercept: holdCompact(entered, release)})
	size := h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	waiting := h.onWaitCompact()
	out := h.hub.Send(h.c.CharacterID, strings.Repeat("长", size/4)) // 加上这句就过了硬门槛
	recv(t, waiting, "回合开始等后台那一次")
	close(release)
	wait(t, out)
	if eps := h.epochs(); h.countBodies(compactMarker) != 1 || len(eps) != 1 || eps[0].trigger != "early" {
		t.Fatalf("只压了后台那一次：%d 次，%+v", h.countBodies(compactMarker), eps)
	}
	if b := h.body(3); !strings.Contains(b, "前面的聊天已经整理成下面的摘要") || !strings.Contains(b, strings.Repeat("长", 10)) || h.lastRebuild() != "compact" {
		t.Fatalf("换上新快照再回：%s", b)
	}
}

// 等到后台提前压没成：这一轮不再压一次，原地只带最近的话
func TestEarlyInFlightFailedThenHard(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + shortSummary + `,` + textReply + `]`, intercept: holdCompact(entered, release)})
	size := h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	waiting := h.onWaitCompact()
	out := h.hub.Send(h.c.CharacterID, strings.Repeat("长", size/4))
	recv(t, waiting, "回合开始等后台那一次")
	close(release)
	wait(t, out)
	if eps := h.epochs(); h.countBodies(compactMarker) != 1 || len(eps) != 1 || eps[0].status != "failed" {
		t.Fatalf("只压了后台那一次、没成：%d 次，%+v", h.countBodies(compactMarker), eps)
	}
	if b := h.body(3); strings.Contains(b, strings.Repeat("辛", 10)) || !strings.Contains(b, strings.Repeat("长", 10)) || h.lastRebuild() != "reset" || !h.logged("压缩没成") {
		t.Fatalf("原地只带最近的话：%s", b)
	}
}

// 7.1 A42 在回复前压完原地重建这条路上也成立：按过停止、还没提醒，过了硬门槛压完重建，这一轮照样提醒
func TestStopReminderSurvivesHardRebuild(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	stopped := make(chan struct{})
	h = newHarnessOpts(t, harnessOpts{
		hold:    2,
		replies: `[` + textReply + `,{"content":["我先说","后半句"],` + usage + `},` + summaryReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(s string) {
				if s == "我先说" {
					once.Do(func() { go func() { h.hub.Stop(h.c.CharacterID); close(stopped) }() })
				}
			}
		},
	})
	h.seedRounds(8)
	h.send("先建实例")
	h.send("说点什么")
	recv(t, stopped, "按停止")
	h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) + 300)
	h.send("过了门槛")
	if b := h.body(3); !strings.Contains(b, "前面的聊天已经整理成下面的摘要") || !strings.Contains(b, "上一轮被所有者手动停止") || !strings.Contains(b, "我先说") {
		t.Fatalf("压完原地重建的这一轮照样提醒被停止：%s", b)
	}
}

// 提前压只压本地闲聊：关了自动压缩的、渠道的会话说完过了八成也不压（它们满了只带最近的话）
func TestEarlyOnlyLocalSummary(t *testing.T) {
	t.Parallel()
	t.Run("关了自动压缩", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`})
		h.nearEarly()
		h.hub.Cfg.Context.NoAutoCompact = true
		h.send("提前")
		h.hub.Idle()
		if c := h.countBodies(compactMarker); c != 0 || len(h.epochs()) != 0 {
			t.Fatalf("关了自动压缩不提前压：压了 %d 次", c)
		}
	})
	t.Run("渠道的会话", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`})
		respond := h.seedGroup()
		respond("先来一句")
		h.setHard(sizeOf(h.liveIn(groupConv, "g")) * 5 / 4)
		respond("提前")
		h.hub.Idle()
		if c := h.countBodies(compactMarker); c != 0 || len(h.epochs()) != 0 {
			t.Fatalf("渠道的会话不提前压：压了 %d 次", c)
		}
	})
}

// 记忆触发的压缩带着上限（只压记忆整理过的，规格-记忆-v2 13.2.7）：碰上后台提前压、那一次又切过了上限（等记忆的钩子超时，
// 没收紧），不算这次压成
func TestCompactWaitsForEarlyBeyondMaxSeq(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`, intercept: holdCompact(entered, release),
		mutate: func(c *Config) { c.Hooks.Compact = 200 * time.Millisecond }})
	owners := h.seedRounds(8)
	h.send("先建实例")
	h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) * 5 / 4)
	h.blockingCompactHook()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	waiting := h.onWaitCompact()
	done := make(chan Compacted, 1)
	go func() {
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "memory", owners[2])
		done <- c
	}()
	recv(t, waiting, "记忆触发的压缩开始等后台那一次")
	close(release)
	c := recv(t, done, "记忆触发的压缩返回")
	if eps := h.epochs(); c.OK || !strings.Contains(c.Err, "晚于这次的上限") || len(eps) != 1 || eps[0].trigger != "early" || eps[0].from <= owners[2] {
		t.Fatalf("后台那一次切过了上限，不算这次压成：%+v %+v", c, eps)
	}
}

// 压完原地重建时读库出错：这一轮报错，这一批记成用过的（不记成见过），下一轮接着用这个实例时追进来
func TestHardRebuildErrorKeepsBatch(t *testing.T) {
	t.Parallel()
	var h *harness
	h = newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`,
		intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
			if strings.Contains(body, compactMarker) {
				// 把快照表挪开：写快照、重建时读快照都会出错
				if _, err := h.st.Events.Exec(`ALTER TABLE context_epochs RENAME TO context_epochs_moved`); err != nil {
					t.Error(err)
				}
				http.Error(w, "坏了", http.StatusBadRequest)
				return true
			}
			return false
		}})
	h.nearHard()
	if o := recv(t, h.hub.Send(h.c.CharacterID, "重建出错的那句"), "这一轮回完"); o.Err == nil {
		t.Fatal("重建读库出错，这一轮应该报错")
	}
	if _, err := h.st.Events.Exec(`ALTER TABLE context_epochs_moved RENAME TO context_epochs`); err != nil {
		t.Fatal(err)
	}
	h.hub.Cfg.Context = ContextLimits{} // 放宽：下一轮接着用原来的实例
	h.send("接着说")
	if n := strings.Count(h.body(1), "重建出错的那句"); n != 1 || !isPrefix(h.messages(0), h.messages(1)) {
		t.Fatalf("出错那一句追进来一次（%d）、实例照旧", n)
	}
}

// 后台提前压的材料在发起时定住（拷一份 Built）：压的时候下一轮已经开始、refresh 换了几道线，自检还按发起时的门槛
// （不然这边一改配置，那边压成的会被当成没压下来）。竞态检测抓不到这个：假网关的读写在中间排了先后
func TestEarlyMaterialFrozen(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`, intercept: holdCompact(entered, release)})
	h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	h.setHard(h.hub.liveOf(h.c.CharacterID).b.Fixed) // 硬门槛收到只装得下固定前缀：下一轮一开始 refresh 就换上
	waiting := h.onWaitCompact()
	out := h.hub.Send(h.c.CharacterID, "下一句")
	recv(t, waiting, "回合开始等后台那一次")
	close(release)
	wait(t, out)
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" {
		t.Fatalf("自检按发起时的门槛：%+v", eps)
	}
}

// 等后台提前压的时候按停止：马上停、不发回复的请求；后台那一次照常做完
func TestHardStoppedWhileWaitingEarly(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`, intercept: holdCompact(entered, release)})
	size := h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	waiting := h.onWaitCompact()
	out := h.hub.Send(h.c.CharacterID, strings.Repeat("长", size/4))
	recv(t, waiting, "回合开始等后台那一次")
	if !h.stopWithin() {
		t.Fatal("等后台压缩的时候按停止应该停得下")
	}
	if r := wait(t, out); !r.Stopped || h.countBodies(strings.Repeat("长", 10)) != 0 {
		t.Fatalf("这一轮算停下、不发回复的请求：%+v", r)
	}
	close(release)
	h.hub.Idle()
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "early" {
		t.Fatalf("后台那一次照常做完：%+v", eps)
	}
}

// 提前压没成：上下文再长 10% 才再试，不在八成到硬门槛之间每轮都压
func TestEarlyBackoff(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + shortSummary + `,` + textReply + `,` + textReply + `,` + shortSummary + `]`})
	size := h.nearEarly()
	h.send("一")
	h.hub.Idle()
	if !h.logged("后台提前压缩没用上") {
		t.Fatalf("没成记一行：%v", h.logs)
	}
	h.send("二")
	h.hub.Idle()
	if n := h.countBodies(compactMarker); n != 1 {
		t.Fatalf("没成以后只长了一点，不再试：压了 %d 次", n)
	}
	h.send(strings.Repeat("长", size/10+100))
	h.hub.Idle()
	if n := h.countBodies(compactMarker); n != 2 {
		t.Fatalf("长了一成多，再试一次：压了 %d 次", n)
	}
}

// 压成了（不管是谁触发的）清掉"上一次提前压没成"的记号：之后又长回八成以上就照常提前压，不等再长 10%（Bun 只有提前压成了才清）
func TestEarlyBackoffClearedByCompact(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"手动压缩", "回复前压缩"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + shortSummary + `,` + summaryReply + `,` + textReply + `,` + textReply + `,` + summaryReply + `]`})
			size := h.nearEarly()
			h.send("一")
			h.hub.Idle()
			failedAt := sizeOf(h.hub.liveOf(h.c.CharacterID)) // 提前压没成时的大小（假网关报的输入 100，比它小）
			if how == "手动压缩" {
				if c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0); !c.OK {
					t.Fatalf("手动压缩应该成功：%+v", c)
				}
				h.send("短") // 换上新快照，变小了
			} else {
				h.send(strings.Repeat("超", size/4)) // 过了硬门槛：先压（rule）、压成了换上新快照再回
				if eps := h.epochs(); len(eps) != 2 || eps[1].trigger != "rule" || eps[1].status != "ok" {
					t.Fatalf("回复前压了一次：%+v", eps)
				}
			}
			l := h.hub.liveOf(h.c.CharacterID)
			// 说完落在硬门槛的八成和"没成时再长 10%"中间（两头各留一截余量；回复和格式开销约 10）
			n := int((l.b.Hard*0.8+float64(failedAt)*1.1)/2) - sizeOf(l)
			h.send(strings.Repeat("长", n))
			h.hub.Idle()
			if c := h.countBodies(compactMarker); c != 3 {
				t.Fatalf("没成、%s、清掉记号以后的提前压，一共三次：%d 次", how, c)
			}
		})
	}
}

// 这一轮出错了不提前压（agent.ts respondNow 末尾的 ok）
func TestEarlySkipsAfterError(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`,
		intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
			if strings.Contains(body, `"出错的一句"`) && !strings.Contains(body, compactMarker) {
				http.Error(w, "坏了", http.StatusBadRequest) // 400 不重试
				return true
			}
			return false
		}})
	h.nearEarly()
	if o := <-h.hub.Send(h.c.CharacterID, "出错的一句"); o.Err == nil {
		t.Fatal("这一轮应该出错")
	}
	h.hub.Idle()
	if c := h.countBodies(compactMarker); c != 0 {
		t.Fatalf("出错的一轮说完不提前压：压了 %d 次", c)
	}
}

// 后台正在提前压时又一轮说完、还过八成：不再压第二次（同一个会话同时只压一个）
func TestEarlyOneAtATime(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + summaryReply + `,` + summaryReply + `]`, intercept: holdCompact(entered, release)})
	h.nearEarly()
	h.send("第一句")
	recv(t, entered, "提前压缩的请求")
	h.send("第二句") // 不过硬门槛，说完照样过八成
	close(release)
	h.hub.Idle()
	if c := h.countBodies(compactMarker); c != 1 {
		t.Fatalf("同一个会话同时只压一个：%d 次", c)
	}
}

// 上一次提前压在这一轮跑的时候写好了（下一轮才换上）：这一轮说完不再压（Bun 这时会再压一次）
func TestEarlySkipsNewerSnapshot(t *testing.T) {
	t.Parallel()
	var h *harness
	release := make(chan struct{})
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`,
		intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
			switch {
			case strings.Contains(body, compactMarker):
				select {
				case <-release:
				case <-r.Context().Done(): // 她那边中止了就不等（改坏以后收尾不卡住）
				}
			case strings.Contains(body, "第二句"):
				once.Do(func() {
					h.hub.mu.Lock()
					job := h.hub.compactJobs[convKey(h.c.CharacterID, consoleConv(h.c.CharacterID))]
					h.hub.mu.Unlock()
					close(release)
					// done 在快照落库并移除正在压缩标记后关闭。
					receive(t, job.done)
				})
			}
			return false
		}})
	h.nearEarly()
	h.send("第一句")
	h.send("第二句")
	h.hub.Idle()
	if n := h.countBodies(compactMarker); n != 1 {
		t.Fatalf("库里已经有新快照，这一轮说完不再压：压了 %d 次", n)
	}
	h.send("第三句")
	if h.lastRebuild() != "compact" {
		t.Fatalf("下一轮换上：%q", h.lastRebuild())
	}
}

// 手动压缩碰上后台正在提前压：等它、用它的结局，不再压一次
func TestCompactWaitsForEarly(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`, intercept: holdCompact(entered, release)})
	h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	waiting := h.onWaitCompact()
	done := make(chan Compacted, 1)
	go func() {
		c, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
		done <- c
	}()
	recv(t, waiting, "手动压缩开始等后台那一次")
	close(release)
	c := recv(t, done, "手动压缩返回")
	if eps := h.epochs(); !c.OK || len(eps) != 1 || eps[0].trigger != "early" || c.EpochID != eps[0].id || h.countBodies(compactMarker) != 1 {
		t.Fatalf("用后台那一次的结局：%+v %+v", c, eps)
	}
}

// 后台在关时取消模型、不提交摘要快照；已经派发的请求仍须保留审计。
func TestEarlyShutdown(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`,
		intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(body, compactMarker) {
				entered <- struct{}{}
				<-r.Context().Done()
				return true
			}
			return false
		}})
	h.nearEarly()
	h.send("提前")
	recv(t, entered, "提前压缩的请求")
	h.cancel()
	h.hub.Idle()
	if eps := h.epochs(); len(eps) != 0 {
		t.Fatalf("后台在关不提交摘要快照：%+v", eps)
	}
}
