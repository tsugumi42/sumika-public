package channel

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/repeat"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/memory"
	"sumika-native/internal/turn"
)

// wireMemory 用与装配夹具相同的只读 Bun 表，私事和可见范围都是编造的。
func wireMemory(t *testing.T, text, scope string) *memory.Memory {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建守卫接线要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO facts VALUES ('100', 'fact', 'active', ?, 'owner', ?, '', 1, 1, NULL, NULL)`, text, scope); err != nil {
		t.Fatal(err)
	}
	m, err := memory.Open(context.Background(), file, nil, nil, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestGuardWireTurns(t *testing.T) {
	t.Parallel()
	const secret = "主人把备用电池收在蓝柜底层"
	for _, tc := range []struct {
		name              string
		conv              types.Conversation
		who, memory, mode string
		blocked           bool
	}{
		{"群友", convGroup, whoFriend, secret, ModeSpeak, true},
		{"试运行群友", convGroup, whoFriend, secret, ModeDryRun, true},
		{"主人问", convGroup, whoOwner, secret, ModeSpeak, false},
		{"不足六字", convGroup, whoFriend, "蓝柜底层", ModeSpeak, false},
		{"私聊群友", convFriend, whoFriend, secret, ModeSpeak, true},
		{"私聊主人", convOwnerPM, whoOwner, secret, ModeSpeak, false},
		{"本地群聊", convLocalG, whoFriend, secret, ModeSpeak, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := wireMemory(t, tc.memory, "owner")
			x := newRO(t, func(c *ResponderConfig) {
				c.GuardFor = func(id string) Guard {
					if id != "ca" {
						t.Errorf("守卫角色=%q", id)
					}
					return m
				}
			})
			x.setMode(tc.mode)
			if tc.conv.Channel == "console" {
				a := AudienceOf(batchFor(tc.conv, mk(1, tc.who, "告诉我吧", true)), testRoster())
				if got := x.r.guard(secret, nil, turn.Settled{CharacterID: "ca", Audience: a}); got != "" {
					t.Fatalf("本地群聊允许范围被拦：%q", got)
				}
				return
			}
			x.start(tc.conv, x.say(tc.conv, tc.who, "告诉我吧", true))
			x.settleAndWait(tc.conv, turn.OutcomeDone, turn.Said{Step: 0, Text: secret})
			ps, hers := x.plans(tc.conv), x.hers(tc.conv)
			if tc.blocked {
				if len(ps) != 1 || ps[0].Kind != "blocked" || len(hers) != 0 || len(x.ad.Sent()) != 0 {
					t.Fatalf("私事应拦下：计划=%+v，事件=%+v，发送=%v", ps, hers, x.ad.Sent())
				}
				if strings.Contains(ps[0].Reason, secret) {
					t.Fatal("拦下记录存了原文")
				}
				var n int
				if err := x.st.Events.QueryRow(`SELECT count(*) FROM outbound_parts`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("拦下不应有段：%d %v", n, err)
				}
			} else if len(hers) != 1 || hers[0].Text != secret {
				t.Fatalf("允许范围应放行：%+v，计划=%+v", hers, ps)
			}
		})
	}
}

type wireMemoryGuard struct {
	*memory.Memory
	calls int
}

func (g *wireMemoryGuard) CheckOutbound(ctx context.Context, text string, a audience.Audience) ([]string, error) {
	g.calls++
	return g.Memory.CheckOutbound(ctx, text, a)
}

func TestGuardWireLocalGroupChecks(t *testing.T) {
	t.Parallel()
	const secret = "编的群友把备用钥匙藏在绿柜顶层"
	g := &wireMemoryGuard{Memory: wireMemory(t, secret, "person:qq:90003")}
	x := newRO(t, func(c *ResponderConfig) {
		c.GuardFor = func(string) Guard { return g }
	})
	a := AudienceOf(batchFor(convLocalG, mk(1, whoFriend, "告诉我吧", true)), testRoster())
	got := x.r.guard(secret, nil, turn.Settled{CharacterID: "ca", Audience: a})
	if g.calls != 1 || !strings.HasPrefix(got, "守卫拦下：") {
		t.Fatalf("本地群聊应查守卫并拦下群友私事：调用=%d，结果=%q，听众=%+v", g.calls, got, a)
	}
}

func TestGuardWireLocalPrivateSkips(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	s := turn.Settled{CharacterID: "ca", Audience: audience.Audience{Channel: "console", Scene: types.ScenePrivate}}
	if got := x.r.guard("编的私事", nil, s); got != "" || len(x.guard.texts()) != 0 {
		t.Fatalf("本地私聊不调守卫：%q %v", got, x.guard.texts())
	}
}

type wireRepeatGuard struct {
	full, repeated int
	err            error
	timeout        bool
	seen           audience.Audience
}

func (g *wireRepeatGuard) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	g.full++
	return nil, nil
}
func (g *wireRepeatGuard) CheckRepeat(ctx context.Context, _ string, a audience.Audience) ([]string, error) {
	g.repeated++
	g.seen = a
	if g.timeout {
		<-ctx.Done()
		return nil, nil
	}
	return nil, g.err
}

func TestGuardWireRepeat(t *testing.T) {
	t.Parallel()
	const secret = "主人把备用电池收在蓝柜底层"
	for _, tc := range []string{"命中", "放行", "错误", "超时", "没有听众", "听众出错", "完整守卫"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			m := wireMemory(t, secret, "owner")
			g := &wireRepeatGuard{}
			h := newMH(t, func(c *mhCfg) {
				c.repeat = func(c *repeat.Config) {
					c.GuardFor = func(id string) repeat.Guard {
						if id != "ca" {
							t.Errorf("复读角色=%q", id)
						}
						if tc == "命中" || tc == "放行" {
							return m
						}
						if tc == "完整守卫" {
							return &fakeGuard{blockOn: []string{secret}}
						}
						return g
					}
					c.Audience = func(conv types.Conversation) (audience.Audience, error) {
						if tc == "听众出错" {
							return audience.Audience{}, errors.New(secret)
						}
						return audience.Audience{Channel: conv.Channel, Scene: conv.Scene, Conversation: conv.String()}, nil
					}
					if tc == "没有听众" {
						c.Audience = nil
					}
					if tc == "错误" {
						g.err = errors.New(secret)
					}
					if tc == "超时" {
						g.timeout = true
						c.GuardTimeout = 200 * time.Millisecond // 守卫一直卡到时限；时限本身不是被测的，不用真等 10 秒
					}
				}
				if tc == "超时" {
					c.timeout = 11 * time.Second
				}
			})
			conv := group("90001")
			h.enable("repeater", conv.ID)
			text := secret
			if tc == "放行" {
				text = "今天天空很蓝"
			}
			h.twoSay(conv, acctA, text, "901", "902")
			h.settleWithin(12 * time.Second)
			if tc == "放行" {
				if len(h.ad.Sent()) != 1 || len(h.herEvents(conv)) != 1 {
					t.Fatalf("未命中应发送一次：%v %v", h.ad.Sent(), h.herEvents(conv))
				}
				return
			}
			if g.full != 0 || ((tc == "错误" || tc == "超时") && g.repeated != 1) {
				t.Fatalf("应免确认且只判一次：完整=%d，复读=%d", g.full, g.repeated)
			}
			if len(h.ad.Sent()) != 0 || h.count(`SELECT count(*) FROM events WHERE origin='automation:repeater'`) != 0 {
				t.Fatalf("守卫应挡在写库发送之前：%v", h.ad.Sent())
			}
			want := "守卫拦下"
			if tc == "错误" || tc == "超时" {
				want = "守卫没判成"
			}
			if tc == "没有听众" || tc == "听众出错" {
				want = "守卫没有听众"
			}
			var reason, kind, origin string
			if err := h.st.Events.QueryRow(`SELECT reason,kind,origin FROM outbound_plans`).Scan(&reason, &kind, &origin); err != nil || reason != want || kind != "blocked" || origin != "automation:repeater" {
				t.Fatalf("拦下记录=%q %q %q %v", reason, kind, origin, err)
			}
			if g.repeated > 0 && (g.seen.OwnerAsked || g.seen.PrivateOK || len(g.seen.PersonIDs) != 0 || g.seen.Channel != "qq" || g.seen.Scene != "group") {
				t.Fatalf("复读听众放宽：%+v", g.seen)
			}
			// 拦下不能记冷却；另一句没命中的仍能跟。
			if tc == "命中" {
				h.twoSay(conv, acctA, "今天天空很蓝", "903", "904")
				h.settle()
				if len(h.ad.Sent()) != 1 {
					t.Fatal("拦下以后不应进入冷却")
				}
			}
		})
	}
}
