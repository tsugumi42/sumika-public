package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 链接设置页的推特卡片配色、高度封顶：选“深色”存上；高度能填 1200、也能填 0（不封顶，行上写“不封顶”）；
// 填 100 过不了配置检查，值不变、提示写在页面上。
func TestLinkPageTweetCardSettings(t *testing.T) {
	h := newLinkHarness(t, 2048, 1500, 20)
	h.click(t, "链接解析（link-reader）")
	h.click(t, "推特卡片：深色") // 旧外壳没有行标题，选项文字自己带上是哪一项
	h.wait(t)
	saveLinkShot(t, h, "link-tweet-card.png") // 只在设了 LINK_SHOT_DIR 时存图
	if got := h.a.linkSettings.view.Configured.Expand.TweetCard.Theme; got != "dark" {
		t.Fatalf("配色没存上：%q", got)
	}
	const row = "推特卡片高度封顶（像素，0 不封顶，480～8000）"
	setHeight := func(v string) {
		t.Helper()
		h.click(t, row)
		h.click(t, "编辑插件数值")
		h.tt.Key(ui.Cmd, ui.KeyA)
		h.tt.Type(v)
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Frame()
	}
	setHeight("1200")
	h.wait(t)
	if got := h.a.linkSettings.view.Configured.Expand.TweetCard.MaxHeight; got != 1200 {
		t.Fatalf("高度没存上：%d", got)
	}
	setHeight("0")
	h.wait(t)
	if got := h.a.linkSettings.view.Configured.Expand.TweetCard.MaxHeight; got != 0 || !h.tt.HasText("不封顶") {
		t.Fatalf("0 要能存、行上写不封顶：%d", got)
	}
	setHeight("100")
	h.wait(t)
	if got := h.a.linkSettings.view.Configured.Expand.TweetCard.MaxHeight; got != 0 || !strings.Contains(h.a.linkSettings.note, "推特卡片高度封顶") {
		t.Fatalf("100 该拒收并用设置页的叫法提示：%d %q", got, h.a.linkSettings.note)
	}
}
