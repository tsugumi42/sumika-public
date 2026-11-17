package app

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

// 热生效：设置页（SetLinkSettings）改了模板，下一条链接用的就是新模板，不用重启接收
func TestTemplateChangeAppliesToNextLinkWithoutRestart(t *testing.T) {
	t.Parallel()
	e, a, peer := newRejectHost(t, 0, "", true)
	conn := sendLink(t, e, a, "961", "https://x.com/paper/status/73001")
	waitFor(t, "第一条链接按内置默认发出", func() bool {
		got := strings.Join(peer.snapshot(), "\n")
		// 默认排版改成规格-链接发送改版 B2：链接、“名字[@ID]：”、「正文」、发帖时间（往年带年份），行间不空行（帧是 JSON，换行是字面的 \n）
		return strings.Contains(got, `https://x.com/paper/status/73001\n编的作者[@paper]：\n「编的推文正文」\n1970-01-01 08:00`)
	})
	d := a.Desktop().Reader
	view, err := d.LinkSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SetLinkSettings(context.Background(), api.LinkSettingsChange{Key: config.LinkTemplate, Site: "x", Text: "新版｜$作者｜$链接"}, view.Version); err != nil {
		t.Fatal(err)
	}
	e.peer.send(t, conn, expandFrame(e, "962", testOwner, "https://x.com/paper/status/73002"))
	waitFor(t, "第二条链接用新模板", func() bool {
		return strings.Contains(strings.Join(peer.snapshot(), "\n"), "新版｜编的作者 @paper｜https://x.com/paper/status/73002")
	})
	// 写错的模板在保存时就被拒，运行快照里还是上一份
	view, _ = d.LinkSettings(context.Background())
	if _, err = d.SetLinkSettings(context.Background(), api.LinkSettingsChange{Key: config.LinkTemplate, Site: "x", Text: "$不存在"}, view.Version); err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("非法模板应当被拒：%v", err)
	}
	if got := a.currentLinks().Values.Expand.Templates["x"]; got != "新版｜$作者｜$链接" {
		t.Fatalf("运行快照被改坏：%q", got)
	}
}
