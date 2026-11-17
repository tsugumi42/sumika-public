package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

type countLoader struct {
	images int
	err    error
}

func (c *countLoader) Image(context.Context, string, linkreader.Media) (linkexpand.Loaded, error) {
	c.images++
	if c.err != nil {
		return linkexpand.Loaded{}, c.err
	}
	return linkexpand.Loaded{Resource: types.MemoryMedia("x", "image/png", 3, 2, []byte("png!")), Kind: types.BodyImage, Note: "备注"}, nil
}
func (c *countLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, linkexpand.VideoLimits, bool) (linkexpand.Loaded, error) {
	return linkexpand.Loaded{}, linkexpand.ErrVideoDisabled
}
func (c *countLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (linkexpand.Loaded, error) {
	return linkexpand.Loaded{}, errors.New("没有")
}

func media(u string) linkreader.Media {
	return linkreader.Media{URL: linkreader.Field[string]{State: "available", Value: u}}
}

func TestCachedLoaderDownloadsOnceAndHandsFreshResources(t *testing.T) {
	inner := &countLoader{}
	c := newCachedLoader(inner, t.TempDir())
	ctx := context.Background()
	var first *types.MediaResource
	for i := 0; i < 3; i++ {
		l, err := c.Image(ctx, "x", media("https://pbs.twimg.com/a.png"))
		if err != nil {
			t.Fatal(err)
		}
		if l.Kind != types.BodyImage || l.Note != "备注" || l.Resource.Width != 3 || l.Resource.Height != 2 || l.Resource.MIME != "image/png" {
			t.Fatalf("缓存要原样保留种类、备注、宽高：%+v %+v", l, l.Resource)
		}
		var got []byte
		err = l.Resource.Read(func(r io.Reader) error {
			var e error
			got, e = io.ReadAll(r)
			return e
		})
		if err != nil || string(got) != "png!" {
			t.Fatalf("字节不对：%q %v", got, err)
		}
		if first != nil && l.Resource == first {
			t.Fatal("每次都要新包一份资源（上一份已经被 Release）")
		}
		first = l.Resource
		_ = l.Resource.Release()
	}
	if inner.images != 1 {
		t.Fatalf("同一张图只该真下载一次，下载了 %d 次", inner.images)
	}
	if _, err := c.Image(ctx, "x", media("https://pbs.twimg.com/b.png")); err != nil || inner.images != 2 {
		t.Fatalf("不同的图要各下各的：%d %v", inner.images, err)
	}
}

func TestCachedLoaderRemembersErrorsButNotCancel(t *testing.T) {
	inner := &countLoader{err: errors.New("404")}
	c := newCachedLoader(inner, t.TempDir())
	for i := 0; i < 2; i++ {
		if _, err := c.Image(context.Background(), "x", media("u")); err == nil {
			t.Fatal("要原样交回错误")
		}
	}
	if inner.images != 1 {
		t.Fatalf("失败也只试一次（各样式结果一致），试了 %d 次", inner.images)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner.err = context.Canceled
	_, _ = c.Image(ctx, "x", media("v"))
	inner.err = nil
	if _, err := c.Image(context.Background(), "x", media("v")); err != nil {
		t.Fatalf("取消不该被记住：%v", err)
	}
}
