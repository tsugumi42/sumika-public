package app

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/api"
)

// 接收中新来的群消息要经概览订阅送到界面：会话列表里该会话的最后一条随之换成新消息。
// 订阅在第一条消息之前就建好（界面切到接收后马上订阅概览的样子），全程不轮询、不重建订阅。
func TestDesktopOverviewFollowsNewInboundWhileReceiving(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeConfigFreq(testToken, "0")
	e.expectNoActions(t)
	a := e.start()
	d := a.Desktop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := d.Reader.WatchOverview(ctx, api.OverviewRequest{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	defer sub.Close()
	conn := e.peer.accept(t)
	conv := "qq:group:" + testGroup

	lastOf := func(s api.Snapshot[api.Overview]) string {
		for _, c := range s.Value.Conversations.Items {
			if c.Key.Conversation == conv && c.Last != nil {
				return c.Last.Text
			}
		}
		return ""
	}
	for i, text := range []string{"编的第一条", "编的第二条"} {
		e.peer.send(t, conn, groupFrame(e.now.Add(time.Duration(i)*time.Second), "9"+string(rune('0'+i)), "20001", text))
		deadline := time.After(10 * time.Second)
		for done := false; !done; {
			select {
			case snap, ok := <-sub.Updates():
				if !ok {
					t.Fatal("概览订阅提前关了")
				}
				if snap.Err != nil {
					t.Fatal(snap.Err)
				}
				done = lastOf(snap) == text
			case <-deadline:
				t.Fatalf("10 秒内概览没送来新消息 %q", text)
			}
		}
	}
}
