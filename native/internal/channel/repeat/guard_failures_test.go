package repeat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
)

// 复读的守卫失败分支（Sol 覆盖表第 6 条）：没有听众函数、取不到听众、没有取守卫的函数、取守卫的函数返回 nil、
// 取听众 / 取守卫 / 检查本身 panic、检查出错、检查中途被取消。本地群聊和 QQ 群各测一遍。
// 每一种都要：零发送、不记冷却（换成放行的守卫再来同一批，能跟）、不回成功、理由不含原文、拦下的记一条「blocked」。

var (
	localGrp = types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "homegroup"}
	qqGrp    = grp
)

// recOut 是会记 Record 的假出口（fakeOut 的 Record 什么都不记）
type recOut struct {
	fakeOut
	rmu  sync.Mutex
	recs [][2]string // kind, reason
}

func (o *recOut) Record(_ context.Context, _, _ string, _ types.Conversation, kind, reason string) (string, error) {
	o.rmu.Lock()
	defer o.rmu.Unlock()
	o.recs = append(o.recs, [2]string{kind, reason})
	return "blocked", nil
}

func (o *recOut) records() [][2]string {
	o.rmu.Lock()
	defer o.rmu.Unlock()
	return append([][2]string(nil), o.recs...)
}

// batchIn 同 batch，只是会话由调用方定
func batchIn(conv types.Conversation, prev []types.Msg, news ...types.Msg) types.Batch {
	b := batch(prev, news...)
	b.Conv = conv
	return b
}

// 守卫的几种坏法
type boomGuard struct {
	err    error
	panics bool
	cancel func() // 非 nil：检查时先把 ctx 取消，再装作放行
}

func (g boomGuard) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	if g.panics {
		panic("假守卫炸了")
	}
	if g.cancel != nil {
		g.cancel()
		return nil, nil
	}
	return nil, g.err
}

// repeatPanicGuard 实现了 CheckRepeat（免确认检查）的守卫，那条路 panic
type repeatPanicGuard struct{ clearGuard }

func (repeatPanicGuard) CheckRepeat(context.Context, string, audience.Audience) ([]string, error) {
	panic("免确认检查炸了")
}

func TestGuardFailuresNeverFollow(t *testing.T) {
	ok := func(c types.Conversation) (audience.Audience, error) {
		return audience.Audience{Channel: c.Channel, Scene: c.Scene, Conversation: c.String()}, nil
	}
	for _, tc := range []struct {
		name   string
		reason string // 理由 / 记录里该有的
		tweak  func(*Config)
		ctx    func() (context.Context, context.CancelFunc)
	}{
		{"没有听众函数", "守卫没有听众", func(c *Config) { c.Audience = nil }, nil},
		{"取听众出错", "守卫没有听众", func(c *Config) {
			c.Audience = func(types.Conversation) (audience.Audience, error) {
				return audience.Audience{}, errors.New("编的错误")
			}
		}, nil},
		{"没有取守卫的函数", "守卫没判成", func(c *Config) { c.GuardFor = nil }, nil},
		{"取守卫返回 nil", "守卫没判成", func(c *Config) { c.GuardFor = func(string) Guard { return nil } }, nil},
		{"取听众 panic", "守卫没判成", func(c *Config) {
			c.Audience = func(types.Conversation) (audience.Audience, error) { panic("取听众炸了") }
		}, nil},
		{"取守卫 panic", "守卫没判成", func(c *Config) { c.GuardFor = func(string) Guard { panic("取守卫炸了") } }, nil},
		{"检查 panic", "守卫没判成", func(c *Config) { c.GuardFor = func(string) Guard { return boomGuard{panics: true} } }, nil},
		{"免确认检查 panic", "守卫没判成", func(c *Config) { c.GuardFor = func(string) Guard { return repeatPanicGuard{} } }, nil},
		{"检查出错", "守卫没判成", func(c *Config) { c.GuardFor = func(string) Guard { return boomGuard{err: errors.New("编的错误")} } }, nil},
		{"检查中途取消", "守卫没判成", nil, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
	} {
		for _, conv := range []types.Conversation{localGrp, qqGrp} {
			t.Run(conv.Channel+"/"+tc.name, func(t *testing.T) {
				out := &recOut{}
				var cfgs []func(*Config)
				cfgs = append(cfgs, func(c *Config) { c.Out = out; c.Audience = ok })
				if tc.tweak != nil {
					cfgs = append(cfgs, tc.tweak)
				}
				ctx := context.Background()
				if tc.ctx != nil {
					var cancel context.CancelFunc
					ctx, cancel = tc.ctx()
					defer cancel()
					cfgs = append(cfgs, func(c *Config) { c.GuardFor = func(string) Guard { return boomGuard{cancel: cancel} } })
				}
				r, _ := newRepeater(t, func(c *Config) {
					for _, f := range cfgs {
						f(c)
					}
				})
				const said = "编的私房话"
				prev := []types.Msg{m("A", txt(said))}
				b := batchIn(conv, prev, m("B", txt(said)))

				v := r.Interceptor().Intercept(ctx, b)
				if v.Taken {
					t.Fatalf("守卫没判成不能回成功：%+v", v)
				}
				if !strings.Contains(v.Reason, tc.reason) || strings.Contains(v.Reason, said) {
					t.Fatalf("理由该含 %q 且不含原文：%q", tc.reason, v.Reason)
				}
				if out.count() != 0 {
					t.Fatalf("不该发，发了 %d 次", out.count())
				}
				recs := out.records()
				if len(recs) != 1 || recs[0][0] != "blocked" || recs[0][1] != tc.reason {
					t.Fatalf("该记一条 blocked/%s：%v", tc.reason, recs)
				}

				// 没记冷却：同一批换成放行的守卫再来一遍，能跟（默认冷却 3 分钟，记了的话这里会写「冷却中」）
				r.cfg.Audience = ok
				r.cfg.GuardFor = func(string) Guard { return clearGuard{} }
				v = r.Interceptor().Intercept(context.Background(), b)
				if !v.Taken || out.count() != 1 {
					t.Fatalf("失败不该记冷却，之后放行的守卫应能跟：%+v 发了 %d 次", v, out.count())
				}
			})
		}
	}
}
