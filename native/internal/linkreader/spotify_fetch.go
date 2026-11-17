package linkreader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// 规格-链接解析 4.13：只读 embed 页的 __NEXT_DATA__，不调 api.spotify.com，不用页面里的匿名 token。
type spotifyImage struct {
	URL   *string         `json:"url"`
	Width json.RawMessage `json:"maxWidth"`
}
type spotifyArtist struct {
	Name *string `json:"name"`
	URI  *string `json:"uri"`
}
type spotifyEntity struct {
	Type        *string          `json:"type"`
	ID          *string          `json:"id"`
	Name        *string          `json:"name"`
	Subtitle    *string          `json:"subtitle"`
	Artists     *[]spotifyArtist `json:"artists"`
	ReleaseDate *struct {
		ISO *string `json:"isoString"`
	} `json:"releaseDate"`
	Duration       json.RawMessage `json:"duration"`
	IsExplicit     json.RawMessage `json:"isExplicit"`
	VisualIdentity *struct {
		Image *[]spotifyImage `json:"image"`
	} `json:"visualIdentity"`
	CoverArt *struct {
		Sources *[]spotifyImage `json:"sources"`
	} `json:"coverArt"`
}
type spotifyNext struct {
	Props struct {
		PageProps struct {
			ForbiddenReason *string `json:"forbiddenReason"`
			State           *struct {
				Data *struct {
					Entity *spotifyEntity `json:"entity"`
				} `json:"data"`
			} `json:"state"`
		} `json:"pageProps"`
	} `json:"props"`
}

var (
	spotifyNextTag   = regexp.MustCompile(`<script id="__NEXT_DATA__"[^>]*>`)
	spotifyArtistURI = regexp.MustCompile(`^spotify:artist:([A-Za-z0-9]{22})$`)
)

const spotifyRegionReason = "NOT_AVAILABLE_IN_MARKET"

func (f *Fetcher) fetchSpotify(ctx context.Context, l Link) (Result, error) {
	const source = "spotify_embed"
	if l.Kind == "show" {
		// embed 对 show 给的是最新一集，不能当节目信息。
		return Result{}, failure("unsupported", source)
	}
	e, err := f.spotifyEmbed(ctx, l)
	if err != nil {
		var fe *FetchError
		if !errors.As(err, &fe) || fe.Kind != "invalid_response" {
			return Result{}, err
		}
		// 只有页面结构变了才走 oEmbed；oEmbed 也失败时返回 embed 的原错误。
		r, oerr := f.spotifyOEmbed(ctx, l)
		if oerr != nil {
			return Result{}, err
		}
		return r, nil
	}
	return spotifyResult(l, e)
}

func (f *Fetcher) spotifyEmbed(ctx context.Context, l Link) (spotifyEntity, error) {
	const source = "spotify_embed"
	page, status, h, err := f.request(ctx, "https://open.spotify.com/embed/"+l.Kind+"/"+l.ID, source)
	if err != nil {
		return spotifyEntity{}, err
	}
	if status == 403 && bytes.Contains(page, []byte(spotifyRegionReason)) {
		return spotifyEntity{}, &FetchError{Kind: "not_returned", Reason: "not_available_in_market", Status: status, Source: source}
	}
	if err = httpFailure(status, h, source); err != nil {
		return spotifyEntity{}, err
	}
	var next spotifyNext
	raw := spotifyNextJSON(page)
	if raw == nil {
		return spotifyEntity{}, failure("invalid_response", source)
	}
	if err = json.Unmarshal(raw, &next); err != nil {
		// 只有语法错误才算结构坏了；类型不符时 encoding/json 会继续解其余字段，那一个字段留空即可。
		var ute *json.UnmarshalTypeError
		if !errors.As(err, &ute) {
			return spotifyEntity{}, failure("invalid_response", source)
		}
	}
	pp := next.Props.PageProps
	if pp.ForbiddenReason != nil && *pp.ForbiddenReason == spotifyRegionReason {
		return spotifyEntity{}, &FetchError{Kind: "not_returned", Reason: "not_available_in_market", Status: status, Source: source}
	}
	if pp.State == nil || pp.State.Data == nil || pp.State.Data.Entity == nil {
		return spotifyEntity{}, failure("invalid_response", source)
	}
	e := *pp.State.Data.Entity
	if e.ID == nil || e.Type == nil {
		return spotifyEntity{}, failure("invalid_response", source)
	}
	if *e.ID != l.ID || *e.Type != l.Kind {
		return spotifyEntity{}, failure("identity_mismatch", source)
	}
	return e, nil
}

// spotifyNextJSON 取 __NEXT_DATA__ 标签后到 </script> 之前的 JSON 文本。
func spotifyNextJSON(page []byte) []byte {
	loc := spotifyNextTag.FindIndex(page)
	if loc == nil {
		return nil
	}
	rest := page[loc[1]:]
	end := bytes.Index(rest, []byte("</script>"))
	if end < 0 {
		return nil
	}
	return rest[:end]
}

func spotifyResult(l Link, e spotifyEntity) (Result, error) {
	const source = "spotify_embed"
	r := newResult(l, source)
	r.Title = stringField(e.Name)
	if r.Title.State != "available" || r.Title.Value == "" {
		return Result{}, failure("invalid_response", source)
	}
	if e.ReleaseDate != nil && e.ReleaseDate.ISO != nil {
		published, err := timestampField(e.ReleaseDate.ISO)
		if err != nil {
			return Result{}, failure("invalid_response", source)
		}
		r.Published = published
	}
	cover := spotifyCover(e)
	switch l.Kind {
	case "track":
		r.Music = &MusicInfo{Artists: missing[[]Author]("interface_omitted"), Album: missing[string]("interface_omitted")}
		setSpotifyArtists(&r, e.Artists)
		flag(&r, "explicit", spotifyFlag(e.IsExplicit))
		appendSpotifyAudio(&r, e)
	case "album":
		// embed 只给显示名（subtitle），没有稳定 ID，不能当 ID 用。
		r.Music = &MusicInfo{Album: missing[string]("not_applicable")}
		if e.Subtitle != nil && *e.Subtitle != "" {
			r.Author.Name = available(*e.Subtitle)
			r.Music.Artists = available([]Author{{Name: available(*e.Subtitle), ID: missing[string]("interface_omitted"), Account: missing[string]("not_applicable"), Avatar: missing[string]("interface_omitted")}})
		}
		flag(&r, "explicit", spotifyFlag(e.IsExplicit))
	case "playlist":
		// 歌单的 authors 在匿名 embed 里是 null，不推断作者；trackList 不作为曲目总数。
	case "artist":
		// 艺人只取名字，热门曲目和统计都不映射。
	case "episode":
		if e.Subtitle != nil && *e.Subtitle != "" {
			r.Author.Name = available(*e.Subtitle)
		}
		flag(&r, "explicit", spotifyFlag(e.IsExplicit))
		appendSpotifyAudio(&r, e)
	}
	if cover != nil {
		appendMedia(&r, imageMetadata(stringField(cover)))
	}
	return r, nil
}

// setSpotifyArtists 与 netease 的规则一致：逐个保留身份，只有单人时 Author.ID 才复用。
func setSpotifyArtists(r *Result, artists *[]spotifyArtist) {
	if artists == nil {
		return
	}
	out := make([]Author, 0, len(*artists))
	var names []string
	for _, a := range *artists {
		author := Author{Name: stringField(a.Name), ID: missing[string]("interface_omitted"), Account: missing[string]("not_applicable"), Avatar: missing[string]("interface_omitted")}
		if a.URI != nil {
			if m := spotifyArtistURI.FindStringSubmatch(*a.URI); m != nil {
				author.ID = available(m[1])
			}
		}
		out = append(out, author)
		if a.Name != nil {
			names = append(names, *a.Name)
		}
	}
	r.Music.Artists = available(out)
	if len(names) > 0 {
		r.Author.Name = available(strings.Join(names, " / "))
	}
	if len(out) == 1 {
		r.Author.ID = out[0].ID
	}
}

// appendSpotifyAudio 只登记时长（Media 里的 audio 条目），不取试听地址、不带封面：
// 封面只在后面单独的 image 条目里出现一次，不在 audio 的缩略图里再写一遍；
// audio 也不进 Blocks（它没有可展示的内容，进了会被渲染成“这一段未能读取”）。
func appendSpotifyAudio(r *Result, e spotifyEntity) {
	duration := missing[float64]("interface_omitted")
	if d := spotifyScalar[int64](e.Duration); d != nil && *d >= 0 {
		duration = available(float64(*d) / 1000)
	}
	if r.Media.State != "available" {
		r.Media = available([]Media{})
	}
	r.Media.Value = append(r.Media.Value, Media{Type: "audio", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: missing[string]("same_as_cover_image"), Duration: duration, Width: missing[int]("not_applicable"), Height: missing[int]("not_applicable"), Size: missing[int64]("interface_omitted")})
}

// spotifyScalar 把一个标量字段按自己的类型解码；缺失、null 或类型不符都返回 nil，不借 encoding/json 的零值。
func spotifyScalar[T int | int64 | bool](raw json.RawMessage) *T {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func spotifyFlag(raw json.RawMessage) *bool { return spotifyScalar[bool](raw) }

// spotifyCover 取最宽的 visualIdentity 图；歌单的 coverArt 宽高为 null，只作备选。
func spotifyCover(e spotifyEntity) *string {
	best, bestWidth := "", -1
	if e.VisualIdentity != nil && e.VisualIdentity.Image != nil {
		for _, img := range *e.VisualIdentity.Image {
			if img.URL == nil || *img.URL == "" {
				continue
			}
			w := 0
			if p := spotifyScalar[int](img.Width); p != nil {
				w = *p
			}
			if w > bestWidth {
				best, bestWidth = *img.URL, w
			}
		}
	}
	if best == "" && e.CoverArt != nil && e.CoverArt.Sources != nil {
		for _, img := range *e.CoverArt.Sources {
			if img.URL != nil && *img.URL != "" {
				best = *img.URL
				break
			}
		}
	}
	if best == "" {
		return nil
	}
	return &best
}

// spotifyOEmbed 只在 embed 结构变化时兜底，只映射标题和缩略图。
func (f *Fetcher) spotifyOEmbed(ctx context.Context, l Link) (Result, error) {
	const source = "spotify_oembed"
	var o struct {
		Title *string `json:"title"`
		Thumb *string `json:"thumbnail_url"`
	}
	if err := f.getJSON(ctx, "https://open.spotify.com/oembed?url="+url.QueryEscape(l.Clean), source, &o); err != nil {
		return Result{}, err
	}
	r := newResult(l, source)
	r.Title = stringField(o.Title)
	if r.Title.State != "available" || r.Title.Value == "" {
		return Result{}, failure("invalid_response", source)
	}
	if o.Thumb != nil && *o.Thumb != "" {
		appendMedia(&r, imageMetadata(stringField(o.Thumb)))
	}
	return r, nil
}
