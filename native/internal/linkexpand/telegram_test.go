package linkexpand

import (
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

func tgResult(text string, images int) linkreader.Result {
	r := siteResult("telegram", "post", "paperchannel/300")
	r.Author = linkreader.Author{Name: av("纸云频道"), Account: av("paperchannel"), ID: nr[string](), Avatar: nr[string]()}
	r.Title = av("纸云频道 #300")
	if text != "" {
		r.Title = av(strings.SplitN(text, "\n", 2)[0])
		r.Text = av(text)
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "text", Text: text})
	}
	r.Stats["views"] = av(int64(12000))
	r.Media = av([]linkreader.Media{})
	for i := 0; i < images; i++ {
		m := linkreader.Media{Type: "image", URL: av(fmt.Sprintf("https://cdn4.cdn-telegram.org/file/p%d.jpg", i))}
		r.Media.Value = append(r.Media.Value, m)
	}
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "image", Media: &r.Media.Value[i]})
	}
	return r
}

// 短帖子直发：链接、频道名[@账号]：、「正文」、时间、统计、图（不写平台名，照推文排，规格-链接发送改版 B10 第 5 条）。
func TestTelegramShortPostDirect(t *testing.T) {
	r := tgResult("这是一条编的短帖子", 1)
	d := Build(&r, "-71", Options{}, 0)
	got := strings.Join(d.Describe(), "\n---\n")
	if strings.Contains(got, "Telegram") {
		t.Fatalf("不该写平台名：\n%s", got)
	}
	for _, want := range []string{"纸云频道[@paperchannel]：", "「这是一条编的短帖子」", "[图片]", "浏览 1.2万", "https://example.invalid/telegram/paperchannel/300"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q:\n%s", want, got)
		}
	}
	if len(d.Parts) != 1 || len(d.Parts[0].Nodes) != 0 {
		t.Fatalf("短帖子不该进合并转发: %s", got)
	}
}

// 长文走合并转发，节点切在空行，链接不被切开。
func TestTelegramLongPostForwardKeepsLinks(t *testing.T) {
	var b strings.Builder
	var links []string
	for i := 1; i <= 200; i++ {
		link := fmt.Sprintf("https://example-lab.github.io/Fable-Files/%03d-jelly-alphabet-%s.html", i, strings.Repeat("k", 20))
		links = append(links, link)
		fmt.Fprintf(&b, "%03d 编造的标题\n%s\n\n", i, link)
	}
	r := tgResult(strings.TrimSpace(b.String()), 0)
	d := Build(&r, "-71", Options{}, 0)
	var nodes []Node
	for _, p := range d.Parts {
		nodes = append(nodes, p.Nodes...)
	}
	if len(nodes) < 2 {
		t.Fatalf("长文应进合并转发并拆成多个节点: parts=%d nodes=%d", len(d.Parts), len(nodes))
	}
	for _, l := range links {
		in := 0
		for _, n := range nodes {
			for _, it := range n.Body {
				if it.Kind == types.BodyText && strings.Contains(it.Text, l) {
					in++
				}
			}
		}
		if in != 1 {
			t.Fatalf("链接要完整地在恰好一个节点里: %s（%d）", l, in)
		}
	}
}

// 超过直发上限（默认 3 张，规格-链接发送改版 B3）的图进合并转发；帖子没有任何内容时标 NoContent（不发空消息）。
func TestTelegramManyImagesForwardAndEmpty(t *testing.T) {
	r := tgResult("四张图", 4)
	d := Build(&r, "-71", Options{}, 0)
	forward := false
	for _, p := range d.Parts {
		if len(p.Nodes) > 0 {
			forward = true
		}
	}
	if !forward {
		t.Fatalf("四张图应进合并转发: %v", d.Describe())
	}
	empty := siteResult("telegram", "post", "paperchannel/301")
	if d := Build(&empty, "-71", Options{}, 0); !d.NoContent || len(d.Parts) != 0 {
		t.Fatalf("没有内容应标 NoContent: %+v", d)
	}
}

// 浏览数按大数字缩写写（一万以上写“1.2万”，不到一万原样），和 B 站、X 一致。
func TestTelegramViewsUseCompactNumbers(t *testing.T) {
	for views, want := range map[int64]string{845: "浏览 845", 12000: "浏览 1.2万", 2820000: "浏览 282万"} {
		r := tgResult("正文", 0)
		r.Stats["views"] = av(views)
		d := Build(&r, "-71", Options{}, 0)
		if got := strings.Join(d.Describe(), "\n"); !strings.Contains(got, want) {
			t.Errorf("%d 应写成 %q：\n%s", views, want, got)
		}
	}
}

// 帖子类站点照推文排（用户 2026-11-13）：转发里链接、频道名[@账号]：、正文、时间、统计和图在同一个节点（用户 2026-11-14），不写「」。
func TestTelegramForwardHeadFollowsTweetLayout(t *testing.T) {
	r := tgResult("四张图", 4)
	d := Build(&r, "-71", Options{}, 0)
	var nodes []Node
	for _, p := range d.Parts {
		nodes = append(nodes, p.Nodes...)
	}
	if len(nodes) != 1 || itemKinds(nodes[0].Body) != "text"+strings.Repeat(",image", 4) {
		t.Fatalf("应有转发，只有一个节点，文字在前、四张图跟在后面：%v", d.Describe())
	}
	head := nodes[0].Body[0].Text
	if head != "https://example.invalid/telegram/paperchannel/300\n纸云频道[@paperchannel]：\n四张图\n浏览 1.2万" {
		t.Fatalf("转发节点的文字：%q", head)
	}
}

// 没有正文时作者行不挂冒号；有正文时冒号后面紧跟正文，不再悬着接时间。
func TestTelegramForwardColonOnlyWithBody(t *testing.T) {
	r := tgResult("", 4)
	d := Build(&r, "-71", Options{}, 0)
	got := strings.Join(d.Describe(), "\n")
	if !strings.Contains(got, "纸云频道[@paperchannel]\n") || strings.Contains(got, "]：") {
		t.Fatalf("没有正文不该有冒号：\n%s", got)
	}
	r = tgResult("四张图", 4)
	d = Build(&r, "-71", Options{}, 0)
	if got := strings.Join(d.Describe(), "\n"); !strings.Contains(got, "]：\n    四张图\n") {
		t.Fatalf("有正文时冒号后面接正文：\n%s", got)
	}
}

// 视频真下载（没标 unsupported）时在转发里单独一个节点，图文留在同一个节点。
func TestTelegramForwardVideoOwnNode(t *testing.T) {
	r := tgResult("正文", 4)
	r.Media.Value = append(r.Media.Value, linkreader.Media{Type: "video", URL: av("https://cdn4.cdn-telegram.org/file/v.mp4")})
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: &r.Media.Value[4]})
	d := Build(&r, "-71", Options{ForwardVideo: true}, 0)
	var nodes []Node
	for _, p := range d.Parts {
		nodes = append(nodes, p.Nodes...)
	}
	if len(nodes) != 2 || itemKinds(nodes[0].Body) != "text,image,image,image,image" || itemKinds(nodes[1].Body) != "video" {
		t.Fatalf("图文一个节点、视频一个节点：%v", d.Describe())
	}
	// 视频不进合并转发（默认）：转发里只有图文一个节点，视频是紧跟在后面的单独一条消息
	d = Build(&r, "-71", Options{}, 0)
	if len(d.Parts) != 2 || len(d.Parts[0].Nodes) != 1 || itemKinds(d.Parts[0].Nodes[0].Body) != "text,image,image,image,image" || d.Parts[1].Kind != types.PartMessage {
		t.Fatalf("视频拆出转发：%v", d.Describe())
	}
}
