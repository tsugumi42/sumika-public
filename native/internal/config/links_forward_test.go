package config

import (
	"slices"
	"strings"
	"testing"
)

// 合并转发的节点名和卡片文字：五项一次写，读得回来；写错整次拒绝，不留半截；手写错的整份配置不通过并点名键
func TestForwardCardSavedAndValidated(t *testing.T) {
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	card := LinkForward{NodeName: "纸鸢", Source: "$标题", News: []string{"$字数", "$图片数"}, Summary: "$UP主", Prompt: "$站点｜$标题"}
	out, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkForwardCard, Card: card})
	if err != nil {
		t.Fatal(err)
	}
	got := c.LinkReader.Expand.Forward
	if got.NodeName != "纸鸢" || got.Source != "$标题" || !slices.Equal(got.News, card.News) || got.Summary != "$UP主" || got.Prompt != "$站点｜$标题" {
		t.Fatalf("没读回来：%+v\n%s", got, out)
	}
	// 再写一次全空：清掉，回到 QQ 默认
	_, c, err = PatchLink(out, "sumika.yml", LinkChange{Key: LinkForwardCard, Card: LinkForward{}})
	if err != nil {
		t.Fatal(err)
	}
	if g := c.LinkReader.Expand.Forward; g.NodeName != "" || g.Source != "" || len(g.News) != 0 || g.Summary != "" || g.Prompt != "" {
		t.Fatalf("全空应该清掉：%+v", g)
	}
	for name, bad := range map[string]LinkForward{
		"未知字段":    {Source: "$不存在"},
		"分段不能用":   {Summary: "$分段"},
		"合并转发不能用": {Prompt: "$合并转发"},
		"预览三行":    {News: []string{"a", "b", "c"}},
		"预览写错":    {News: []string{"$标题", "$"}},
		"节点名换行":   {NodeName: "甲\n乙"},
		"节点名太长":   {NodeName: strings.Repeat("长", 41)},
		"节点名零宽字符": {NodeName: "甲\u200b乙"},
	} {
		if _, _, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkForwardCard, Card: bad}); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
	for name, hand := range map[string]string{
		"source":  "linkReader:\n  expand:\n    forward:\n      source: \"$没有这个\"\n",
		"news":    "linkReader:\n  expand:\n    forward:\n      news: [\"a\", \"b\", \"c\"]\n",
		"summary": "linkReader:\n  expand:\n    forward:\n      summary: \"$合并转发\"\n",
	} {
		if _, err := Parse([]byte(hand), "sumika.yml"); err == nil || !strings.Contains(err.Error(), "forward."+name) {
			t.Errorf("手写错的 %s 应报错并点名键：%v", name, err)
		}
	}
}

func TestForwardCardCloneDoesNotShareNews(t *testing.T) {
	a := LinkReaderConfig{Expand: LinkExpand{Forward: LinkForward{News: []string{"甲", "乙"}}}}
	b := a.Clone()
	b.Expand.Forward.News[0] = "改了"
	if a.Expand.Forward.News[0] != "甲" {
		t.Fatal("Clone 没有复制预览行")
	}
}
