package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 回合的测试用的假东西：假内核（记下每次调用，Commit 照真内核的规矩查）、假写库（不碰库，可以卡住）、假守卫。
// 号、群、名字都是编的（LESSONS）。

// callLog 是按先后记下的调用，几个假东西共用一份，测试能看「谁先谁后」
type callLog struct {
	mu    sync.Mutex
	items []string
}

func (l *callLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, fmt.Sprintf(format, args...))
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}

// count 是以 prefix 开头的有几条
func (l *callLog) count(prefix string) int {
	n := 0
	for _, s := range l.all() {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// index 是第一条以 prefix 开头的位置，没有 -1
func (l *callLog) index(prefix string) int {
	for i, s := range l.all() {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

// fakeKernel 假内核：Respond 记下 Input、当作开始跑；SteerIn 在跑才收；StopIn 在跑才停，停下以后（autoStop）异步发"回合定了：stopped"；
// Commit 照真内核的规矩查（见 commit）
type fakeKernel struct {
	t    *testing.T
	log  *callLog
	st   *store.Store // 非 nil：Commit 要查库
	r    *Responder
	mu   sync.Mutex
	n    int
	run  map[string]*fakeTurn // 会话 → 正在跑的
	ins  []turn.Input
	used map[string]int64 // 会话 → 这一回合用过的事件里最大的 seq
	// 可以改的行为
	steerOK    bool
	autoStop   bool
	commitErr  error
	commitDone func(characterID, conversation, turnID string, lines []turn.Committed) // Commit 报成功之前调（测顺序）
	onStop     func()                                                                 // 停下的时候（返回之前）调：真内核在这里丢掉没送到的插话
	commits    []commitCall
	steerSeqs  [][]int64
	seen       []turn.Seen
}

type fakeTurn struct {
	id  string
	in  turn.Input
	out chan turn.Outcome
}

type commitCall struct {
	TurnID string
	Lines  []turn.Committed
}

func newFakeKernel(t *testing.T, log *callLog, st *store.Store) *fakeKernel {
	return &fakeKernel{t: t, log: log, st: st, run: map[string]*fakeTurn{}, used: map[string]int64{}, steerOK: true, autoStop: true}
}
func (k *fakeKernel) NotifySeen(e turn.Seen) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen = append(k.seen, e)
}

func (k *fakeKernel) seenEvents() []turn.Seen {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]turn.Seen(nil), k.seen...)
}

func (k *fakeKernel) Respond(in turn.Input) <-chan turn.Outcome {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n++
	id := fmt.Sprintf("t%d", k.n)
	ft := &fakeTurn{id: id, in: in, out: make(chan turn.Outcome, 1)}
	in.Audience.Conversation = in.Conversation // 真内核也这样改
	ft.in = in
	k.run[in.CharacterID+"|"+in.Conversation] = ft
	k.ins = append(k.ins, in)
	k.used[in.CharacterID+"|"+in.Conversation] = 0
	for _, s := range in.Seqs {
		k.used[in.CharacterID+"|"+in.Conversation] = max(k.used[in.CharacterID+"|"+in.Conversation], s)
	}
	k.log.add("respond:%s", id)
	return ft.out
}

func (k *fakeKernel) SteerIn(characterID, conversation, text string, seqs []int64) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := characterID + "|" + conversation
	if k.run[key] == nil || !k.steerOK || len(seqs) == 0 {
		k.log.add("steer-refused")
		return "", false
	}
	k.n++
	id := fmt.Sprintf("s%d", k.n)
	for _, s := range seqs {
		k.used[key] = max(k.used[key], s)
	}
	k.steerSeqs = append(k.steerSeqs, seqs)
	k.log.add("steer:%s", id)
	return id, true
}

func (k *fakeKernel) StopIn(characterID, conversation string) bool {
	k.mu.Lock()
	key := characterID + "|" + conversation
	ft := k.run[key]
	if ft == nil {
		k.mu.Unlock()
		k.log.add("stop-idle")
		return false
	}
	delete(k.run, key)
	auto := k.autoStop
	k.mu.Unlock()
	k.log.add("stop:%s", ft.id)
	if f := k.onStop; f != nil {
		f()
	}
	if auto {
		go k.settleTurn(ft, characterID, turn.OutcomeStopped, nil, nil)
	}
	return true
}

func (k *fakeKernel) RestartIn(characterID, conversation string) { k.log.add("restart") }

func (k *fakeKernel) Commit(characterID, conversation, turnID string, lines []turn.Committed) error {
	k.log.add("commit:%s:%d", turnID, len(lines))
	k.mu.Lock()
	k.commits = append(k.commits, commitCall{turnID, lines})
	err, used := k.commitErr, k.used[characterID+"|"+conversation]
	done := k.commitDone
	k.mu.Unlock()
	if err != nil {
		return err
	}
	// 真内核的规矩：在库里、在这个会话、是她说出口的那一行、字一样、比这一回合用过的事件都晚、一步一行、两步不共用一行
	seenStep, seenSeq := map[int]bool{}, map[int64]bool{}
	for _, c := range lines {
		if seenStep[c.Step] || seenSeq[c.Seq] || c.Seq == 0 {
			return fmt.Errorf("假内核：第 %d 步交回的 seq %d 重复或为 0", c.Step, c.Seq)
		}
		seenStep[c.Step], seenSeq[c.Seq] = true, true
		if c.Seq <= used {
			return fmt.Errorf("假内核：seq %d 不比这一回合用过的 %d 晚", c.Seq, used)
		}
		if k.st != nil {
			var conv, origin, text string
			err := k.st.Events.QueryRow(`SELECT conversation, COALESCE(origin, ''), text FROM events WHERE seq = ?`, c.Seq).Scan(&conv, &origin, &text)
			if err != nil {
				return fmt.Errorf("假内核：seq %d 不在库里：%w", c.Seq, err)
			}
			if conv != conversation || origin != "character" || text != c.Text {
				return fmt.Errorf("假内核：seq %d 在库里不是她说出口的那一行或者字不一样（会话 %s 来源 %s）", c.Seq, conv, origin)
			}
		}
	}
	if done != nil {
		done(characterID, conversation, turnID, lines)
	}
	k.log.add("commit-ok:%s", turnID)
	return nil
}

// settleTurn 是一个回合定了：告诉通道、告诉订阅者
func (k *fakeKernel) settleTurn(ft *fakeTurn, character, outcome string, said []turn.Said, err error) {
	ft.out <- turn.Outcome{TurnID: ft.id, Err: err}
	k.r.Settled(turn.Settled{
		TurnID: ft.id, CharacterID: character, Conversation: ft.in.Conversation, Scope: ft.in.Scope, Outcome: outcome, Err: err,
		Said: said, Audience: ft.in.Audience, Scenes: ft.in.Scenes,
	})
}

// finish 让这个会话正在跑的回合定了
func (k *fakeKernel) finish(character, conversation, outcome string, said ...turn.Said) {
	k.t.Helper()
	k.finishErr(character, conversation, outcome, nil, said...)
}

func (k *fakeKernel) finishErr(character, conversation, outcome string, err error, said ...turn.Said) {
	k.t.Helper()
	k.mu.Lock()
	key := character + "|" + conversation
	ft := k.run[key]
	delete(k.run, key)
	k.mu.Unlock()
	if ft == nil {
		k.t.Fatalf("会话 %s 没有在跑的回合", conversation)
	}
	k.settleTurn(ft, character, outcome, said, err)
}

func (k *fakeKernel) inputs() []turn.Input {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]turn.Input(nil), k.ins...)
}

func (k *fakeKernel) commitList() []commitCall {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]commitCall(nil), k.commits...)
}

// fakeWriter 假写库：不碰库，记下收到的；hold 非 nil 时 SendHeld 先卡在那里
type fakeWriter struct {
	log  *callLog
	mu   sync.Mutex
	recs []recCall
	sent [][]types.Part
	seq  int64
	hold chan struct{}
}

type recCall struct{ Kind, Reason string }

func (w *fakeWriter) SendHeld(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part) (Held, error) {
	w.mu.Lock()
	hold := w.hold
	w.mu.Unlock()
	if hold != nil {
		w.log.add("write-wait")
		select {
		case <-hold:
		case <-ctx.Done():
			return Held{}, ErrClosed
		}
	}
	w.mu.Lock()
	w.seq++
	seq := 1000 + w.seq
	w.sent = append(w.sent, parts)
	w.mu.Unlock()
	w.log.add("write:%d", seq)
	return Held{PlanID: fmt.Sprintf("p%d", seq), EventSeq: seq, start: func() error { w.log.add("start:%d", seq); return nil }}, nil
}

func (w *fakeWriter) Record(ctx context.Context, characterID, origin string, conv types.Conversation, kind, reason string) (string, error) {
	w.mu.Lock()
	w.recs = append(w.recs, recCall{kind, reason})
	w.mu.Unlock()
	w.log.add("record:%s", kind)
	return "r", nil
}

func (w *fakeWriter) records() []recCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]recCall(nil), w.recs...)
}

// fakeGuard 假守卫：文字里有 blockOn 里任何一个词就拦下（返回记忆编号 m1），err 非 nil 就报错，route 是它报的路
type fakeGuard struct {
	mu      sync.Mutex
	blockOn []string
	err     error
	panics  bool
	route   string
	// routePanics 让 GuardRoute 自己 panic
	routePanics bool
	seen        []string
	slow        time.Duration
}

func (g *fakeGuard) CheckOutbound(ctx context.Context, text string, a audience.Audience) ([]string, error) {
	g.mu.Lock()
	g.seen = append(g.seen, text)
	blockOn, err, panics, slow := g.blockOn, g.err, g.panics, g.slow
	g.mu.Unlock()
	if panics {
		panic("假守卫炸了")
	}
	if slow > 0 {
		select {
		case <-time.After(slow):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	for _, w := range blockOn {
		if strings.Contains(text, w) {
			return []string{"m1"}, nil
		}
	}
	return nil, nil
}

func (g *fakeGuard) GuardRoute() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.routePanics {
		panic("假守卫报路炸了")
	}
	return g.route
}

func (g *fakeGuard) set(f func(*fakeGuard)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *fakeGuard) texts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...)
}

var errBoom = errors.New("炸了")
