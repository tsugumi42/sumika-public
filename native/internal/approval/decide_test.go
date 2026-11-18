package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newApprovals(t *testing.T) *Approvals {
	return &Approvals{pending: map[string]*pend{}, Grants: LoadGrants(filepath.Join(t.TempDir(), "tool-grants.yml")), Conv: NewConvGrants(), Now: time.Now}
}

func card(id string) Request {
	return Request{ID: id, CharacterID: "c", Options: []string{"once", "conversation", "always"}, CreatedAt: 0, ExpiresAt: 600_000}
}

// 请求回调超过等待时限才完成时，同一订阅方也不能先收到结局再收到等待。
func TestResolvedAfterRequest(t *testing.T) {
	a := newApprovals(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	order := make(chan string, 2)
	a.Subscribe(func(req Request) {
		close(entered)
		<-release
		order <- fmt.Sprintf("request:%d", req.Version)
	}, func(req Request, d Decision) {
		order <- fmt.Sprintf("resolved:%s:%d", d.Kind, req.Version)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	asked := make(chan Decision, 1)
	go func() { asked <- a.Ask(ctx, card("x"), nil) }()
	approvalRecv(t, entered, "请求通知进入")
	cancel()
	if d := approvalRecv(t, asked, "通知卡住时取消"); d.Kind != "cancelled" {
		t.Fatalf("应该是取消，得到 %s", d.Kind)
	}
	select {
	case got := <-order:
		t.Fatalf("请求尚未完成却先收到 %s", got)
	default:
	}
	// 放行由显式事件控制，不用睡眠猜通知是否迟到。
	release <- struct{}{}
	if first, second := approvalRecv(t, order, "请求副作用"), approvalRecv(t, order, "结局副作用"); first != "request:1" || second != "resolved:cancelled:2" {
		t.Fatalf("通知顺序或版本错：%s → %s", first, second)
	}
}

// 取消可先放开回合，但授权事务的最终结局仍必须和磁盘一致。
func TestDecideRacesCancel(t *testing.T) {
	for i := 0; i < 300; i++ {
		a := newApprovals(t)
		ctx, cancel := context.WithCancel(context.Background())
		key := fmt.Sprint("k", i)
		resolved := make(chan Decision, 1)
		a.OnResolved = append(a.OnResolved, func(_ Request, d Decision) { resolved <- d })
		a.OnRequest = append(a.OnRequest, func(req Request) {
			go a.Decide(req.ID, "always", "")
			go cancel()
		})
		a.Ask(ctx, card(key), &Pending{Key: key, Label: "这一类", Tool: "powershell", Conversation: "console:c"})
		d := approvalRecv(t, resolved, "取消和批准竞争的事务结局")
		if has := a.Grants.Has(key); has != (d.Kind == "approved") {
			t.Fatalf("第 %d 次：结果是 %s，授权记了没有：%v", i, d.Kind, has)
		}
	}
}

func approvalRecv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Minute): // 只是出错时的上限，不是等待；机器忙时几秒都不够
		t.Fatalf("等%s超过一分钟", what)
		var zero T
		return zero
	}
}

func TestGrantWriteDoesNotBlockApprovals(t *testing.T) {
	for _, outcome := range []string{"写成", "写失败", "写失败前取消"} {
		t.Run(outcome, func(t *testing.T) {
			a := newApprovals(t)
			requests := make(chan Request, 4)
			a.OnRequest = []func(Request){func(r Request) { requests <- r }}
			asked := make(chan Decision, 1)
			go func() { asked <- a.Ask(context.Background(), card("a"), &Pending{Key: "write:a", Tool: "write"}) }()
			approvalRecv(t, requests, "第一张卡片")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			writeFile = func(name string, b []byte, perm os.FileMode) error {
				close(entered)
				<-release
				if outcome != "写成" {
					return errors.New("授权写入失败")
				}
				return os.WriteFile(name, b, perm)
			}
			decided := make(chan error, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				ok, err := a.Decide("a", "always", "")
				if (outcome == "写成") != ok {
					err = fmt.Errorf("认领写入结局不对：ok=%v err=%v", ok, err)
				}
				decided <- err
			}()
			t.Cleanup(func() {
				once.Do(func() { close(release) })
				approvalRecv(t, exited, "写入协程退出")
				// 改坏时先放开磁盘再清理，不能把测试挂在全局锁上。
				a.CancelAll()
				writeFile = os.WriteFile
			})
			approvalRecv(t, entered, "授权开始写文件")
			other := make(chan Decision, 1)
			go func() { other <- a.Ask(context.Background(), card("b"), nil) }()
			approvalRecv(t, requests, "写盘期间的新卡片")
			finished := make(chan bool, 1)
			go func() { ok, _ := a.Decide("b", "once", ""); finished <- ok }()
			if !approvalRecv(t, finished, "另一张卡片批准") || approvalRecv(t, other, "另一张卡片结局").Kind != "approved" {
				t.Fatal("写授权文件不能挡住另一张卡片批准")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { other <- a.Ask(ctx, card("c"), nil) }()
			approvalRecv(t, requests, "另一张等待取消的卡片")
			cancel()
			if approvalRecv(t, other, "写盘期间取消").Kind != "cancelled" {
				t.Fatal("写授权文件不能挡住另一张卡片取消")
			}
			expires := card("d")
			go func() { other <- a.Ask(context.Background(), expires, nil) }()
			approvalRecv(t, requests, "另一张立即到期的卡片")
			a.mu.Lock()
			a.pending["d"].timer.Reset(0)
			a.mu.Unlock()
			if approvalRecv(t, other, "写盘期间超时").Kind != "expired" {
				t.Fatal("写授权文件不能挡住另一张卡片到期")
			}
			go func() { finished <- a.Grants.Has("write:a") }()
			if approvalRecv(t, finished, "写盘期间读取授权") {
				t.Fatal("没写成前不能记授权")
			}
			if outcome == "写失败前取消" && a.Cancel("a") {
				t.Fatal("认领后的卡片不能提前取消，需等写入结局")
			}
			once.Do(func() { close(release) })
			err := approvalRecv(t, decided, "授权写入结局")
			if (err == nil) != (outcome == "写成") {
				t.Fatalf("写入错误没有照实返回：%v", err)
			}
			if outcome == "写失败" {
				if len(a.Pending()) != 1 || !a.Cancel("a") {
					t.Fatal("写失败且没有取消时，卡片应继续等待")
				}
			}
			want := "cancelled"
			if outcome == "写成" {
				want = "approved"
			}
			if d := approvalRecv(t, asked, "写盘卡片的最终结局"); d.Kind != want {
				t.Fatalf("写盘卡片应为 %s，实际 %+v", want, d)
			}
			if memory, disk := a.Grants.Has("write:a"), LoadGrants(a.Grants.file).Has("write:a"); memory != (outcome == "写成") || disk != memory {
				t.Fatalf("内存和文件授权不一致：memory=%v disk=%v", memory, disk)
			}
		})
	}
}

func TestApprovalCallbacksDoNotBlockOutcome(t *testing.T) {
	for _, callback := range []string{"request", "resolved"} {
		for _, outcome := range []string{"cancelled", "expired"} {
			t.Run(callback+"/"+outcome, func(t *testing.T) {
				a := newApprovals(t)
				requested, entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				defer close(release)
				block := func() { close(entered); <-release; close(exited) }
				a.OnRequest = []func(Request){func(Request) {
					close(requested)
					if callback == "request" {
						block()
					}
				}}
				a.OnResolved = []func(Request, Decision){func(Request, Decision) {
					if callback == "resolved" {
						block()
					}
				}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan Decision, 1)
				go func() { done <- a.Ask(ctx, card("blocked"), nil) }()
				approvalRecv(t, requested, "通知开始")
				if outcome == "cancelled" {
					cancel()
				} else {
					a.mu.Lock()
					a.pending["blocked"].timer.Reset(0) // 确认进入回调以后再让真实计时器到期
					a.mu.Unlock()
				}
				approvalRecv(t, entered, "被阻塞的回调")
				if d := approvalRecv(t, done, "回调卡住时交付结局"); d.Kind != outcome {
					t.Fatalf("应当收到 %s，实际 %+v", outcome, d)
				}
				// 回调一直卡到测试收尾：等回调的改法在上面的 approvalRecv 就过不去，不用量多快
				if len(a.Pending()) != 0 {
					t.Fatal("结局确定以后不能继续挂着卡片")
				}
				// 在测试返回前收回卡住的回调；结局必须在放开之前收到。
				t.Cleanup(func() { approvalRecv(t, exited, "迟到回调退出") })
			})
		}
	}
}

func TestApprovalSubscribersKeepEveryOutcome(t *testing.T) {
	a := newApprovals(t)
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() { close(release); approvalRecv(t, exited, "慢订阅排空") }()
	const cards = 20 // 超过旧名额池的 16 个，慢订阅不能让其他订阅丢消息。
	var once sync.Once
	slow := make(chan State, cards*2)
	a.Subscribe(func(r Request) {
		once.Do(func() { close(entered); <-release })
		slow <- State{Request: r}
	}, func(r Request, d Decision) {
		slow <- State{Request: r, Decision: &d}
		if r.ID == "19" {
			close(exited)
		}
	})
	fast := make(chan Request, cards)
	a.OnRequest = []func(Request){func(r Request) { fast <- r }}
	resolved := make(chan Decision, cards)
	a.OnResolved = []func(Request, Decision){func(_ Request, d Decision) { resolved <- d }}
	for i := range cards {
		id := fmt.Sprint(i)
		asked := make(chan Decision, 1)
		go func() { asked <- a.Ask(context.Background(), card(id), nil) }()
		approvalRecv(t, fast, "独立订阅的请求")
		if i == 0 {
			approvalRecv(t, entered, "慢订阅进入")
		}
		a.Cancel(id)
		if d := approvalRecv(t, resolved, "独立订阅的结局"); d.Kind != "cancelled" {
			t.Fatalf("结局错：%+v", d)
		}
		approvalRecv(t, asked, "慢订阅不挡回合")
	}
	release <- struct{}{}
	for i := range cards {
		result := approvalRecv(t, slow, "积压结局")
		if result.Decision == nil {
			if result.Request.ID != fmt.Sprint(i) || result.Request.Version != 1 {
				t.Fatalf("第 %d 张等待卡片错：%+v", i, result)
			}
			result = approvalRecv(t, slow, "等待之后的结局")
		}
		if result.Request.ID != fmt.Sprint(i) || result.Request.Version != 2 || result.Decision == nil || result.Decision.Kind != "cancelled" {
			t.Fatalf("第 %d 张卡片丢失或倒序：%+v", i, result)
		}
	}
}

func TestApprovalReconnectRejectsOldState(t *testing.T) {
	a := newApprovals(t)
	req := card("ended")
	req.Version = 2
	a.publish(State{Request: req, Decision: &Decision{Kind: "cancelled"}})
	req.Version = 1
	a.publish(State{Request: req})
	waiting := card("waiting")
	waiting.Version = 1
	a.publish(State{Request: waiting})
	states := make(chan State, 4)
	a.Subscribe(func(r Request) { states <- State{Request: r} }, func(r Request, d Decision) { states <- State{Request: r, Decision: &d} })
	got := map[string]State{}
	for range 2 {
		s := approvalRecv(t, states, "重连重放")
		got[s.Request.ID] = s
	}
	if ended := got["ended"]; ended.Request.Version != 2 || ended.Decision == nil || ended.Decision.Kind != "cancelled" || got["waiting"].Request.Version != 1 || got["waiting"].Decision != nil {
		t.Fatalf("重连状态不对：%+v", got)
	}
	snapshot := a.Snapshot()
	for _, state := range snapshot {
		if state.Request.ID == "ended" {
			state.Decision.Kind = "approved"
		}
	}
	for _, state := range a.Snapshot() {
		if state.Request.ID == "ended" && state.Decision.Kind != "cancelled" {
			t.Fatal("调用方改了快照，污染了审批记录")
		}
	}
}

func TestClaimedGrantWaitCanStop(t *testing.T) {
	for _, outcome := range []string{"写成", "写失败"} {
		for _, stop := range []string{"cancelled", "expired"} {
			t.Run(outcome+"/"+stop, func(t *testing.T) {
				a := newApprovals(t)
				requests := make(chan Request, 1)
				resolved := make(chan Decision, 1)
				a.Subscribe(func(r Request) { requests <- r }, func(_ Request, d Decision) { resolved <- d })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				asked := make(chan Decision, 1)
				go func() { asked <- a.Ask(ctx, card("claimed"), &Pending{Key: "write:a", Tool: "write"}) }()
				approvalRecv(t, requests, "授权卡片")
				entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				writeFile = func(name string, b []byte, mode os.FileMode) error {
					close(entered)
					<-release // 不自行返回：Ask 的取消不能依赖写函数返回。
					if outcome == "写失败" {
						return errors.New("授权写入失败")
					}
					return os.WriteFile(name, b, mode)
				}
				t.Cleanup(func() {
					once.Do(func() { close(release) })
					approvalRecv(t, exited, "授权事务收尾")
					writeFile = os.WriteFile
				})
				decided := make(chan error, 1)
				go func() {
					defer close(exited)
					_, err := a.Decide("claimed", "always", "")
					decided <- err
				}()
				approvalRecv(t, entered, "授权写入卡住")
				if stop == "cancelled" {
					cancel()
				} else {
					a.mu.Lock()
					a.pending["claimed"].timer.Reset(0)
					a.mu.Unlock()
				}
				if d := approvalRecv(t, asked, "写函数不返回时回合仍停止"); d.Kind != stop {
					t.Fatalf("应先返回 %s，实际 %+v", stop, d)
				}
				if len(a.Pending()) != 1 || a.Grants.Has("write:a") {
					t.Fatal("未完成的授权事务必须继续跟踪，不能提前记授权")
				}
				once.Do(func() { close(release) })
				if err := approvalRecv(t, decided, "授权写入结果"); (err == nil) != (outcome == "写成") {
					t.Fatalf("写入错误必须照实交回：%v", err)
				}
				want := stop
				if outcome == "写成" {
					want = "approved"
				}
				d := approvalRecv(t, resolved, "后台事务补发结局")
				if d.Kind != want || (outcome == "写失败" && !strings.Contains(d.Reason, "授权写入失败")) {
					t.Fatalf("最终结局应为 %s，写失败也要留下原因，实际 %+v", want, d)
				}
				if memory, disk := a.Grants.Has("write:a"), LoadGrants(a.Grants.file).Has("write:a"); memory != (outcome == "写成") || disk != memory || len(a.Pending()) != 0 {
					t.Fatalf("事务收尾不一致：内存 %v、磁盘 %v、挂起 %d", memory, disk, len(a.Pending()))
				}
			})
		}
	}
}

func TestApprovalPersistenceFailureIsReported(t *testing.T) {
	for _, phase := range []string{"request", "resolved"} {
		t.Run(phase, func(t *testing.T) {
			a := newApprovals(t)
			a.Persist = func(_ Request, d *Decision) error {
				if (phase == "request" && d == nil) || (phase == "resolved" && d != nil) {
					return errors.New("步骤库写失败")
				}
				return nil
			}
			a.OnRequest = []func(Request){func(r Request) { a.Decide(r.ID, "once", "") }}
			var mu sync.Mutex
			var reported []string
			a.Report = func(title, _ string) { mu.Lock(); reported = append(reported, title); mu.Unlock() }
			a.Logf = func(string, ...any) {}
			done := make(chan Decision, 1)
			go func() { done <- a.Ask(context.Background(), card("failed"), nil) }()
			if d := approvalRecv(t, done, "步骤写失败交回原因"); d.Kind != "denied" || !strings.Contains(d.Reason, "步骤库写失败") {
				t.Fatalf("写库失败不能放行或静默丢：%+v", d)
			}
			// 主人要看得到：卡没发出来、或点了允许却没生效（2026-11-08 静默失败盘点 一-11、三-4）
			want := map[string]string{"request": "审批没能建立", "resolved": "你的允许没有生效"}[phase]
			mu.Lock()
			defer mu.Unlock()
			if len(reported) != 1 || reported[0] != want {
				t.Fatalf("应提醒 %q，实际 %v", want, reported)
			}
		})
	}
}

func TestQueuedGrantWaitCanStop(t *testing.T) {
	a := newApprovals(t)
	requests := make(chan Request, 1)
	resolved := make(chan Decision, 1)
	a.Subscribe(func(r Request) { requests <- r }, func(_ Request, d Decision) { resolved <- d })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	asked := make(chan Decision, 1)
	go func() { asked <- a.Ask(ctx, card("queued"), &Pending{Key: "write:queued", Tool: "write"}) }()
	approvalRecv(t, requests, "排队授权的卡片")
	a.Grants.writeMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(a.Grants.writeMu.Unlock) }
	exited := make(chan struct{})
	t.Cleanup(func() { unlock(); approvalRecv(t, exited, "排队授权事务收尾") })
	go func() {
		defer close(exited)
		if ok, err := a.Decide("queued", "always", ""); !ok || err != nil {
			t.Errorf("排队写入没有完成：%v %v", ok, err)
		}
	}()
	deadline := time.NewTimer(time.Minute) // 只防卡死；等的是另一个协程认领，机器忙时 1 秒不够
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		a.mu.Lock()
		claimed := a.pending["queued"].claimed
		a.mu.Unlock()
		if claimed {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("卡片没进入已认领、等写盘锁的状态")
		}
	}
	cancel()
	if d := approvalRecv(t, asked, "等写盘锁时取消"); d.Kind != "cancelled" {
		t.Fatalf("应先取消回合，实际 %+v", d)
	}
	unlock()
	if d := approvalRecv(t, resolved, "排队授权最终结局"); d.Kind != "approved" || !LoadGrants(a.Grants.file).Has("write:queued") {
		t.Fatalf("放开锁后仍要写成授权并补发批准：%+v", d)
	}
}

func TestApprovalPersistenceWaitCanStop(t *testing.T) {
	for _, stop := range []string{"cancelled", "expired"} {
		t.Run(stop, func(t *testing.T) {
			a := newApprovals(t)
			requests := make(chan Request, 1)
			resolved := make(chan Decision, 1)
			a.Subscribe(func(r Request) { requests <- r }, func(_ Request, d Decision) { resolved <- d })
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			a.Persist = func(_ Request, d *Decision) error {
				if d != nil {
					close(entered)
					<-release
				}
				return nil
			}
			t.Cleanup(func() {
				once.Do(func() { close(release) })
				if d := approvalRecv(t, resolved, "原批准记录最终补发"); d.Kind != "approved" {
					t.Errorf("停止等待不能丢掉已认领的批准记录：%+v", d)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			asked := make(chan Decision, 1)
			go func() { asked <- a.Ask(ctx, card("persisting"), nil) }()
			approvalRecv(t, requests, "准备批准的卡片")
			a.mu.Lock()
			p := a.pending["persisting"]
			a.mu.Unlock()
			if ok, err := a.Decide("persisting", "once", ""); !ok || err != nil {
				t.Fatalf("认领批准失败：%v %v", ok, err)
			}
			approvalRecv(t, entered, "批准结局正在写库")
			if stop == "cancelled" {
				cancel()
			} else {
				// 批准已认领且正在写库，到期不能改成未执行；先放开写库。
				if a.settle("persisting", "expired", "") {
					t.Fatal("已认领的批准不能被收成到期")
				}
				a.expire(p)
				once.Do(func() { close(release) })
			}
			want := stop
			if stop == "expired" {
				want = "approved"
			}
			if d := approvalRecv(t, asked, "结局写库卡住后停止等待"); d.Kind != want {
				t.Fatalf("应返回 %s，实际 %+v", want, d)
			}
		})
	}
}

// 始终允许的记录读坏了：格式坏的改名留存、之后照常写；读不了的不写回，免得把原有授权整份盖掉（盘点 三-3）
func TestGrantsLoadFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "tool-grants.yml")
	if err := os.WriteFile(bad, []byte("grants: [ {key: \"x\""), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGrants(bad)
	if g.LoadErr == nil {
		t.Fatal("格式坏了要报出来")
	}
	kept, _ := filepath.Glob(bad + ".坏-*")
	if len(kept) != 1 {
		t.Fatalf("坏文件要改名留存：%v", kept)
	}
	if err := g.Put(Rule{Key: "write:a", Label: "a", Tool: "write", At: 1}); err != nil || !LoadGrants(bad).Has("write:a") {
		t.Fatalf("改名后照常写：%v", err)
	}

	unreadable := filepath.Join(dir, "dir-grants.yml")
	if err := os.Mkdir(unreadable, 0o755); err != nil { // 目录读不成文件，模拟读不了
		t.Fatal(err)
	}
	g = LoadGrants(unreadable)
	g.file = filepath.Join(dir, "writable.yml") // 换成写得进去的位置：挡住写回的只能是"读不了"这一条判断
	if g.LoadErr == nil || g.Put(Rule{Key: "write:b", Label: "b", Tool: "write", At: 1}) == nil {
		t.Fatalf("读不了的不能写回：%v", g.LoadErr)
	}
	if st, err := os.Stat(unreadable); err != nil || !st.IsDir() {
		t.Fatal("读不了的原位置被动了")
	}
	if g := LoadGrants(filepath.Join(dir, "none.yml")); g.LoadErr != nil {
		t.Fatalf("没有文件不是错：%v", g.LoadErr)
	}
}
