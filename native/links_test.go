package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"testing"
	"time"
)

func TestMessageLinkActions(t *testing.T) {
	a := &app{}
	v := view("link", "human", "编的名字", "看 https://example.com/a?utm_source=x 和 https://example.com/b", t0)
	m := msgFromView(v.Ref.Conversation, v, charOf)
	tt := testerOf(600, 400, func(c *ui.Context) {
		a.messageRows(c, m, m.blocks, true, true, false, false, time.UnixMilli(t0), false, 0)
	})
	click(t, tt, "https://example.com/a?utm_source=x")
	if urls := tt.OpenedURLs(); len(urls) != 1 || urls[0] != "https://example.com/a?utm_source=x" {
		t.Fatalf("打开原链接：%v", urls)
	}
	if err := tt.RightClick("https://example.com/a?utm_source=x"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("复制所有链接") || !tt.HasText("复制所有干净链接") {
		t.Fatalf("菜单：%v", tt.Texts())
	}
	click(t, tt, "复制所有干净链接")
	if got := tt.Clipboard(); got != "https://example.com/a\nhttps://example.com/b" {
		t.Fatalf("干净链接剪贴板 %q", got)
	}
	for _, key := range []api.ConversationKey{demoKey("console:a"), demoKey("home:room"), demoKey("qq:private:200"), demoKey("qq:group:1001")} {
		m := msgFromView(key, v, charOf)
		if len(m.links) != 2 {
			t.Fatalf("场合 %+v 链接 %v", key, m.links)
		}
	}
}
