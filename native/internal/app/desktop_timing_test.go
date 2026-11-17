package app

import (
	"context"
	"os"
	"testing"
	"time"

	"sumika-native/internal/api"
)

// 只在给了 SUMIKA_DESK_FIXTURE（desk-fixture 造的目录）时跑：拆开量正式模式首屏的三段
func TestDesktopStartupBreakdown(t *testing.T) {
	t.Parallel()
	data := os.Getenv("SUMIKA_DESK_FIXTURE")
	if data == "" {
		t.Skip("没给 SUMIKA_DESK_FIXTURE")
	}
	root, _ := os.Getwd()
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		a, err := Open(context.Background(), Options{Root: root + "/../../..", DataDir: data, Logf: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		open := time.Since(t0)
		d := a.Desktop()
		t1 := time.Now()
		sub, err := d.Reader.WatchOverview(context.Background(), api.OverviewRequest{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		<-sub.Updates()
		overview := time.Since(t1)
		sub.Close()
		t2 := time.Now()
		k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:c0"}
		cs, err := d.Reader.WatchConversation(context.Background(), api.ConversationRequest{Key: k, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		<-cs.Updates()
		page := time.Since(t2)
		cs.Close()
		d.Close()
		a.Close()
		t.Logf("第 %d 遍：Open %v、概览首个快照 %v、会话首页 %v", i+1, open.Round(time.Millisecond), overview.Round(time.Millisecond), page.Round(time.Millisecond))
	}
}
