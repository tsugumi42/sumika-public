package snowluma

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
)

// 连不上或断了以后的退避：1、2、4……最多 60 秒，每次再加一点抖动（规格-渠道中间件第十三节，照 Bun 的 client.ts）。
// 断线满 30 秒提醒一次；29 秒内回来的不吵（照 Bun 的 qq-health.ts，规格-渠道中间件第十四节"掉线提醒"）。
// 提醒门槛和退避上限是两回事：连不上的间隔会长到 60 秒，提醒仍然在第 30 秒那一次。
const (
	// DefaultBackoff 是第一次重连前等的时长；录帧工具（cmd/snowluma-record）也用它，两边退避一样
	DefaultBackoff = time.Second
	maxBackoff     = 60 * time.Second
	defaultAlert   = 30 * time.Second
	// stableAfter：待办第⑤项未定稳定门槛，按 W-SHUTDOWN 取 30 秒；核验通过可提前恢复。
	stableAfter = 30 * time.Second
	// maxFrameBytes 是单帧的上限。coder/websocket 默认只有 32 KB，带图的消息一帧就超
	maxFrameBytes = 16 << 20
	// heartbeatMisses 是"几个心跳间隔没收到任何帧就判掉线"（规格-渠道中间件第十三节）。
	// 半开的连接不会自己报错，只能靠这个计时器发现；间隔用协议端给的值，不写死
	heartbeatMisses = 3
	// handshakeTimeout 是拨号加握手的上限。协议端卡死（收了 TCP 却一直不回升级）时 Dial 会一直挂着，
	// 既不重连也不提醒；到点算一次连不上，走退避和 30 秒提醒
	handshakeTimeout = 10 * time.Second
)

// Config 是连一个 OneBot 正向 WS 端点的配置。地址和令牌由调用方从配置里读；这个包不读文件、不写死。
type Config struct {
	URL     string               // ws:// 或 wss:// 的 OneBot 正向 WS 地址
	Token   string               // access token；空表示握手不带 Authorization。这个值不进日志、不进错误
	Account string               // 这个端点对应的号：报连接状态、提醒的键（qq:<账号>:conn）都用它
	Logf    func(string, ...any) // 统一日志出口；nil 时照旧用 log.Printf
	Warnf   func(string, ...any) // WARN（出错但会自己接上）；nil 时用 Logf
	Errorf  func(string, ...any) // ERROR（有数据会丢、或要人处理）；nil 时用 Logf
	// CallTimeout 是一次发送动作等回包的上限；0 用 callTimeout（10 秒）。只有测试调小，才不用陪真实的 10 秒等“协议端不回包”。
	CallTimeout time.Duration
	// QueryGap 是两次只读查询之间最少隔多久；0 用 queryGap（2 秒）。只有测试调小：假时钟不前进时，每次查询都要真等满 2 秒。
	QueryGap time.Duration
	// Selfcheck 是运行自检的计数器（规格-运行时自检）：收到的帧、断开事件往里数；nil 不数。只数，不改任何路径的行为
	Selfcheck *selfcheck.Counters
}

// Alerter 是提醒小包（internal/alert）里用到的两个方法，签名照 alert.Hub。
// 只收接口、不收 *alert.Hub：这个包不依赖提醒的实现，测试可以换成记数的假的。
type Alerter interface {
	Raise(alert.Alert)
	Clear(key string, note alert.Note)
}

// Conn 是一条已建立的连接。Read、Write 在 ctx 取消时返回；Write 和 Read 可以同时进行。
type Conn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close()
}

// dialFunc 拨号。测试用 httptest 起假协议端，所以不换这个字段，拨的仍是真连接。
type dialFunc func(context.Context, Config) (Conn, error)

// NewWS 造一个连 OneBot 正向 WS 的适配器：拨号、收帧、交给 translate 翻译；断了按退避重连，断线满 30 秒提醒一次。
// 同一条连接上也发 action（文字、戳一戳）。发不发由出口决定：装配处的共存钳制还在时，QQ 停在试运行，不会调到 Send。
// clock 为空时用真的钟；alerts 必须有（没有出口就是没接好）。
func NewWS(cfg Config, clock types.Clock, alerts Alerter) (*Adapter, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("snowluma: 没有配置 WebSocket 地址")
	}
	if err := CheckURL(cfg.URL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Account) == "" {
		return nil, errors.New("snowluma: 没有配置账号，掉线提醒的键要用它")
	}
	if alerts == nil {
		return nil, errors.New("snowluma: 没有提醒出口，断线满 30 秒要 Raise 一次")
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Adapter{
		cfg:          cfg,
		clock:        clock,
		alerts:       alerts,
		dial:         Dial,
		jitter:       Jitter,
		backoffStart: DefaultBackoff,
		backoffMax:   maxBackoff,
		alertAfter:   defaultAlert,
		started:      make(chan struct{}),
	}, nil
}

// warnf、errorf 按级别写；没有注入对应出口时退回 logf（级别在 applog 里是 INFO）
func (a *Adapter) warnf(format string, args ...any) {
	if a.cfg.Warnf != nil {
		a.cfg.Warnf(format, args...)
		return
	}
	a.logf(format, args...)
}

func (a *Adapter) errorf(format string, args ...any) {
	if a.cfg.Errorf != nil {
		a.cfg.Errorf(format, args...)
		return
	}
	a.logf(format, args...)
}

func (a *Adapter) logf(format string, args ...any) {
	if a.cfg.Logf != nil {
		a.cfg.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// realClock 是生产用的钟；测试换 fake.Clock 自己拨。
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(d time.Duration) types.Timer {
	return realTimer{time.NewTimer(d)}
}

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }

// CheckURL 拒绝把令牌写进地址的配置（?access_token=、user:pass@）：地址会进日志和断开原因，令牌要放 Config.Token，
// 走 Authorization 头。错误里不带地址本身。适配器和录帧工具都在开始前查
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("snowluma: WebSocket 地址格式不对")
	}
	if u.User != nil || u.Query().Has("access_token") {
		return errors.New("snowluma: 令牌不要写在地址里，放进 Token（走 Authorization 头，不进日志）")
	}
	return nil
}

// Jitter 在 [d/2, d] 里均匀取一个：重连不会所有号同一刻撞上来。录帧工具也用它。
func Jitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half+1)
}

// Dial 拨 OneBot 正向 WS。令牌走 Authorization: Bearer；错误里只说握手结果，不带令牌和完整地址。
// 适配器和录帧工具（cmd/snowluma-record）共用。
func Dial(ctx context.Context, cfg Config) (Conn, error) {
	return dialWithin(ctx, cfg, handshakeTimeout)
}

// dialWithin 是带握手上限的 Dial。上限只管握手：握完就收掉，连上的连接不受它影响
// （coder/websocket 处理 HTTPClient.Timeout 也是这样，dial.go 的 cloneWithDefaults）。
func dialWithin(ctx context.Context, cfg Config, limit time.Duration) (Conn, error) {
	hctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	opts := &websocket.DialOptions{}
	if cfg.Token != "" {
		opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + cfg.Token}}
	}
	c, resp, err := websocket.Dial(hctx, cfg.URL, opts)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("WebSocket 握手被拒（HTTP %d）", resp.StatusCode)
		}
		// 没拿到回应（连不上、超时）时库的错误里有 url.Error，带着完整地址；只留里面的原因（主机和端口，不带路径和参数）
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("WebSocket 拨号失败：%w", ue.Err)
		}
		return nil, err
	}
	c.SetReadLimit(maxFrameBytes)
	return &wsConn{c}, nil
}

type wsConn struct{ c *websocket.Conn }

func (c *wsConn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := c.c.Read(ctx)
	return data, err
}

func (c *wsConn) Write(ctx context.Context, p []byte) error {
	return c.c.Write(ctx, websocket.MessageText, p)
}

// Close 直接断开，不走关闭握手。要关的连接多半已经坏了（读出错、看门狗判掉线）：半开时对方不会回关闭帧，
// 带握手的 Close 要干等库里 5 秒的超时（coder/websocket 的 waitCloseHandshake，读协程占着读锁），重连和掉线状态都跟着晚。
// 正常退出时 ctx 一取消库自己就断开了，也用不上握手（规格第十三节"重连前先关掉旧连接：半开的旧连接不等它超时"）
func (c *wsConn) Close() { _ = c.c.CloseNow() }

// reconnectState 只由收帧、重连循环使用；握手成功不改变这一段掉线的起点和退避。
type reconnectState struct {
	since        time.Time
	down, raised bool
	backoff      time.Duration
}

func (s *reconnectState) recover(a *Adapter) {
	if s.down && s.raised {
		a.clear(s.since)
	}
	s.down, s.raised = false, false
	s.backoff = a.backoffStart
}

// runWS 是连接、收帧、断了重连的那条循环；ctx 取消就停，返回前把所有计时器停掉。
func (a *Adapter) runWS(ctx context.Context, sink types.Sink) error {
	s := reconnectState{backoff: a.backoffStart}
	for ctx.Err() == nil {
		c, err := a.dial(ctx, a.cfg)
		if err != nil {
			// ctx 在拨号途中被取消时，主循环条件也有这一道；这里仍要提前看一次，
			// 否则下面会把"拨号失败"当成一次真的掉线：多写一行日志、多发一个断开状态，退出前中间件会看到假掉线。
			// 测试只留主循环那一道时照样过（waitBackoff 撞上 ctx.Done 就返回），不代表这一道多余。
			if ctx.Err() != nil {
				break
			}
			if !s.down {
				s.down, s.since = true, a.clock.Now()
				a.warnf("连不上协议端，按退避重连: %s", a.safeError(err))
				a.cfg.Selfcheck.Disconnect(a.clock.Now()) // 从在线变成连不上：一次断开事件；之后同一次断线里的退避重试不再数
				a.report(ctx, sink, types.StatusDisconnected, a.reason(err))
			} else {
				a.report(ctx, sink, types.StatusReconnecting, a.reason(err))
			}
		} else {
			// 先报连上再核验。核验没过时 Send 不写 action，不会把「连上」当成可以发。
			l := a.attach(c)
			sid := a.openSession()
			a.report(ctx, sink, types.StatusConnected, "")
			vctx, vcancel := context.WithCancel(ctx)
			go a.verify(vctx, l, sid)
			err = a.readFrames(ctx, sink, l, &s)
			vcancel()
			mismatch := a.closeSession()
			a.detach(l)
			c.Close()
			if ctx.Err() != nil {
				break
			}
			a.cfg.Selfcheck.Disconnect(a.clock.Now()) // 一条建立过的连接结束了：一次断开事件（连上又马上断的抖动每次都数）
			// closeSession 在同一把锁里挡住旧核验后再看一次：核验可能恰好在 readFrames 返回以后成功。
			select {
			case <-l.ready:
				s.recover(a)
			default:
			}
			if mismatch {
				a.warnf("登录号和配置不一致，60 秒以后再连")
				a.report(ctx, sink, types.StatusDisconnected, "登录号和配置不一致")
				if !a.waitOnly(ctx, mismatchRetry) {
					break
				}
				continue
			}
			if ctx.Err() != nil {
				break
			}
			if !s.down {
				s.down, s.since = true, a.clock.Now()
			}
			a.warnf("连接断了，按退避重连: %s", a.safeError(err))
			a.report(ctx, sink, types.StatusDisconnected, a.reason(err))
		}
		if !a.waitBackoff(ctx, s.backoff, s.since, &s.raised) {
			break
		}
		s.backoff = a.nextBackoff(s.backoff)
	}
	return nil
}

// errHeartbeatStopped 是看门狗判掉线时返回给 runWS 的错误（走退避重连和 30 秒提醒）。
var errHeartbeatStopped = errors.New("心跳和所有帧都停了，判掉线")

type readResult struct {
	frame []byte
	err   error
}

// readFrames 一条连接上一直读到出错，或者看门狗判掉线。
//
// 看门狗（规格-渠道中间件第十三节"超过 3 个心跳间隔没收到就判掉线"）：收到第一个带间隔的心跳以后，
// 按该间隔的 heartbeatMisses 倍计时；这条连接上任何一帧（心跳、消息、通知）都算"收到"，重新计时
// （通信还在就不是掉线；半开才是"一帧都没有"）。收到新的心跳就换成它给的间隔。没收到过心跳时不计时：
// 不知道间隔，不写死（第二十节第 5 条）。到期关连接交给 runWS：重连前先关旧连接（第十三节）。
func (a *Adapter) readFrames(ctx context.Context, sink types.Sink, l *live, state *reconnectState) error {
	c := l.c
	connectedAt := a.clock.Now()
	stable := a.clock.NewTimer(stableAfter)
	defer stable.Stop()
	stableC, readyC := stable.C(), (<-chan struct{})(l.ready)
	var alertTimer types.Timer
	var alertC <-chan time.Time
	if state.down && !state.raised {
		alertTimer = a.clock.NewTimer(a.remaining(state.since))
		alertC = alertTimer.C()
		defer alertTimer.Stop()
	}
	recover := func() {
		state.recover(a)
		stableC, readyC, alertC = nil, nil, nil
		stable.Stop()
		if alertTimer != nil {
			alertTimer.Stop()
		}
	}
	// 读错误和恢复信号可能同时就绪；不能因 select 先取了读错误而丢掉已经完成的恢复。
	defer func() {
		if ctx.Err() == nil && stableC != nil {
			select {
			case <-l.ready:
				recover()
			default:
				if a.clock.Now().Sub(connectedAt) >= stableAfter {
					recover()
				}
			}
		}
	}()
	results := make(chan readResult)
	done := make(chan struct{})
	defer close(done)
	// 读挡在 c.Read 里，看门狗要和它赛跑，所以读放一个协程；返回时 runWS 会关连接，Read 跟着返回、协程退出。
	go func() {
		for {
			frame, err := c.Read(ctx)
			select {
			case results <- readResult{frame: frame, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var interval time.Duration
	var dog types.Timer
	var dogC <-chan time.Time
	defer func() {
		if dog != nil {
			dog.Stop()
		}
	}()
	// got 处理读到的一帧：先按它重新计时。action 回包按 echo 交给还在等的调用，不翻译成事件；其余交给接收端
	got := func(r readResult) error {
		if r.err != nil {
			return r.err
		}
		if iv := heartbeatInterval(r.frame); iv > 0 {
			interval = iv
		}
		if interval > 0 {
			if dog != nil {
				dog.Stop()
			}
			dog = a.clock.NewTimer(interval * heartbeatMisses)
			dogC = dog.C()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.noteFrame(r.frame)
		a.noteHeartbeat(r.frame)
		if !a.takeReply(r.frame) {
			a.dispatch(ctx, sink, r.frame)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stableC:
			recover()
		case <-readyC:
			recover()
		case <-alertC:
			// 核验可能已经完成、只是收帧循环还没取到信号；不能先报一次假掉线。
			select {
			case <-l.ready:
				recover()
			default:
				alertC = nil
				state.raised = true
				a.raise(state.since)
			}
		case <-dogC:
			// 交给接收端那一步卡得比看门狗还久时，看门狗会和这期间已经读到的帧同时就绪，select 随便挑一个：
			// 先看有没有读到的帧，有就不算掉线（连接是好的，慢的是接收端）
			select {
			case r := <-results:
				if err := got(r); err != nil {
					return err
				}
				continue
			default:
			}
			a.warnf("%v 没收到任何帧（心跳间隔 %v 的 %d 倍），判掉线", interval*heartbeatMisses, interval, heartbeatMisses)
			return errHeartbeatStopped
		case r := <-results:
			if err := got(r); err != nil {
				return err
			}
		}
	}
}

// waitBackoff 等退避到点。等待期间如果断线满 30 秒就 Raise 一次，之后同一段掉线不再提醒。
// 返回 false 表示 ctx 取消了（调用方要退出）。
func (a *Adapter) waitBackoff(ctx context.Context, d time.Duration, since time.Time, raised *bool) bool {
	wait := a.clock.NewTimer(a.jitter(d))
	defer wait.Stop()
	var at types.Timer
	var alertC <-chan time.Time
	if !*raised {
		at = a.clock.NewTimer(a.remaining(since))
		alertC = at.C()
		defer at.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-wait.C():
			// 退避和提醒同时到点时也不能丢掉这次提醒。
			if !*raised && a.remaining(since) == 0 {
				*raised = true
				a.raise(since)
			}
			return true
		case <-alertC:
			alertC = nil
			*raised = true
			a.raise(since)
		}
	}
}

// remaining 是离"断线满 30 秒"还差多久；已经过了就是 0。
func (a *Adapter) remaining(since time.Time) time.Duration {
	d := a.alertAfter - a.clock.Now().Sub(since)
	if d < 0 {
		return 0
	}
	return d
}

func (a *Adapter) raise(since time.Time) {
	down := a.clock.Now().Sub(since)
	a.alerts.Raise(alert.Alert{
		Key:   a.alertKey(),
		Class: "账号掉线",
		Title: "QQ 连接断了",
		Body:  fmt.Sprintf("QQ 连接断了 %d 秒，正在自动重连", int(down/time.Second)),
	})
}

func (a *Adapter) clear(since time.Time) {
	down := a.clock.Now().Sub(since)
	a.alerts.Clear(a.alertKey(), alert.Note{
		Title: "QQ 连接恢复了",
		Body:  fmt.Sprintf("断了 %d 秒", int(down/time.Second)),
	})
}

// now 是适配器的钟；New 造的（调用方给读帧函数的）没有钟，用真的
func (a *Adapter) now() time.Time {
	if a.clock == nil {
		return time.Now()
	}
	return a.clock.Now()
}

func (a *Adapter) alertKey() string { return "qq:" + a.cfg.Account + ":conn" }

func (a *Adapter) nextBackoff(d time.Duration) time.Duration {
	return nextBackoff(d, a.backoffMax)
}

// NextBackoff 是重连退避的下一次等待：从 1 秒起翻倍，最多 60 秒（规格第十三节）。录帧工具也用它。
func NextBackoff(d time.Duration) time.Duration { return nextBackoff(d, maxBackoff) }

func nextBackoff(d, max time.Duration) time.Duration {
	if d >= max {
		return max
	}
	d *= 2
	if d > max {
		d = max
	}
	return d
}

// report 把连接状态交给中间件；接收端没收下只记日志，不影响连接。
func (a *Adapter) report(ctx context.Context, sink types.Sink, kind, reason string) {
	err := sink.Status(ctx, types.Status{Kind: kind, Account: a.cfg.Account, Time: a.clock.Now(), Reason: reason})
	if err != nil {
		a.errorf("接收端未收下连接状态: %v", err)
	}
}

// safeError 不记录协议端给的关闭正文，只留关闭码和固定类别；其他错误抹掉令牌。
func (a *Adapter) safeError(err error) string {
	if err == nil {
		return ""
	}
	if code := websocket.CloseStatus(err); code != -1 {
		category := "其他"
		switch code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			category = "正常关闭"
		case websocket.StatusPolicyViolation:
			category = "策略拒绝"
		case websocket.StatusProtocolError, websocket.StatusUnsupportedData, websocket.StatusInvalidFramePayloadData, websocket.StatusMessageTooBig:
			category = "协议错误"
		case websocket.StatusInternalError, websocket.StatusServiceRestart, websocket.StatusTryAgainLater:
			category = "服务端错误"
		}
		return fmt.Sprintf("WebSocket 关闭，关闭码 %d，类别 %s", code, category)
	}
	s := err.Error()
	if a.cfg.Token != "" {
		s = strings.ReplaceAll(s, a.cfg.Token, "***")
	}
	return s
}

// reason 是报给中间件的断开原因：截短，不含令牌。
func (a *Adapter) reason(err error) string {
	if err == nil {
		return ""
	}
	r := []rune(a.safeError(err))
	if len(r) > 200 {
		return string(r[:200])
	}
	return string(r)
}

// dispatch 把一帧翻译以后交给接收端。翻译不了的帧只丢这一帧，连接照常。
func (a *Adapter) dispatch(ctx context.Context, sink types.Sink, frame []byte) {
	a.cfg.Selfcheck.Frame() // 收到的每一帧恰好落进一个去向：解码失败、不是消息、交给入库（规格-运行时自检 S1）
	in, notice, status, err := translate(frame)
	if err != nil {
		a.cfg.Selfcheck.Add(selfcheck.FrameDecodeFailed)
		a.warnf("无法解析帧（%d 字节）: %v", len(frame), err)
		return
	}
	if in == nil {
		a.cfg.Selfcheck.Add(selfcheck.FrameNonMessage)
	}
	// 帧里没有可用的时间（eventTime 给零值）就用收到的时间
	now := a.now()
	if in != nil && in.Time.IsZero() {
		in.Time = now
	}
	if notice != nil && notice.Time.IsZero() {
		notice.Time = now
	}
	if status != nil && status.Time.IsZero() {
		status.Time = now
	}
	// 一帧可能同时有通知和状态（被踢下线），都交出去
	if in != nil {
		a.cfg.Selfcheck.Add(selfcheck.FrameToIntake)
		if err := sink.Inbound(ctx, *in); err != nil {
			a.errorf("接收端未收下消息: %v", err)
		}
	}
	if notice != nil {
		if err := sink.Notice(ctx, *notice); err != nil {
			a.errorf("接收端未收下通知: %v", err)
		}
	}
	if status != nil {
		if err := sink.Status(ctx, *status); err != nil {
			a.errorf("接收端未收下状态: %v", err)
		}
	}
}
