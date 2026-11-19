package linkreader

import (
	"context"
	"strconv"
)

type youtubePlayer struct {
	Details *struct {
		ID          string     `json:"videoId"`
		Title       *string    `json:"title"`
		Author      *string    `json:"author"`
		Channel     *string    `json:"channelId"`
		Description *string    `json:"shortDescription"`
		Duration    *decimalID `json:"lengthSeconds"`
		Views       *decimalID `json:"viewCount"`
		Thumbnail   struct {
			Images []struct {
				URL    string `json:"url"`
				Width  *int   `json:"width"`
				Height *int   `json:"height"`
			} `json:"thumbnails"`
		} `json:"thumbnail"`
	} `json:"videoDetails"`
	Microformat struct {
		Player struct {
			Published *string `json:"publishDate"`
			Uploaded  *string `json:"uploadDate"`
			// 点赞数在 microformat 里（作者关了点赞显示时不给）
			Likes *decimalID `json:"likeCount"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
	Playability struct {
		Status string `json:"status"`
	} `json:"playabilityStatus"`
}

func (f *Fetcher) fetchYouTube(ctx context.Context, l Link) (Result, error) {
	const source = "youtube_watch"
	b, status, h, err := f.request(ctx, "https://www.youtube.com/watch?v="+l.ID+"&hl=en", source)
	if err != nil {
		return Result{}, err
	}
	if err := httpFailure(status, h, source); err != nil {
		return Result{}, err
	}
	var p youtubePlayer
	if !pageJSON(b, "ytInitialPlayerResponse", &p) {
		return Result{}, failure("invalid_response", source)
	}
	if p.Details == nil {
		if p.Playability.Status == "LOGIN_REQUIRED" {
			return Result{}, failure("login_required", source)
		}
		return Result{}, failure("not_returned", source)
	}
	d := p.Details
	if d.ID != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	if d.Title == nil || *d.Title == "" {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title, r.Text = stringField(d.Title), previewField(d.Description)
	r.Author.Name, r.Author.ID = stringField(d.Author), stringField(d.Channel)
	date := p.Microformat.Player.Published
	if date == nil {
		date = p.Microformat.Player.Uploaded
	}
	r.Published, err = timestampField(date)
	if err != nil {
		return Result{}, err
	}
	stat(&r, "views", nil)
	stat(&r, "likes", nil)
	if d.Views != nil {
		count, err := strconv.ParseInt(string(*d.Views), 10, 64)
		if err != nil {
			return Result{}, failure("invalid_response", source)
		}
		stat(&r, "views", &count)
	}
	if v := p.Microformat.Player.Likes; v != nil {
		// 格式不对只当没给，不让整条取数失败
		if count, err := strconv.ParseInt(string(*v), 10, 64); err == nil && count >= 0 {
			stat(&r, "likes", &count)
		}
	}
	appendBody(&r)
	m := Media{Type: "video", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: missing[string]("interface_omitted"), Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"), Duration: missing[float64]("interface_omitted"), Size: missing[int64]("interface_omitted")}
	if d.Duration != nil {
		duration, err := strconv.ParseInt(string(*d.Duration), 10, 64)
		if err != nil {
			return Result{}, failure("invalid_response", source)
		}
		m.Duration = available(float64(duration))
	}
	// 仅取真实返回的最大缩略图；缩略图尺寸不当作视频尺寸。
	best := -1
	area := -1
	for i, pic := range d.Thumbnail.Images {
		current := 0
		if pic.Width != nil && pic.Height != nil {
			current = *pic.Width * *pic.Height
		}
		if pic.URL != "" && current > area {
			best, area = i, current
		}
	}
	if best >= 0 {
		pic := d.Thumbnail.Images[best]
		m.Thumbnail = available(pic.URL)
		image := imageMetadata(available(pic.URL))
		image.Width, image.Height = intField(pic.Width), intField(pic.Height)
		appendMedia(&r, image)
	}
	appendMedia(&r, m)
	return r, nil
}
