package linkreader

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
)

// 样本是 2026-11-07 匿名 embed 页裁剪出的公开字段（track、album、playlist、artist、episode）。
// spotify-region.html 与 spotify-oembed.json 是按规格手写的合成样本，不是抓来的原样。
func spotifyPage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSpotifyReplay(t *testing.T) {
	cases := []struct {
		kind, id, file string
		check          func(t *testing.T, r Result)
	}{
		{"track", "ExampleTrack0000000001", "spotify-track.html", func(t *testing.T, r Result) {
			if r.Title.Value != "编造的霓虹街" || r.Author.Name.Value != "编造歌手甲" || r.Author.ID.Value != "ExampleArtist000000001" {
				t.Fatalf("track identity: %+v", r)
			}
			if r.Music == nil || r.Music.Album.State == "available" || len(r.Music.Artists.Value) != 1 {
				t.Fatalf("track music: %+v", r.Music)
			}
			if r.Published.State != "available" || r.Published.Value.Format("2006-01-02") != "2026-01-02" {
				t.Fatalf("track date: %+v", r.Published)
			}
			if r.Flags["explicit"].Value != false || r.Flags["explicit"].State != "available" {
				t.Fatalf("explicit flag: %+v", r.Flags)
			}
			if len(r.Media.Value) != 2 || r.Media.Value[0].Type != "audio" || r.Media.Value[0].URL.State != "unsupported" || r.Media.Value[0].Duration.Value != 200.04 {
				t.Fatalf("track audio: %+v", r.Media)
			}
			if r.Media.Value[1].URL.Value != "https://image-cdn-fa.spotifycdn.com/image/fixture-17" {
				t.Fatalf("largest cover not chosen: %s", r.Media.Value[1].URL.Value)
			}
		}},
		{"album", "ExampleAlbum0000000001", "spotify-album.html", func(t *testing.T, r Result) {
			if r.Title.Value != "编造的专辑·加长版" || r.Author.Name.Value != "编造歌手乙" || r.Author.ID.State == "available" {
				t.Fatalf("album display name must not become an ID: %+v", r.Author)
			}
			if r.Published.State == "available" {
				t.Fatalf("null release date must not be invented: %+v", r.Published)
			}
			if r.Music == nil || r.Music.Album.State == "available" || r.Music.Artists.State != "available" {
				t.Fatalf("album music: %+v", r.Music)
			}
		}},
		{"playlist", "ExamplePlaylist0000001", "spotify-playlist.html", func(t *testing.T, r Result) {
			if r.Title.Value != "编造的今日热歌" || r.Author.Name.State == "available" || r.Music != nil {
				t.Fatalf("playlist: %+v", r)
			}
			if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "image" || !strings.HasPrefix(r.Media.Value[0].URL.Value, "https://image-cdn-fa.spotifycdn.com/") {
				t.Fatalf("playlist cover: %+v", r.Media)
			}
		}},
		{"artist", "ExampleArtist000000002", "spotify-artist.html", func(t *testing.T, r Result) {
			if r.Title.Value != "编造歌手丙" || r.Music != nil || len(r.Media.Value) != 1 {
				t.Fatalf("artist: %+v", r)
			}
		}},
		{"episode", "ExampleEpisode00000001", "spotify-episode.html", func(t *testing.T, r Result) {
			if r.Title.Value != "第1集 - 编造的访谈" || r.Author.Name.Value != "编造的播客节目" || r.Author.ID.State == "available" {
				t.Fatalf("episode: %+v", r.Author)
			}
			if r.Media.Value[0].Duration.Value != 8311.102 || r.Flags["explicit"].Value != true {
				t.Fatalf("episode fields: %+v %+v", r.Media.Value[0], r.Flags)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://open.spotify.com/embed/"+c.kind+"/"+c.id {
					t.Fatalf("unexpected URL %s", req.URL)
				}
				if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || !strings.Contains(req.Header.Get("Accept"), "text/html") || req.Header.Get("Referer") != "" {
					t.Fatalf("headers: %v", req.Header)
				}
				return response(200, spotifyPage(t, c.file)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/"+c.kind+"/"+c.id))
			if err != nil {
				t.Fatal(err)
			}
			if r.Platform != "spotify" || r.Kind != c.kind || r.ID != c.id || r.Source != "spotify_embed" || len(r.Stats) != 0 {
				t.Fatalf("header: %+v", r)
			}
			c.check(t, r)
		})
	}
}

func TestSpotifyShowIsNotFetched(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, spotifyPage(t, "spotify-episode.html")), nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/show/ExampleShow00000000001"))
	wantError(t, err, "unsupported")
	if calls != 0 {
		t.Fatalf("show sent %d requests", calls)
	}
}

func TestSpotifyFailureShapes(t *testing.T) {
	const track = "ExampleTrack0000000001"
	trackURL := "https://open.spotify.com/track/" + track
	page := func(t *testing.T) string { return spotifyPage(t, "spotify-track.html") }
	structural := map[string]bool{"missing_entity": true, "malformed_json": true, "no_script": true}
	for _, c := range []struct {
		name   string
		status int
		body   func(t *testing.T) string
		target string
		kind   string
	}{
		{"not_found", 404, func(*testing.T) string { return "" }, trackURL, "not_found"},
		{"gone", 410, func(*testing.T) string { return "" }, trackURL, "not_found"},
		{"rate_limited", 429, func(*testing.T) string { return "" }, trackURL, "rate_limited"},
		{"login_wall", 403, func(*testing.T) string { return "<html>login</html>" }, trackURL, "login_required"},
		{"identity_mismatch", 200, page, "https://open.spotify.com/album/ExampleAlbum0000000001", "identity_mismatch"},
		{"type_mismatch", 200, func(t *testing.T) string {
			return strings.Replace(page(t), `"type":"track"`, `"type":"episode"`, 1)
		}, trackURL, "identity_mismatch"},
		{"missing_entity", 200, func(*testing.T) string {
			return `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{}}}</script>`
		}, trackURL, "invalid_response"},
		{"malformed_json", 200, func(*testing.T) string {
			return `<script id="__NEXT_DATA__" type="application/json">{"props":</script>`
		}, trackURL, "invalid_response"},
		{"no_script", 200, func(*testing.T) string { return "<html>challenge</html>" }, trackURL, "invalid_response"},
		{"bad_date", 200, func(t *testing.T) string {
			return strings.Replace(page(t), `"isoString":"2026-01-02T00:00:00Z"`, `"isoString":"yesterday"`, 1)
		}, trackURL, "invalid_response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if strings.Contains(req.URL.Path, "/oembed") {
					return response(404, ""), nil
				}
				return response(c.status, c.body(t)), nil
			})
			_, err := f.Fetch(context.Background(), link(t, c.target))
			wantError(t, err, c.kind)
			// 页面结构变了才允许一次 oEmbed 兜底；其他失败不发第二个请求。
			wantCalls := 1
			if structural[c.name] {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want %d", calls, wantCalls)
			}
		})
	}
}

func TestSpotifyRegionBlock(t *testing.T) {
	for _, status := range []int{403, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				return response(status, spotifyPage(t, "spotify-region.html")), nil
			})
			_, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/episode/512ojhOuo1ktJprKbVcKyQ"))
			fe, ok := err.(*FetchError)
			if !ok || fe.Kind != "not_returned" || fe.Reason != "not_available_in_market" {
				t.Fatalf("region: %#v", err)
			}
		})
	}
}

func TestSpotifyOEmbedFallback(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(req.URL.Path, "/oembed") {
			if req.URL.Query().Get("url") != "https://open.spotify.com/track/ExampleTrack0000000001" {
				t.Fatalf("oembed target: %s", req.URL.RawQuery)
			}
			return response(200, spotifyPage(t, "spotify-oembed.json")), nil
		}
		return response(200, "<html>changed</html>"), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/track/ExampleTrack0000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "spotify_oembed" || r.Title.Value != "编造的霓虹街" || calls != 2 || r.Author.Name.State == "available" {
		t.Fatalf("fallback: %+v calls=%d", r, calls)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "image" {
		t.Fatalf("fallback thumbnail: %+v", r.Media)
	}
}

func TestSpotifyZeroAndMissingFields(t *testing.T) {
	// 没有封面的专辑：不伪造媒体；没有发布日期的单集：时长和日期留缺失原因。
	noCover := strings.Replace(spotifyPage(t, "spotify-album.html"), `"visualIdentity":{"image":[`, `"visualIdentity":{"image":[],"x":[`, 1)
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, noCover), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/album/ExampleAlbum0000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Media.State == "available" && len(r.Media.Value) != 0 {
		t.Fatalf("invented media: %+v", r.Media)
	}
	ep := strings.Replace(spotifyPage(t, "spotify-episode.html"), `"releaseDate":{"isoString":"2026-02-03T18:00:00Z"}`, `"releaseDate":null`, 1)
	ep = strings.Replace(ep, `"duration":8311102`, `"duration":-1`, 1)
	f = testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, ep), nil })
	r, err = f.Fetch(context.Background(), link(t, "https://open.spotify.com/episode/ExampleEpisode00000001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Published.State == "available" || r.Media.Value[0].Duration.State == "available" {
		t.Fatalf("missing date or negative duration accepted: %+v %+v", r.Published, r.Media.Value[0].Duration)
	}
}

func TestSpotifyLongTitleKept(t *testing.T) {
	long := strings.Repeat("长", 5000)
	page := strings.Replace(spotifyPage(t, "spotify-artist.html"), `"name":"编造歌手丙"`, `"name":"`+long+`"`, 1)
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, page), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/artist/ExampleArtist000000002"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Title.Value != long || r.Title.Truncated {
		t.Fatal("title silently truncated")
	}
}

func TestSpotifyShortLinkExpansion(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		calls := 0
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				resp := response(302, "")
				resp.Header.Set("Location", "https://open.spotify.com/intl-de/track/ExampleTrack0000000001?si=abc")
				return resp, nil
			}
			return response(200, spotifyPage(t, "spotify-track.html")), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://spotify.link/AbC123xyz"))
		if err != nil || r.Title.Value != "编造的霓虹街" || calls != 2 {
			t.Fatalf("short: %v %+v calls=%d", err, r, calls)
		}
	})
	for _, c := range []struct{ name, target, kind string }{
		{"external", "https://evil.example/track/ExampleTrack0000000001", "unsafe_target"},
		{"show", "https://open.spotify.com/show/ExampleShow00000000001", "unsupported"},
		{"lookalike", "https://open.spotify.com.evil.example/track/ExampleTrack0000000001", "unsafe_target"},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				calls++
				resp := response(302, "")
				resp.Header.Set("Location", c.target)
				return resp, nil
			})
			_, err := f.Fetch(context.Background(), link(t, "https://spotify.link/AbC123xyz"))
			wantError(t, err, c.kind)
			if calls != 1 {
				t.Fatalf("untrusted target connected: %d", calls)
			}
		})
	}
}

func TestSpotifyHostPolicy(t *testing.T) {
	fake := netip.MustParseAddr("198.18.0.9")
	if !allowedWithProxy("open.spotify.com", fake) || !allowedWithProxy("i.scdn.co", fake) || allowedWithProxy("image-cdn-zz.spotifycdn.com", fake) || allowedWithProxy("open.spotify.com.evil.example", fake) {
		t.Fatal("fake-ip exemption must be exact")
	}
	if requestPlatform("https://spotify.link/AbC123xyz") != "spotify" || requestPlatform("https://open.spotify.com/track/x") != "spotify" {
		t.Fatal("spotify requests must use their own rate bucket")
	}
	for host, want := range map[string]bool{"image-cdn-fa.spotifycdn.com": true, "image-cdn-ak.spotifycdn.com": true, "i.scdn.co": true, "image-cdn-zz.spotifycdn.com": false, "p.scdn.co": false} {
		if moreMediaHost(host) != want {
			t.Fatalf("media host %s: want %v", host, want)
		}
		if got := mediaHost("spotify", host); got != want {
			t.Fatalf("download host %s: want %v", host, want)
		}
	}
}

// 单个字段类型不符（时长变小数、宽度变浮点）只让那个字段缺失，整页仍从 embed 取出。
func TestSpotifyScalarTypeMismatchKeepsPage(t *testing.T) {
	page := strings.Replace(spotifyPage(t, "spotify-track.html"), `"duration":200040`, `"duration":200040.5`, 1)
	page = strings.Replace(page, `"maxWidth":640`, `"maxWidth":640.0`, 1)
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, page), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/track/ExampleTrack0000000001"))
	if err != nil {
		t.Fatalf("one bad scalar failed the whole page: %v", err)
	}
	if r.Source != "spotify_embed" || r.Title.Value != "编造的霓虹街" || r.Author.Name.Value != "编造歌手甲" || r.Published.State != "available" {
		t.Fatalf("embed fields lost: source=%s %+v", r.Source, r)
	}
	if len(r.Media.Value) == 0 || r.Media.Value[0].Duration.State == "available" {
		t.Fatalf("mismatched duration must be missing, not invented: %+v", r.Media)
	}
}

// 封面经 inspectMedia：三个精确主机在公网或 fake-ip 下都放行，名单外的 spotifycdn 子域拒绝。
func TestSpotifyCoverThroughInspectMedia(t *testing.T) {
	lookupWith := func(raw string) func(context.Context, string) ([]netip.Addr, error) {
		return func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(raw)}, nil
		}
	}
	for _, c := range []struct {
		host string
		ip   string
		want bool
	}{
		{"image-cdn-fa.spotifycdn.com", "1.1.1.1", true},
		{"image-cdn-ak.spotifycdn.com", "198.18.0.9", true},
		{"i.scdn.co", "198.18.0.9", true},
		{"image-cdn-zz.spotifycdn.com", "1.1.1.1", false},
	} {
		r := Result{Media: available([]Media{{URL: available("https://" + c.host + "/image/cover")}})}
		(&Fetcher{lookup: lookupWith(c.ip), policy: testPolicy(true)}).inspectMedia(context.Background(), &r)
		if got := r.Media.Value[0].URL.State == "available"; got != c.want {
			t.Fatalf("%s via %s: accepted=%v want %v (%+v)", c.host, c.ip, got, c.want, r.Media.Value[0].URL)
		}
	}
}

// 短链跳转指回自己：按第五节的通用规则报 redirect_loop，且只请求到重复的那一跳为止。
func TestSpotifyShortLinkLoopStops(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		calls++
		resp := response(302, "")
		resp.Header.Set("Location", "https://spotify.link/Loop123xyz")
		return resp, nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://spotify.link/AbC123xyz"))
	wantError(t, err, "redirect_loop")
	if calls != 2 {
		t.Fatalf("loop requests=%d want 2", calls)
	}
}

// 单曲和单集的封面在 Media 里只出现一次（以前 audio 的缩略图和 image 条目各写一遍）；
// audio 条目只留时长，不进 Blocks（进了会被渲染成“这一段未能读取”）。
func TestSpotifyCoverOnlyOnce(t *testing.T) {
	for _, c := range []struct{ kind, id, file string }{
		{"track", "ExampleTrack0000000001", "spotify-track.html"},
		{"episode", "ExampleEpisode00000001", "spotify-episode.html"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, spotifyPage(t, c.file)), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://open.spotify.com/"+c.kind+"/"+c.id))
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int{}
			for _, m := range r.Media.Value {
				for _, u := range []Field[string]{m.URL, m.Thumbnail} {
					if u.State == "available" {
						seen[u.Value]++
					}
				}
			}
			if len(seen) != 1 {
				t.Fatalf("应该只有一个封面地址：%v", seen)
			}
			for u, n := range seen {
				if n != 1 {
					t.Fatalf("封面 %s 出现 %d 次", u, n)
				}
			}
			for _, b := range r.Blocks {
				if b.Type == "audio" {
					t.Fatalf("audio 不该进 Blocks：%+v", r.Blocks)
				}
			}
			if r.Media.Value[0].Type != "audio" || r.Media.Value[0].Duration.State != "available" {
				t.Fatalf("时长丢了：%+v", r.Media.Value[0])
			}
		})
	}
}
