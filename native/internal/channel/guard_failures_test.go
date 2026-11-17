package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 回合守卫的失败分支按场合补齐（Sol 覆盖表第 6、7 条）：原来只在 QQ 群测了出错 / panic / 超时、
// 改写后的字加累计（settle_test.go 的 TestGuardFailureBlocksWithReason、TestGuardSeesRewrittenAndAccumulated）。
// 这里补本地群聊、QQ 私聊（朋友、主人各一格）；本地私聊的生产路径是直接跳过守卫，只验证跳过。号、话都是编的。

// guardOccasion 是一格场合：会话加说话的人
type guardOccasion struct {
	name string
	conv types.Conversation
	who  string
}

var guardOccasions = []guardOccasion{
	{"本地群聊", convLocalG, whoFriend},
	{"QQ私聊朋友", convFriend, whoFriend},
	{"QQ私聊主人", convOwnerPM, whoOwner},
}

// gro 是 ro 加一个本地渠道（console）的假适配器：本地群聊要真的走到出口，没有它写库那一步就报「没有适配器」，测不出守卫
type gro struct {
	*ro
	con *fake.Adapter
}

func newGRO(t *testing.T, tweak ...func(*ResponderConfig)) *gro {
	t.Helper()
	con := fake.New("console", types.Caps{})
	x := newROWith(t, func(c *OutletConfig) {
		c.Adapters = append(c.Adapters, con)
		c.Roster.Accounts = append(append([]Account(nil), c.Roster.Accounts...), Account{"console", "ca", "ca"})
	}, tweak...)
	con.Now = x.clk.Now
	con.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		return fake.Confirmed(c.Part.ID, fmt.Sprintf("%d", 7000+c.N), c.At), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); con.Run(ctx, &fake.Recorder{}) }()
	select {
	case <-con.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("本地渠道的假适配器没起来")
	}
	t.Cleanup(func() { cancel(); <-done })
	return &gro{ro: x, con: con}
}

// sent 是两个渠道一共发出去的段数
func (x *gro) sent() int { return len(x.ad.Sent()) + len(x.con.Sent()) }

// 守卫出错、超时、panic：每个场合都拦下，reason 记「守卫没判成」，不写库、不发，日志不含原文和守卫的报错
func TestGuardFailureBlocksInEveryOccasion(t *testing.T) {
	t.Parallel()
	for _, oc := range guardOccasions {
		for _, tc := range []struct {
			name string
			set  func(*fakeGuard)
			cfg  func(*ResponderConfig)
		}{
			{"出错", func(g *fakeGuard) { g.err = errBoom }, nil},
			{"panic", func(g *fakeGuard) { g.panics = true }, nil},
			{"超时", func(g *fakeGuard) { g.slow = 5 * time.Second }, func(c *ResponderConfig) { c.GuardTimeout = 30 * time.Millisecond }},
		} {
			t.Run(oc.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var tweak []func(*ResponderConfig)
				if tc.cfg != nil {
					tweak = append(tweak, tc.cfg)
				}
				x := newGRO(t, tweak...)
				x.guard.set(tc.set)
				x.start(oc.conv, x.say(oc.conv, oc.who, "在吗", true))
				x.settleAndWait(oc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: "绝密内容"})
				if hers := x.hers(oc.conv); len(hers) != 0 {
					t.Fatalf("守卫没判成不该放行：%+v", hers)
				}
				ps := x.plans(oc.conv)
				if len(ps) != 1 || ps[0].Kind != "blocked" || ps[0].Reason != "守卫没判成" {
					t.Fatalf("计划：%+v", ps)
				}
				if log := x.logText(); strings.Contains(log, "绝密") || strings.Contains(log, "炸了") || strings.Contains(log, "假守卫炸了") {
					t.Fatalf("日志里不该有原文和守卫的报错：%s", log)
				}
				if x.sent() != 0 {
					t.Fatal("不该发")
				}
				if cs := x.k.commitList(); len(cs) != 1 || len(cs[0].Lines) != 0 {
					t.Fatalf("拦下的句子不能交回内核当成说过：%+v", cs)
				}
			})
		}
	}
}

// 对照：同样的场合、守卫放行时这一句真的写进库。没有这一条，上面「没写库」可能只是这个场合本来就写不进去
func TestGuardFailureControlWritesWhenClear(t *testing.T) {
	t.Parallel()
	for _, oc := range guardOccasions {
		t.Run(oc.name, func(t *testing.T) {
			t.Parallel()
			x := newGRO(t)
			x.start(oc.conv, x.say(oc.conv, oc.who, "在吗", true))
			x.settleAndWait(oc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: "编的一句话"})
			if hers := x.hers(oc.conv); len(hers) != 1 || hers[0].Text != "编的一句话" {
				t.Fatalf("守卫放行时应写库：%+v 计划=%+v", hers, x.plans(oc.conv))
			}
			eventually(t, "放行的一句发出去", func() bool { return x.sent() == 1 }, func() string { return x.logText() })
		})
	}
}

// 按角色取守卫的函数自己 panic：和守卫 panic 一样拦下，不写库、不发、不交回内核，屏障照样放开（Idle 能返回）
func TestGuardForPanicBlocksInEveryOccasion(t *testing.T) {
	t.Parallel()
	for _, oc := range guardOccasions {
		t.Run(oc.name, func(t *testing.T) {
			t.Parallel()
			x := newGRO(t, func(c *ResponderConfig) {
				c.GuardFor = func(string) Guard { panic("取守卫炸了") }
			})
			x.start(oc.conv, x.say(oc.conv, oc.who, "在吗", true))
			x.settleAndWait(oc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: "绝密内容"})
			if hers := x.hers(oc.conv); len(hers) != 0 {
				t.Fatalf("取守卫 panic 不该放行：%+v", hers)
			}
			ps := x.plans(oc.conv)
			if len(ps) != 1 || ps[0].Kind != "blocked" || ps[0].Reason != "守卫没判成" {
				t.Fatalf("计划：%+v", ps)
			}
			if x.sent() != 0 {
				t.Fatal("不该发")
			}
			if log := x.logText(); strings.Contains(log, "绝密") {
				t.Fatalf("日志里不该有原文：%s", log)
			}
		})
	}
}

// 记守卫走哪条路这一步出意外（守卫的 GuardRoute panic、提醒主人的 OnGuardRoute panic）：判决已经出来了也按没判成拦下，
// 不能冲到 process 的兜底（那样这一回合不记计划、也不交回内核）
func TestGuardRouteNotePanicBlocksInEveryOccasion(t *testing.T) {
	t.Parallel()
	for _, oc := range guardOccasions {
		for _, tc := range []struct {
			name string
			set  func(*fakeGuard)
			cfg  func(*ResponderConfig)
		}{
			{"GuardRoute panic", func(g *fakeGuard) { g.routePanics = true }, nil},
			{"OnGuardRoute panic", func(g *fakeGuard) { g.route = GuardRouteEmbedDown },
				func(c *ResponderConfig) { c.OnGuardRoute = func(string, string) { panic("提醒炸了") } }},
		} {
			t.Run(oc.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var tweak []func(*ResponderConfig)
				if tc.cfg != nil {
					tweak = append(tweak, tc.cfg)
				}
				x := newGRO(t, tweak...)
				x.guard.set(tc.set)
				x.start(oc.conv, x.say(oc.conv, oc.who, "在吗", true))
				x.settleAndWait(oc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: "编的一句话"})
				if hers := x.hers(oc.conv); len(hers) != 0 {
					t.Fatalf("记路径出意外不该放行：%+v", hers)
				}
				ps := x.plans(oc.conv)
				if len(ps) != 1 || ps[0].Kind != "blocked" || ps[0].Reason != "守卫没判成" {
					t.Fatalf("计划：%+v", ps)
				}
				if x.sent() != 0 {
					t.Fatal("不该发")
				}
				if cs := x.k.commitList(); len(cs) != 1 || len(cs[0].Lines) != 0 {
					t.Fatalf("拦下的句子不能交回内核当成说过：%+v", cs)
				}
				if log := x.logText(); strings.Contains(log, "炸了") || strings.Contains(log, "编的一句话") {
					t.Fatalf("日志里不该有原文和 panic 内容：%s", log)
				}
			})
		}
	}
}

// 本地私聊的生产路径直接跳过守卫：守卫就算会出错、会 panic、会超时也不调用，原样放行
func TestGuardLocalPrivateSkipsEvenWhenGuardFails(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"出错", "panic", "超时", "命中"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var x *ro
			x = newRO(t, func(c *ResponderConfig) {
				c.GuardTimeout = 30 * time.Millisecond
				c.GuardFor = func(string) Guard {
					t.Error("本地私聊不该去取守卫")
					return x.guard // 万一取了，它会按 mode 出错 / panic / 超时 / 命中，结果就不是空串
				}
			})
			x.guard.set(func(g *fakeGuard) {
				switch mode {
				case "出错":
					g.err = errBoom
				case "panic":
					g.panics = true
				case "超时":
					g.slow = 5 * time.Second
				case "命中":
					g.blockOn = []string{"编的私事"}
				}
			})
			a := audience.Audience{Channel: "console", Scene: types.ScenePrivate, Conversation: convLocalPM.String()}
			if got := x.r.guard("编的私事", []string{"前一句"}, turn.Settled{CharacterID: "ca", Conversation: convLocalPM.String(), Audience: a}); got != "" {
				t.Fatalf("本地私聊该跳过守卫：%q", got)
			}
			if seen := x.guard.texts(); len(seen) != 0 {
				t.Fatalf("本地私聊不该调守卫：%q", seen)
			}
		})
	}
}

// 守卫看到的是改写以后的字，并把这一回合前面放行的句子接在前面：本地群聊、QQ 私聊也一样。
// 本地群聊给一个会去掉句号的改写，才看得出「看的是改写后还是原文」
func TestGuardSeesRewrittenAndAccumulatedInEveryOccasion(t *testing.T) {
	t.Parallel()
	trimStop := Shaper{Rewrite: func(s string) string { return strings.TrimSuffix(s, "。") }}
	for _, oc := range guardOccasions {
		t.Run(oc.name, func(t *testing.T) {
			x := newGRO(t, func(c *ResponderConfig) {
				c.Shapers = map[string]Shaper{"qq": QQShaper, "console": trimStop}
			})
			x.guard.set(func(g *fakeGuard) { g.blockOn = []string{"周五\n考数学"} }) // 两句接起来（换行隔开）才命中
			x.start(oc.conv, x.say(oc.conv, oc.who, "他什么时候考试", true))
			x.settleAndWait(oc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: "他周五。"}, turn.Said{Step: 1, Text: "考数学。"})
			seen := x.guard.texts()
			if len(seen) != 2 || seen[0] != "他周五" || seen[1] != "他周五\n考数学" {
				t.Fatalf("守卫看到的：%q（该是改写以后的字，第二句带着前面放行的）", seen)
			}
			if hers := x.hers(oc.conv); len(hers) != 1 || hers[0].Text != "他周五" {
				t.Fatalf("库里：%+v", hers)
			}
			if ps := x.plans(oc.conv); len(ps) != 2 || ps[1].Kind != "blocked" {
				t.Fatalf("计划：%+v", ps)
			}
		})
	}
}

// ---------- 引用文字和合并转发（guardActions，outlet_guard.go）----------
// 原来只在 QQ 群测了（outlet_actions_test.go 的 TestActionGuard）。这里补本地群聊、QQ 私聊（朋友、主人）的
// 命中 / 缺守卫 / 取守卫函数 panic / 出错 / panic / 中途取消，都要整条计划不发、不落原文；本地私聊按设计跳过守卫。

// cancelGuard 检查时先把 ctx 取消，再装作放行（取消得靠守卫自己看 ctx，不能靠发送前就失败来凑数）
type cancelGuard struct{ cancel context.CancelFunc }

func (g cancelGuard) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	g.cancel()
	return nil, nil
}

// newActionOH 是带本地渠道（console）假适配器的出口夹具；本地渠道的号 ca 绑给角色 ca
func newActionOH(t *testing.T, tweak ...ohOpt) (*oh, *fake.Adapter) {
	t.Helper()
	con := fake.New("console", actionCaps())
	h := newOH(t, actionCaps(), append([]ohOpt{func(c *OutletConfig) {
		c.Adapters = append(c.Adapters, con)
		c.Roster.Accounts = append(append([]Account(nil), c.Roster.Accounts...), Account{"console", "ca", "ca"})
	}}, tweak...)...)
	con.Now = h.clk.Now
	h.run(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); con.Run(ctx, &fake.Recorder{}) }()
	select {
	case <-con.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("本地渠道的假适配器没起来")
	}
	t.Cleanup(func() { cancel(); <-done })
	return h, con
}

func actionPart(kind string) types.Part {
	if kind == types.PartMessage {
		return types.Part{Kind: kind, Body: types.TextBody("第二段"), Quote: "-90004"}
	}
	return actionParts()[1]
}

func TestActionGuardInEveryOccasion(t *testing.T) {
	t.Parallel()
	localGroup := types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "homegroup"}
	occasions := []struct {
		name string
		conv types.Conversation
	}{
		{"本地群聊", localGroup},
		{"QQ私聊朋友", private(acctA, friend)},
		{"QQ私聊主人", private(acctA, owner)},
	}
	for _, oc := range occasions {
		for _, kind := range []string{types.PartForward, types.PartMessage} {
			for _, mode := range []string{"放行", "命中", "缺守卫", "取守卫返回nil", "取守卫panic", "出错", "panic", "中途取消"} {
				t.Run(oc.name+"/"+kind+"/"+mode, func(t *testing.T) {
					t.Parallel()
					g := &fakeGuard{}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var gf func(string) Guard
					switch mode {
					case "命中":
						g.blockOn = []string{"第二段"}
					case "出错":
						g.err = errors.New("编的错误")
					case "panic":
						g.panics = true
					}
					switch mode {
					case "缺守卫":
					case "取守卫返回nil":
						gf = func(string) Guard { return nil }
					case "取守卫panic":
						gf = func(string) Guard { panic("取守卫炸了") }
					case "中途取消":
						gf = func(string) Guard { return cancelGuard{cancel} }
					default:
						gf = func(string) Guard { return g }
					}
					h, con := newActionOH(t, func(c *OutletConfig) { c.GuardFor = gf })
					sent := func() int { return len(h.ad.Sent()) + len(con.Sent()) }
					id, err := h.out.Send(ctx, "ca", "automation:other-plugin", oc.conv, []types.Part{actionPart(kind)})
					if mode == "放行" {
						if err != nil {
							t.Fatal(err)
						}
						h.waitPlan(id, store.PlanDone)
						if sent() != 1 {
							t.Fatalf("放行发送 %d 次", sent())
						}
						return
					}
					if err == nil || sent() != 0 {
						t.Fatalf("应整条不发：err=%v sends=%d", err, sent())
					}
					var n int
					if err := h.st.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != 0 {
						t.Fatalf("拦下仍落原文 %d 行", n)
					}
					if err := h.st.Events.QueryRow(`SELECT count(*) FROM outbound_plans`).Scan(&n); err != nil || n != 0 {
						t.Fatalf("拦下不该有计划：%d %v", n, err)
					}
					if strings.Contains(err.Error(), "第二段") || strings.Contains(h.logText(), "第二段") {
						t.Fatalf("报错和日志里不该有原文：%v / %s", err, h.logText())
					}
				})
			}
		}
	}
}

// 本地私聊按设计不过守卫（和回合一样）：就算守卫缺失、会出错、会 panic、会命中，引用和转发也照发，守卫一次都没被调
func TestActionGuardLocalPrivateSkips(t *testing.T) {
	t.Parallel()
	conv := types.Conversation{Channel: "console", Scene: types.ScenePrivate, ID: "ca", Self: "ca"}
	for _, kind := range []string{types.PartForward, types.PartMessage} {
		for _, mode := range []string{"缺守卫", "取守卫返回nil", "取守卫panic", "出错", "panic", "命中"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				g := &fakeGuard{blockOn: []string{"第二段"}}
				var gf func(string) Guard
				switch mode {
				case "缺守卫":
				case "取守卫返回nil":
					gf = func(string) Guard { return nil }
				case "取守卫panic":
					gf = func(string) Guard { panic("取守卫炸了") }
				case "出错":
					g.blockOn, g.err = nil, errors.New("编的错误")
					gf = func(string) Guard { return g }
				case "panic":
					g.panics = true
					gf = func(string) Guard { return g }
				default:
					gf = func(string) Guard { return g }
				}
				h, con := newActionOH(t, func(c *OutletConfig) { c.GuardFor = gf })
				id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", conv, []types.Part{actionPart(kind)})
				if err != nil {
					t.Fatalf("本地私聊不该被守卫拦：%v", err)
				}
				h.waitPlan(id, store.PlanDone)
				if len(con.Sent()) != 1 {
					t.Fatalf("发送 %d 次", len(con.Sent()))
				}
				if seen := g.texts(); len(seen) != 0 {
					t.Fatalf("本地私聊不该调守卫：%q", seen)
				}
			})
		}
	}
}

// 名单里的来源（链接展开）不过记忆守卫：守卫命中、出错、panic 都照发，守卫一次都没被调（ADR-312）；
// 名单写进了 character 也不放过她自己的话
func TestActionGuardSkipsUnguardedOrigin(t *testing.T) {
	t.Parallel()
	for _, oc := range []types.Conversation{private(acctA, owner), private(acctA, friend)} {
		for _, kind := range []string{types.PartForward, types.PartMessage} {
			for _, mode := range []string{"命中", "出错", "panic"} {
				t.Run(oc.ID+"/"+kind+"/"+mode, func(t *testing.T) {
					t.Parallel()
					g := &fakeGuard{}
					switch mode {
					case "命中":
						g.blockOn = []string{"第二段"}
					case "出错":
						g.err = errors.New("编的错误")
					case "panic":
						g.panics = true
					}
					h, con := newActionOH(t, func(c *OutletConfig) {
						c.GuardFor = func(string) Guard { return g }
						c.UnguardedOrigins = map[string]bool{"automation:link-reader": true, types.OriginCharacter: true}
					})
					id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", oc, []types.Part{actionPart(kind)})
					if err != nil {
						t.Fatal(err)
					}
					h.waitPlan(id, store.PlanDone)
					if n := len(h.ad.Sent()) + len(con.Sent()); n != 1 {
						t.Fatalf("链接展开应照发：发送 %d 次", n)
					}
					g.mu.Lock()
					seen := len(g.seen)
					g.mu.Unlock()
					if seen != 0 {
						t.Fatalf("链接展开不该调守卫，调了 %d 次", seen)
					}
					if _, err := h.out.Send(context.Background(), "ca", types.OriginCharacter, oc, []types.Part{actionPart(kind)}); err == nil {
						t.Fatal("她自己的话写进名单也不能绕过守卫")
					}
				})
			}
		}
	}
}
