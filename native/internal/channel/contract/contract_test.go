package contract_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/contract"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 假适配器要过全部规矩；按群和私聊分别声明能力，邻居不变。
func TestFakePassesContract(t *testing.T) {
	for _, caps := range []types.Caps{
		{MediaGroup: types.MediaCaps{Image: true}, Poke: true, Echo: true, MaxText: 500},
		{},
		{MediaGroup: types.MediaCaps{Image: true}},
		{Poke: true},
		{QuoteGroup: true, QuotePrivate: true, ForwardGroup: true, ForwardPrivate: true, ReactGroup: true},
	} {
		t.Run(fmt.Sprintf("%+v", caps), func(t *testing.T) {
			contract.Adapter(t, func() types.Adapter { return fake.New("qq", caps) })
		})
	}
}

// 起来的信号有两种（Started() 或第一个 Status），好实现用哪种都该过；Run 起得慢也该过（契约要等它起来再查，不是赶在它前面）
type statusOnly struct {
	types.Adapter // 只露接口里的方法，看不到假适配器的 Started()
	f             *fake.Adapter
}

func (s statusOnly) Run(ctx context.Context, sink types.Sink) error {
	go func() {
		select {
		case <-s.f.Started():
			_ = s.f.PushStatus(ctx, types.Status{Kind: types.StatusConnected, Account: "acct-a", Time: time.Now()})
		case <-ctx.Done():
		}
	}()
	return s.f.Run(ctx, sink)
}

type slowStart struct{ *fake.Adapter }

func (s slowStart) Run(ctx context.Context, sink types.Sink) error {
	select {
	case <-time.After(30 * time.Millisecond):
	case <-ctx.Done():
		return nil
	}
	return s.Adapter.Run(ctx, sink)
}

func TestOtherGoodAdaptersPassContract(t *testing.T) {
	caps := types.Caps{MediaGroup: types.MediaCaps{Image: true}, Poke: true}
	t.Run("只靠第一个 Status 报起来", func(t *testing.T) {
		contract.Adapter(t, func() types.Adapter { f := fake.New("qq", caps); return statusOnly{Adapter: f, f: f} })
	})
	t.Run("Run 起得慢", func(t *testing.T) {
		contract.Adapter(t, func() types.Adapter { return slowStart{fake.New("qq", caps)} })
	})
}

// 运行中的那次 Send、Poke 真的发生了：Run 起得慢的适配器，旧写法的契约会赶在它前面调（调到的是"还没运行"那条路）。
// OnSend、OnPoke 只在适配器运行中才会被调到，所以拿它们数
func TestReceiptRuleReallyCallsWhileRunning(t *testing.T) {
	var sends, pokes atomic.Int32
	f := func() types.Adapter {
		a := fake.New("qq", types.Caps{Poke: true})
		a.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			sends.Add(1)
			return fake.Confirmed(c.Part.ID, "1", time.Now()), nil
		})
		a.OnPoke(func(context.Context, fake.PokeCall) (types.Receipt, error) {
			pokes.Add(1)
			return fake.Confirmed("", "2", time.Now()), nil
		})
		return slowStart{a}
	}
	for _, c := range contract.Cases() {
		if c.Name != "receipt-valid" {
			continue
		}
		if err := c.Check(f); err != nil {
			t.Fatalf("好实现没过 %s：%v", c.Name, err)
		}
	}
	if sends.Load() != 1 || pokes.Load() != 1 {
		t.Fatalf("运行中应该各调到 1 次 Send、1 次 Poke，实际 Send %d 次、Poke %d 次", sends.Load(), pokes.Load())
	}
}

// 下面是故意写坏的实现：每一种都要在它对应的那条规矩上红（第十六节 C 改坏检查）。
// 每个坏实现都嵌着一个假适配器，只改坏一处；good 是同样 Caps 的好实现，用来证明红是这一处改坏造成的，不是别的

type broken struct {
	*fake.Adapter
	name     func() string
	caps     func() types.Caps
	run      func(ctx context.Context, sink types.Sink) error
	send     func(ctx context.Context, account string, p types.Part) (types.Receipt, error)
	poke     func(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error)
	capsCall int
}

func (b *broken) Name() string {
	if b.name != nil {
		return b.name()
	}
	return b.Adapter.Name()
}

func (b *broken) Caps() types.Caps {
	b.capsCall++
	if b.caps != nil {
		return b.caps()
	}
	return b.Adapter.Caps()
}

func (b *broken) Run(ctx context.Context, sink types.Sink) error {
	if b.run != nil {
		return b.run(ctx, sink)
	}
	return b.Adapter.Run(ctx, sink)
}

func (b *broken) Send(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
	if b.send != nil {
		return b.send(ctx, account, p)
	}
	return b.Adapter.Send(ctx, account, p)
}

func (b *broken) Poke(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error) {
	if b.poke != nil {
		return b.poke(ctx, account, c, target)
	}
	return b.Adapter.Poke(ctx, account, c, target)
}

// life 记下 b 的 Run 跑过没有，坏实现据此只在"还没 Run"或"Run 跑过又停了"的时候坏
type life struct {
	ran atomic.Bool
	b   *broken
}

func trackRun(b *broken) *life {
	l := &life{b: b}
	b.run = func(ctx context.Context, sink types.Sink) error {
		l.ran.Store(true)
		return b.Adapter.Run(ctx, sink)
	}
	return l
}

func (l *life) notYet() bool  { return !l.ran.Load() }
func (l *life) stopped() bool { return l.ran.Load() && !l.b.Adapter.Running() }

// silent 把 Started() 藏起来：只露接口里的方法
type silent struct{ types.Adapter }

func confirmed(p types.Part) types.Receipt {
	return fake.Confirmed(p.ID, "9001", time.Now())
}

// badCase 是一种坏实现。times 大于 1 的是"只在某个时刻才坏"的（比如只在运行中坏）：要连着造很多次，次次都红，
// 偶尔红一次不算数——检查赶在适配器起来之前就调，这种坏实现多半查不到
type badCase struct {
	name  string
	rule  string // 应该红的那条规矩
	caps  types.Caps
	build func(b *broken)
	times int
	wrap  func(types.Adapter) types.Adapter
}

func bad(name, rule string, caps types.Caps, build func(b *broken)) badCase {
	return badCase{name: name, rule: rule, caps: caps, build: build, times: 1}
}

func (c badCase) repeat(n int) badCase { c.times = n; return c }

func (c badCase) wrapped(w func(types.Adapter) types.Adapter) badCase { c.wrap = w; return c }

func TestContractCatchesBrokenAdapters(t *testing.T) {
	// 时限调短，写坏的实现（不返回、不理 ctx、不起来）才红得快；用完还原
	stop, call, start, settle := contract.StopTimeout, contract.CallTimeout, contract.StartTimeout, contract.Settle
	contract.StopTimeout, contract.CallTimeout, contract.StartTimeout = 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond
	contract.Settle = 50 * time.Millisecond
	t.Cleanup(func() {
		contract.StopTimeout, contract.CallTimeout, contract.StartTimeout, contract.Settle = stop, call, start, settle
	})

	release := make(chan struct{}) // 放走那些故意卡住的 goroutine
	t.Cleanup(func() { close(release) })

	all := types.Caps{MediaGroup: types.MediaCaps{Image: true}, Poke: true}
	none := types.Caps{}
	zeroSend := func(context.Context, string, types.Part) (types.Receipt, error) { return types.Receipt{}, nil }
	zeroPoke := func(context.Context, string, types.Conversation, string) (types.Receipt, error) {
		return types.Receipt{}, nil
	}
	for _, c := range []badCase{
		bad("渠道名是空的", "caps-fixed", all, func(b *broken) { b.name = func() string { return "" } }),
		bad("Caps 每次读都不一样", "caps-fixed", all, func(b *broken) {
			b.caps = func() types.Caps { c := all; c.MaxText = b.capsCall; return c }
		}),
		bad("运行中 Caps 变了", "caps-fixed", all, func(b *broken) {
			b.caps = func() types.Caps {
				c := all
				if b.Adapter.Running() {
					c.MaxText = 1
				}
				return c
			}
		}).repeat(100),
		bad("取消 ctx 以后 Run 不返回", "run-returns-on-cancel", all, func(b *broken) {
			b.run = func(ctx context.Context, sink types.Sink) error { <-release; return nil }
		}),
		bad("取消 ctx 以后 Run 返回 context.Canceled", "run-returns-on-cancel", all, func(b *broken) {
			b.run = func(ctx context.Context, sink types.Sink) error { <-ctx.Done(); return ctx.Err() }
		}),
		bad("Run 一进来就返回 nil", "run-returns-on-cancel", all, func(b *broken) {
			b.run = func(context.Context, types.Sink) error { return nil }
		}),
		bad("Run 起来以后没等取消就返回 nil", "run-returns-on-cancel", all, func(b *broken) {
			b.run = func(ctx context.Context, sink types.Sink) error {
				inner, cancel := context.WithCancel(ctx)
				go func() { <-b.Adapter.Started(); cancel() }()
				return b.Adapter.Run(inner, sink)
			}
		}),
		bad("Run 起来了，却既不给 Started 也不报任何状态", "run-returns-on-cancel", all, func(b *broken) {}).
			wrapped(func(a types.Adapter) types.Adapter { return silent{a} }),
		bad("Send 回零值回执", "receipt-valid", all, func(b *broken) { b.send = zeroSend }),
		bad("只在运行中 Send 回零值回执", "receipt-valid", all, func(b *broken) {
			b.send = func(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
				if b.Adapter.Running() {
					return types.Receipt{}, nil
				}
				return b.Adapter.Send(ctx, account, p)
			}
		}).repeat(100),
		bad("只在运行中 Poke 回零值回执", "receipt-valid", all, func(b *broken) {
			b.poke = func(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error) {
				if b.Adapter.Running() {
					return types.Receipt{}, nil
				}
				return b.Adapter.Poke(ctx, account, c, target)
			}
		}).repeat(100),
		bad("还没 Run 时 Send 回零值回执", "receipt-valid", all, func(b *broken) {
			l := trackRun(b)
			b.send = func(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
				if l.notYet() {
					return types.Receipt{}, nil
				}
				return b.Adapter.Send(ctx, account, p)
			}
		}),
		bad("Send 回执的状态乱写", "receipt-valid", all, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) {
				r := confirmed(p)
				r.Status = "ok"
				return r, nil
			}
		}),
		bad("Send 回执不抄段号", "receipt-valid", all, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) {
				r := confirmed(p)
				r.PartID = ""
				return r, nil
			}
		}),
		bad("Send 把失败放在 error 里", "receipt-valid", all, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) {
				return fake.Failed(p.ID, "断了", time.Now()), errors.New("断了")
			}
		}),
		bad("Send 不理 ctx", "receipt-valid", all, func(b *broken) {
			b.send = func(context.Context, string, types.Part) (types.Receipt, error) {
				<-release
				return types.Receipt{}, nil
			}
		}),
		bad("Poke 回零值回执", "receipt-valid", all, func(b *broken) { b.poke = zeroPoke }),
		bad("不支持图片却确认了", "unsupported-is-failure", none, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) { return confirmed(p), nil }
		}),
		bad("不支持图片，回的是普通失败不是 ErrUnsupported", "unsupported-is-failure", none, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) {
				return fake.Failed(p.ID, "不会", time.Now()), nil
			}
		}),
		bad("不支持戳却确认了", "unsupported-is-failure", none, func(b *broken) {
			b.poke = func(context.Context, string, types.Conversation, string) (types.Receipt, error) {
				return fake.Confirmed("", "9002", time.Now()), nil
			}
		}),
		bad("停了以后 Send 还报确认", "stopped-never-confirms", all, func(b *broken) {
			b.send = func(_ context.Context, _ string, p types.Part) (types.Receipt, error) { return confirmed(p), nil }
		}),
		bad("停了以后 Poke 还报确认", "stopped-never-confirms", all, func(b *broken) {
			b.poke = func(context.Context, string, types.Conversation, string) (types.Receipt, error) {
				return fake.Confirmed("", "9003", time.Now()), nil
			}
		}),
		// 下面几种是"关掉以后回执缺失"（第十六节 B）：没报确认，但回的东西不合规
		bad("停了以后 Send 回零值回执", "stopped-never-confirms", all, func(b *broken) {
			l := trackRun(b)
			b.send = func(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
				if l.stopped() {
					return types.Receipt{}, nil
				}
				return b.Adapter.Send(ctx, account, p)
			}
		}),
		bad("停了以后 Poke 回零值回执", "stopped-never-confirms", all, func(b *broken) {
			l := trackRun(b)
			b.poke = func(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error) {
				if l.stopped() {
					return types.Receipt{}, nil
				}
				return b.Adapter.Poke(ctx, account, c, target)
			}
		}),
		bad("停了以后 Send 返回普通 error", "stopped-never-confirms", all, func(b *broken) {
			l := trackRun(b)
			b.send = func(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
				if l.stopped() {
					return types.Receipt{}, errors.New("closed")
				}
				return b.Adapter.Send(ctx, account, p)
			}
		}),
		bad("停了以后 Poke 返回普通 error", "stopped-never-confirms", all, func(b *broken) {
			l := trackRun(b)
			b.poke = func(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error) {
				if l.stopped() {
					return types.Receipt{}, errors.New("closed")
				}
				return b.Adapter.Poke(ctx, account, c, target)
			}
		}),
		bad("停了以后 Send 文字段说自己不支持", "stopped-never-confirms", all, func(b *broken) {
			l := trackRun(b)
			b.send = func(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
				if l.stopped() {
					return types.Receipt{}, types.ErrUnsupported
				}
				return b.Adapter.Send(ctx, account, p)
			}
		}),
	} {
		t.Run(c.name, func(t *testing.T) {
			var rule contract.Case
			for _, x := range contract.Cases() {
				if x.Name == c.rule {
					rule = x
				}
			}
			if rule.Check == nil {
				t.Fatalf("没有叫 %q 的规矩", c.rule)
			}
			// 好实现要过（或者不适用），否则红不能证明什么；同样连着造 times 次
			for i := 0; i < c.times; i++ {
				if err := rule.Check(func() types.Adapter { return fake.New("qq", c.caps) }); err != nil && !errors.Is(err, contract.ErrNotApplicable) {
					t.Fatalf("同样 Caps 的好实现没过 %s（第 %d 次）：%v", c.rule, i+1, err)
				}
			}
			// 坏实现要红，次次都红
			green := 0
			var last error
			for i := 0; i < c.times; i++ {
				err := rule.Check(func() types.Adapter {
					b := &broken{Adapter: fake.New("qq", c.caps)}
					c.build(b)
					if c.wrap != nil {
						return c.wrap(b)
					}
					return b
				})
				if err == nil || errors.Is(err, contract.ErrNotApplicable) {
					green++
				} else {
					last = err
				}
			}
			if green > 0 {
				t.Fatalf("故意写坏的实现 %d 次里有 %d 次过了规矩 %s", c.times, green, c.rule)
			}
			t.Logf("红了（%d 次都红）：%v", c.times, last)
		})
	}
}
