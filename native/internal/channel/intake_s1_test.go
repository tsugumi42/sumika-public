package channel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
)

// intakeBalance 是入库一站的守恒差额：交来的 − 各个结局之和。每次交来都恰好落进一个结局，所以处理完以后恒为 0
func intakeBalance(sc *selfcheck.Counters) int64 {
	return sc.Get(selfcheck.IntakeSubmitted) - (sc.Get(selfcheck.IntakeRejected) + sc.Get(selfcheck.IntakeClosed) + sc.Get(selfcheck.IntakeCancelled) +
		sc.Get(selfcheck.IntakeAdmitDropped) + sc.Get(selfcheck.IntakePanicked) + sc.Get(selfcheck.IntakeStoreFailed) +
		sc.Get(selfcheck.IntakeDuplicate) + sc.Get(selfcheck.IntakeStored))
}

// only 检查这一轮只有 want 这个结局被数了一次，别的结局都是 0，守恒差额是 0
func only(t *testing.T, sc *selfcheck.Counters, want selfcheck.Counter) {
	t.Helper()
	outcomes := []selfcheck.Counter{selfcheck.IntakeRejected, selfcheck.IntakeClosed, selfcheck.IntakeCancelled, selfcheck.IntakeAdmitDropped,
		selfcheck.IntakePanicked, selfcheck.IntakeStoreFailed, selfcheck.IntakeDuplicate, selfcheck.IntakeStored}
	for _, k := range outcomes {
		w := int64(0)
		if k == want {
			w = 1
		}
		if got := sc.Get(k); got != w {
			t.Errorf("结局 %s 应数 %d 次，数了 %d 次", k.Name(), w, got)
		}
	}
	if sc.Get(selfcheck.IntakeSubmitted) != 1 || intakeBalance(sc) != 0 {
		t.Errorf("交来 %d 次，守恒差额 %d（应是 1 次、差额 0）", sc.Get(selfcheck.IntakeSubmitted), intakeBalance(sc))
	}
}

// B3：入库一站每条返回路径恰好记一个结局（规格-运行时自检 S1；规格第三节漏掉的出口：没收下、补录被取消、panic、入库失败都在这里逐条造）。
// 埋点不改任何路径的返回值，所以每条都同时核对原来的返回。
func TestSelfcheckEveryIntakeExitCountedOnce(t *testing.T) {
	t.Parallel()
	newSC := func(t *testing.T, tweak func(*Config)) (*harness, *selfcheck.Counters) {
		sc := selfcheck.NewCounters()
		h := newHarness(t, func(c *Config) {
			c.Selfcheck = sc
			if tweak != nil {
				tweak(c)
			}
		})
		return h, sc
	}

	t.Run("入库", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		tk, err := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "你好"), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r, err := waitTicket(t, tk); err != nil || r.Seq == 0 || r.Duplicate {
			t.Fatalf("%+v %v", r, err)
		}
		h.in.Idle(context.Background())
		only(t, sc, selfcheck.IntakeStored)
	})

	t.Run("重复", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		for i := 0; i < 2; i++ {
			tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "你好"), Options{})
			waitTicket(t, tk)
		}
		h.in.Idle(context.Background())
		if sc.Get(selfcheck.IntakeStored) != 1 || sc.Get(selfcheck.IntakeDuplicate) != 1 || sc.Get(selfcheck.IntakeSubmitted) != 2 || intakeBalance(sc) != 0 {
			t.Fatalf("两次交同一条：入库 %d、重复 %d、交来 %d、差额 %d", sc.Get(selfcheck.IntakeStored), sc.Get(selfcheck.IntakeDuplicate), sc.Get(selfcheck.IntakeSubmitted), intakeBalance(sc))
		}
	})

	t.Run("准入丢弃", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, func(c *Config) { c.Admit = func(types.Inbound) Admission { return Admission{Drop: true} } })
		tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "不收的"), Options{})
		if r, err := waitTicket(t, tk); err != nil || !r.Dropped {
			t.Fatalf("%+v %v", r, err)
		}
		only(t, sc, selfcheck.IntakeAdmitDropped)
	})

	t.Run("没收下：不认识的号", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		if _, err := h.in.Submit(context.Background(), msg("987654", group("900"), "1", friend, "x"), Options{}); !errors.Is(err, ErrUnknownAccount) {
			t.Fatalf("应报不认识的号：%v", err)
		}
		only(t, sc, selfcheck.IntakeRejected)
	})

	t.Run("没收下：Kind 不对", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		in := msg(acctA, group("900"), "1", friend, "x")
		in.Kind = "不是消息"
		if _, err := h.in.Submit(context.Background(), in, Options{}); err == nil {
			t.Fatal("Kind 不对应报错")
		}
		only(t, sc, selfcheck.IntakeRejected)
	})

	t.Run("没收下：ctx 已取消", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := h.in.Submit(ctx, msg(acctA, group("900"), "1", friend, "x"), Options{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
		only(t, sc, selfcheck.IntakeRejected)
	})

	t.Run("关了以后到的", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		h.in.Close()
		if err := h.in.Inbound(context.Background(), msg(acctA, group("900"), "1", friend, "x")); !errors.Is(err, ErrClosed) {
			t.Fatalf("%v", err)
		}
		only(t, sc, selfcheck.IntakeClosed)
	})

	t.Run("排着没开始就被丢", func(t *testing.T) {
		t.Parallel()
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		sc := selfcheck.NewCounters()
		h := newHarness(t, func(c *Config) {
			c.Selfcheck = sc
			c.OnStored = func(s Stored) {
				if s.Inbound.PlatformID == "1" {
					entered <- struct{}{}
					<-block
				}
			}
		})
		var once sync.Once
		release := func() { once.Do(func() { close(block) }) }
		defer release()
		first, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "在做的"), Options{})
		waitEntered(t, entered)
		second, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "排着的"), Options{})
		done := make(chan struct{})
		go func() { h.in.Close(); close(done) }()
		for !h.in.q.isClosed() {
			time.Sleep(time.Millisecond)
		}
		release()
		<-done
		if _, err := waitTicket(t, second); !errors.Is(err, ErrClosed) {
			t.Fatalf("排着的应得到 ErrClosed：%v", err)
		}
		if _, err := waitTicket(t, first); err != nil {
			t.Fatalf("在做的应做完：%v", err)
		}
		if sc.Get(selfcheck.IntakeStored) != 1 || sc.Get(selfcheck.IntakeClosed) != 1 || sc.Get(selfcheck.IntakeSubmitted) != 2 || intakeBalance(sc) != 0 {
			t.Fatalf("在做的入库 1、排着的丢弃 1：入库 %d、关了丢弃 %d、交来 %d、差额 %d",
				sc.Get(selfcheck.IntakeStored), sc.Get(selfcheck.IntakeClosed), sc.Get(selfcheck.IntakeSubmitted), intakeBalance(sc))
		}
	})

	t.Run("补录排队期间被取消", func(t *testing.T) {
		t.Parallel()
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		sc := selfcheck.NewCounters()
		h := newHarness(t, func(c *Config) {
			c.Selfcheck = sc
			c.OnStored = func(s Stored) {
				if s.Inbound.PlatformID == "1" {
					entered <- struct{}{}
					<-block
				}
			}
		})
		var once sync.Once
		release := func() { once.Do(func() { close(block) }) }
		defer release()
		h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "占着队列"), Options{})
		waitEntered(t, entered)
		ctx, cancel := context.WithCancel(context.Background())
		tk, err := h.in.Submit(ctx, msg(acctA, group("900"), "2", friend, "补录的"), Options{Backfill: true})
		if err != nil {
			t.Fatal(err)
		}
		cancel() // 排队期间断线
		release()
		if _, err := waitTicket(t, tk); !errors.Is(err, context.Canceled) {
			t.Fatalf("补录凭据应得到 ctx 的错误：%v", err)
		}
		h.in.Idle(context.Background())
		if sc.Get(selfcheck.IntakeCancelled) != 1 || sc.Get(selfcheck.IntakeStored) != 1 || intakeBalance(sc) != 0 {
			t.Fatalf("入库 1、补录取消 1：入库 %d、取消 %d、差额 %d", sc.Get(selfcheck.IntakeStored), sc.Get(selfcheck.IntakeCancelled), intakeBalance(sc))
		}
	})

	t.Run("处理时 panic", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, func(c *Config) { c.Admit = func(types.Inbound) Admission { panic("准入检查出了意外") } })
		tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "x"), Options{})
		if _, err := waitTicket(t, tk); err == nil {
			t.Fatal("panic 以后凭据应得到错误")
		}
		h.in.Idle(context.Background())
		only(t, sc, selfcheck.IntakePanicked)
	})

	t.Run("入库后的步骤 panic 不重复计", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, func(c *Config) { c.OnStored = func(Stored) { panic("拦截链出了意外") } })
		tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "x"), Options{})
		if r, err := waitTicket(t, tk); err != nil || r.Seq == 0 {
			t.Fatalf("入库已经成功：%+v %v", r, err)
		}
		h.in.Idle(context.Background())
		only(t, sc, selfcheck.IntakeStored) // 入库已经算了结局，后面的 panic 不再记第二个
	})

	t.Run("入库失败", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		if _, err := h.st.Events.Exec(`CREATE TRIGGER boom BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, '模拟写不进'); END`); err != nil {
			t.Fatal(err)
		}
		tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "x"), Options{})
		if _, err := waitTicket(t, tk); err == nil {
			t.Fatal("应失败")
		}
		h.in.Idle(context.Background())
		only(t, sc, selfcheck.IntakeStoreFailed)
	})

	t.Run("自己的号", func(t *testing.T) {
		t.Parallel()
		h, sc := newSC(t, nil)
		in := msg(acctA, group("900"), "1", acctA, "自己发的")
		tk, _ := h.in.Submit(context.Background(), in, Options{})
		waitTicket(t, tk)
		h.in.Idle(context.Background())
		if intakeBalance(sc) != 0 || sc.Get(selfcheck.IntakeSubmitted) != 1 {
			t.Fatalf("自己的号交来的也要有结局：差额 %d", intakeBalance(sc))
		}
	})
}

// 端到端：真的入库一站数出的计数，交给真的评估循环判 S1。正常处理完是 ok；缺一个结局（造出异常）就变 bad，不为零连续两次静默点才报。
func TestSelfcheckS1EndToEndWithRealIntake(t *testing.T) {
	t.Parallel()
	sc := selfcheck.NewCounters()
	h := newHarness(t, func(c *Config) { c.Selfcheck = sc })
	for i, id := range []string{"1", "2", "3"} {
		tk, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), id, friend, "第"+string(rune('一'+i))+"条"), Options{})
		if _, err := waitTicket(t, tk); err != nil {
			t.Fatal(err)
		}
	}
	h.in.Idle(context.Background())

	now := time.Now().Add(time.Hour) // 入库一站用真时钟记"最近一次交来"，评估的钟要在它后面
	dir := t.TempDir()
	run := selfcheck.New(selfcheck.Config{Dir: dir, Counters: sc, Checks: selfcheck.Checks(selfcheck.DefaultThresholds()),
		Now: func() time.Time { return now }, StartDelay: -1})
	rowOf := func() selfcheck.CheckRow {
		run.Evaluate(context.Background())
		day := now.Format("2006-01-02")
		now = now.Add(5 * time.Minute)
		b, err := os.ReadFile(filepath.Join(dir, day+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var f selfcheck.DayFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Checks {
			if r.ID == "intake.unaccounted" {
				return r
			}
		}
		t.Fatal("文件里没有 intake.unaccounted")
		return selfcheck.CheckRow{}
	}
	// 这里没有适配器，三条都直接从 Submit 交来；评估时最后一次交来已经过了 5 秒（评估的钟在一小时以后）
	if r := rowOf(); r.State != selfcheck.Ok && r.State != selfcheck.Unknown {
		t.Fatalf("正常处理完：%+v", r)
	}
	if r := rowOf(); r.State != selfcheck.Ok {
		t.Fatalf("正常处理完应是 ok：%+v", r)
	}
	sc.Add(selfcheck.IntakeSubmitted) // 造出异常：交来了、没有结局
	if r := rowOf(); r.State == selfcheck.Bad {
		t.Fatalf("第一次不为零先不报：%+v", r)
	}
	if r := rowOf(); r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("缺一个结局应 bad：%+v", r)
	}
}
