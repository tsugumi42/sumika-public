package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

func afdianPost(images int) linkreader.Result {
	r := siteResult("afdian", "post", "90120")
	r.Title, r.Author.Name = av("纸船连载"), av("纸云")
	var ms []linkreader.Media
	for i := 1; i <= images; i++ {
		ms = append(ms, goldenImage(i))
	}
	return withMedia(r, ms...)
}

// #75 AF-04：爱发电动态有 14 张图（每张本来就是又高又长的条漫），模板只写 $封面，群里只收到第一张。
// 现在用 $图片：不超过直发上限（3 张）一条消息发完，多了走合并转发，设成从不转发时只发前 3 张。
func TestAfdianPostSendsAllImagesByTheSharedRules(t *testing.T) {
	// 3 张：一条消息，文字在图后面
	d := Build(ptr(afdianPost(3)), "", Options{}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || countImageItems(d.Parts[0].Body) != 3 {
		t.Fatalf("3 张图应该一条消息直发：\n%s", strings.Join(d.Describe(), "\n---\n"))
	}
	// 14 张：合并转发，前 10 张一图一节点，末尾说明另有 4 张
	d = Build(ptr(afdianPost(14)), "", Options{}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("14 张图应进合并转发：\n%s", strings.Join(d.Describe(), "\n---\n"))
	}
	images, text := 0, ""
	for _, n := range d.Parts[0].Nodes {
		for _, it := range n.Body {
			if it.Kind == types.BodyImage {
				images++
			} else if it.Kind == types.BodyText {
				text += it.Text + "\n"
			}
		}
	}
	if first := itemsTextOf(d.Parts[0].Nodes[0]); strings.Index(first, "请看原文") < strings.Index(first, "纸船连载") {
		t.Fatalf("“请看原文”那句该在标题后面：%q", first)
	}
	if images != 10 || !strings.Contains(text, "只发前10张，另有4张，请看原文。") || !strings.Contains(text, "纸船连载") {
		t.Fatalf("转发里图 %d 张，文字：%s", images, text)
	}
	// 设成从不转发：只发前 3 张，总数记下来，Prepare 在文字后标（3/14）
	o := Options{Sites: map[string]SiteSetting{"afdian": {Forward: linktemplate.ForwardNever}}}
	d = Build(ptr(afdianPost(14)), "", o, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || countImageItems(d.Parts[0].Body) != 3 {
		t.Fatalf("从不转发应只发前 3 张：\n%s", strings.Join(d.Describe(), "\n---\n"))
	}
}

// 创作者主页本来就靠这张图（头像）：换成 $图片 以后不能丢。
func TestAfdianCreatorKeepsItsImage(t *testing.T) {
	r := siteResult("afdian", "creator", "paper")
	r.Title, r.Author.Name = av("纸云"), av("纸云")
	r = withMedia(r, goldenImage(1))
	if got := describe(t, r); !strings.Contains(got, "[图片]") {
		t.Fatalf("创作者主页的图丢了：\n%s", got)
	}
}

func ptr[T any](v T) *T { return &v }

func countImageItems(items []Item) (n int) {
	for _, it := range items {
		if it.Kind == types.BodyImage {
			n++
		}
	}
	return n
}

// dateVal 一律按北京时间取日期：小黑盒的 Published 是北京时间零点（UTC 前一天 16:00），以前按 UTC 会写成前一天。
func TestDateValUsesBeijingTime(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2025, 11, 16, 0, 0, 0, 0, cst), "2025-11-16"},         // 北京时间零点
		{time.Date(2025, 11, 15, 16, 0, 0, 0, time.UTC), "2025-11-16"},   // 同一时刻的 UTC 写法
		{time.Date(2025, 11, 16, 0, 0, 0, 0, time.UTC), "2025-11-16"},    // Spotify 这类 UTC 零点，北京时间 08:00，日期不变
		{time.Date(2025, 11, 16, 15, 59, 59, 0, time.UTC), "2025-11-16"}, // 北京时间 23:59:59
		{time.Date(2025, 11, 16, 16, 0, 0, 0, time.UTC), "2025-11-17"},   // 北京时间已经是第二天
	} {
		if got := dateVal(av(c.at)).text; got != c.want {
			t.Fatalf("%v → %q，要 %q", c.at, got, c.want)
		}
	}
	// 发行日期优先用时间戳；没有时间戳（即将推出）才用原文
	r := siteResult("xiaoheihe", "game", "90201")
	r.PublishedText = av("2025-11-16")
	r.Published = av(time.Date(2025, 11, 16, 0, 0, 0, 0, cst))
	if got := releaseVal(&r).text; got != "2025-11-16" {
		t.Fatalf("发行日期 %q", got)
	}
	r.Published = nr[time.Time]()
	r.PublishedText = av("即将推出")
	if got := releaseVal(&r).text; got != "即将推出" {
		t.Fatalf("没有时间戳应写原文：%q", got)
	}
}
