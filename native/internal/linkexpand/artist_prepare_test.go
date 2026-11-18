package linkexpand

import (
	"context"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// 画师的推文走完准备（规格-画师原图 验收 6）：卡片画成推特卡片，后面每张原图都在、下载地址仍是 name=orig（卡片里的图才换 medium）；
// 只发原图时不画卡片。卡片画不出来退通用卡片的路径由 TestTweetCardMediaFailureStillRenders 等现有测试管，原图项和卡片项互不影响。图、作者都是编的。

func artistTweet() linkreader.Result {
	r := xImages(2)
	r.Author.Avatar = av("https://pbs.twimg.com/profile_images/1/a_200x200.jpg")
	for i := range r.Media.Value {
		r.Media.Value[i].URL = av("https://pbs.twimg.com/media/art" + string(rune('a'+i)) + ".jpg?format=jpg&name=orig")
	}
	return r
}

func imageSegs(parts []types.Part) (n int) {
	for _, p := range parts {
		for _, s := range p.Body {
			if s.Kind == types.BodyImage {
				n++
			}
		}
	}
	return n
}

func TestArtistCardAndOriginalsPrepared(t *testing.T) {
	r := artistTweet()
	loader := &pngLoader{}
	cards := linkcard.New()
	defer cards.Close()
	p := Preparer{Media: loader, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil },
		Guard: func(context.Context, string) error { return nil }}
	d := Build(&r, "", Options{Artist: ArtistCardAndOriginals}, 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 1 || imageSegs(prepared.Parts) != 3 {
		t.Fatalf("应该是一条消息：卡片加两张原图：%d 段 %d 张图", len(prepared.Parts), imageSegs(prepared.Parts))
	}
	var first *types.BodySegment
	for i := range prepared.Parts[0].Body {
		if prepared.Parts[0].Body[i].Kind == types.BodyImage {
			first = &prepared.Parts[0].Body[i]
			break
		}
	}
	if first == nil || first.Media.Width != 720 {
		t.Fatalf("第一张应该是画成的推特卡片：%+v", first)
	}
	for i := range r.Media.Value {
		orig := r.Media.Value[i].URL.Value
		found := false
		for _, c := range loader.calls {
			found = found || c == orig
		}
		if !found {
			t.Fatalf("原图没按 name=orig 下载 %s：%v", orig, loader.calls)
		}
	}
}

// 只发原图：不画卡片（Cards 为 nil 也不会用到），两张原图都在。
func TestArtistOriginalsOnlyPrepared(t *testing.T) {
	r := artistTweet()
	loader := &pngLoader{}
	p := Preparer{Media: loader, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil },
		Guard: func(context.Context, string) error { return nil }}
	d := Build(&r, "", Options{Artist: ArtistOriginalsOnly}, 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if imageSegs(prepared.Parts) != 2 {
		t.Fatalf("originalsOnly 应该是两张原图：%d", imageSegs(prepared.Parts))
	}
}
