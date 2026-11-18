package chatui

import (
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

type blockMedia struct {
	state     MediaResult
	images    []int
	downloads []int
}

func (m *blockMedia) Image(_ string, pos int, _ bool, _ int) MediaResult {
	m.images = append(m.images, pos)
	return m.state
}
func (m *blockMedia) Face(string) MediaResult { return MediaResult{Description: "[笑脸]"} }
func (m *blockMedia) Download(_ string, pos int) MediaResult {
	m.downloads = append(m.downloads, pos)
	return m.state
}

func TestBlocksRenderOrderAndHostActions(t *testing.T) {
	media := &blockMedia{state: MediaResult{State: MediaFailed, Description: "下载失败", Progress: -1}}
	var openedID string
	var openedPos int
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: media,
		OnDownload: func(id string, pos int) { openedID, openedPos = id, pos },
		OnForward:  func(id string, pos int) { openedID, openedPos = id, pos },
	}
	m := &Msg{ID: "msg", Blocks: []segview.Block{
		{Kind: segview.Inline, Runs: []segview.Run{{Text: "开头"}, {Text: "@大家", Bold: true}}},
		{Kind: segview.Note, Text: "[旧记录提示]"},
		{Kind: segview.StickerNote, Text: "[商城表情]"},
		{Kind: segview.Images, Imgs: []segview.Segment{{Position: 0, HasMedia: true, HasHash: true}, {Position: 3, HasMedia: true, HasHash: true}}},
		{Kind: segview.CardBlock, Card: segview.Segment{Card: &segview.Card{Title: "卡片标题", Description: "卡片描述", Source: "卡片来源", URL: "https://example.test/"}}},
		{Kind: segview.ForwardBlock, Card: segview.Segment{Position: 5, Forward: &segview.Forward{Title: "聊天记录", Lines: [3]string{"第一行"}, Count: 2, Missing: 1}}},
		{Kind: segview.Video, VideoIndex: 6, Video: segview.Segment{CanDownload: true, DurationMS: 61000}},
		{Kind: segview.File, VideoIndex: 7, Video: segview.Segment{Name: "附件.txt", Size: 23, CanDownload: true}},
	}, Notes: []string{"被打断", "已撤回"}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 900, 1600)
	tt.Frame()
	text := strings.Join(tt.Texts(), "|")
	last := -1
	for _, want := range []string{"开头", "@大家", "[旧记录提示]", "[商城表情]", "卡片标题", "卡片描述", "卡片来源", "聊天记录", "第一行", "视频", "附件.txt", "被打断", "已撤回"} {
		at := strings.Index(text, want)
		if at <= last {
			t.Fatalf("内容缺失或顺序错误 %q: %s", want, text)
		}
		last = at
	}
	if len(media.images) < 2 || media.images[0] != 0 || media.images[1] != 3 {
		t.Fatalf("图片原位置 %v", media.images)
	}
	if err := tt.Click("查看转发消息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if openedID != "msg" || openedPos != 5 {
		t.Fatalf("转发动作 = %q/%d", openedID, openedPos)
	}
}

func TestMediaProgressAndCancellation(t *testing.T) {
	media := &blockMedia{state: MediaResult{State: MediaDownloading, Progress: -1}}
	var id string
	pos := -1
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: media, OnCancelDownload: func(got string, n int) { id, pos = got, n }}
	m := &Msg{ID: "m", Blocks: []segview.Block{{Kind: segview.File, VideoIndex: 0, Video: segview.Segment{Name: "附件", CanDownload: true}}}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 800, 500)
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), "|"), "%") {
		t.Fatal("未知进度不应显示百分比")
	}
	if err := tt.Click("取消下载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if id != "m" || pos != 0 {
		t.Fatalf("取消动作 = %q/%d", id, pos)
	}
}

type genericFaceMedia struct{}

func (genericFaceMedia) Image(string, int, bool, int) MediaResult { return MediaResult{} }
func (genericFaceMedia) Download(string, int) MediaResult         { return MediaResult{} }
func (genericFaceMedia) Face(id string) MediaResult {
	return MediaResult{Description: segview.FaceFallback(id, "face")}
}

func TestSuperfaceFallbackAndMarkdownDirection(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: genericFaceMedia{}, MD: NewMD()}
	m := &Msg{ID: "m", Self: true, Markdown: true, Blocks: []segview.Block{
		{Kind: segview.Inline, Runs: []segview.Run{{Text: "**粗体**"}}},
		{Kind: segview.Note, Text: "间隔"},
		{Kind: segview.Inline, Runs: []segview.Run{{Text: "[超级表情123]", Face: &segview.FaceRef{ID: "123", Kind: "superface"}}}},
	}}
	// Second inline uses the plain-text path to exercise face fallback.
	m.Markdown = false
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 800, 600)
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "[超级表情123]") {
		t.Fatalf("丢失超级表情回退: %v", tt.Texts())
	}
	m.Blocks = m.Blocks[:1]
	m.Markdown = true
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "粗体") || strings.Contains(strings.Join(tt.Texts(), "|"), "**粗体**") {
		t.Fatalf("右侧 Markdown 未解析: %v", tt.Texts())
	}
}

type readyFaceMedia struct{ img ui.ImageSource }

func (r readyFaceMedia) Image(string, int, bool, int) MediaResult { return MediaResult{} }
func (r readyFaceMedia) Download(string, int) MediaResult         { return MediaResult{} }
func (r readyFaceMedia) Face(string) MediaResult {
	return MediaResult{State: MediaReady, Image: r.img, Description: "[微笑]"}
}

func TestInlineImageCopiesFaceName(t *testing.T) {
	img := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 8, 8)))
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: readyFaceMedia{img}}
	m := &Msg{ID: "m", Blocks: []segview.Block{{Kind: segview.Inline, Runs: []segview.Run{
		{Text: "前"}, {Text: "[表情1]", Face: &segview.FaceRef{ID: "1", Kind: "face"}}, {Text: "后"},
	}}}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 800, 400)
	tt.Frame()
	n := tree(tt).find("inline")
	if n == nil {
		t.Fatal("没有行内正文")
	}
	tt.ClickAt(n.Box.X+2, n.Box.Y+n.Box.H/2)
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "前[微笑]后" {
		t.Fatalf("复制 = %q", got)
	}
}

func TestQuoteNavigationAndPreview(t *testing.T) {
	var id, target string
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, OnQuote: func(a, b string) { id, target = a, b }}
	m := &Msg{ID: "source", Text: "正文", Quote: &Quote{Name: "某人", Text: "原文", Target: "target"}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 800, 400)
	tt.Frame()
	if err := tt.Click("引用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if id != "source" || target != "target" {
		t.Fatalf("引用跳转 = %q/%q", id, target)
	}
	id, target = "", ""
	m.Quote.Preview = true
	tt.Frame()
	_ = tt.Click("引用")
	tt.Frame()
	if id != "" {
		t.Fatal("预览引用不应跳转")
	}
	m.Quote.Gone = true
	tt.Frame()
	if !tt.HasText("原消息不在本地") {
		t.Fatalf("缺失引用状态: %v", tt.Texts())
	}
}

func TestInlineLinkOpensOriginalAddress(t *testing.T) {
	var got string
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, OnLink: func(url string) { got = url }}
	url := "https://example.test/path?x=1"
	m := &Msg{ID: "m", Blocks: []segview.Block{{Kind: segview.Inline, Runs: []segview.Run{
		{Text: "见 "},
		{Text: url, Link: &segview.LinkRef{Raw: url, Open: url, Clean: "https://example.test/path"}},
	}}}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 800, 300)
	tt.Frame()
	if err := tt.Click(url); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got != url {
		t.Fatalf("打开链接 = %q", got)
	}
}
