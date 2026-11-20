package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"sumika-native/internal/alert"
	"time"
)

// 登录号核验（规格-QQ发送上线第四节）。phase 只对当前这条连接有效。
const (
	phaseDown = iota
	phaseChecking
	phaseReady
	phaseMismatch
	phaseUnhealthy
	phaseSelf
)

// mismatchRetry 是号对不上以后再连的间隔（Bun 的 mismatchRetry）。不走普通退避。
const mismatchRetry = 60 * time.Second

var (
	errLoginMismatch = errors.New("登录号和配置不一致")
	errUnhealthy     = errors.New("协议端报收发不健康")
	errSelfMismatch  = errors.New("帧里的号和登录号不一致")
)

// openSession 开始一次连接上的核验：先标成还不能发，等第一帧。
func (a *Adapter) openSession() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session++
	a.phase = phaseChecking
	a.loginID = ""
	a.lastSelf = ""
	a.selfLogged = false
	a.saw = make(chan struct{})
	a.rotateKickLocked()
	return a.session
}

// closeSession 让还在跑的核验写不回来。号对不上时，等 60 秒的这段里发送仍是这个原因。
func (a *Adapter) closeSession() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session++
	mismatch := a.mismatchWait
	a.mismatchWait = false
	if mismatch {
		a.phase = phaseMismatch
	} else {
		a.phase = phaseDown
	}
	a.saw = nil
	a.rotateKickLocked()
	return mismatch
}

func (a *Adapter) rotateKickLocked() {
	if a.kick != nil {
		select {
		case <-a.kick:
		default:
			close(a.kick)
		}
	}
	a.kick = make(chan struct{})
}

func (a *Adapter) sessionAlive(sid uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session == sid
}

// verify 在第一帧之后、任何发送之前跑。查询失败关掉连接，交给原来的掉线重连；
// 号不对走 60 秒；不健康留着连接，每 30 秒再查。健康以后不再轮询。
func (a *Adapter) verify(ctx context.Context, l *live, sid uint64) {
	if !a.waitFirst(ctx, l) || ctx.Err() != nil || !a.sessionAlive(sid) {
		return
	}
	frame, wrote, err := a.call(ctx, l, "get_login_info", map[string]any{})
	if ctx.Err() != nil || !a.sessionAlive(sid) {
		return
	}
	uid, ok := loginUserID(frame)
	if err != nil || !wrote || !ok || !actionOK(frame) {
		// 进入 Write 后即使报错也可能已发；没有能用的回包就关掉，走掉线重连。
		if wrote && ctx.Err() == nil && a.sessionAlive(sid) {
			a.warnf("核验查登录号没拿到能用的回包，关掉连接重连: %s", a.closeWhy(err, frame, ok))
			l.c.Close()
		}
		return
	}
	if uid != a.cfg.Account {
		a.armMismatch(sid)
		l.c.Close()
		return
	}
	a.mu.Lock()
	if a.session != sid {
		a.mu.Unlock()
		return
	}
	a.loginID = uid
	a.loginName = loginNickname(frame)
	selfBad := a.lastSelf != "" && a.lastSelf != uid
	a.mu.Unlock()
	if selfBad {
		a.noteSelfMismatch(sid)
		return
	}
	a.watchStatus(ctx, l, sid)
}

func (a *Adapter) watchStatus(ctx context.Context, l *live, sid uint64) {
	for {
		if ctx.Err() != nil || !a.sessionAlive(sid) {
			return
		}
		frame, wrote, err := a.call(ctx, l, "get_status", map[string]any{})
		if ctx.Err() != nil || !a.sessionAlive(sid) {
			return
		}
		if err != nil || !wrote || !actionOK(frame) {
			if wrote && ctx.Err() == nil && a.sessionAlive(sid) {
				a.warnf("核验查状态没拿到能用的回包，关掉连接重连: %s", a.closeWhy(err, frame, true))
				l.c.Close()
			}
			return
		}
		if statusHealthy(frame) {
			a.markReady(sid)
			return
		}
		if !a.markUnhealthy(sid) {
			return
		}
		if !a.wait(ctx, l, a.statusEvery()) {
			return
		}
	}
}

func (a *Adapter) statusEvery() time.Duration {
	if a.alertAfter > 0 {
		return a.alertAfter
	}
	return defaultAlert
}

func (a *Adapter) waitFirst(ctx context.Context, l *live) bool {
	a.mu.Lock()
	saw := a.saw
	a.mu.Unlock()
	if saw == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-l.dead:
		return false
	case <-saw:
		return true
	}
}

func (a *Adapter) wait(ctx context.Context, l *live, d time.Duration) bool {
	t := a.timer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-l.dead:
		return false
	case <-t.C():
		return true
	}
}

func (a *Adapter) waitOnly(ctx context.Context, d time.Duration) bool {
	t := a.timer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}

// noteFrame 记下这一帧，并在 self_id 和已经核过的登录号不一致时停发。回复帧没有 post_type，不拿来比。
func (a *Adapter) noteFrame(frame []byte) {
	var saw chan struct{}
	logSelf := false
	a.mu.Lock()
	if a.saw != nil {
		saw = a.saw
		a.saw = nil
	}
	if sid := frameSelfID(frame); sid != "" && a.phase != phaseDown {
		a.lastSelf = sid
		if a.loginID != "" && sid != a.loginID {
			logSelf = !a.selfLogged
			a.selfLogged = true
			a.phase = phaseSelf
			a.rotateKickLocked()
		}
	}
	a.mu.Unlock()
	if saw != nil {
		close(saw)
	}
	if logSelf {
		a.errorf("帧里的 self_id 和登录号不一致，停发")
	}
}

// noteSelfMismatch 只停 sid 这一次连接：放开锁到这里之间可能已经换了新连接，旧连接的冲突不能写到新连接上。
func (a *Adapter) noteSelfMismatch(sid uint64) {
	a.mu.Lock()
	if a.session != sid || (a.selfLogged && a.phase == phaseSelf) {
		a.mu.Unlock()
		return
	}
	a.selfLogged = true
	a.phase = phaseSelf
	a.rotateKickLocked()
	a.mu.Unlock()
	a.errorf("帧里的 self_id 和登录号不一致，停发")
}

func (a *Adapter) armMismatch(sid uint64) {
	a.mu.Lock()
	if a.session != sid {
		a.mu.Unlock()
		return
	}
	a.phase = phaseMismatch
	a.mismatchWait = true
	raise := !a.loginRaised
	a.loginRaised = true
	a.rotateKickLocked()
	a.mu.Unlock()
	if raise && a.alerts != nil {
		a.alerts.Raise(alert.Alert{
			Key:    a.loginKey(),
			Class:  "QQ 发送",
			Title:  "协议端登录的号和配置不一致",
			Body:   "对不上",
			Detail: "",
		})
	}
	a.errorf("登录号和配置不一致，停发")
}

func (a *Adapter) markUnhealthy(sid uint64) bool {
	a.mu.Lock()
	if a.session != sid || a.phase == phaseSelf {
		a.mu.Unlock()
		return false
	}
	a.phase = phaseUnhealthy
	raise := !a.statusRaised
	a.statusRaised = true
	a.rotateKickLocked()
	a.mu.Unlock()
	if raise && a.alerts != nil {
		a.alerts.Raise(alert.Alert{
			Key:    a.statusKey(),
			Class:  "QQ 发送",
			Title:  "协议端报收发不健康",
			Body:   "收发不健康",
			Detail: "",
		})
	}
	return true
}

func (a *Adapter) markReady(sid uint64) {
	a.mu.Lock()
	if a.session != sid || a.phase == phaseSelf {
		a.mu.Unlock()
		return
	}
	if a.loginID != "" && a.lastSelf != "" && a.lastSelf != a.loginID {
		a.mu.Unlock()
		a.noteSelfMismatch(sid)
		return
	}
	a.phase = phaseReady
	if a.live != nil && a.live.ready != nil {
		select {
		case <-a.live.ready:
		default:
			close(a.live.ready)
		}
	}
	clearLogin := a.loginRaised
	clearStatus := a.statusRaised
	a.loginRaised = false
	a.statusRaised = false
	a.rotateKickLocked()
	a.mu.Unlock()
	if a.alerts == nil {
		return
	}
	if clearLogin {
		a.alerts.Clear(a.loginKey(), alert.Note{Title: "登录号对上了", Body: "对上了"})
	}
	if clearStatus {
		a.alerts.Clear(a.statusKey(), alert.Note{Title: "协议端收发恢复了", Body: "可以发了"})
	}
}

func (a *Adapter) loginKey() string  { return "qq:" + a.cfg.Account + ":login" }
func (a *Adapter) statusKey() string { return "qq:" + a.cfg.Account + ":status" }

// allowSend 在同一把锁里核验许可并拿连接；这次调用不得转投后来的连接。
func (a *Adapter) allowSend(ctx context.Context) (*live, error) {
	for {
		a.mu.Lock()
		phase := a.phase
		kick := a.kick
		l := a.live
		a.mu.Unlock()
		switch phase {
		case phaseReady:
			if l == nil {
				return nil, errNotConnected
			}
			return l, nil
		case phaseChecking:
			if kick == nil {
				return nil, errNotConnected
			}
			select {
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-kick:
			}
		case phaseMismatch:
			return nil, errLoginMismatch
		case phaseUnhealthy:
			return nil, errUnhealthy
		case phaseSelf:
			return nil, errSelfMismatch
		default:
			return nil, errNotConnected
		}
	}
}

// sending 是测试看「现在能不能发」。
func (a *Adapter) sending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase == phaseReady
}

// Ready 供宿主第一次摘发送钳制时核对；后续健康变化仍由 allowSend 在同一连接上拦住。
func (a *Adapter) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase == phaseReady && a.live != nil
}

func actionOK(frame []byte) bool {
	var o object
	if len(frame) == 0 || json.Unmarshal(frame, &o) != nil || o == nil {
		return false
	}
	if str(o, "status") == "failed" {
		return false
	}
	return string(o["retcode"]) == "0"
}

func loginUserID(frame []byte) (string, bool) {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return "", false
	}
	data := o["data"]
	if len(data) == 0 || string(data) == "null" {
		return "", false
	}
	var d object
	if json.Unmarshal(data, &d) != nil || d == nil {
		return "", false
	}
	return decimalField(d["user_id"])
}

// loginNickname 取 get_login_info 回包里的昵称；没有或不是字符串返回空。
func loginNickname(frame []byte) string {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return ""
	}
	var d object
	if json.Unmarshal(o["data"], &d) != nil || d == nil {
		return ""
	}
	var nick string
	if json.Unmarshal(d["nickname"], &nick) != nil {
		return "" // 不是字符串（数字等）不当昵称
	}
	return cardNodeName(nick)
}

func statusHealthy(frame []byte) bool {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return false
	}
	data := o["data"]
	var d object
	if json.Unmarshal(data, &d) != nil || d == nil {
		return false
	}
	return string(d["online"]) == "true" && string(d["good"]) == "true"
}

func frameSelfID(frame []byte) string {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return ""
	}
	if _, ok := o["post_type"]; !ok {
		return ""
	}
	id, _ := decimalField(o["self_id"])
	return id
}

func decimalField(raw json.RawMessage) (string, bool) {
	if id, ok := decimalID(string(raw)); ok {
		return id, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return decimalID(s)
}

// closeWhy 是核验关连接前记进日志的原因（诊断用，2026-11-20）：出错、没回包、回包里没有要的字段、回包报失败
func (a *Adapter) closeWhy(err error, frame []byte, ok bool) string {
	switch {
	case err != nil:
		return a.safeError(err)
	case frame == nil:
		return "没有回包"
	case !ok:
		return "回包里没有登录号"
	}
	return "回包报失败"
}
