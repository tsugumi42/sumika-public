package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
	"testing"
	"time"
)

func TestTextOnlyMessageCachesLinks(t *testing.T) {
	for _, key := range []api.ConversationKey{
		{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"},
		{Platform: "local", Channel: "console", Scene: "group", Conversation: "local:group:编的群"},
		{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:10000:10001"},
		{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:90002"},
	} {
		t.Run(key.Platform+"-"+key.Scene, func(t *testing.T) {
			v := api.MessageView{Ref: api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "cached"}}, Text: "编的文字 https://example.com/a?utm_source=test", ActorKind: "human"}
			m := msgFromView(key, v, charOf)
			if len(m.blocks) != 1 || len(m.links) != 1 || m.links[0].Clean != "https://example.com/a" {
				t.Fatalf("无段DTO没有缓存文字和链接：blocks=%v links=%v", m.blocks, m.links)
			}
			a := &app{}
			tt := ui.NewTester(func(c *ui.Context) { a.messageBody(c, m, true, true, time.UnixMilli(t0)) }, 600, 400)
			tt.Frame()
			for range 3 {
				tt.Frame()
			}
			click(t, tt, "https://example.com/a?utm_source=test")
			if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/a?utm_source=test" {
				t.Fatalf("缓存链接点击：%v", got)
			}
			var doc []segview.Block
			allocations := testing.AllocsPerRun(10, func() { doc = docOf(m) })
			if allocations != 0 || len(doc) != 1 {
				t.Fatalf("读取绘制文档重新扫描并分配：allocs=%g doc=%v", allocations, doc)
			}
		})
	}
}
