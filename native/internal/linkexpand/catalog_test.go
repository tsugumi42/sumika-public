package linkexpand

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 站点和页面类型清单（linktemplate.Catalog）和识别、模板站点、Supports 的对账。

func TestCatalogMatchesRecognizer(t *testing.T) {
	for _, p := range linktemplate.Catalog() {
		for _, k := range p.Kinds {
			if k.Example == "" {
				continue
			}
			l, ok := linkreader.Recognize(k.Example)
			if !ok {
				t.Errorf("%s/%s 的示例链接识别不出来：%s", p.ID, k.ID, k.Example)
				continue
			}
			got, found := linktemplate.LookupKind(l.Platform, l.Kind)
			if l.Platform != p.ID || !found || got.ID != k.ID {
				t.Errorf("%s/%s 的示例识别成了 %s/%s", p.ID, k.ID, l.Platform, l.Kind)
			}
		}
	}
}

func TestCatalogExpandRowsHaveSites(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range linktemplate.Catalog() {
		if seen[p.ID] {
			t.Errorf("网站 %s 重复", p.ID)
		}
		seen[p.ID] = true
		ids := map[string]bool{}
		for _, k := range p.Kinds {
			if ids[k.ID] {
				t.Errorf("%s/%s 重复", p.ID, k.ID)
			}
			ids[k.ID] = true
			if k.Label == "" {
				t.Errorf("%s/%s 没有显示名", p.ID, k.ID)
			}
			if k.Support != linktemplate.SupportExpand {
				if k.Note == "" {
					t.Errorf("%s/%s 不能展开，要写原因", p.ID, k.ID)
				}
				if got := linktemplate.SiteKey(p.ID, k.ID); got != "default" {
					t.Errorf("%s/%s 不能展开却有站点键 %s", p.ID, k.ID, got)
				}
				continue
			}
			if _, _, ok := linktemplate.SiteByKey(k.Site); !ok {
				t.Errorf("%s/%s 能展开，但模板站点 %q 不存在", p.ID, k.ID, k.Site)
			}
			if !Supports(p.ID, k.ID) || linktemplate.SiteKey(p.ID, k.ID) != k.Site {
				t.Errorf("%s/%s 的 Supports 或站点键和清单对不上", p.ID, k.ID)
			}
			for _, a := range k.Aliases {
				if linktemplate.SiteKey(p.ID, a) != k.Site {
					t.Errorf("%s 的别名 %s 没对到 %s", p.ID, a, k.Site)
				}
			}
		}
	}
	// 每个模板站点（除 default）至少被清单里一行用到，免得加了模板没人接
	used := map[string]bool{}
	for _, p := range linktemplate.Catalog() {
		for _, k := range p.Kinds {
			if k.Site != "" {
				used[k.Site] = true
			}
		}
	}
	for _, s := range linktemplate.Sites() {
		if s.Key != "default" && !used[s.Key] {
			t.Errorf("模板站点 %s 没有被清单任何一行使用", s.Key)
		}
	}
}

// 表驱动之前的 Supports 行为原样保住（旧的十三种都能展开），再加上后来补的直播间和个人主页；其余只能取数或不支持的都不能展开。
func TestSupportsMatchesCatalog(t *testing.T) {
	yes := []string{"bilibili/video", "bilibili/opus", "bilibili/dynamic", "bilibili/mall", "x/post", "x/article", "github/repository", "youtube/video",
		"douyin/video", "douyin/note", "netease/song", "pixiv/artwork", "pixiv/novel", "bilibili/live", "bilibili/user", "bilibili/workshop", "bilibili/article", "bilibili/episode", "bilibili/course", "bilibili/audio", "bilibili/collection",
		"pixiv/series", "pixiv/novel_series", "xiaoheihe/game", "booth/item", "steam/app", "steam/profile", "afdian/album_post", "bluesky/post", "misskey/note", "mastodon/status", "spotify/episode", "tiktok/user"}
	no := []string{"bilibili/short", "github/issue", "github/release", "netease/playlist", "steam/reviews", "xiaohongshu/note", "xiaoheihe/post", "spotify/show", "x/short", "unknown/video", "bilibili/"}
	for _, k := range yes {
		p, kind, _ := cutSlash(k)
		if !Supports(p, kind) {
			t.Errorf("%s 应能展开", k)
		}
	}
	for _, k := range no {
		p, kind, _ := cutSlash(k)
		if Supports(p, kind) {
			t.Errorf("%s 不该能展开", k)
		}
	}
}

func cutSlash(s string) (string, string, bool) {
	for i := range len(s) {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("离线测试：不连网")
}

// 清单的“能取数”一列和 fetch.go 的分发互查：标了不支持的，Fetch 回 unsupported；其余的绝不是 unsupported（离线时是网络类错误）。
func TestCatalogSupportMatchesFetchDispatch(t *testing.T) {
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: failTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range linktemplate.Catalog() {
		for _, k := range p.Kinds {
			if k.Example == "" {
				continue
			}
			l, ok := linkreader.Recognize(k.Example)
			if !ok {
				continue
			}
			_, ferr := f.Fetch(context.Background(), l)
			var fe *linkreader.FetchError
			unsupported := errors.As(ferr, &fe) && fe.Kind == "unsupported"
			if k.Support == linktemplate.SupportNone && !unsupported {
				t.Errorf("%s/%s 标了不支持，Fetch 却没回 unsupported：%v", p.ID, k.ID, ferr)
			}
			if k.Support != linktemplate.SupportNone && unsupported {
				t.Errorf("%s/%s 标了能取数，Fetch 却回 unsupported", p.ID, k.ID)
			}
		}
	}
}
