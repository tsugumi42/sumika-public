package snowluma

import (
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 真 WebSocket 握手后立即断开；假钟只推动退避，不让核验或稳定门槛误判恢复。
func TestWSFlappingKeepsBackoffAndAlert(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sink, cancel, done := runWS(t, a)
	defer func() { cancel(); waitRun(t, done) }()
	for i, seconds := range []int{1, 2, 4, 8, 16, 32, 60} {
		c := p.accept(t)
		recvStatus(t, sink, types.StatusConnected)
		c.CloseNow()
		recvStatus(t, sink, types.StatusDisconnected)
		d := time.Duration(seconds) * time.Second
		clk.WaitTimer(t, d)
		// 第五次退避跨过掉线满 30 秒的门槛，先单独触发提醒，再走完退避。
		if i == 4 {
			clk.Advance(15 * time.Second)
			alerts.waitRaises(t, 1)
			clk.Advance(time.Second)
		} else {
			clk.Advance(d)
		}
	}
	c := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	if r, c := alerts.counts(); r != 1 || c != 0 {
		t.Fatalf("连续闪断应只提醒一次且不清除：Raise=%d Clear=%d", r, c)
	}
	c.CloseNow()
	recvStatus(t, sink, types.StatusDisconnected)
	if r, c := alerts.counts(); r != 1 || c != 0 {
		t.Fatalf("再次闪断重复了提醒：Raise=%d Clear=%d", r, c)
	}
}

func TestWSRecoveryResetsBackoff(t *testing.T) {
	for _, verified := range []bool{false, true} {
		name := "稳定满三十秒"
		if verified {
			name = "核验通过"
		}
		t.Run(name, func(t *testing.T) {
			clk := fake.NewClock(time.Unix(1000, 0))
			p := newPeer(t, testToken)
			alerts := newTestAlerts()
			a := newWS(t, p, clk, alerts)
			sink, cancel, done := runWS(t, a)
			defer func() { cancel(); waitRun(t, done) }()
			// 两次闪断把下一次退避推到四秒；恢复前已有一条掉线提醒。
			for _, d := range []time.Duration{time.Second, 2 * time.Second} {
				c := p.accept(t)
				recvStatus(t, sink, types.StatusConnected)
				c.CloseNow()
				recvStatus(t, sink, types.StatusDisconnected)
				clk.WaitTimer(t, d)
				clk.Advance(d)
			}
			c := p.accept(t)
			recvStatus(t, sink, types.StatusConnected)
			clk.WaitTimer(t, 27*time.Second)
			clk.Advance(27 * time.Second)
			alerts.waitRaises(t, 1)
			if verified {
				p.send(t, c, privateFrame("303", "200", "核验屏障"))
				recvInbound(t, sink)
			} else {
				// 没有第一帧，尚未开始核验；连接稳定门槛可独立恢复连接提醒。
				clk.Advance(3 * time.Second)
			}
			key, _ := alerts.waitClears(t, 1)
			if key != a.alertKey() {
				t.Fatalf("清掉了别的提醒：%s", key)
			}
			c.CloseNow()
			recvStatus(t, sink, types.StatusDisconnected)
			clk.WaitTimer(t, time.Second)
			clk.Advance(time.Second)
			p.accept(t)
			recvStatus(t, sink, types.StatusConnected)
			if r, c := alerts.counts(); r != 1 || c != 1 {
				t.Fatalf("恢复后提醒不成对：Raise=%d Clear=%d", r, c)
			}
		})
	}
}
