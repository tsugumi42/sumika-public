package linkexpand

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	assetfonts "sumika-native/internal/assets/fonts"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// pngLoader 是假下载器：每个地址回一张编的小图，记下请求了哪些地址；fail 里的地址回错误
type pngLoader struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (s *pngLoader) Image(_ context.Context, _ string, src linkreader.Media) (Loaded, error) {
	s.mu.Lock()
	s.calls = append(s.calls, src.URL.Value)
	s.mu.Unlock()
	if s.fail[src.URL.Value] {
		return Loaded{}, errors.New("编的下载失败")
	}
	img := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia("x", "image/png", 60, 40, b.Bytes())}, nil
}
func (*pngLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, errors.New("不该下载视频")
}
func (*pngLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("不该下载动图")
}

func tweetResult() linkreader.Result {
	r := xPost("今天的纸船下水了 #纸船")
	r.Author.Avatar = av("https://pbs.twimg.com/profile_images/1/a_200x200.jpg")
	r.Author.Verification = av(linkreader.VerifyBlue)
	r.Media = av([]linkreader.Media{
		{Type: "image", URL: av("https://pbs.twimg.com/media/a.jpg?format=jpg&name=orig"), Width: av(1200), Height: av(800)},
		{Type: "video", URL: av("https://video.twimg.com/a.mp4"), Thumbnail: av("https://pbs.twimg.com/ext_tw_video_thumb/1/b.jpg")},
	})
	q := xPost("被引用的那条，悄悄话")
	q.Author.Name, q.Author.Account = av("纸灯"), av("paper_lamp")
	r.Quote = av(&q)
	r.Stats = map[string]linkreader.Field[int64]{"likes": av(int64(12)), "replies": av(int64(3))}
	return r
}

// x 站点模板用 $卡片 时画推特卡片：一张 720 宽的图；画上去的全部文字（含引用正文）过了文字守卫；
// 头像、正文图、视频缩略图都下载了，图换成 medium 档；被拒时整张转 180° 由发送端做（卡片是普通静态图）。
func TestTweetCardPrepare(t *testing.T) {
	r := tweetResult()
	loader := &pngLoader{}
	var fonts []string
	if f := assetfonts.WenYuanPath(); f != "" {
		fonts = []string{f}
	}
	cards := linkcard.NewWith(linkcard.Options{FontPaths: fonts})
	defer cards.Close()
	var guarded []string
	p := Preparer{Media: loader, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil },
		Guard: func(_ context.Context, s string) error { guarded = append(guarded, s); return nil }}
	d := Build(&r, "", Options{Templates: map[string]string{"x": "@卡片"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	var seg *types.BodySegment
	for i := range prepared.Parts[0].Body {
		if prepared.Parts[0].Body[i].Kind == types.BodyImage {
			seg = &prepared.Parts[0].Body[i]
			break
		}
	}
	if seg == nil || seg.Media.Width != 720 || !strings.Contains(seg.Semantic, "被引用的那条") {
		t.Fatalf("没画成推特卡片：%+v", prepared.Parts[0].Body)
	}
	all := strings.Join(guarded, "\n")
	for _, want := range []string{"今天的纸船下水了", "被引用的那条，悄悄话", "paper_lamp"} {
		if !strings.Contains(all, want) {
			t.Fatalf("卡片上的 %q 没过守卫：%q", want, guarded)
		}
	}
	want := []string{"https://pbs.twimg.com/profile_images/1/a_200x200.jpg", "https://pbs.twimg.com/media/a.jpg?format=jpg&name=medium", "https://pbs.twimg.com/ext_tw_video_thumb/1/b.jpg"}
	for _, w := range want {
		found := false
		for _, c := range loader.calls {
			found = found || c == w
		}
		if !found {
			t.Fatalf("没下载 %s：%v", w, loader.calls)
		}
	}
}

// 守卫拦下卡片上的字（比如只在引用正文里）：整条停下，不画、不退回别的卡片
func TestTweetCardGuardBlocks(t *testing.T) {
	r := tweetResult()
	cards := linkcard.New()
	defer cards.Close()
	blocked := errors.New("守卫拦下")
	p := Preparer{Media: &pngLoader{}, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil },
		Guard: func(_ context.Context, s string) error {
			if strings.Contains(s, "悄悄话") {
				return blocked
			}
			return nil
		}}
	d := Build(&r, "", Options{Templates: map[string]string{"x": "@卡片"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, 0)
	if _, err := p.Prepare(context.Background(), &r, &d); !errors.Is(err, blocked) {
		t.Fatalf("守卫拦下的应整条停下：%v", err)
	}
}

// 图下不到：卡片照画（那一格画占位），不整张失败；非 X 站点不走推特卡片
func TestTweetCardMediaFailureStillRenders(t *testing.T) {
	r := tweetResult()
	loader := &pngLoader{fail: map[string]bool{"https://pbs.twimg.com/media/a.jpg?format=jpg&name=medium": true}}
	cards := linkcard.New()
	defer cards.Close()
	p := Preparer{Media: loader, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
	d := Build(&r, "", Options{Templates: map[string]string{"x": "@卡片"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if body := prepared.Parts[0].Body; len(body) == 0 || body[0].Kind != types.BodyImage || body[0].Media.Width != 720 {
		t.Fatalf("图下不到也该画卡片：%+v", body)
	}
	other := r
	other.Platform = "bluesky"
	if _, _, ok, err := p.tweetCard(context.Background(), &other, p.Direct, Item{}); ok || err != nil {
		t.Fatal("别的站不画推特卡片")
	}
}

// 不封顶（maxHeight 0）时，文章、有图夹在段落中间的帖子按顺序把文字和图交给卡片（Flow），图都下载了、文字都过了守卫；
// 封顶时不走这条（图仍是帖子自己的图片网格），也不白下载。
func TestTweetCardFlowWhenUncapped(t *testing.T) {
	mk := func() linkreader.Result {
		r := xPost("https://x.com/i/article/1")
		img := func(n string) *linkreader.Media {
			return &linkreader.Media{Type: "image", URL: av("https://pbs.twimg.com/media/" + n + ".jpg?format=jpg&name=orig"), Width: av(1200), Height: av(800)}
		}
		a, b := img("a"), img("b")
		r.Blocks = []linkreader.Block{
			{Type: "article_title", Text: "文章的标题"},
			{Type: "article_cover", Media: a},
			{Type: "text", Text: "第一段正文。"},
			{Type: "image", Media: b},
			{Type: "divider"},
			{Type: "text", Text: "第二段正文。"},
		}
		r.Media = av([]linkreader.Media{*a, *b})
		r.Article = av("full")
		return r
	}
	run := func(maxHeight int) ([]string, []string, Prepared) {
		r := mk()
		loader := &pngLoader{}
		var fonts []string
		if f := assetfonts.WenYuanPath(); f != "" {
			fonts = []string{f}
		}
		cards := linkcard.NewWith(linkcard.Options{FontPaths: fonts, MaxHeight: func() int { return maxHeight }})
		defer cards.Close()
		var guarded []string
		p := Preparer{Media: loader, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
			Check: func(context.Context) error { return nil },
			Guard: func(_ context.Context, s string) error { guarded = append(guarded, s); return nil }}
		d := Build(&r, "", Options{Templates: map[string]string{"x": "@卡片"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, 0)
		prepared, err := p.Prepare(context.Background(), &r, &d)
		if err != nil {
			t.Fatal(err)
		}
		return guarded, loader.calls, prepared
	}
	guarded, calls, prepared := run(0)
	defer prepared.Release()
	all := strings.Join(guarded, "\n")
	for _, want := range []string{"文章的标题", "第一段正文。", "第二段正文。"} {
		if !strings.Contains(all, want) {
			t.Fatalf("长文里的 %q 没过守卫：%q", want, guarded)
		}
	}
	got := map[string]int{}
	for _, c := range calls {
		got[c]++
	}
	if got["https://pbs.twimg.com/media/a.jpg?format=jpg&name=medium"] != 1 || got["https://pbs.twimg.com/media/b.jpg?format=jpg&name=medium"] != 1 {
		t.Fatalf("每张图该只下载一次：%v", calls)
	}
	// 封顶：不走 Flow，文字守卫里没有标题（卡片画的是 Text 和图片网格）
	guarded, _, prepared2 := run(960)
	defer prepared2.Release()
	if strings.Contains(strings.Join(guarded, "\n"), "文章的标题") {
		t.Fatalf("封顶时不该画长文内容：%q", guarded)
	}
}

func TestCardPoll(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	p := &linkreader.PollInfo{
		Choices: []linkreader.PollChoice{
			{Label: "甲", Percent: av(60.0)},
			{Label: "乙", Count: av(int64(40))},
			{Label: "丙"},
		},
		Total:  av(int64(100)),
		EndsAt: av(now.Add(-time.Hour)),
	}
	got := cardPoll(p, now)
	if got == nil || len(got.Choices) != 3 || !got.Choices[0].OK || got.Choices[0].Percent != 60 || !got.Choices[1].OK || got.Choices[1].Percent != 40 || got.Choices[2].OK {
		t.Fatalf("选项：%+v", got)
	}
	if got.Footer != "共 100 票 · 已结束" {
		t.Fatalf("脚注：%q", got.Footer)
	}
	if cardPoll(&linkreader.PollInfo{}, now) != nil || cardPoll(nil, now) != nil {
		t.Fatalf("没有选项不画")
	}
}
