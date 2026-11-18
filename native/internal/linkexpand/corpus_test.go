package linkexpand

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// 回归基线的样本：各站点、各形态的编造 Result。旧的硬编码 Build 录下的输出存在 testdata/build_golden.json，
// 改成模板以后每个样本的 Draft 必须与它逐字相同（规格-链接解析模板第四节）。样本里的名字、号码、链接都是编的。

type goldenCase struct {
	name     string
	build    func(t *testing.T) linkreader.Result
	format   string // 旧的 bilibiliFormat：card / cover-text（只有 B 站视频和会员购用到）
	previous int
}

func av[T any](v T) linkreader.Field[T] { return linkreader.Field[T]{State: "available", Value: v} }
func nr[T any]() linkreader.Field[T] {
	return linkreader.Field[T]{State: "not_returned", Reason: "interface_omitted"}
}

func goldenBase(platform, kind, id string) linkreader.Result {
	r := linkreader.Result{Platform: platform, Kind: kind, ID: id, CleanURL: "https://example.invalid/" + platform + "/" + id, Source: "fixture",
		FetchedAt: time.Unix(1, 0), Stats: map[string]linkreader.Field[int64]{}, Flags: map[string]linkreader.Field[any]{}}
	r.Author.Name, r.Author.Account = av("纸灯实验室"), av("paper_lab")
	r.Published = av(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))
	r.Article = linkreader.Field[string]{State: "not_returned", Reason: "not_applicable"}
	return r
}
func goldenImage(n int) linkreader.Media {
	return linkreader.Media{Type: "image", URL: av(fmt.Sprintf("https://i0.example.invalid/pic-%d.png", n))}
}
func goldenVideo() linkreader.Media {
	return linkreader.Media{Type: "video", URL: av("https://v.example.invalid/clip.mp4"), Thumbnail: av("https://i0.example.invalid/thumb.png"), Duration: av(135.0)}
}
func withStats(r linkreader.Result, kv map[string]int64) linkreader.Result {
	for k, v := range kv {
		r.Stats[k] = av(v)
	}
	return r
}

func goldenBili(parts int) linkreader.Result {
	r := goldenBase("bilibili", "video", "BV1xx411c7mD")
	r.Title = av("纸船试航")
	r.Media = av([]linkreader.Media{goldenImage(1)})
	r.Media.Value[0].Thumbnail = av("https://i0.example.invalid/cover.png")
	r = withStats(r, map[string]int64{"view": 1200, "favorite": 31, "share": 4, "reply": 8, "danmaku": 6, "like": 52, "coin": 10})
	r.SelectedPart = av(linkreader.VideoPart{Number: 1, Title: av("起航"), Duration: av(135)})
	for i := 1; i <= parts; i++ {
		r.Parts.Value = append(r.Parts.Value, linkreader.VideoPart{Number: i, Title: av(fmt.Sprintf("第%d段", i)), Duration: av(60 * i)})
	}
	r.Parts.State = "available"
	if parts > 1 {
		r.SelectedPart = av(linkreader.VideoPart{Number: 2, Title: av("折纸步骤"), Duration: av(95)})
	}
	return r
}

func goldenCases() []goldenCase {
	var out []goldenCase
	add := func(name string, f func(t *testing.T) linkreader.Result) {
		out = append(out, goldenCase{name: name, build: f})
	}
	// B 站视频：两种旧格式 × 单 P / 多 P / 缺统计 / 带视频 / 无封面 / 发过两次
	for _, format := range []string{"card", "cover-text"} {
		for _, v := range []struct {
			name string
			f    func(t *testing.T) linkreader.Result
		}{
			{"bili-video", func(*testing.T) linkreader.Result { return goldenBili(1) }},
			{"bili-video-multipart", func(*testing.T) linkreader.Result { return goldenBili(3) }},
			{"bili-video-nostats", func(*testing.T) linkreader.Result {
				r := goldenBili(1)
				r.Stats = map[string]linkreader.Field[int64]{"view": av(int64(0))}
				return r
			}},
			{"bili-video-with-video", func(*testing.T) linkreader.Result {
				r := goldenBili(1)
				r.Media.Value = append(r.Media.Value, goldenVideo())
				return r
			}},
			{"bili-video-nocover", func(*testing.T) linkreader.Result {
				r := goldenBili(1)
				r.Media = av([]linkreader.Media{})
				return r
			}},
		} {
			c := goldenCase{name: format + "/" + v.name, build: v.f, format: format}
			out = append(out, c)
		}
		out = append(out, goldenCase{name: format + "/bili-video-previous", build: func(*testing.T) linkreader.Result { return goldenBili(1) }, format: format, previous: 2})
		out = append(out, goldenCase{name: format + "/bili-mall", build: func(t *testing.T) linkreader.Result { return mallResult(t) }, format: format, previous: 2})
	}
	// B 站动态
	add("bili-dynamic", func(*testing.T) linkreader.Result {
		r := goldenBase("bilibili", "dynamic", "70001")
		r.Title = av("动态标题")
		r.Text = av("动态正文")
		r = withStats(r, map[string]int64{"like": 3, "comment": 2, "forward": 1})
		r.Blocks = []linkreader.Block{{Type: "text", Text: "第一段"}, {Type: "image", Media: new(goldenImage(1))}, {Type: "text", Text: "最后一段"}}
		return r
	})
	add("bili-opus-notitle", func(*testing.T) linkreader.Result {
		r := goldenBase("bilibili", "opus", "70002")
		r.Text = av("只有正文")
		return r
	})
	// X
	xShort := func() linkreader.Result {
		r := goldenBase("x", "post", "80001")
		r.Text = av("短帖正文")
		r.Media = av([]linkreader.Media{goldenImage(1)})
		r = withStats(r, map[string]int64{"likes": 5, "reposts": 1, "replies": 2, "quotes": 0, "bookmarks": 3, "views": 900})
		r.Blocks = []linkreader.Block{{Type: "text", Text: "短帖正文"}, {Type: "image", Media: new(goldenImage(1))}}
		return r
	}
	add("x-short-image", func(*testing.T) linkreader.Result { return xShort() })
	add("x-short-video", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Media = av([]linkreader.Media{goldenVideo()})
		r.Blocks = []linkreader.Block{{Type: "text", Text: "短帖正文"}, {Type: "video", Media: new(goldenVideo())}}
		return r
	})
	add("x-short-nostats", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Stats = map[string]linkreader.Field[int64]{}
		return r
	})
	add("x-short-sensitive", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Flags["possibly_sensitive"] = av[any](true)
		return r
	})
	add("x-long", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Text = av(strings.Repeat("甲", 5600)) // 16800 字节，超过默认 16500
		r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}, {Type: "image", Media: new(goldenImage(1))}, {Type: "text", Text: "段尾丙"}}
		return r
	})
	// 301 个字按字数的老规则会进合并转发；现在按实发字节，远低于 16500，直发（ADR-391）
	add("x-over300", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Text = av(strings.Repeat("乙", 301))
		r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}, {Type: "image", Media: new(goldenImage(1))}}
		return r
	})
	add("x-article", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Article = av("长文章")
		return r
	})
	add("x-quote", func(*testing.T) linkreader.Result {
		r := xShort()
		q := goldenBase("x", "post", "80002")
		q.Text = av("被引用的话")
		r.Quote = av(&q)
		r.Blocks = []linkreader.Block{{Type: "text", Text: "短帖正文"}, {Type: "quote", Quote: &q}}
		return r
	})
	add("x-truncated", func(*testing.T) linkreader.Result {
		r := xShort()
		r.Text.Truncated = true
		return r
	})
	// pixiv
	pixiv := func(kind string, pages int) linkreader.Result {
		r := goldenBase("pixiv", kind, "90001")
		r.Title = av("纸船试航")
		r.Text = av("作品文字")
		r.Flags["x_restrict"] = av[any](0)
		r.Flags["ai_generated"] = av[any](false)
		r.Stats["pages"] = av(int64(pages))
		for i := 0; i < pages; i++ {
			r.Media.Value = append(r.Media.Value, goldenImage(i))
		}
		r.Media.State = "available"
		return r
	}
	for _, n := range []int{1, 2, 3, 11} {
		add(fmt.Sprintf("pixiv-%d", n), func(*testing.T) linkreader.Result { return pixiv("artwork", n) })
	}
	add("pixiv-r18", func(*testing.T) linkreader.Result {
		r := pixiv("artwork", 1)
		r.Flags["x_restrict"] = av[any](1)
		return r
	})
	add("pixiv-norating", func(*testing.T) linkreader.Result {
		r := pixiv("artwork", 1)
		delete(r.Flags, "x_restrict")
		return r
	})
	add("pixiv-ugoira", func(*testing.T) linkreader.Result {
		r := pixiv("artwork", 1)
		r.Flags["illust_type"] = av[any](2)
		r.Ugoira = &linkreader.UgoiraInfo{}
		return r
	})
	add("pixiv-novel", func(*testing.T) linkreader.Result {
		r := pixiv("novel", 0)
		r.Tags = av([]string{"原创", "纸船", strings.Repeat("长", 45)})
		r.Novel = &linkreader.NovelInfo{Description: av(strings.Repeat("简", 320)), CharacterCount: av(int64(12345))}
		r.Text = av(strings.Repeat("甲", 1600))
		return r
	})
	add("pixiv-novel-bare", func(*testing.T) linkreader.Result {
		r := pixiv("novel", 0)
		r.Text = av("短小说")
		return r
	})
	// GitHub / YouTube / 抖音 / 网易云
	add("github", func(*testing.T) linkreader.Result {
		r := goldenBase("github", "repository", "paper/boat")
		r.Title = av("paper/boat")
		r.Text = av("纸船的说明")
		r.GitHub = &linkreader.GitHubInfo{Language: av("Go"), License: av("MIT"), Updated: av(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))}
		r = withStats(r, map[string]int64{"stars": 12, "forks": 3, "open_issues": 1})
		return r
	})
	add("github-bare", func(*testing.T) linkreader.Result {
		r := goldenBase("github", "repository", "paper/boat")
		r.Title = av("paper/boat")
		return r
	})
	add("youtube", func(*testing.T) linkreader.Result {
		r := goldenBase("youtube", "video", "abc123")
		r.Title = av("Paper Boat Test")
		r.Media = av([]linkreader.Media{goldenImage(1), goldenVideo()})
		r = withStats(r, map[string]int64{"views": 1000, "likes": 50, "comments": 7})
		return r
	})
	add("douyin-video", func(*testing.T) linkreader.Result {
		r := goldenBase("douyin", "video", "d1")
		r.Text = av("抖音文案")
		r.Media = av([]linkreader.Media{goldenImage(1), goldenVideo()})
		r = withStats(r, map[string]int64{"likes": 32, "comments": 4, "favorites": 5, "shares": 6})
		return r
	})
	add("douyin-note", func(*testing.T) linkreader.Result {
		r := goldenBase("douyin", "note", "d2")
		r.Text = av("图集文案")
		r.Media = av([]linkreader.Media{goldenImage(1), goldenImage(2)})
		r = withStats(r, map[string]int64{"likes": 32, "comments": 4, "favorites": 5, "shares": 6})
		return r
	})
	add("netease", func(*testing.T) linkreader.Result {
		r := goldenBase("netease", "song", "n1")
		r.Title = av("纸船")
		r.Author.Name = av("河岸合奏团")
		r.Music = &linkreader.MusicInfo{Album: av("折纸集")}
		r.Media = av([]linkreader.Media{goldenImage(1), {Type: "audio", URL: av("https://a.example.invalid/a.mp3"), Duration: av(200.0)}})
		return r
	})
	// 失败与不支持
	add("no-title-no-text", func(*testing.T) linkreader.Result {
		r := goldenBase("x", "post", "80009")
		return r
	})
	add("unsupported-kind", func(*testing.T) linkreader.Result { return goldenBase("github", "issue", "1") })
	add("unknown-platform", func(*testing.T) linkreader.Result { return goldenBase("nowhere", "page", "1") })
	return out
}
