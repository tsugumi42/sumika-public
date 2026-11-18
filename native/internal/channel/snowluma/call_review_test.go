package snowluma

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 只在内存里控制写入和断线，号码沿用同包编的号。
type reviewConn struct {
	write  func(context.Context, []byte) error
	writes atomic.Int32
	closed chan struct{}
	once   sync.Once
}

func newReviewConn() *reviewConn { return &reviewConn{closed: make(chan struct{})} }
func (c *reviewConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errConnLost
	}
}
func (c *reviewConn) Write(ctx context.Context, b []byte) error {
	c.writes.Add(1)
	if c.write != nil {
		return c.write(ctx, b)
	}
	return errors.New("编的写错误")
}
func (c *reviewConn) Close() { c.once.Do(func() { close(c.closed) }) }

func reviewAdapter() (*Adapter, *fake.Clock, *reviewConn, *live, uint64) {
	clk := fake.NewClock(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	a := &Adapter{cfg: Config{Account: sendAccount, Logf: func(string, ...any) {}}, clock: clk, alerts: newTestAlerts()}
	c := newReviewConn()
	l := a.attach(c)
	sid := a.openSession()
	a.noteFrame([]byte(heartbeatFrame()))
	return a, clk, c, l, sid
}

func reviewSend(ctx context.Context, a *Adapter, poke bool) types.Receipt {
	if poke {
		r, _ := a.Poke(ctx, sendAccount, groupConv(), sendFriend)
		return r
	}
	r, _ := a.Send(ctx, sendAccount, types.Part{ID: "编的段", Kind: types.PartMessage, Conv: groupConv(), Body: types.TextBody(sendText)})
	return r
}

func TestAuthorizedConnectionCannotMoveToReplacement(t *testing.T) {
	for _, action := range []string{"send_group_msg", "group_poke"} {
		t.Run(action, func(t *testing.T) {
			a, _, old, l, sid := reviewAdapter()
			a.markReady(sid)
			allowed, err := a.allowSend(context.Background())
			if err != nil || allowed != l {
				t.Fatalf("没有取得旧连接许可：%v", err)
			}
			// 把「取得许可」和「写 action」之间的整次重连确定地插进来。
			a.closeSession()
			a.detach(l)
			next := newReviewConn()
			a.attach(next)
			a.openSession()
			frame, wrote, err := a.call(context.Background(), allowed, action, map[string]any{})
			r := a.finish("编的段", kindText, frame, wrote, err)
			if next.writes.Load() != 0 || old.writes.Load() != 0 || r.Status != types.ReceiptFailed || !errors.Is(err, errNotConnected) {
				t.Fatalf("旧许可转投了新连接：old=%d new=%d status=%s err=%v", old.writes.Load(), next.writes.Load(), r.Status, err)
			}
		})
	}
}

func TestWriteErrorIsUnknownUnlessReplyMatched(t *testing.T) {
	for _, poke := range []bool{false, true} {
		for _, reply := range []bool{false, true} {
			name := "text"
			if poke {
				name = "poke"
			}
			if reply {
				name += "_reply"
			}
			t.Run(name, func(t *testing.T) {
				a, _, c, _, sid := reviewAdapter()
				a.markReady(sid)
				c.write = func(_ context.Context, b []byte) error {
					if reply {
						a.takeReply(withEcho(`{"retcode":0,"data":{"message_id":-7654321}}`, parseAction(b).Echo))
					}
					return errors.New("编的：对端收下后写入仍报错")
				}
				r := reviewSend(context.Background(), a, poke)
				want := types.ReceiptUnknown
				if reply {
					want = types.ReceiptConfirmed
				}
				if r.Status != want {
					t.Fatalf("Write 已进入，reply=%v，回执=%s，要 %s", reply, r.Status, want)
				}
				wantID := ""
				if reply && !poke {
					wantID = "-7654321"
				}
				if r.PlatformID != wantID || c.writes.Load() != 1 {
					t.Fatalf("回执=%+v writes=%d", r, c.writes.Load())
				}
				a.mu.Lock()
				pending := len(a.pend)
				a.mu.Unlock()
				if pending != 0 {
					t.Fatalf("写错误后还留着 %d 个等待项", pending)
				}
			})
		}
	}
}

func TestMatchedReplyDeliveryExcludesCancellation(t *testing.T) {
	a, _, _, _, _ := reviewAdapter()
	// 无缓冲仅作交付屏障，把「已经匹配、尚未投递」停住；生产通道缓冲为 1。
	ch := make(chan callResult)
	a.pend = map[string]chan callResult{"s1": ch}
	replied := make(chan struct{})
	go func() { a.takeReply([]byte(`{"echo":"s1","retcode":0}`)); close(replied) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !a.mu.TryLock() {
			break
		}
		matched := a.pend["s1"] == nil
		a.mu.Unlock()
		if matched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回包没有进入匹配阶段")
		}
		runtime.Gosched()
	}
	exited := make(chan struct{})
	go func() { a.complete("s1", ch, true, context.Canceled); close(exited) }()
	select {
	case <-exited:
		t.Error("回包匹配后、投递前，取消提前完成")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case res := <-ch:
		if string(res.frame) != `{"echo":"s1","retcode":0}` {
			t.Errorf("回包被丢掉：%s", res.frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("匹配回包没有投递")
	}
	<-replied
	<-exited
}

func TestCallDeadlineInterruptsWrite(t *testing.T) {
	for _, kind := range []string{"Send", "Poke", "login", "status", "checking"} {
		t.Run(kind, func(t *testing.T) {
			a, clk, c, l, sid := reviewAdapter()
			entered := make(chan struct{})
			c.write = func(ctx context.Context, b []byte) error {
				if kind == "status" && parseAction(b).Action == "get_login_info" {
					a.takeReply(withEcho(`{"retcode":0,"data":{"user_id":90001}}`, parseAction(b).Echo))
					return nil
				}
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "Send" || kind == "Poke" {
				a.markReady(sid)
			}
			done := make(chan types.Receipt, 1)
			go func() {
				if kind == "login" || kind == "status" {
					a.verify(ctx, l, sid)
					done <- types.Receipt{}
				} else {
					done <- reviewSend(ctx, a, kind == "Poke")
				}
			}()
			if kind != "checking" {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("没有进入 Write")
				}
			}
			// 固定起点；9 秒不能结束，第 10 秒必须打断卡住的 Write 或许可等待。
			clk.WaitTimer(t, callTimeout)
			clk.Advance(9 * time.Second)
			select {
			case <-done:
				t.Fatal("不足 10 秒就结束")
			default:
			}
			clk.Advance(time.Second)
			select {
			case r := <-done:
				if kind == "Send" || kind == "Poke" {
					if r.Status != types.ReceiptUnknown {
						t.Fatalf("卡住的 Write 回执=%s，要未知", r.Status)
					}
				} else if kind == "checking" {
					if r.Status != types.ReceiptFailed || c.writes.Load() != 0 {
						t.Fatalf("核验等待到点：%+v writes=%d", r, c.writes.Load())
					}
				} else {
					select {
					case <-c.closed:
					default:
						t.Fatal("核验 Write 超时没有关闭连接")
					}
					if a.sending() {
						t.Fatal("核验写超时仍允许发送")
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("第 10 秒没有打断卡住的调用")
			}
		})
	}
}

func TestSelfMismatchDuringVerificationStaysBlocked(t *testing.T) {
	a, _, c, l, sid := reviewAdapter()
	status := make(chan sentAction, 1)
	var logs atomic.Int32
	a.cfg.Logf = func(string, ...any) { logs.Add(1) }
	c.write = func(_ context.Context, b []byte) error {
		act := parseAction(b)
		if act.Action == "get_login_info" {
			a.takeReply(withEcho(`{"retcode":0,"data":{"user_id":90001}}`, act.Echo))
		} else {
			status <- act
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.verify(ctx, l, sid); close(done) }()
	var act sentAction
	select {
	case act = <-status:
	case <-time.After(2 * time.Second):
		t.Fatal("没有进入健康查询")
	}
	a.noteFrame([]byte(`{"post_type":"meta_event","self_id":90008}`))
	a.noteFrame([]byte(heartbeatFrame()))
	a.takeReply(withEcho(`{"retcode":0,"data":{"online":true,"good":true}}`, act.Echo))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("健康回包未处理")
	}
	if a.sending() {
		t.Fatal("同一次连接的 self_id 冲突被健康回包恢复")
	}
	for _, poke := range []bool{false, true} {
		r := reviewSend(ctx, a, poke)
		if r.Status != types.ReceiptUnknown || r.Err != errSelfMismatch.Error() {
			t.Errorf("冲突后回执=%+v", r)
		}
	}
	// 日志去重不能挡住停发判断；即使日志已经记过，也必须重新设置冲突状态。
	a.mu.Lock()
	a.phase = phaseChecking
	a.mu.Unlock()
	a.noteFrame([]byte(`{"post_type":"meta_event","self_id":90008}`))
	a.mu.Lock()
	phase := a.phase
	a.mu.Unlock()
	if phase != phaseSelf || logs.Load() != 1 {
		t.Errorf("日志去重影响停发：phase=%d logs=%d", phase, logs.Load())
	}
	if c.writes.Load() != 2 {
		t.Errorf("冲突后仍有发送 action，writes=%d", c.writes.Load())
	}
}

func TestLoginIDForms(t *testing.T) {
	for _, tc := range []struct {
		name, account, user string
		ready               bool
	}{
		{"整数", "90001", "90001", true},
		{"字符串", "90001", `"90001"`, true},
		{"配置前导零", "090001", "90001", false},
		{"回包前导零", "90001", `"090001"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newActionPeer(t, confirmReply())
			peer.setLoginQ(tc.user)
			ad, err := NewWS(Config{URL: peer.url(), Token: sendToken, Account: tc.account, Logf: func(string, ...any) {}}, fake.NewClock(time.Now()), newTestAlerts())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			sink := newTestSink()
			go func() { done <- ad.Run(ctx, sink) }()
			t.Cleanup(func() { cancel(); waitRun(t, done) })
			recvStatus(t, sink, types.StatusConnected)
			if tc.ready {
				deadline := time.Now().Add(2 * time.Second)
				for !ad.sending() && time.Now().Before(deadline) {
					runtime.Gosched()
				}
				if !ad.sending() {
					t.Fatal("有效登录号没有通过核验")
				}
			} else {
				recvStatus(t, sink, types.StatusDisconnected)
			}
			for _, poke := range []bool{false, true} {
				var r types.Receipt
				if poke {
					r, err = ad.Poke(ctx, tc.account, groupConv(), sendFriend)
				} else {
					r, err = ad.Send(ctx, tc.account, types.Part{Kind: types.PartMessage, Conv: groupConv(), Body: types.TextBody(sendText)})
				}
				if err != nil || (r.Status == types.ReceiptConfirmed) != tc.ready {
					t.Fatalf("登录格式后的回执=%+v err=%v", r, err)
				}
			}
			wantSends := 0
			if tc.ready {
				wantSends = 2
			}
			if got := len(peer.actions()); got != wantSends {
				t.Fatalf("登录格式后的发送=%d，要 %d", got, wantSends)
			}
			if !tc.ready && peer.count("get_status") != 0 {
				t.Fatal("登录未通过仍查询健康")
			}
		})
	}
}

// 旧连接的核验放开锁以后才记 self_id 冲突，这时已经换了新连接：冲突不能写到新连接上（复核 snd-12c 第 1 条）。
func TestStaleSelfMismatchDoesNotBlockNewSession(t *testing.T) {
	a, _, _, _, old := reviewAdapter()
	fresh := a.openSession()
	a.noteSelfMismatch(old)
	a.mu.Lock()
	phase, logged := a.phase, a.selfLogged
	a.mu.Unlock()
	if phase == phaseSelf || logged {
		t.Fatalf("旧连接的冲突写到了新连接 %d 上：phase=%v selfLogged=%v", fresh, phase, logged)
	}
	a.noteSelfMismatch(fresh)
	a.mu.Lock()
	phase = a.phase
	a.mu.Unlock()
	if phase != phaseSelf {
		t.Fatalf("本次连接的冲突没有停发：phase=%v", phase)
	}
}
