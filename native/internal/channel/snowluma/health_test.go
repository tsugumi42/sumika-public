package snowluma

import (
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 运行中心跳里的 status.good / online（health.go）。号、时间都是编的。

// hb 造一拍心跳；status 为空串就不带 status 字段。time 每拍不同，barrier 靠它认出"这一拍已经处理完"。
func hb(sec int, status string) string {
	s := ""
	if status != "" {
		s = `,"status":` + status
	}
	return fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":%d,"self_id":%s,"interval":30000%s}`, sec, sendAccount, s)
}

// beat 发一拍心跳，再发一拍不带 status 的作栅栏，等栅栏的状态到了接收端：读帧循环是一条线，栅栏处理完，前面那拍一定处理完了。
func (e *sendEnv) beat(sec int, status string) {
	e.t.Helper()
	e.peer.send(e.t, hb(sec, status))
	e.peer.send(e.t, hb(sec+1, ""))
	deadline := time.After(testPatience)
	for {
		select {
		case st := <-e.sink.statuses:
			if st.Kind == types.StatusHeartbeat && st.Time.Unix() == int64(sec+1) {
				return
			}
		case <-deadline:
			fail(e.t, "5 秒内没等到时间 %d 的栅栏心跳", sec+1)
		}
	}
}

func (e *sendEnv) wantCounts(raises, clears int, why string) {
	e.t.Helper()
	if r, c := e.alerts.counts(); r != raises || c != clears {
		e.t.Fatalf("%s：Raise=%d Clear=%d，要 %d/%d", why, r, c, raises, clears)
	}
}

// good 真 → 假 → 真：假的时候 Raise 一次，同一状态再来不重复，缺 status 的心跳既不提醒也不当恢复，真了 Clear 一次
func TestHeartbeatGoodFlipRaisesOnceClearsOnce(t *testing.T) {
	e := newSendEnv(t, confirmReply())
	e.waitSending()
	base := 100000

	e.beat(base, `{"online":true,"good":true}`)
	e.wantCounts(0, 0, "good 一直是真")

	e.beat(base+10, `{"online":true,"good":false}`)
	e.wantCounts(1, 0, "good 变假")
	x := e.alerts.raised[0]
	if x.Key != "qq:"+sendAccount+":recv" || x.Title != "SnowLuma 连着但收不到 QQ 消息" || x.Detail != "" {
		t.Fatalf("提醒：%#v", x)
	}

	e.beat(base+20, `{"online":true,"good":false}`)
	e.beat(base+30, `{"online":false,"good":false}`)
	e.wantCounts(1, 0, "同一状态再来")

	e.beat(base+40, "")
	e.beat(base+50, `{}`)
	e.wantCounts(1, 0, "缺 status 或 status 里没有 good、online")

	e.beat(base+60, `{"online":true,"good":true}`)
	e.wantCounts(1, 1, "good 变回真")
	if key := e.alerts.cleared[0]; key != "qq:"+sendAccount+":recv" {
		t.Fatalf("Clear 的键：%s", key)
	}

	e.beat(base+70, `{"online":true,"good":true}`)
	e.wantCounts(1, 1, "恢复后再来真的")

	// online 变假同样算：再来一轮
	e.beat(base+80, `{"online":false,"good":true}`)
	e.wantCounts(2, 1, "online 变假")
	e.beat(base+90, `{"online":true,"good":true}`)
	e.wantCounts(2, 2, "online 变回真")
}

// 没有 status、status 不是对象、good 和 online 不是字面的布尔：都不知道好坏，不当成坏
func TestHeartbeatWithoutUsableStatusIsNotBad(t *testing.T) {
	e := newSendEnv(t, confirmReply())
	e.waitSending()
	base := 200000
	for i, status := range []string{"", `null`, `{}`, `[]`, `"bad"`, `{"good":null}`, `{"good":"false"}`, `{"good":0,"online":0}`, `{"other":false}`} {
		e.beat(base+i*10, status)
	}
	e.wantCounts(0, 0, "没有可用的 status")
}

// 核验没过的时候由 get_status 那一路提醒（status 键）；这期间心跳报假不再另外提醒
func TestHeartbeatBadBeforeReadyIsLeftToGetStatus(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		p.setStatusQ(`{"online":true,"good":false}`)
	})
	x := e.alerts.waitRaises(t, 1)
	if x.Key != "qq:"+sendAccount+":status" {
		t.Fatalf("提醒：%#v", x)
	}
	e.beat(300000, `{"online":true,"good":false}`)
	e.wantCounts(1, 0, "核验没过时心跳报假")
	if e.ad.sending() {
		t.Fatal("不健康却可以发")
	}
}

func TestHeartbeatHealth(t *testing.T) {
	for _, tc := range []struct {
		name           string
		frame          string
		known, healthy bool
	}{
		{"good 真", hb(1, `{"online":true,"good":true}`), true, true},
		{"只有 good 真", hb(1, `{"good":true}`), true, true},
		{"good 假", hb(1, `{"online":true,"good":false}`), true, false},
		{"只有 online 假", hb(1, `{"online":false}`), true, false},
		{"一真一假算假", hb(1, `{"online":true,"good":false}`), true, false},
		{"没有 status", hb(1, ""), false, false},
		{"不是心跳", `{"post_type":"message","message":"heartbeat","status":{"good":false}}`, false, false},
		{"别的元事件", `{"post_type":"meta_event","meta_event_type":"lifecycle","status":{"good":false}}`, false, false},
		{"不是 JSON", `heartbeat`, false, false},
	} {
		known, healthy := heartbeatHealth([]byte(tc.frame))
		if known != tc.known || healthy != tc.healthy {
			t.Errorf("%s：known=%v healthy=%v，要 %v/%v", tc.name, known, healthy, tc.known, tc.healthy)
		}
	}
}
