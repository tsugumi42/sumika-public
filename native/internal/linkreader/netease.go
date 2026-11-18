package linkreader

import (
	"context"
	"net/url"
	"strings"
	"time"
)

type musicArtist struct {
	ID   decimalID `json:"id"`
	Name *string   `json:"name"`
}
type musicAlbum struct {
	ID          decimalID      `json:"id"`
	Name        *string        `json:"name"`
	Cover       *string        `json:"picUrl"`
	Description *string        `json:"description"`
	Published   *int64         `json:"publishTime"`
	Artists     *[]musicArtist `json:"artists"`
	Artist      *musicArtist   `json:"artist"`
	Size        *int64         `json:"size"`
}
type musicSong struct {
	ID       decimalID      `json:"id"`
	Name     *string        `json:"name"`
	Artists  *[]musicArtist `json:"artists"`
	Album    *musicAlbum    `json:"album"`
	Duration *int64         `json:"duration"`
}
type musicPlaylist struct {
	ID          decimalID `json:"id"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Cover       *string   `json:"coverImgUrl"`
	Created     *int64    `json:"createTime"`
	Tracks      *int64    `json:"trackCount"`
	Plays       *int64    `json:"playCount"`
	Favorites   *int64    `json:"subscribedCount"`
	Tags        *[]string `json:"tags"`
	Creator     *struct {
		Name *string   `json:"nickname"`
		ID   decimalID `json:"userId"`
	} `json:"creator"`
}

func (f *Fetcher) fetchNetease(ctx context.Context, l Link) (Result, error) {
	const source = "netease_api"
	endpoint := ""
	switch l.Kind {
	case "song":
		endpoint = "/api/song/detail/?ids=" + url.QueryEscape("["+l.ID+"]")
	case "album":
		endpoint = "/api/album/" + l.ID
	case "playlist":
		endpoint = "/api/playlist/detail?id=" + l.ID + "&n=0"
	}
	var p struct {
		Code     *int           `json:"code"`
		Songs    []musicSong    `json:"songs"`
		Album    *musicAlbum    `json:"album"`
		Playlist *musicPlaylist `json:"result"`
	}
	if err := f.getJSON(ctx, "https://music.163.com"+endpoint, source, &p); err != nil {
		return Result{}, err
	}
	if p.Code == nil {
		return Result{}, failure("invalid_response", source)
	}
	if *p.Code != 200 {
		kind := "interface_error"
		switch *p.Code {
		case 301, -462:
			kind = "login_required"
		case 404:
			kind = "not_found"
		case 429:
			kind = "rate_limited"
		}
		return Result{}, &FetchError{Kind: kind, Code: *p.Code, Source: source}
	}
	r := newResult(l, source)
	r.Music = &MusicInfo{Artists: missing[[]Author]("interface_omitted"), Album: missing[string]("not_applicable")}
	var cover *string
	switch l.Kind {
	case "song":
		if len(p.Songs) != 1 {
			return Result{}, failure("not_returned", source)
		}
		s := p.Songs[0]
		if string(s.ID) != l.ID {
			return Result{}, failure("identity_mismatch", source)
		}
		r.Title = stringField(s.Name)
		setMusicArtists(&r, s.Artists)
		if s.Album != nil {
			r.Music.Album = stringField(s.Album.Name)
			cover = s.Album.Cover
		}
		duration := missing[float64]("interface_omitted")
		if s.Duration != nil {
			if *s.Duration < 0 {
				return Result{}, failure("invalid_response", source)
			}
			duration = available(float64(*s.Duration) / 1000)
		}
		appendMedia(&r, Media{Type: "audio", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: stringField(cover), Duration: duration, Width: missing[int]("not_applicable"), Height: missing[int]("not_applicable"), Size: missing[int64]("interface_omitted")})
	case "album":
		a := p.Album
		if a == nil {
			return Result{}, failure("invalid_response", source)
		}
		if string(a.ID) != l.ID {
			return Result{}, failure("identity_mismatch", source)
		}
		r.Title, r.Text, cover = stringField(a.Name), previewField(a.Description), a.Cover
		r.Published = millisField(a.Published)
		artists := a.Artists
		if artists == nil && a.Artist != nil {
			one := []musicArtist{*a.Artist}
			artists = &one
		}
		setMusicArtists(&r, artists)
		stat(&r, "tracks", a.Size)
	case "playlist":
		p := p.Playlist
		if p == nil {
			return Result{}, failure("invalid_response", source)
		}
		if string(p.ID) != l.ID {
			return Result{}, failure("identity_mismatch", source)
		}
		r.Title, r.Text, cover = stringField(p.Name), previewField(p.Description), p.Cover
		r.Published = millisField(p.Created)
		if p.Creator != nil {
			r.Author.Name = stringField(p.Creator.Name)
			if p.Creator.ID != "" {
				r.Author.ID = available(string(p.Creator.ID))
			}
		}
		if p.Tags != nil {
			r.Tags = available(*p.Tags)
		}
		stat(&r, "tracks", p.Tracks)
		stat(&r, "plays", p.Plays)
		stat(&r, "favorites", p.Favorites)
	}
	if r.Title.State != "available" || r.Title.Value == "" {
		return Result{}, failure("invalid_response", source)
	}
	appendBody(&r)
	if cover != nil {
		appendMedia(&r, imageMetadata(stringField(cover)))
	}
	return r, nil
}
func millisField(p *int64) Field[time.Time] {
	if p == nil {
		return missing[time.Time]("interface_omitted")
	}
	return available(time.UnixMilli(*p).UTC())
}
func setMusicArtists(r *Result, artists *[]musicArtist) {
	if artists == nil {
		return
	}
	out := make([]Author, 0, len(*artists))
	var names []string
	for _, a := range *artists {
		author := Author{Name: stringField(a.Name), ID: missing[string]("interface_omitted"), Account: missing[string]("not_applicable"), Avatar: missing[string]("interface_omitted")}
		if a.ID != "" {
			author.ID = available(string(a.ID))
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
