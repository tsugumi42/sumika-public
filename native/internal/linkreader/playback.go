package linkreader

import (
	"context"
	"net/url"
	"strconv"
)

// Playback 只返回候选；音画仍由受限流式下载器共用一次预算，不让协议端联网。
type Playback struct{ Video, Audio []Media }

func (f *Fetcher) BiliPlayback(ctx context.Context, r *Result) (Playback, error) {
	if r.Platform != "bilibili" || r.Kind != "video" || r.SelectedPart.State != "available" {
		return Playback{}, failure("playback_unavailable", "bili_play")
	}
	cid := r.SelectedPart.Value.CID
	if n, e := strconv.ParseUint(cid, 10, 64); e != nil || n == 0 {
		return Playback{}, failure("invalid_part", "bili_play")
	}
	q := url.Values{"cid": {cid}, "qn": {"32"}, "fnval": {"1"}, "fourk": {"0"}}
	if len(r.ID) > 2 && r.ID[:2] == "BV" {
		q.Set("bvid", r.ID)
	} else if len(r.ID) > 2 && r.ID[:2] == "av" {
		q.Set("avid", r.ID[2:])
	} else {
		return Playback{}, failure("invalid_id", "bili_play")
	}
	type track struct {
		URL       string `json:"url"`
		BaseURL   string `json:"baseUrl"`
		CodecID   int    `json:"codecid"`
		Bandwidth int64  `json:"bandwidth"`
		Size      int64  `json:"size"`
	}
	type response struct {
		Code int `json:"code"`
		Data struct {
			DURL []track                        `json:"durl"`
			DASH struct{ Video, Audio []track } `json:"dash"`
		} `json:"data"`
	}
	request := func() (response, error) {
		var p response
		e := f.getJSON(ctx, "https://api.bilibili.com/x/player/playurl?"+q.Encode(), "bili_play", &p)
		if e != nil {
			return p, e
		}
		if p.Code != 0 {
			return p, failure("playback_unavailable", "bili_play")
		}
		return p, nil
	}
	p, e := request()
	if e != nil {
		return Playback{}, e
	}
	media := func(t track) Media {
		raw := t.URL
		if raw == "" {
			raw = t.BaseURL
		}
		m := Media{Type: "video", URL: available(raw)}
		if t.Size > 0 {
			m.Size = available(t.Size)
		}
		return m
	}
	var out Playback
	for _, t := range p.Data.DURL {
		if t.URL != "" {
			out.Video = append(out.Video, media(t))
		}
	}
	if len(out.Video) > 0 {
		return out, nil
	}
	q.Set("fnval", "16")
	p, e = request()
	if e != nil {
		return Playback{}, e
	}
	// 匿名默认清晰度内优先 AVC；同清晰度不把备用音轨也下载一遍。
	var selected *track
	for i := range p.Data.DASH.Video {
		t := &p.Data.DASH.Video[i]
		if t.CodecID == 7 && (selected == nil || t.Bandwidth < selected.Bandwidth) {
			selected = t
		}
	}
	if selected == nil {
		return Playback{}, failure("playback_unavailable", "bili_play")
	}
	out.Video = []Media{media(*selected)}
	selected = nil
	for i := range p.Data.DASH.Audio {
		t := &p.Data.DASH.Audio[i]
		if selected == nil || t.Bandwidth < selected.Bandwidth {
			selected = t
		}
	}
	if selected != nil {
		out.Audio = []Media{media(*selected)}
	}
	return out, nil
}
