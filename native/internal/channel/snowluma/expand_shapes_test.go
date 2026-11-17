package snowluma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/store"
)

type shapeMedia struct {
	png, gif []byte
	images   int
}

func (m *shapeMedia) Image(context.Context, string, linkreader.Media) (linkexpand.Loaded, error) {
	m.images++
	return linkexpand.Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia(fmt.Sprint("shape-", m.images), "image/png", 2, 2, m.png)}, nil
}
func (m *shapeMedia) Video(context.Context, string, *linkreader.Result, linkreader.Media, linkexpand.VideoLimits, bool) (linkexpand.Loaded, error) {
	return linkexpand.Loaded{}, linkexpand.ErrVideoDisabled
}
func (m *shapeMedia) Ugoira(context.Context, *linkreader.UgoiraInfo) (linkexpand.Loaded, error) {
	m.images++
	return linkexpand.Loaded{Kind: types.BodyGIF, Resource: types.MemoryMedia(fmt.Sprint("moving-", m.images), "image/gif", 2, 2, m.gif)}, nil
}
func shapeField[T any](v T) linkreader.Field[T] {
	return linkreader.Field[T]{State: "available", Value: v}
}
func shapeResult(platform, kind string) linkreader.Result {
	r := linkreader.Result{Platform: platform, Kind: kind, ID: "70001", CleanURL: "https://example.invalid/fiction", Source: "fixture", FetchedAt: time.Unix(1, 0), Flags: map[string]linkreader.Field[any]{"possibly_sensitive": shapeField[any](false), "x_restrict": shapeField[any](0)}, Stats: map[string]linkreader.Field[int64]{"likes": shapeField[int64](0)}, Title: shapeField("纸船试航"), Text: shapeField("段首甲"), Media: shapeField([]linkreader.Media{}), Expansion: shapeField("short"), Article: linkreader.Field[string]{State: "not_returned", Reason: "not_applicable"}}
	r.Author.Name = shapeField("纸云")
	r.Author.Account = shapeField("paper_cloud")
	r.Published = shapeField(time.Unix(1, 0))
	return r
}
func shapeImage(n int) linkreader.Media {
	return linkreader.Media{Type: "image", URL: shapeField(fmt.Sprintf("https://pbs.twimg.com/fiction-%d.png", n))}
}
func TestExpandMessageShapes(t *testing.T) {
	var imageBytes, moving bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if e := png.Encode(&imageBytes, img); e != nil {
		t.Fatal(e)
	}
	if e := gif.Encode(&moving, img, nil); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name, platform, kind string
		forward              bool
		alter                func(*linkreader.Result)
	}{
		{"bili-cover", "bilibili", "video", false, nil},
		{"bili-dynamic", "bilibili", "dynamic", false, nil}, // 短动态一条消息：正文不超过 100 字且图不超过直发上限（默认 3 张）
		{"bili-dynamic-long", "bilibili", "dynamic", true, func(r *linkreader.Result) { r.Text = shapeField(strings.Repeat("甲", 101)) }},
		{"x-short", "x", "post", false, nil},
		{"x-long", "x", "post", true, func(r *linkreader.Result) {
			r.Expansion = shapeField("long")
			r.Text = shapeField(strings.Repeat("甲", 2001))
			r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}, {Type: "image", Media: new(shapeImage(1))}, {Type: "text", Text: "段尾丙"}}
		}},
		{"x-article", "x", "post", true, func(r *linkreader.Result) { r.Article = shapeField("长文章"); r.Expansion = shapeField("long") }},
		{"github", "github", "repository", false, nil},
		{"youtube", "youtube", "video", false, nil},
		{"douyin-gallery", "douyin", "note", true, nil},
		{"music", "netease", "song", false, nil},
		{"pixiv-novel", "pixiv", "novel", true, func(r *linkreader.Result) {
			r.Novel = &linkreader.NovelInfo{Description: shapeField("编的简介"), CharacterCount: shapeField[int64](0)}
			r.Text = shapeField(strings.Repeat("甲", 2001))
		}},
		{"pixiv-ugoira", "pixiv", "artwork", false, func(r *linkreader.Result) {
			r.Flags["illust_type"] = shapeField[any](2)
			r.Ugoira = &linkreader.UgoiraInfo{}
		}},
	}
	for _, n := range []int{1, 2, 3, 10, 11} {
		cases = append(cases, struct {
			name, platform, kind string
			forward              bool
			alter                func(*linkreader.Result)
		}{fmt.Sprintf("pixiv-%d", n), "pixiv", "artwork", n > linkexpand.DefaultDirectImageMax, func(r *linkreader.Result) {
			r.Stats["pages"] = shapeField(int64(n))
			for i := range n {
				r.Media.Value = append(r.Media.Value, shapeImage(i))
			}
		}})
	}
	// 形状测试的媒体能力取生产 Caps（ADR-310）：链接展开没有失败记录时拿到的就是这一组
	all := New(nil).Caps().Media(types.ScenePrivate)
	for _, conv := range []types.Conversation{privateConv(), groupConv()} {
		for _, tc := range cases {
			t.Run(conv.Scene+"/"+tc.name, func(t *testing.T) {
				var message atomic.Int64
				e := newSendEnvSetup(t, func(sentAction) (string, bool) {
					return fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"message_id":%d}}`, -90010-message.Add(1)), true
				}, nil)
				r := shapeResult(tc.platform, tc.kind)
				if tc.alter != nil {
					tc.alter(&r)
				}
				renderer := linkcard.New()
				defer renderer.Close()
				media := &shapeMedia{png: imageBytes.Bytes(), gif: moving.Bytes()}
				d := linkexpand.Build(&r, "-71", linkexpand.Options{Templates: map[string]string{"bilibili-video": "@封面文字", "bilibili-mall": "@封面文字"}}, 2)
				prepare := linkexpand.Preparer{Media: media, Cards: renderer, Direct: all, Forward: all, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
				ready, err := prepare.Prepare(context.Background(), &r, &d)
				if err != nil {
					t.Fatal(err)
				}
				// 链接插件发之前去掉非消息段上的引用（linkplugin 的 dropForwardQuotes，规格-链接发送改版 A5：QQ 的合并转发带不了回复段），这里照做
				for i := range ready.Parts {
					if ready.Parts[i].Kind != types.PartMessage {
						ready.Parts[i].Quote = ""
					}
				}
				id, err := e.out.SendOwned(context.Background(), "ca", "automation:link-reader", conv, ready.Parts, ready.Resources)
				if err != nil {
					t.Fatal(err)
				}
				plan := e.waitPlan(id, store.PlanDone)
				// 整条进合并转发时只有转发这一个动作（规格-链接发送改版 B4：转发前不再另发导语）；直发是一条消息
				const count = 1
				if len(plan.Parts) != count {
					t.Fatal("动作数量", len(plan.Parts), count)
				}
				actions := e.peer.waitActions(t, count)
				first := actions[0]
				if !tc.forward {
					var segments []struct {
						Type string
						Data map[string]json.RawMessage
					}
					if err = json.Unmarshal(first.Params["message"], &segments); err != nil {
						t.Fatal(err)
					}
					if len(segments) < 2 || segments[0].Type != "reply" || string(segments[0].Data["id"]) != `"-71"` {
						t.Fatal("引用位置", string(first.Raw))
					}
				} else if strings.Contains(first.Raw, `"type":"reply"`) {
					t.Fatal("合并转发不带回复段", first.Raw)
				}
				var joined strings.Builder
				for _, a := range actions {
					joined.WriteString(a.Raw)
				}
				if !strings.Contains(joined.String(), "发过 2 次") {
					t.Fatal("历史次数丢失", joined.String())
				}
				if tc.forward {
					var nodes []struct {
						Data struct {
							Uin     string
							Content []struct{ Type string }
						}
					}
					if err = json.Unmarshal(actions[0].Params["messages"], &nodes); err != nil {
						t.Fatal(err)
					}
					if len(nodes) == 0 || len(nodes) > 80 {
						t.Fatal("节点数量", len(nodes))
					}
					for _, n := range nodes {
						if n.Data.Uin != sendAccount {
							t.Fatal("作者冒充发送者")
						}
					}
				}
				if tc.name == "pixiv-11" && (media.images != 10 || !strings.Contains(joined.String(), "另有1张")) {
					t.Fatal("十一页未限制前十页", media.images, joined.String())
				}
			})
		}
	}
	for _, platform := range []string{"x", "pixiv"} {
		for _, sensitive := range []any{true, nil, "坏类型"} {
			t.Run(platform+fmt.Sprint(sensitive), func(t *testing.T) {
				e := newSendEnvSetup(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-90111}}`), nil)
				r := shapeResult(platform, "post")
				r.Media.Value = []linkreader.Media{shapeImage(1)}
				r.Flags["possibly_sensitive"] = shapeField(sensitive)
				if platform == "pixiv" {
					r.Kind = "artwork"
					r.Flags["x_restrict"] = shapeField[any](1)
					if sensitive != true {
						r.Flags["x_restrict"] = shapeField(sensitive)
					}
				}
				d := linkexpand.Build(&r, "-71", linkexpand.Options{}, 0)
				media := &shapeMedia{png: imageBytes.Bytes()}
				p := linkexpand.Preparer{Media: media, Direct: all, Forward: all, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
				ready, err := p.Prepare(context.Background(), &r, &d)
				if err != nil {
					t.Fatal(err)
				}
				id, err := e.out.SendOwned(context.Background(), "ca", "automation:link-reader", privateConv(), ready.Parts, ready.Resources)
				if err != nil {
					t.Fatal(err)
				}
				e.waitPlan(id, store.PlanDone)
				wire := string(e.peer.waitActions(t, 1)[0].Raw)
				// 用户 2026-11-08 要求去掉敏感内容限制：分级是什么都照常带图和文字，只由会话名单决定展开与否
				if media.images != 1 || !strings.Contains(wire, `"type":"image"`) {
					t.Fatal("分级不该再拦媒体", media.images, wire)
				}
				if platform == "pixiv" && !strings.Contains(wire, "纸船试航") {
					t.Fatal("pixiv 的主体被替换了", wire)
				}
			})
		}
	}
}
