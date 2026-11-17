package linkreader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func fixtureObject(t *testing.T, name, key string) string {
	t.Helper()
	var objects map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixture(t, name)), &objects); err != nil {
		t.Fatal(err)
	}
	return string(objects[key])
}

func TestMoreRecognizeAndClean(t *testing.T) {
	for _, c := range []struct{ raw, platform, kind, id, clean string }{
		{"github.com/Example-Lab/Widget.git?utm_source=qq&tab=readme-ov-file#readme", "github", "repository", "example-lab/widget", "https://github.com/example-lab/widget?tab=readme-ov-file#readme"},
		// ID 和干净链接都小写（缓存、去重按它）；用户写的大小写在 Display 里，见 TestGitHubDisplayKeepsCase
		{"https://github.com/Example-Lab/Widget/issues/12", "github", "issue", "example-lab/widget/issues/12", "https://github.com/example-lab/widget/issues/12"},
		{"https://github.com/example-lab/widget/issues/7?x=1", "github", "issue", "example-lab/widget/issues/7", "https://github.com/example-lab/widget/issues/7?x=1"},
		{"https://github.com/example-lab/widget/pull/8", "github", "pull_request", "example-lab/widget/pull/8", "https://github.com/example-lab/widget/pull/8"},
		{"https://github.com/example-lab/widget/releases/tag/v1%2Fpatch", "github", "release", "example-lab/widget/releases/tag/v1/patch", "https://github.com/example-lab/widget/releases/tag/v1/patch"},
		{"https://github.com/example-lab/widget/releases/latest", "github", "release", "example-lab/widget/releases/latest", "https://github.com/example-lab/widget/releases/latest"},
		{"https://gist.github.com/example-lab/abcdef123456", "github", "gist", "abcdef123456", "https://gist.github.com/abcdef123456"},
		{"youtu.be/abcdefghijk?si=track&t=12&list=PLsample&x=%2f&x=", "youtube", "video", "abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk&t=12&list=PLsample&x=%2f&x="},
		{"https://m.youtube.com/shorts/abcdefghijk?si=track", "youtube", "video", "abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk"},
		{"https://www.youtube-nocookie.com/embed/abcdefghijk?start=10", "youtube", "video", "abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk&start=10"},
		{"https://www.youtube.com/watch?x=1&v=abcdefghijk&pp=track&t=4", "youtube", "video", "abcdefghijk", "https://www.youtube.com/watch?x=1&v=abcdefghijk&t=4"},
		{"https://v.douyin.com/Sample_1/", "douyin", "short", "", "https://v.douyin.com/Sample_1/"},
		{"https://www.iesdouyin.com/share/video/90001/?share_token=drop&x=keep", "douyin", "video", "90001", "https://www.douyin.com/video/90001?x=keep"},
		{"https://www.douyin.com/video/90001?did=sample-device&ts=1&from_aid=6383&from_ssr=1&video_share_track_ver=1&share_track_info=%7B%7D&x=keep", "douyin", "video", "90001", "https://www.douyin.com/video/90001?x=keep"},
		{"https://jingxuan.douyin.com/m/slides/90003?app=yumme", "douyin", "note", "90003", "https://www.douyin.com/note/90003"},
		{"https://m.douyin.com/share/note/90003", "douyin", "note", "90003", "https://www.douyin.com/note/90003"},
		{"https://163cn.tv/Sample1/", "netease", "short", "", "https://163cn.tv/Sample1/"},
		{"https://music.163.com/#/song?id=186016&userid=track&x=%2f&x=", "netease", "song", "186016", "https://music.163.com/song?id=186016&x=%2f&x="},
		{"https://y.music.163.com/m/playlist?id=3778678&uct2=track", "netease", "playlist", "3778678", "https://music.163.com/playlist?id=3778678"},
		{"https://music.163.com/album?id=18905", "netease", "album", "18905", "https://music.163.com/album?id=18905"},
		{"https://www.pixiv.net/en/artworks/100000000?utm_source=qq&x=keep#1", "pixiv", "artwork", "100000000", "https://www.pixiv.net/artworks/100000000?x=keep#1"},
		{"https://pixiv.net/member_illust.php?mode=medium&illust_id=100000000&ref=share", "pixiv", "artwork", "100000000", "https://www.pixiv.net/artworks/100000000"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != c.platform || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean || Clean(l.Clean) != c.clean {
				t.Fatalf("got %+v %v; want %s", l, ok, c.clean)
			}
			if got := Extract("分享（" + c.raw + "）"); len(got) != 1 || got[0].Clean != c.clean {
				t.Fatalf("text extraction: %+v", got)
			}
			card := fmt.Sprintf(`{"meta":{"music":{"musicUrl":%q}}}`, c.raw)
			if got, _ := FromCard(card); len(got) != 1 || got[0].Clean != c.clean {
				t.Fatalf("card extraction: %+v", got)
			}
		})
	}
	for _, raw := range []string{
		"https://github.com/example-lab/widget/tree/main", "https://github.com/example-lab/..", "https://github.com/search/code", "https://github.com/example-lab/widget/issues/0",
		"https://youtube.com/watch?v=abcdefghijk&v=12345678901", "https://youtu.be/abcdefghijk/extra", "https://youtube.com/watch?v=short", "https://youtube.com/redirect?q=https://evil.example", "https://youtube.com.evil.example/watch?v=abcdefghijk",
		"https://music.163.com/song?id=1&id=2", "https://music.163.com/#/song?id=0", "https://music.163.com/#https://evil.example/song?id=1",
		"https://www.douyin.com/user/90001", "https://www.pixiv.net/users/90001", "https://user:pass@www.pixiv.net/artworks/100000000", "https://www.pixiv.net:444/artworks/100000000",
		"https://www.pixiv.net/member_illust.php?mode=medium&illust_id=100000000&mode=%zz",
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %s: %+v", raw, l)
		}
	}
	for _, c := range []struct{ raw, want string }{
		{"https://music.163.com/song?id=1&userid=2&x=keep", "https://music.163.com/song?id=1&x=keep"},
		{"https://music.163.com/#/song?id=1&userid=2&x=%2f", "https://music.163.com/song?id=1&x=%2f"},
		{"https://www.douyin.com/video/1?share_token=drop&x=keep", "https://www.douyin.com/video/1?x=keep"},
		{"https://www.pixiv.net/artworks/1?ref=drop&x=keep", "https://www.pixiv.net/artworks/1?x=keep"},
		{"https://example.com/song?id=1&userid=2&ref=keep", "https://example.com/song?id=1&userid=2&ref=keep"},
	} {
		if got, err := CleanAny(c.raw); err != nil || got != c.want {
			t.Fatalf("desktop clean %s: %s %v", c.raw, got, err)
		}
	}
}

func TestMoreShortLinks(t *testing.T) {
	for _, c := range []struct{ short, target, clean string }{
		{"https://v.douyin.com/sample", "https://www.iesdouyin.com/share/video/90001/?share_token=drop", "https://www.douyin.com/video/90001"},
		{"https://jx.douyin.com/sample", "https://www.douyin.com/note/90003", "https://www.douyin.com/note/90003"},
		{"https://163cn.tv/sample", "https://y.music.163.com/m/song?id=186016&userid=drop", "https://music.163.com/song?id=186016"},
	} {
		t.Run(c.short, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				resp := response(302, "")
				resp.Header.Set("Location", c.target)
				return resp, nil
			})
			got, err := f.Resolve(context.Background(), link(t, c.short))
			if err != nil || got.Clean != c.clean || f.Requests() != 1 {
				t.Fatalf("short result: %+v %v requests=%d", got, err, f.Requests())
			}
		})
	}
}

func TestMoreGitHubReplay(t *testing.T) {
	for _, c := range []struct{ kind, path, title string }{
		{"repository", "example-lab/widget", "example-lab/widget"}, {"issue", "example-lab/widget/issues/7", "编造的问题"}, {"pull_request", "example-lab/widget/pull/8", "编造的改动"}, {"release", "example-lab/widget/releases/tag/v1.2.3", "编造的发行版"}, {"gist", "abcdef123456", "编造的代码片段"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			host := "github.com"
			if c.kind == "gist" {
				host = "gist.github.com"
			}
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
					t.Fatal("non-anonymous")
				}
				return response(200, fixtureObject(t, "more-github", c.kind)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://"+host+"/"+c.path))
			if err != nil {
				t.Fatal(err)
			}
			if r.Title.Value != c.title || r.Author.ID.Value != "90002" {
				t.Fatalf("metadata: %+v", r)
			}
			switch c.kind {
			case "repository":
				if r.Stats["stars"].State != "available" || r.Stats["stars"].Value != 0 || r.Stats["forks"].Value != 12 || r.GitHub.Language.Value != "Go" || r.GitHub.Updated.Value.Format(time.RFC3339) != "2026-11-03T01:02:03Z" || r.Media.Value[0].URL.Value != "https://opengraph.githubassets.com/1/example-lab/widget" {
					t.Fatalf("repository fields: %+v", r)
				}
			case "issue":
				if r.GitHub.State.Value != "closed" || r.Stats["comments"].State != "available" || r.Stats["comments"].Value != 0 || r.Text.Value != "问题正文开头" {
					t.Fatalf("issue: %+v", r)
				}
			case "pull_request":
				if r.Flags["merged"].Value != true || r.Stats["comments"].Value != 3 {
					t.Fatalf("PR: %+v", r)
				}
			case "release":
				if r.Published.Value.Format(time.RFC3339) != "2026-11-02T12:00:00Z" || r.Text.Value != "更新说明" {
					t.Fatalf("release: %+v", r)
				}
			case "gist":
				if r.Text.Value != "package sample\n" || r.Stats["files"].Value != 1 {
					t.Fatalf("gist: %+v", r)
				}
			}
		})
	}
}

func TestMoreYouTubeReplay(t *testing.T) {
	for _, c := range []struct {
		name, id, title, channel string
		likes                    bool
	}{{"one", "example0001", "编造的视频标题甲", "编造频道乙", false}, {"two", "example0002", "编造的视频标题乙", "编造频道甲", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				return response(200, "<script>var ytInitialPlayerResponse = "+fixture(t, "more-yt-"+c.name)+";</script>"), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://youtu.be/"+c.id+"?si=drop&t=12"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Title.Value != c.title || r.Author.Name.Value != c.channel || r.Author.ID.Value != "UC_fixture_channel" || r.Published.State != "available" || r.Stats["views"].State != "available" || (r.Stats["likes"].State == "available") != c.likes || r.Media.Value[1].Duration.State != "available" || r.Media.Value[0].URL.State != "available" || !strings.Contains(r.CleanURL, "t=12") {
				t.Fatalf("video metadata: %+v", r)
			}
			// 点赞数在 microformat.likeCount 里：two 的样本带，one 的没带就记“没返回”
			if c.likes && r.Stats["likes"].Value != 123456 {
				t.Fatalf("likes: %+v", r.Stats["likes"])
			}
			if c.name == "one" && r.Media.Value[1].Duration.Value != 19 {
				t.Fatalf("duration: %+v", r.Media.Value[1])
			}
			if c.likes && (!r.Text.Truncated || len([]rune(r.Text.Value)) != 800) {
				t.Fatalf("description preview: %+v", r.Text)
			}
		})
	}
}

func TestMoreDouyinReplay(t *testing.T) {
	for _, c := range []struct{ kind, id, key string }{{"video", "90001", "video"}, {"note", "90003", "note"}} {
		t.Run(c.kind, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" {
					t.Fatal("cookie")
				}
				return response(200, fixtureObject(t, "more-douyin", c.key)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://www.douyin.com/"+c.kind+"/"+c.id))
			if err != nil {
				t.Fatal(err)
			}
			if r.Author.ID.Value != "90002" || r.Author.Name.Value != "编造作者甲" {
				t.Fatalf("author: %+v", r)
			}
			if c.kind == "video" {
				if r.Stats["likes"].State != "available" || r.Stats["likes"].Value != 0 || r.Stats["comments"].Value != 12 || r.Stats["shares"].Value != 3 || r.Media.Value[0].Duration.Value != 12.345 || r.Media.Value[0].Thumbnail.State != "available" {
					t.Fatalf("video: %+v", r)
				}
			} else if r.Stats["likes"].State != "not_returned" || len(r.Media.Value) != 2 || !strings.Contains(r.Blocks[1].Media.URL.Value, "first.jpg") || !strings.Contains(r.Blocks[2].Media.URL.Value, "second.jpg") {
				t.Fatalf("ordered note: %+v", r)
			}
		})
	}
}

func TestMoreNeteaseReplay(t *testing.T) {
	for _, c := range []struct{ kind, id, name string }{{"song", "90001", "编造的歌曲"}, {"playlist", "90002", "编造的榜单"}} {
		t.Run(c.kind, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fixture(t, "more-ncm-"+c.kind)), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://music.163.com/#/"+c.kind+"?id="+c.id))
			if err != nil {
				t.Fatal(err)
			}
			if r.Title.Value != c.name || r.Author.ID.Value != "90002" {
				t.Fatalf("music: %+v", r)
			}
			if c.kind == "song" {
				if r.Music.Album.Value != "编造专辑" || r.Music.Artists.Value[0].Name.Value != "编造歌手0" || r.Media.Value[0].Duration.Value != 269 || r.Media.Value[1].URL.State != "available" {
					t.Fatalf("song fields: %+v", r)
				}
			} else if r.Stats["tracks"].Value != 200 || r.Media.Value[0].URL.State != "available" {
				t.Fatalf("playlist: %+v", r)
			}
		})
	}
	t.Run("album", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, `{"code":200,"album":{"id":90001,"name":"编造专辑","artists":[{"id":90002,"name":"编造歌手甲"}],"picUrl":"https://p1.music.126.net/album.jpg","publishTime":0,"size":12}}`), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://music.163.com/album?id=90001"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Title.Value != "编造专辑" || r.Stats["tracks"].Value != 12 || r.Author.Name.Value != "编造歌手甲" || r.Published.State != "available" || r.Published.Value.UnixMilli() != 0 {
			t.Fatalf("album: %+v", r)
		}
	})
}

func TestMorePixivReplay(t *testing.T) {
	for _, c := range []struct {
		name, id string
		pages    int
	}{{"one", "100000000", 1}, {"two", "123456789", 3}} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Referer") != "https://www.pixiv.net/" || req.Header.Get("Cookie") != "" {
					t.Fatal("anonymous referer")
				}
				name := "more-pixiv-" + c.name
				if strings.HasSuffix(req.URL.Path, "/pages") {
					name = "more-pixiv-pages"
				}
				return response(200, fixture(t, name)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/"+c.id))
			if err != nil {
				t.Fatal(err)
			}
			if r.Author.ID.Value != "90002" || r.Stats["pages"].Value != int64(c.pages) || len(r.Media.Value) != c.pages || r.Tags.State != "available" || r.Flags["possibly_sensitive"].Value != false || r.Stats["bookmarks"].State != "available" || r.Stats["views"].State != "available" {
				t.Fatalf("artwork: %+v", r)
			}
			if c.name == "one" && (r.Tags.Value[0] != "编造标签甲" || r.Media.Value[0].Width.Value != 1767) {
				t.Fatalf("tags and dimensions: %+v", r)
			}
			for i, m := range r.Media.Value {
				if m.Referer != "https://www.pixiv.net/" || m.URL.State != "available" {
					t.Fatalf("image: %+v", m)
				}
				if !strings.Contains(m.URL.Value, fmt.Sprintf("%s_p%d.", c.id, i)) {
					t.Fatalf("page order: %+v", m)
				}
			}
		})
	}
	t.Run("restriction_and_partial_pages", func(t *testing.T) {
		for _, restrict := range []string{"0", "1", "2", "null", "9"} {
			body := strings.Replace(fixture(t, "more-pixiv-two"), `"xRestrict": 0`, `"xRestrict": `+restrict, 1)
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/pages") {
					return response(403, `{}`), nil
				}
				return response(200, body), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/123456789"))
			if err != nil {
				t.Fatal(err)
			}
			flag := r.Flags["possibly_sensitive"]
			if restrict == "null" || restrict == "9" {
				if flag.State != "not_returned" {
					t.Fatalf("unknown restriction: %+v", flag)
				}
			} else if flag.State != "available" || flag.Value != (restrict != "0") {
				t.Fatalf("restriction %s: %+v", restrict, flag)
			}
			if r.Title.State != "available" || !r.Media.Truncated || r.Media.Reason != "anonymous_access_denied" || len(r.Media.Value) != 1 {
				t.Fatalf("partial pages: %+v", r)
			}
		}
	})
}

func TestMoreInputBoundaries(t *testing.T) {
	for _, c := range []struct{ name, url, body, kind string }{
		{"github_identity", "https://github.com/example-lab/other", fixtureObject(t, "more-github", "repository"), "identity_mismatch"},
		{"github_private", "https://github.com/example-lab/widget", strings.Replace(fixtureObject(t, "more-github", "repository"), `"private":false`, `"private":true`, 1), "invalid_response"},
		{"youtube_identity", "https://youtu.be/abcdefghijk", `<script>var ytInitialPlayerResponse = ` + fixture(t, "more-yt-one") + `;</script>`, "identity_mismatch"},
		{"youtube_login", "https://youtu.be/abcdefghijk", `ytInitialPlayerResponse={"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`, "login_required"},
		{"youtube_html", "https://youtu.be/abcdefghijk", "<html>challenge</html>", "invalid_response"},
		{"douyin_identity", "https://www.douyin.com/video/90009", fixtureObject(t, "more-douyin", "video"), "identity_mismatch"},
		{"douyin_filtered", "https://www.douyin.com/note/90003", fixtureObject(t, "more-douyin", "blocked_slides"), "not_returned"},
		{"douyin_bad_count", "https://www.douyin.com/video/90001", strings.Replace(fixtureObject(t, "more-douyin", "video"), `"digg_count":0`, `"digg_count":"0"`, 1), "invalid_response"},
		{"netease_identity", "https://music.163.com/song?id=90009", fixture(t, "more-ncm-song"), "identity_mismatch"},
		{"netease_login", "https://music.163.com/album?id=18905", fixture(t, "more-ncm-album-denied"), "login_required"},
		{"pixiv_identity", "https://www.pixiv.net/artworks/90009", fixture(t, "more-pixiv-one"), "identity_mismatch"},
		{"pixiv_bad_restriction", "https://www.pixiv.net/artworks/100000000", strings.Replace(fixture(t, "more-pixiv-one"), `"xRestrict": 0`, `"xRestrict": false`, 1), "invalid_response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, c.url))
			wantError(t, err, c.kind)
		})
	}
}

func TestMoreGitHubRateLimit(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
			calls := 0
			f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := response(status, fixtureObject(t, "more-github", "repository"))
				resp.Header.Set("X-RateLimit-Remaining", "0")
				resp.Header.Set("X-RateLimit-Reset", fmt.Sprint(now.Add(time.Hour).Unix()))
				return resp, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Fetch(context.Background(), link(t, "https://github.com/example-lab/widget"))
			if status == 403 {
				wantError(t, err, "rate_limited")
			} else if err != nil {
				t.Fatal(err)
			}
			now = now.Add(61 * time.Second)
			_, err = f.Fetch(context.Background(), link(t, "https://github.com/example-lab/widget/issues/7"))
			wantError(t, err, "rate_limited")
			if calls != 1 {
				t.Fatalf("rate limit sent another request: %d", calls)
			}
			if err := f.rate.allow("netease", true); err != nil {
				t.Fatal("platform cooldown leaked", err)
			}
		})
	}
}
