package linkreader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ByteBudget 属于单个视频；音轨、画轨和每个分片共用，不能为子请求重置。
type ByteBudget struct{ Limit, Received int64 }

func (b *ByteBudget) Remaining() int64 { return max(0, b.Limit-b.Received) }

type budgetWriter struct {
	dst    io.Writer
	budget *ByteBudget
}

func (w budgetWriter) Write(p []byte) (int, error) {
	w.budget.Received += int64(len(p))
	if w.budget.Received > w.budget.Limit {
		return 0, ErrMediaLimit
	}
	return w.dst.Write(p)
}

func mediaHost(platform, host string) bool {
	host = strings.ToLower(host)
	suffix := func(domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }
	switch platform {
	case "x":
		return host == "pbs.twimg.com" || host == "video.twimg.com" || host == "mosaic.fxtwitter.com"
	case "bilibili":
		return suffix("hdslb.com") || suffix("biliimg.com") || suffix("bilivideo.com") || suffix("bilivideo.cn")
	case "pixiv":
		return host == "i.pximg.net" || host == "s.pximg.net"
	case "youtube":
		return host == "i.ytimg.com" || host == "img.youtube.com" || suffix("googlevideo.com")
	case "douyin":
		return suffix("douyinpic.com") || suffix("douyincdn.com") || suffix("byteimg.com") || suffix("douyinvod.com")
	case "netease":
		return suffix("music.126.net")
	case "steam":
		return host == "shared.akamai.steamstatic.com" || host == "images.steamusercontent.com" || host == "avatars.fastly.steamstatic.com" || host == "avatars.akamai.steamstatic.com"
	case "afdian":
		return suffix("afdiancdn.com")
	case "spotify":
		return host == "image-cdn-fa.spotifycdn.com" || host == "image-cdn-ak.spotifycdn.com" || host == "i.scdn.co"
	// 下面几家是自动展开接进来以后才需要下载图片的：只放取数端会给出的图床，不放行整个域。
	case "booth":
		return host == "booth.pximg.net"
	case "bluesky":
		return host == "cdn.bsky.app"
	case "telegram":
		return suffix("cdn-telegram.org") || suffix("telesco.pe")
	case "misskey":
		return host == "media.misskeyusercontent.jp" || host == "proxy.misskeyusercontent.jp"
	case "mastodon":
		return host == "files.mastodon.social"
	case "tiktok":
		return suffix("tiktokcdn-us.com") || suffix("tiktokcdn.com")
	}
	return false
}

// bskyVideoThumb 说这是 Bluesky 视频的封面图：video.bsky.app/watch/<did>/<cid>/thumbnail.jpg，
// 它会 302 到 video.cdn.bsky.app/hls/<did>/<cid>/thumbnail.jpg（2026-11 实测），两个主机都只放行这一种路径。
// 同主机上的 playlist.m3u8 是 HLS 清单，不是能直接存的视频文件，所以主机本身不放行。
func bskyVideoThumb(platform string, u *url.URL) bool {
	if platform != "bluesky" || !strings.HasSuffix(u.Path, "/thumbnail.jpg") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "video.bsky.app":
		return strings.HasPrefix(u.Path, "/watch/")
	case "video.cdn.bsky.app":
		return strings.HasPrefix(u.Path, "/hls/")
	}
	return false
}

func mediaURL(platform, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.HasSuffix(u.Host, ":") || !(mediaHost(platform, u.Hostname()) || bskyVideoThumb(platform, u)) {
		return nil, failure("unsafe_target", "media")
	}
	if p := u.Port(); p != "" && !((u.Scheme == "http" && p == "80") || (u.Scheme == "https" && p == "443")) {
		return nil, failure("unsafe_target", "media")
	}
	return u, nil
}

// mediaBudgetKey 是媒体下载记在哪份限速预算上。X 的图床（pbs、video.twimg.com）单独一份：
// 推特卡片一张就要下头像和最多八张图，和取数接口共用每分钟那几次会把后面链接的取数挤掉；图床的 429 也只冷却图床。
// 其他平台的媒体照旧和取数共用。
func mediaBudgetKey(platform string) string {
	if platform == "x" {
		return "x_media"
	}
	return platform
}

// Stream 使用元信息客户端相同的安全 Transport、连接池、平台限速和逐跳检查。
// 客户端不沿用元信息的10秒总超时；总截止由整个媒体任务的 ctx 决定。
func (f *Fetcher) Stream(ctx context.Context, platform, raw string, dst io.Writer, budget *ByteBudget) error {
	if budget.Limit <= 0 {
		return failure("invalid_options", "media")
	}
	key := mediaBudgetKey(platform)
	current := raw
	for range 6 {
		u, err := mediaURL(platform, current)
		if err != nil {
			return err
		}
		if f.interval > 0 {
			if err = f.rate.allow(key, false); err != nil {
				return err
			}
			if err = f.waitRequest(ctx, u.Hostname()); err != nil {
				return err
			}
		}
		if err = f.rate.allow(key, true); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return failure("unsafe_target", "media")
		}
		req.Header.Set("User-Agent", f.userAgent)
		if platform == "pixiv" {
			req.Header.Set("Referer", "https://www.pixiv.net/")
		}
		if platform == "bilibili" {
			req.Header.Set("Referer", "https://www.bilibili.com/")
		}
		if platform == "booth" {
			req.Header.Set("Referer", "https://booth.pm/")
		}
		client := f.mediaClient
		if platform == "bilibili" || platform == "douyin" || platform == "netease" || platform == "afdian" {
			client = f.mediaBiliClient
		}
		f.requests.Add(1)
		resp, err := client.Do(req)
		// 还没收到响应头就被断开（EOF、连接重置）时一个字节都没写，同一地址重试一次；和 Fetch 的元信息请求同一判定。
		if err != nil && ctx.Err() == nil && retryableNetErr(err) {
			f.requests.Add(1)
			resp, err = client.Do(req.Clone(ctx))
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var fe *FetchError
			if errors.As(err, &fe) {
				return fe
			}
			fe = failure("network", "media")
			fe.Detail = netErrDetail(err)
			return fe
		}
		if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			location, err := resp.Location()
			resp.Body.Close()
			if err != nil {
				return failure("invalid_redirect", "media")
			}
			current = location.String()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			f.rate.cooldown(key, resp.Header.Get("Retry-After"))
		}
		if err := httpFailure(resp.StatusCode, resp.Header, "media"); err != nil {
			resp.Body.Close()
			return err
		}
		if resp.ContentLength > budget.Remaining() {
			resp.Body.Close()
			return ErrMediaLimit
		}
		_, err = io.Copy(budgetWriter{dst: dst, budget: budget}, io.LimitReader(resp.Body, budget.Remaining()+1))
		closeErr := resp.Body.Close()
		if errors.Is(err, ErrMediaLimit) {
			return ErrMediaLimit
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || closeErr != nil {
			fe := failure("network", "media")
			if err == nil {
				err = closeErr
			}
			fe.Detail = "读取中断：" + netErrDetail(err)
			return fe
		}
		return nil
	}
	return failure("too_many_redirects", "media")
}
