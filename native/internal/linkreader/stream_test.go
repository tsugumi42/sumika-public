package linkreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamMediaBoundaries(t *testing.T) {
	for _, declared := range []int64{-1, 1, 32, 33} {
		for _, n := range []int{31, 32, 33} {
			t.Run(strings.Join([]string{string(rune('a' + declared + 1)), string(rune('a' + n))}, "/"), func(t *testing.T) {
				f, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: declared, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", n)))}, nil
				})})
				if e != nil {
					t.Fatal(e)
				}
				var dst bytes.Buffer
				budget := &ByteBudget{Limit: 32}
				e = f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", &dst, budget)
				overflow := n > 32 || declared > 32
				if overflow && !errors.Is(e, ErrMediaLimit) {
					t.Fatalf("declared=%d actual=%d error=%v", declared, n, e)
				}
				if !overflow && (e != nil || dst.Len() != n) {
					t.Fatalf("边界内 declared=%d actual=%d wrote=%d err=%v", declared, n, dst.Len(), e)
				}
				if dst.Len() > 32 || budget.Received > 33 {
					t.Fatal("超限仍写入", dst.Len(), budget.Received)
				}
			})
		}
	}
	var requests atomic.Int32
	f, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Stream(context.Background(), "x", "https://pbs.twimg.com/redirect", io.Discard, &ByteBudget{Limit: 32}); e == nil || requests.Load() != 1 {
		t.Fatalf("重定向内网：requests=%d err=%v", requests.Load(), e)
	}
	f, e = NewFetcher(FetchOptions{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	cancel()
	if e = f.Stream(ctx, "x", "https://video.twimg.com/cancel.mp4", io.Discard, &ByteBudget{Limit: 32}); !errors.Is(e, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("取消未退出：%v %v", e, time.Since(started))
	}
	f, e = NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 20)))}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	shared := &ByteBudget{Limit: 32}
	if e = f.Stream(context.Background(), "bilibili", "https://cdn.bilivideo.com/video", io.Discard, shared); e != nil {
		t.Fatal(e)
	}
	if e = f.Stream(context.Background(), "bilibili", "https://cdn.bilivideo.com/audio", io.Discard, shared); !errors.Is(e, ErrMediaLimit) || shared.Received != 33 {
		t.Fatalf("DASH两轨重置了预算：%+v %v", shared, e)
	}
	t.Logf("共享音画预算：实际读取=%d，上限=%d；私网重定向请求=1；取消=%v", shared.Received, shared.Limit, time.Since(started))
}

// 自动展开接入的新站要能下载自己的图床，且只放行取数端会给出的主机，不放行整个域或别家的主机。
func TestMediaHostForNewSites(t *testing.T) {
	for _, c := range []struct {
		platform, host string
		want           bool
	}{
		{"booth", "booth.pximg.net", true}, {"booth", "i.pximg.net", false}, {"booth", "booth.pm", false},
		{"bluesky", "cdn.bsky.app", true}, {"bluesky", "video.bsky.app", false}, {"bluesky", "bsky.app", false},
		{"misskey", "media.misskeyusercontent.jp", true}, {"misskey", "proxy.misskeyusercontent.jp", true}, {"misskey", "misskey.io", false},
		{"mastodon", "files.mastodon.social", true}, {"mastodon", "mastodon.social", false},
		{"tiktok", "p16-common-sign.tiktokcdn-us.com", true}, {"tiktok", "p16.tiktokcdn.com", true}, {"tiktok", "tiktokcdn.com.evil.example", false}, {"tiktok", "www.tiktok.com", false},
		{"bluesky", "cdn.bsky.app.evil.example", false}, {"spotify", "cdn.bsky.app", false},
		// Steam 个人资料头像 2026-11 起给 akamai 域名（原来是 fastly），两个精确主机都要放行
		{"steam", "avatars.akamai.steamstatic.com", true}, {"steam", "avatars.fastly.steamstatic.com", true}, {"steam", "avatars.evil.steamstatic.com", false}, {"steam", "avatars.akamai.steamstatic.com.evil.example", false},
	} {
		if got := mediaHost(c.platform, c.host); got != c.want {
			t.Errorf("%s 的图床 %s：%v，应为 %v", c.platform, c.host, got, c.want)
		}
	}
}

// 回归：Bluesky 视频的封面在 video.bsky.app 上，原来被当成不安全地址，视频帖连封面都没有（样张 BS-02）。
// 只放行 thumbnail.jpg；同主机的 HLS 清单和其他路径仍然拒绝。
func TestMediaURLBlueskyVideoThumbnail(t *testing.T) {
	base := "https://video.bsky.app/watch/did%3Aplc%3Aexample/bafkreiexample/"
	for _, c := range []struct {
		platform, raw string
		ok            bool
	}{
		{"bluesky", base + "thumbnail.jpg", true},
		{"bluesky", "https://video.cdn.bsky.app/hls/did:plc:example/bafkreiexample/thumbnail.jpg", true},
		{"bluesky", "https://video.cdn.bsky.app/hls/did:plc:example/bafkreiexample/playlist.m3u8", false},
		{"bluesky", "https://video.cdn.bsky.app/other/thumbnail.jpg", false},
		{"bluesky", base + "playlist.m3u8", false},
		{"bluesky", "https://video.bsky.app/other/thumbnail.jpg", false},
		{"bluesky", "https://video.bsky.app.evil.example/watch/x/y/thumbnail.jpg", false},
		{"bluesky", "http://video.bsky.app:8080/watch/x/y/thumbnail.jpg", false},
		{"misskey", base + "thumbnail.jpg", false},
	} {
		_, err := mediaURL(c.platform, c.raw)
		if (err == nil) != c.ok {
			t.Errorf("%s %s：err=%v，应为 ok=%v", c.platform, c.raw, err, c.ok)
		}
	}
}
