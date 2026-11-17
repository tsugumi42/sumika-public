package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 长的专栏整条进合并转发（不另发提示）：节点里有头信息和正文（文字、图按原顺序）。
func TestBiliArticleDefaultForward(t *testing.T) {
	r := articleResult()
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "text", Text: strings.Repeat("第三段很长。", 20)})
	got := describe(t, r)
	for _, want := range []string{"合并转发（", "折纸入门笔记", "UP主：纸灯实验室", "阅读 1200 | 点赞 52 | 评论 8 | 收藏 31 | 投币 10 | 转发 4", "第一段：先把纸对折。", "[图片]", "第二段：再折一次。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "未返回") {
		t.Fatalf("不该写未返回：\n%s", got)
	}
}

// 短的专栏（正文不超过 100 字、图不超过 3 张）直接发一条，不进合并转发（用户 2026-11-12，复核 #19 B-22、B-25）；
// 站点 forwardOver 设成 0 是不按字数（和别的站一样，复核问题 2），长专栏也直发；一律转发用 forward: always。
func TestBiliArticleShortDirect(t *testing.T) {
	r := articleResult()
	d := Build(&r, "", Options{}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("短专栏应是一条消息：%v", d.Describe())
	}
	got := strings.Join(d.Describe(), "\n---\n")
	for _, want := range []string{"折纸入门笔记", "第一段：先把纸对折。", "[图片]", "第二段：再折一次。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	zero := 0
	long := articleResult()
	long.Blocks = append(long.Blocks, linkreader.Block{Type: "text", Text: strings.Repeat("第三段很长。", 20)})
	if d = Build(&long, "", Options{}, 0); d.Parts[0].Kind != types.PartForward {
		t.Fatalf("默认 100 字时长专栏进转发：%v", d.Describe())
	}
	d = Build(&long, "", Options{Sites: map[string]SiteSetting{"bilibili-article": {ForwardOver: &zero}}}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("forwardOver 为 0 不按字数，长专栏也直发：%v", d.Describe())
	}
	d = Build(&r, "", Options{Sites: map[string]SiteSetting{"bilibili-article": {Forward: "always"}}}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("forward: always 一律转发：%v", d.Describe())
	}
	// 图超过 3 张：进转发
	many := articleResult()
	for i := 2; i <= 5; i++ {
		many = withMedia(many, goldenImage(i))
	}
	if d = Build(&many, "", Options{}, 0); d.Parts[0].Kind != types.PartForward {
		t.Fatalf("图多的短专栏应进转发：%v", d.Describe())
	}
}

func TestBiliArticleCard(t *testing.T) {
	r := articleResult()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-article": "@卡片"}, Sites: map[string]SiteSetting{"bilibili-article": {Forward: "never"}}}, 0)
	c := d.Parts[0].Body[0].Card
	if c == nil || c.Title != "折纸入门笔记" || c.Author != "UP主：纸灯实验室" || len(c.Stats) != 2 || c.Stats[0] != "阅读 1200｜点赞 52｜评论 8" {
		t.Fatalf("%+v", c)
	}
}

// 只取到卡片信息（正文没取到）：标题、统计照出，不写“未返回”，也不编正文。
func TestBiliArticleCardOnly(t *testing.T) {
	r := siteResult("bilibili", "article", "cv810002")
	r.Title = av("只有卡片信息")
	r.Text = linkreader.Field[string]{State: "not_returned", Reason: "article_body_unavailable"}
	r = withStats(r, map[string]int64{"view": 9})
	got := describe(t, r)
	if !strings.Contains(got, "只有卡片信息") || strings.Contains(got, "B站专栏") || strings.Contains(got, "未返回") {
		t.Fatalf("\n%s", got)
	}
}
