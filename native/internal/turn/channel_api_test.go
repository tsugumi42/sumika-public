package turn

// 渠道中间件要内核补的三样（规格-渠道中间件第九节第 3、4、6 条）：按会话找正在跑的实例、每个 Respond 恰好一次"回合定了"、
// 场合段；连同独立审查（2026-10-26，四路审查 + 反驳核实）查出的几处：渠道的插话不由内核写库、群里不拼"私下"、
// 请求之前停下算 stopped、停止提醒不被停下的那一轮用掉。对着写明的预期测

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
)

const groupConv = "qq:group:test-9"

// factoryFirstLine 是出厂提示词 prompts/<name>.md 的第一行（不空）
func factoryFirstLine(t *testing.T, name string) string {
	t.Helper()
	repo, err := config.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "prompts", name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	t.Fatalf("%s 是空的", name)
	return ""
}

// store 一条群里的消息（渠道中间件做的事），返回 seq
func (h *harness) storeGroup(text string) int64 {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupConv, ActorKind: "human", ActorID: "p1", Origin: "human", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return seq
}

// storeHers 扮演中间件把她在平台群里说的一句写进库
func (h *harness) storeHers(text string) int64 {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupConv, ActorKind: "character", ActorID: h.c.CharacterID, Origin: "character", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return seq
}

// inGroup 是平台群里有人说了一句：存进库、排上队（scenes 给渠道中间件选的场合段）
func (h *harness) inGroup(text string, scenes []string) <-chan Outcome {
	h.t.Helper()
	seq := h.storeGroup(text)
	return h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{seq}, Text: text,
		Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "public", Scenes: scenes})
}

// notificationLog 用容量 1 的唤醒合并通知；读取累计快照，不靠固定毫秒轮询。
type notificationLog[T any] struct {
	mu      sync.Mutex
	got     []T
	changed chan struct{}
}

func newNotificationLog[T any]() *notificationLog[T] {
	return &notificationLog[T]{changed: make(chan struct{}, 1)}
}

func (s *notificationLog[T]) add(_ context.Context, e T) {
	s.mu.Lock()
	s.got = append(s.got, e)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *notificationLog[T]) snapshot() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]T(nil), s.got...)
}

func (s *notificationLog[T]) wait(t *testing.T, ready func([]T) bool) []T {
	t.Helper()
	timer := time.NewTimer(barrierLimit)
	defer timer.Stop()
	for {
		got := s.snapshot()
		if ready(got) {
			return got
		}
		select {
		case <-s.changed:
		case <-timer.C:
			t.Fatalf("等不到目标通知：%+v", s.snapshot())
		}
	}
}

func (s *notificationLog[T]) waitN(t *testing.T, n int) []T {
	t.Helper()
	return s.wait(t, func(got []T) bool { return len(got) >= n })
}

func (h *harness) watchSettled() *notificationLog[Settled] {
	s := newNotificationLog[Settled]()
	h.use(fakePlugin{"watch", func(r *Registry) (func(), error) {
		r.OnSettled(s.add)
		return nil, nil
	}})
	return s
}

// settledFor 等某个回合的“回合定了”到（最多 barrierLimit）。
func settledFor(t *testing.T, log *notificationLog[Settled], turnID string) Settled {
	t.Helper()
	var target Settled
	log.wait(t, func(got []Settled) bool {
		for _, s := range got {
			if s.TurnID == turnID {
				target = s
				return true
			}
		}
		return false
	})
	return target
}

// holdGroup：群里那个会话的请求卡住，直到 release 关掉或者她那边中止（主窗口的照常回）
func holdGroup(entered chan<- struct{}, release <-chan struct{}) func(context.Context, string) {
	var once sync.Once
	return func(ctx context.Context, body string) {
		if !strings.Contains(body, "群里这句") {
			return
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
}

// 第九节第 4 条：SteerIn、StopIn、InterruptIn、RestartIn 按（角色，会话）找；主窗口的入口碰不到群里那个会话，群里的也碰不到主窗口
func TestControlsByConversation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`,
		onBody:  holdGroup(entered, release),
	})
	got := h.watchSettled()
	group := h.inGroup("群里这句", nil)
	<-entered
	// 群里那个在跑，主窗口没在跑：主窗口的几个入口都碰不到它
	if _, ok := h.hub.Steer(h.c.CharacterID, "给主窗口的"); ok {
		t.Fatal("主窗口没在跑，Steer 不该收下")
	}
	if h.hub.Stop(h.c.CharacterID) {
		t.Fatal("主窗口没在跑，Stop 不该停到群里那个")
	}
	h.hub.Interrupt(h.c.CharacterID)
	select {
	case <-group:
		t.Fatal("主窗口的 Stop、Interrupt 不该让群里那个结束")
	default:
	}
	// 渠道的插话要带中间件存好的 seq，不带不收
	if _, ok := h.hub.SteerIn(h.c.CharacterID, groupConv, "没存的插话", nil); ok {
		t.Fatal("渠道的插话不带 seq，SteerIn 不该收下")
	}
	if _, ok := h.hub.SteerIn(h.c.CharacterID, groupConv, "给群里的插话", []int64{h.storeGroup("给群里的插话")}); !ok {
		t.Fatal("群里那个会话在跑，SteerIn 应该收下")
	}
	// 主窗口这时说一句，照常回完，群里那个还卡着
	consoleTurn := h.send("主窗口这句")
	select {
	case <-group:
		t.Fatal("主窗口那一轮不该让群里那个回完")
	default:
	}
	// 停群里那个：只停它
	if !h.hub.StopIn(h.c.CharacterID, groupConv) {
		t.Fatal("StopIn 应该停得到群里那个")
	}
	r := wait(t, group)
	if !r.Stopped {
		t.Fatal("被 StopIn 停下的那一轮结局应该是 stopped")
	}
	checkSteerOnce(t, h, "给群里的插话", "dropped")
	if s := settledFor(t, got, r.TurnID); s.Outcome != OutcomeStopped || s.Conversation != groupConv || s.Scope != "public" || s.CharacterID != h.c.CharacterID {
		t.Fatalf("群里那一轮的回合定了：%+v，应该是 stopped、%s、public", s, groupConv)
	}
	if s := settledFor(t, got, consoleTurn.TurnID); s.Outcome != OutcomeDone || s.Conversation != consoleConv(h.c.CharacterID) || s.Scope != ConsoleScope {
		t.Fatalf("主窗口那一轮的回合定了：%+v", s)
	}
	if h.hub.StopIn(h.c.CharacterID, groupConv) || h.hub.Stop(h.c.CharacterID) {
		t.Fatal("都不在跑了，Stop 不该返回 true")
	}
	// RestartIn 只丢这个会话的实例
	h.hub.RestartIn(h.c.CharacterID, groupConv)
	h.hub.mu.Lock()
	var groupLeft, consoleLeft int
	for k := range h.hub.live {
		if strings.HasPrefix(k, convKey(h.c.CharacterID, groupConv)+"|") {
			groupLeft++
		}
		if strings.HasPrefix(k, convKey(h.c.CharacterID, consoleConv(h.c.CharacterID))+"|") {
			consoleLeft++
		}
	}
	h.hub.mu.Unlock()
	if groupLeft != 0 || consoleLeft != 1 {
		t.Fatalf("RestartIn 之后群里的实例 %d 个（应该 0）、主窗口的 %d 个（应该 1）", groupLeft, consoleLeft)
	}
}

// 第九节第 4 条：InterruptIn 打断群里那个；没送到的插话退回去（不是丢掉），回合定了是 stopped
func TestInterruptInConversation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
		onBody:  holdGroup(entered, release),
	})
	got := h.watchSettled()
	group := h.inGroup("群里这句", nil)
	<-entered
	h.hub.SteerIn(h.c.CharacterID, groupConv, "排着的插话", []int64{h.storeGroup("排着的插话")})
	h.hub.InterruptIn(h.c.CharacterID, groupConv)
	r := wait(t, group)
	if !r.Stopped {
		t.Fatal("被打断的那一轮结局应该是 stopped")
	}
	checkSteerOnce(t, h, "排着的插话", "dropped+requeue")
	if s := settledFor(t, got, r.TurnID); s.Outcome != OutcomeStopped {
		t.Fatalf("被打断的那一轮回合定了应该是 stopped：%+v", s)
	}
}

// ADR-184 第 3 条（审查查出）：渠道的插话送到时内核不写库、不说"主人亲口说的"；中间件存的那几条记成这一轮用过的
func TestChannelSteerDelivered(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
		onBody:  holdGroup(entered, release),
	})
	group := h.inGroup("群里这句", nil)
	<-entered
	seq := h.storeGroup("[10:00] 小王：顺便问一句")
	if _, ok := h.hub.SteerIn(h.c.CharacterID, groupConv, "[10:00] 小王：顺便问一句", []int64{seq}); !ok {
		t.Fatal("SteerIn 应该收下")
	}
	close(release)
	wait(t, group)
	checkSteerOnce(t, h, "[10:00] 小王：顺便问一句", "delivered")
	if len(h.bodies) != 2 || strings.Count(h.body(1), "顺便问一句") != 1 {
		t.Fatalf("插话应该在第二次请求里出现一次（一共 %d 次请求）", len(h.bodies))
	}
	if strings.Contains(h.body(1), "主人亲口说的") || !strings.Contains(h.body(1), strings.Trim(jsjson.Quote(steerPrefixChannel), `"`)) {
		t.Fatalf("渠道的插话前缀不该说是主人说的：%s", h.lastUser(1))
	}
	var owner, total int
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND actor_kind = 'owner'`, groupConv).Scan(&owner)
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ? AND text = ?`, groupConv, "[10:00] 小王：顺便问一句").Scan(&total)
	if owner != 0 || total != 1 {
		t.Fatalf("内核不该替渠道写库：群里主人说的 %d 条（应该 0）、这句插话 %d 条（应该 1，中间件那条）", owner, total)
	}
	h.hub.mu.Lock()
	done := h.hub.done[convKey(h.c.CharacterID, groupConv)][seq]
	h.hub.mu.Unlock()
	if !done {
		t.Fatal("中间件存的那条插话应该记成这一轮用过的")
	}
}

// 审查查出：请求之前就被停下（在拼参考信息的时候），回合定了是 stopped 不是 error，也不发请求；
// 这时候按的停止，提醒留给下一轮，不被停下的这一轮用掉
func TestStopBeforeRequest(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	got := h.watchSettled()
	inHook := make(chan struct{})
	var once sync.Once
	h.use(contextPlugin("slow-recall", func(ctx context.Context, in ContextIn) (*Contribution, error) {
		first := false
		once.Do(func() { first = true })
		if !first {
			return nil, nil
		}
		close(inHook)
		<-ctx.Done() // 记忆召回慢，停止落在这时候
		return nil, nil
	}))
	o := h.hub.Send(h.c.CharacterID, "第一句")
	<-inHook
	if !h.hub.Stop(h.c.CharacterID) {
		t.Fatal("这一轮在跑，Stop 应该停得到")
	}
	first := <-o
	if first.Result == nil || !first.Result.Stopped || len(h.bodies) != 0 {
		t.Fatalf("请求之前停下：结局应该是 stopped、一次请求都不发（发了 %d 次）：%+v", len(h.bodies), first)
	}
	if s := settledFor(t, got, first.TurnID); s.Outcome != OutcomeStopped {
		t.Fatalf("请求之前停下，回合定了应该是 stopped：%+v", s)
	}
	h.send("第二句")
	// 提醒说的是"第一句"那一轮被停了，要排在第一句后面（被停下的那一轮先用掉了的话，它会排在第一句前面）
	b := h.body(0)
	if r, q := strings.Index(b, "上一轮被所有者手动停止了"), strings.Index(b, `"content":"第一句"`); r < 0 || q < 0 || r < q {
		t.Fatalf("停在拼参考信息那时候的提醒应该留给下一轮、排在第一句后面（提醒在 %d、第一句在 %d）：%s", r, q, b)
	}
}

// 第九节第 6 条：每个 Respond 恰好一次"回合定了"，回合 id 和 Outcome 的一样；提前出错（找不到模型、场合段读不到）也发，结局 error
func TestSettledOncePerRespond(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	got := h.watchSettled()
	// 场合段读不到：建实例之前就出错
	bad := <-h.inGroup("第一句", []string{"no-such-scene"})
	if bad.Err == nil || !strings.Contains(bad.Err.Error(), "no-such-scene") || bad.TurnID == "" {
		t.Fatalf("场合段读不到应该报错、带回合 id：%+v", bad)
	}
	// 找不到模型：读完卡就出错
	h.hub.Cfg.Models = map[string]string{h.c.CharacterID: "no-such-model"}
	noModel := <-h.hub.Send(h.c.CharacterID, "第二句")
	h.hub.Cfg.Models = map[string]string{h.c.CharacterID: h.c.Characters[h.c.CharacterID].Model}
	if noModel.Err == nil || noModel.TurnID == "" {
		t.Fatalf("找不到模型应该报错、带回合 id：%+v", noModel)
	}
	// 正常的一轮
	ok := <-h.hub.Send(h.c.CharacterID, "第三句")
	if ok.Err != nil || ok.TurnID == "" || ok.Result.TurnID != ok.TurnID {
		t.Fatalf("正常的一轮：%+v", ok)
	}
	// 再来一轮当"哨兵"：每个订阅者的队列按先后送，哨兵到了，前面三个回合要是多发过一次，一定排在哨兵前面
	sentinel := <-h.hub.Send(h.c.CharacterID, "第四句")
	settledFor(t, got, sentinel.TurnID)
	want := []struct {
		id, outcome, conv, scope string
	}{
		{bad.TurnID, OutcomeError, groupConv, "public"},
		{noModel.TurnID, OutcomeError, consoleConv(h.c.CharacterID), ConsoleScope},
		{ok.TurnID, OutcomeDone, consoleConv(h.c.CharacterID), ConsoleScope},
		{sentinel.TurnID, OutcomeDone, consoleConv(h.c.CharacterID), ConsoleScope},
	}
	all := got.snapshot()
	if len(all) != len(want) {
		t.Fatalf("四个 Respond 应该各定一次，收到 %d 次：%+v", len(all), all)
	}
	for i, w := range want {
		if s := all[i]; s.TurnID != w.id || s.Outcome != w.outcome || s.Conversation != w.conv || s.Scope != w.scope || s.CharacterID != h.c.CharacterID {
			t.Fatalf("第 %d 个回合定了：%+v，应该是回合 %s、%s、%s、%s", i+1, s, w.id, w.outcome, w.conv, w.scope)
		}
	}
	if bad.TurnID == noModel.TurnID || noModel.TurnID == ok.TurnID {
		t.Fatal("每个回合的 id 应该不一样")
	}
}

// 第九节第 3 条：Scenes 有就按它逐段拼、按给的先后，换掉本地私聊的场合那一块；空的照旧（主窗口 45 份对拍守着）。
// 审查查出：角色卡的"私下"那段，只有平台群不拼（规格-语言 3.3、ADR-123、ADR-140），两头都量
func TestScenesAndPrivateSection(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`})
	const private = "喜欢被夸" // live-second 里铃那张卡的"私下"
	wait(t, h.inGroup("群里第一句", []string{"scene-group", "platform-qq"}))
	sys := h.messages(0)[0]
	at := map[string]int{}
	for _, name := range []string{"scene-group", "platform-qq"} {
		line := strings.Trim(jsjson.Quote(factoryFirstLine(t, name)), `"`)
		if at[name] = strings.Index(sys, line); at[name] < 0 {
			t.Fatalf("系统提示里应该有 %s 那一段：%s", name, sys)
		}
	}
	if at["scene-group"] > at["platform-qq"] {
		t.Fatal("Scenes 应该按给的先后拼")
	}
	if strings.Contains(sys, "本地主窗口里的私聊") {
		t.Fatalf("给了 Scenes 就不该再拼本地私聊的场合：%s", sys)
	}
	if strings.Contains(sys, private) {
		t.Fatalf("QQ 群有外人，不该拼角色卡的私下那段：%s", sys)
	}
	// 主窗口不给 Scenes：照旧是本地私聊的场合，拼私下
	h.send("主窗口这句")
	if s := h.messages(1)[0]; !strings.Contains(s, "本地主窗口里的私聊") || !strings.Contains(s, private) {
		t.Fatalf("主窗口的系统提示应该照旧、带私下：%s", s)
	}
	// 本地群聊不算有外人、QQ 私聊也不算：都拼私下
	for i, aud := range []audience.Audience{
		{Channel: "console", Scene: "group", PrivateOK: true},
		{Channel: "qq", Scene: "private", PersonIDs: []string{"p2"}},
	} {
		conv := []string{"console:" + h.c.CharacterID + ":group:home", "qq:private:p2"}[i]
		seq, _ := h.st.AppendEvent(store.NewEvent{Channel: aud.Channel, Conversation: conv, ActorKind: "human", ActorID: "p2", Origin: "human", Kind: "message", Text: "你好", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
		wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "你好", Audience: aud, Addressed: true, Scope: "s"}))
		if s := h.messages(2 + i)[0]; !strings.Contains(s, private) {
			t.Fatalf("%s 没有外人，应该拼私下：%s", conv, s)
		}
	}
}
