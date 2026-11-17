package qq

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 戳回去的测试：假出口，通知手工造。号、群都是编的

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const (
	herAcct = "20001"
	friendA = "30001"
	friendB = "30002"
	friendC = "30003"
	friendD = "30004"
)

var (
	grp    = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5001"}
	grp2   = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5002"}
	priv   = types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: friendA, Self: herAcct}
	pokeAt = func(conv types.Conversation, actor, target string, at time.Time) types.Batch {
		n := types.Notice{Kind: types.NoticePoke, Account: herAcct, Conv: conv, Time: at, Actor: actor, Target: target}
		return types.Batch{Conv: conv, Character: "ca", Account: herAcct, Now: at, Trigger: types.NoticePoke, Notice: &n}
	}
)

type send struct {
	Character, Origin string
	Conv              types.Conversation
	Parts             []types.Part
}

type fakeOut struct {
	mu    sync.Mutex
	sends []send
	err   error
}

func (f *fakeOut) Send(ctx context.Context, character, origin string, conv types.Conversation, parts []types.Part) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.sends = append(f.sends, send{character, origin, conv, parts})
	return fmt.Sprintf("plan-%d", len(f.sends)), nil
}

func (f *fakeOut) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func newPoke(t *testing.T, tweak func(*PokeConfig)) (*Poke, *fakeOut) {
	t.Helper()
	out := &fakeOut{}
	cfg := PokeConfig{Out: out}
	if tweak != nil {
		tweak(&cfg)
	}
	p, err := NewPoke(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, out
}

func run(p *Poke, b types.Batch) types.Verdict {
	return p.Interceptor().Intercept(context.Background(), b)
}

func wantBack(t *testing.T, v types.Verdict, out *fakeOut, sends int) {
	t.Helper()
	if !v.Taken || out.count() != sends {
		t.Fatalf("该戳回去，发了 %d 次（想要 %d）：%+v", out.count(), sends, v)
	}
}

func wantNot(t *testing.T, v types.Verdict, out *fakeOut, sends int, reasonHas string) {
	t.Helper()
	if v.Taken || out.count() != sends {
		t.Fatalf("不该戳回去，发了 %d 次（想要 %d）：%+v", out.count(), sends, v)
	}
	if !strings.Contains(v.Reason, reasonHas) {
		t.Fatalf("原因里该有 %q：%q", reasonHas, v.Reason)
	}
}

// 有人戳她：戳回去，发的是 poke 段、戳那个人，来源 automation:poke，发给这个角色在这个会话里；群和私聊都一样
func TestPokeBack(t *testing.T) {
	for name, conv := range map[string]types.Conversation{"群": grp, "私聊": priv} {
		t.Run(name, func(t *testing.T) {
			p, out := newPoke(t, nil)
			v := run(p, pokeAt(conv, friendA, herAcct, t0))
			wantBack(t, v, out, 1)
			s := out.sends[0]
			if s.Character != "ca" || s.Origin != "automation:poke" || s.Conv != conv || len(s.Parts) != 1 || !reflect.DeepEqual(s.Parts[0], types.Part{Kind: types.PartPoke, Ref: friendA}) {
				t.Fatalf("发出去的：%+v", s)
			}
			if !strings.Contains(v.Reason, friendA) {
				t.Fatalf("日志里要看得出是谁戳的：%q", v.Reason)
			}
		})
	}
}

// 同一个人 60 秒内只戳回一次，再戳不回、原因里写限流；过了 60 秒又戳回；别人不受影响；别的会话不受影响
func TestSamePersonCooldown(t *testing.T) {
	p, out := newPoke(t, nil)
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
	v := run(p, pokeAt(grp, friendA, herAcct, t0.Add(59*time.Second)))
	wantNot(t, v, out, 1, "同一个人 60 秒内只戳回一次")
	if !strings.Contains(v.Reason, friendA) || !strings.Contains(v.Reason, "还要等 1 秒") {
		t.Fatalf("原因要说清是谁、还要等多久：%q", v.Reason)
	}
	wantBack(t, run(p, pokeAt(grp, friendB, herAcct, t0.Add(59*time.Second))), out, 2)
	wantBack(t, run(p, pokeAt(grp2, friendA, herAcct, t0.Add(59*time.Second))), out, 3)
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(60*time.Second))), out, 4)
}

// 被限流挡住的戳不刷新冷却：一直戳的人，每 60 秒只能换来一次
func TestRefusedPokeDoesNotRestartCooldown(t *testing.T) {
	p, out := newPoke(t, nil)
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
	for i := 1; i <= 5; i++ {
		wantNot(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(time.Duration(i*10)*time.Second))), out, 1, "")
	}
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(60*time.Second))), out, 2)
}

// 同一个会话每分钟最多戳回 3 次，第 4 个人不回；一分钟滑出去以后又可以
func TestPerConversationLimit(t *testing.T) {
	p, out := newPoke(t, nil)
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
	wantBack(t, run(p, pokeAt(grp, friendB, herAcct, t0.Add(10*time.Second))), out, 2)
	wantBack(t, run(p, pokeAt(grp, friendC, herAcct, t0.Add(20*time.Second))), out, 3)
	v := run(p, pokeAt(grp, friendD, herAcct, t0.Add(30*time.Second)))
	wantNot(t, v, out, 3, "一分钟内已经戳回 3 次")
	if !strings.Contains(v.Reason, friendD) {
		t.Fatalf("原因里要有是谁：%q", v.Reason)
	}
	// 别的会话不受影响
	wantBack(t, run(p, pokeAt(grp2, friendD, herAcct, t0.Add(30*time.Second))), out, 4)
	// 第一次戳回是 t0，到 t0+60s 它滑出窗口，空出一个名额
	wantBack(t, run(p, pokeAt(grp, friendD, herAcct, t0.Add(60*time.Second))), out, 5)
}

// 数字都可以调
func TestLimitsAreConfigurable(t *testing.T) {
	p, out := newPoke(t, func(c *PokeConfig) { c.Cooldown = 5 * time.Second; c.PerMinute = 1 })
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
	wantNot(t, run(p, pokeAt(grp, friendB, herAcct, t0.Add(time.Second))), out, 1, "一分钟内已经戳回 1 次")
	wantNot(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(2*time.Second))), out, 1, "同一个人 5 秒内")
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(61*time.Second))), out, 2)
}

// 对方是机器人（名单和按行为认出的模板号）不戳回；判断拿到的是（渠道、号）
func TestBotIsNotPokedBack(t *testing.T) {
	var gotChannel string
	p, out := newPoke(t, func(c *PokeConfig) {
		c.IsBot = func(channel, user string) bool { gotChannel = channel; return user == friendB }
	})
	wantNot(t, run(p, pokeAt(grp, friendB, herAcct, t0)), out, 0, "机器人")
	if gotChannel != "qq" {
		t.Fatalf("机器人名单按（渠道，号）查：%q", gotChannel)
	}
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
	// 机器人被挡住不占限流名额
	wantBack(t, run(p, pokeAt(grp, friendC, herAcct, t0)), out, 2)
}

// 戳的是别人、她自己戳出去的回声、自己戳自己：都不回
func TestOnlyPokesAtHerAreAnswered(t *testing.T) {
	p, out := newPoke(t, nil)
	wantNot(t, run(p, pokeAt(grp, friendA, friendB, t0)), out, 0, "戳的不是她") // 群友戳群友
	wantNot(t, run(p, pokeAt(grp, herAcct, friendA, t0)), out, 0, "戳的不是她") // 她戳出去的回声
	wantNot(t, run(p, pokeAt(grp, herAcct, herAcct, t0)), out, 0, "她自己戳的") // 自己戳自己
	wantNot(t, run(p, pokeAt(grp, friendA, "20002", t0)), out, 0, "戳的不是她") // 戳的是同群另一个角色的号：这条通知也会被那个号收到，由它自己的链去回
	// 以上都没占名额：真正戳她的还能回
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 1)
}

// 一个角色在同一个群里有两个号（20001、20003）：戳她的任何一个号都回，不管这份通知是哪个号收到的
// （入库一站只把先到的那份交给链，先到的不一定是被戳的那个号）；她自己的另一个号戳她、同群另一个角色的号被戳，都不回
func TestPokeAtAnyAccountOfHers(t *testing.T) {
	const second = "20003"
	at := func(recv, actor, target string) types.Batch {
		b := pokeAt(grp, actor, target, t0)
		b.Notice.Account, b.Account, b.Mine = recv, recv, []string{herAcct, second}
		return b
	}
	for name, tc := range map[string]struct {
		b    types.Batch
		back bool
		why  string
	}{
		"戳第二个号，第一个号收到的": {at(herAcct, friendA, second), true, ""},
		"戳第一个号，第二个号收到的": {at(second, friendA, herAcct), true, ""},
		"戳第二个号，第二个号收到的": {at(second, friendA, second), true, ""},
		"她的另一个号戳她":      {at(herAcct, second, herAcct), false, "她自己戳的"},
		"她的两个号互戳的回声":    {at(second, herAcct, second), false, "她自己戳的"},
		"戳同群另一个角色的号":    {at(herAcct, friendA, "20002"), false, "戳的不是她"},
		"她的号戳了别人（回声）":   {at(second, second, friendA), false, "戳的不是她"},
	} {
		t.Run(name, func(t *testing.T) {
			p, out := newPoke(t, nil)
			v := run(p, tc.b)
			if tc.back {
				wantBack(t, v, out, 1)
			} else {
				wantNot(t, v, out, 0, tc.why)
			}
		})
	}
}

// 同一条通知的两份（她的两个号各收到一份）万一都交到了链里（比如戳的人是另一个角色，不存库就没有去重），只戳回一次：
// 第二份被"同一个人"的冷却挡住
func TestTwoCopiesOfOnePokeAnswerOnce(t *testing.T) {
	p, out := newPoke(t, nil)
	first, second := pokeAt(grp, friendA, herAcct, t0), pokeAt(grp, friendA, herAcct, t0)
	first.Mine, second.Mine = []string{herAcct, "20003"}, []string{herAcct, "20003"}
	second.Notice.Account, second.Account = "20003", "20003"
	wantBack(t, run(p, first), out, 1)
	wantNot(t, run(p, second), out, 1, "同一个人")
}

// 两个角色同群，状态各自的：一个人戳两个角色，各回一次
func TestStateIsPerCharacter(t *testing.T) {
	p, out := newPoke(t, nil)
	a := pokeAt(grp, friendA, herAcct, t0)
	b := pokeAt(grp, friendA, "20002", t0)
	b.Character, b.Account, b.Notice.Account = "cb", "20002", "20002"
	wantBack(t, run(p, a), out, 1)
	wantBack(t, run(p, b), out, 2)
	if out.sends[1].Character != "cb" {
		t.Fatalf("第二次是 cb 回的：%+v", out.sends[1])
	}
	wantNot(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(time.Second))), out, 2, "同一个人")
}

// 出口发不出去（渠道断开、发送检查没过、档位变了）：不算戳回去，不记冷却，下一次戳可以再回
func TestSendFailureDoesNotUseUpTheCooldown(t *testing.T) {
	p, out := newPoke(t, nil)
	out.err = errors.New("发送检查没过：风控间隔")
	wantNot(t, run(p, pokeAt(grp, friendA, herAcct, t0)), out, 0, "风控间隔")
	out.err = nil
	wantBack(t, run(p, pokeAt(grp, friendA, herAcct, t0.Add(time.Second))), out, 1)
}

// 链取消了 ctx（超时、渠道关了）以后，不能再通过出口发东西
func TestNoPokeAfterContextDone(t *testing.T) {
	p, out := newPoke(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v := p.Interceptor().Intercept(ctx, pokeAt(grp, friendA, herAcct, t0)); v.Taken || out.count() != 0 {
		t.Fatalf("%+v %d", v, out.count())
	}
}

// 不是戳一戳的通知、没有通知的批次：当没接，不 panic
func TestOtherBatches(t *testing.T) {
	p, out := newPoke(t, nil)
	wantNot(t, run(p, types.Batch{Conv: grp, Trigger: types.TriggerMessage}), out, 0, "不是戳一戳")
	b := pokeAt(grp, friendA, herAcct, t0)
	b.Notice.Kind = types.NoticeRecall
	wantNot(t, run(p, b), out, 0, "不是戳一戳")
}

// 很多人戳过她以后，状态不会一直长
func TestStateIsPruned(t *testing.T) {
	p, out := newPoke(t, func(c *PokeConfig) { c.PerMinute = 1 << 30 })
	for i := 0; i < 1000; i++ {
		run(p, pokeAt(grp, fmt.Sprintf("%d", 40000+i), herAcct, t0.Add(time.Duration(i)*time.Second)))
	}
	if out.count() != 1000 {
		t.Fatalf("每个人都该回一次：%d", out.count())
	}
	p.mu.Lock()
	n, m := len(p.byPerson), len(p.byConv[fmt.Sprintf("ca\x00%s", grp.String())])
	p.mu.Unlock()
	if n > 300 || m > 61 {
		t.Fatalf("状态没清：byPerson %d，byConv %d", n, m)
	}
}

// 并发（不同会话同时跑）：-race 下不出数据竞争
func TestConcurrent(t *testing.T) {
	p, out := newPoke(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: fmt.Sprintf("%d", 6000+i)}
			if v := run(p, pokeAt(conv, friendA, herAcct, t0)); !v.Taken {
				t.Errorf("会话 %d：%+v", i, v)
			}
		}(i)
	}
	wg.Wait()
	if out.count() != 20 {
		t.Fatalf("%d", out.count())
	}
}

// 插件声明：名字、优先级 10、QQ 的私聊和群、只关心戳一戳；没有出口造不出来
func TestInterceptorDeclaration(t *testing.T) {
	p, _ := newPoke(t, nil)
	d := p.Interceptor()
	if d.Name != "poke" || d.Priority != 10 || len(d.Triggers) != 1 || d.Triggers[0] != types.NoticePoke || len(d.Occasions) != 2 {
		t.Fatalf("%+v", d)
	}
	for _, c := range []types.Conversation{grp, priv} {
		ok := false
		for _, o := range d.Occasions {
			ok = ok || o.Matches(c)
		}
		if !ok {
			t.Fatalf("该在 %+v 里跑", c)
		}
	}
	if d.Occasions[0].Matches(types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "g"}) || d.Occasions[1].Matches(types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "g"}) {
		t.Fatal("戳一戳是 QQ 独有的，本地群聊不跑")
	}
	if len(d.DefaultOn) != 1 || d.DefaultOn[0] != (types.Occasion{Channel: "qq", Scene: types.ScenePrivate}) {
		t.Fatalf("戳回去只在私聊里默认开，群里默认关（ADR-189）：%+v", d.DefaultOn)
	}
	if _, err := NewPoke(PokeConfig{}); err == nil {
		t.Fatal("没有出口该报错")
	}
}
