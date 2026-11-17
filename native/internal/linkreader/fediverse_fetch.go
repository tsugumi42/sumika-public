package linkreader

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Misskey 和 Mastodon 的公开接口只用匿名请求；字段按规格 4.8 映射。
// 缺失与零分开：JSON 没给的字段是 not_returned，给了 0 是 available。

const (
	fediMisskeyHost  = "misskey.io"
	fediMastodonHost = "mastodon.social"
)

var (
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>|</p>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
)

type misskeyUser struct {
	ID             *string `json:"id"`
	Username       *string `json:"username"`
	Host           *string `json:"host"`
	Name           *string `json:"name"`
	AvatarURL      *string `json:"avatarUrl"`
	Description    *string `json:"description"`
	NotesCount     *int64  `json:"notesCount"`
	FollowersCount *int64  `json:"followersCount"`
	FollowingCount *int64  `json:"followingCount"`
	CreatedAt      *string `json:"createdAt"`
}

type misskeyFile struct {
	Type       *string `json:"type"`
	URL        *string `json:"url"`
	Thumbnail  *string `json:"thumbnailUrl"`
	Size       *int64  `json:"size"`
	Sensitive  *bool   `json:"isSensitive"`
	Properties struct {
		Width  *int `json:"width"`
		Height *int `json:"height"`
	} `json:"properties"`
}

type misskeyPoll struct {
	Choices []struct {
		Text  *string `json:"text"`
		Votes *int64  `json:"votes"`
	} `json:"choices"`
	Multiple  *bool   `json:"multiple"`
	ExpiresAt *string `json:"expiresAt"`
}

type misskeyNote struct {
	ID            *string          `json:"id"`
	CreatedAt     *string          `json:"createdAt"`
	Text          *string          `json:"text"`
	CW            *string          `json:"cw"`
	User          *misskeyUser     `json:"user"`
	Files         *[]misskeyFile   `json:"files"`
	Poll          *misskeyPoll     `json:"poll"`
	RenoteID      *string          `json:"renoteId"`
	Renote        *misskeyNote     `json:"renote"`
	RenoteCount   *int64           `json:"renoteCount"`
	RepliesCount  *int64           `json:"repliesCount"`
	ReactionCount *int64           `json:"reactionCount"`
	Reactions     map[string]int64 `json:"reactions"`
}

type mastodonAccount struct {
	ID             *string `json:"id"`
	Username       *string `json:"username"`
	Acct           *string `json:"acct"`
	DisplayName    *string `json:"display_name"`
	Avatar         *string `json:"avatar"`
	Note           *string `json:"note"`
	CreatedAt      *string `json:"created_at"`
	StatusesCount  *int64  `json:"statuses_count"`
	FollowersCount *int64  `json:"followers_count"`
	FollowingCount *int64  `json:"following_count"`
}

type mastodonMedia struct {
	Type       *string `json:"type"`
	URL        *string `json:"url"`
	PreviewURL *string `json:"preview_url"`
	Meta       struct {
		Original struct {
			Width    *int     `json:"width"`
			Height   *int     `json:"height"`
			Duration *float64 `json:"duration"`
		} `json:"original"`
	} `json:"meta"`
}

type mastodonPoll struct {
	Options []struct {
		Title *string `json:"title"`
		Votes *int64  `json:"votes_count"`
	} `json:"options"`
	Multiple  *bool   `json:"multiple"`
	ExpiresAt *string `json:"expires_at"`
}

type mastodonCard struct {
	URL          *string `json:"url"`
	Title        *string `json:"title"`
	Description  *string `json:"description"`
	ProviderName *string `json:"provider_name"`
	Image        *string `json:"image"`
}

type mastodonStatus struct {
	ID               *string          `json:"id"`
	CreatedAt        *string          `json:"created_at"`
	Content          *string          `json:"content"`
	SpoilerText      *string          `json:"spoiler_text"`
	Sensitive        *bool            `json:"sensitive"`
	Visibility       *string          `json:"visibility"`
	Account          *mastodonAccount `json:"account"`
	MediaAttachments *[]mastodonMedia `json:"media_attachments"`
	Poll             *mastodonPoll    `json:"poll"`
	Card             *mastodonCard    `json:"card"`
	Reblog           *mastodonStatus  `json:"reblog"`
	FavouritesCount  *int64           `json:"favourites_count"`
	ReblogsCount     *int64           `json:"reblogs_count"`
	RepliesCount     *int64           `json:"replies_count"`
}

func (f *Fetcher) fetchFediverse(ctx context.Context, l Link) (Result, error) {
	host, key, ok := strings.Cut(l.ID, "/")
	if !ok || key == "" {
		return Result{}, failure("invalid_target", "fediverse")
	}
	switch l.Platform {
	case "misskey":
		return f.fetchMisskey(ctx, l, host, key)
	case "mastodon":
		return f.fetchMastodon(ctx, l, host, key)
	}
	return Result{}, failure("unsupported", l.Platform)
}

// ---- Misskey ----

func (f *Fetcher) fetchMisskey(ctx context.Context, l Link, host, key string) (Result, error) {
	const source = "misskey_api"
	if host != fediMisskeyHost {
		return Result{}, failure("invalid_target", source)
	}
	if l.Kind == "note" {
		var n misskeyNote
		if err := f.postJSON(ctx, "https://"+host+"/api/notes/show", source, map[string]string{"noteId": key}, &n); err != nil {
			return Result{}, err
		}
		if n.ID == nil || *n.ID != key {
			return Result{}, failure("identity_mismatch", source)
		}
		return misskeyNoteResult(host, n, source, false)
	}
	// 用户：请求用 Clean 里的原始大小写，ID 里的小写名只用于缓存键。
	name := fediNameFromClean(l.Clean)
	if name == "" {
		return Result{}, failure("invalid_target", source)
	}
	var u misskeyUser
	if err := f.postJSON(ctx, "https://"+host+"/api/users/show", source, map[string]any{"username": name, "host": nil}, &u); err != nil {
		return Result{}, err
	}
	if u.Username == nil || !strings.EqualFold(*u.Username, name) || (u.Host != nil && *u.Host != "") {
		return Result{}, failure("identity_mismatch", source)
	}
	r := newResult(l, source)
	r.Author = misskeyAuthor(u)
	r.Title = available(r.Author.Name.Value)
	var err error
	if r.Published, err = timestampField(u.CreatedAt); err != nil {
		return Result{}, err
	}
	if u.Description != nil {
		desc := misskeyPlain(*u.Description)
		r.Text = fediText(&desc)
	} else {
		r.Text = fediText(nil)
	}
	stat(&r, "notes", u.NotesCount)
	stat(&r, "followers", u.FollowersCount)
	stat(&r, "following", u.FollowingCount)
	appendBody(&r)
	return r, nil
}

func (f *Fetcher) postJSON(ctx context.Context, raw, source string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return failure("invalid_target", source)
	}
	b, status, h, err := f.send(ctx, http.MethodPost, raw, source, body, "application/json")
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return misskeyFailure(status, b, h, source)
	}
	if json.Unmarshal(b, out) != nil {
		return failure("invalid_response", source)
	}
	return nil
}

// misskeyFailure 只认平台的几个固定错误码，其余按 HTTP 状态映射；错误码不进入任何用户可见文字。
func misskeyFailure(status int, body []byte, h http.Header, source string) error {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch e.Error.Code {
		case "NO_SUCH_NOTE", "NO_SUCH_USER":
			return &FetchError{Kind: "not_found", Source: source, Status: status, Reason: e.Error.Code}
		case "ACCESS_DENIED":
			return &FetchError{Kind: "login_required", Source: source, Status: status, Reason: e.Error.Code}
		case "RATE_LIMIT_EXCEEDED":
			return &FetchError{Kind: "rate_limited", Source: source, Status: status, Reason: e.Error.Code}
		}
	}
	return httpFailure(status, h, source)
}

func misskeyAuthor(u misskeyUser) Author {
	username := ""
	if u.Username != nil {
		username = *u.Username
	}
	name := username
	if u.Name != nil && *u.Name != "" {
		name = *u.Name
	}
	account := username
	if u.Host != nil && *u.Host != "" {
		account += "@" + *u.Host
	}
	a := Author{Name: available(name), Account: available(account), ID: missing[string]("interface_omitted"), Avatar: fediString(u.AvatarURL)}
	if u.ID != nil && *u.ID != "" {
		a.ID = available(*u.ID)
	}
	return a
}

func misskeyNoteResult(host string, n misskeyNote, source string, nested bool) (Result, error) {
	if n.ID == nil || *n.ID == "" {
		return Result{}, failure("invalid_response", source)
	}
	id := *n.ID
	l := Link{Platform: "misskey", Kind: "note", ID: host + "/" + id, Clean: "https://" + host + "/notes/" + id}
	r := newResult(l, source)
	var err error
	if r.Published, err = timestampField(n.CreatedAt); err != nil {
		return Result{}, err
	}
	if n.User == nil {
		return Result{}, failure("invalid_response", source)
	}
	r.Author = misskeyAuthor(*n.User)
	// Misskey 没有标题字段；不拿正文首行冒充标题。
	r.Title = missing[string]("platform_has_no_title")

	var parts []string
	if n.Text != nil && *n.Text != "" {
		parts = append(parts, misskeyPlain(*n.Text))
	}
	if n.Poll != nil {
		parts = append(parts, misskeyPollText(*n.Poll))
	}
	if len(parts) > 0 {
		joined := strings.Join(parts, "\n")
		r.Text = previewField(&joined)
	} else {
		r.Text = Field[string]{State: "not_returned", Reason: "empty_text"}
	}
	if n.Poll != nil && n.Poll.ExpiresAt != nil {
		r.Flags["poll_expires_at"] = available[jsonValue](*n.Poll.ExpiresAt)
	}

	cw := ""
	if n.CW != nil {
		cw = *n.CW
	}
	r.Flags["content_warning"] = available[jsonValue](cw)

	if n.Files == nil {
		r.Media = missing[[]Media]("interface_omitted")
	} else {
		unsupported := 0
		anySensitive, allKnown := false, true
		media := []Media{}
		for _, file := range *n.Files {
			m, ok := misskeyMedia(file)
			if !ok {
				unsupported++
				continue
			}
			if file.Sensitive == nil {
				allKnown = false
			} else if *file.Sensitive {
				anySensitive = true
			}
			media = append(media, m)
		}
		r.Media = available(media)
		switch {
		case anySensitive:
			r.Flags["sensitive"] = available[jsonValue](true)
		case allKnown && len(*n.Files) > 0:
			r.Flags["sensitive"] = available[jsonValue](false)
		default:
			r.Flags["sensitive"] = missing[jsonValue]("interface_omitted")
		}
		if unsupported > 0 {
			r.Flags["unsupported_files"] = available[jsonValue](int64(unsupported))
		}
	}

	var quoted *Result
	if n.Renote != nil {
		if nested {
			r.Quote = missing[*Result]("not_applicable")
		} else {
			q, qerr := misskeyNoteResult(host, *n.Renote, source, true)
			switch {
			case qerr != nil:
				r.Quote = Field[*Result]{State: "failed", Reason: "invalid_response"}
			case n.RenoteID != nil && (n.Renote.ID == nil || *n.Renote.ID != *n.RenoteID):
				r.Quote = Field[*Result]{State: "failed", Reason: "identity_mismatch"}
			default:
				r.Quote = available(&q)
				quoted = &q
			}
		}
	} else if n.RenoteID != nil && *n.RenoteID != "" {
		r.Quote = Field[*Result]{State: "not_returned", Reason: "renote_omitted"}
	}
	if nested {
		r.MoreQuotes = available(n.Renote != nil)
	} else {
		r.MoreQuotes = available(n.Renote != nil && n.Renote.Renote != nil)
	}

	stat(&r, "renotes", n.RenoteCount)
	stat(&r, "replies", n.RepliesCount)
	reactions := n.ReactionCount
	if reactions == nil && n.Reactions != nil {
		var sum int64
		for _, v := range n.Reactions {
			sum += v
		}
		reactions = &sum
	}
	stat(&r, "reactions", reactions)

	// 正文块在前，媒体块在后；指针在 Media 定稿以后再取，避免切片扩容后指向旧数组。
	appendBody(&r)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
	if quoted != nil {
		r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: quoted})
	}
	return r, nil
}

func misskeyPollText(p misskeyPoll) string {
	var opts []string
	for _, c := range p.Choices {
		text := ""
		if c.Text != nil {
			text = *c.Text
		}
		if c.Votes != nil {
			text += "（" + itoa64(*c.Votes) + " 票）"
		}
		opts = append(opts, text)
	}
	head := "投票"
	if p.Multiple != nil && *p.Multiple {
		head = "多选投票"
	}
	return head + "：" + strings.Join(opts, "、")
}

func misskeyMedia(f misskeyFile) (Media, bool) {
	mime := ""
	if f.Type != nil {
		mime = strings.ToLower(*f.Type)
	}
	var typ string
	switch {
	case mime == "image/gif":
		typ = "gif"
	case strings.HasPrefix(mime, "image/"):
		typ = "image"
	case strings.HasPrefix(mime, "video/"):
		typ = "video"
	case strings.HasPrefix(mime, "audio/"):
		typ = "audio"
	default:
		return Media{}, false
	}
	m := Media{
		Type:      typ,
		URL:       fediString(f.URL),
		Thumbnail: fediString(f.Thumbnail),
		Width:     intField(f.Properties.Width),
		Height:    intField(f.Properties.Height),
		Duration:  missing[float64]("interface_omitted"),
		Size:      missing[int64]("interface_omitted"),
		Variants:  missing[[]MediaVariant]("not_applicable"),
		Sensitive: missing[bool]("interface_omitted"),
	}
	if f.Size != nil {
		m.Size = available(*f.Size)
	}
	if f.Sensitive != nil {
		m.Sensitive = available(*f.Sensitive)
	}
	return m, true
}

// ---- Mastodon ----

func (f *Fetcher) fetchMastodon(ctx context.Context, l Link, host, key string) (Result, error) {
	const source = "mastodon_api"
	if host != fediMastodonHost {
		return Result{}, failure("invalid_target", source)
	}
	name := fediNameFromClean(l.Clean)
	if name == "" {
		return Result{}, failure("invalid_target", source)
	}
	if l.Kind == "status" {
		var s mastodonStatus
		if err := f.getJSON(ctx, "https://"+host+"/api/v1/statuses/"+url.PathEscape(key), source, &s); err != nil {
			return Result{}, err
		}
		if s.ID == nil || *s.ID != key || s.Account == nil || s.Account.Username == nil || !strings.EqualFold(*s.Account.Username, name) {
			return Result{}, failure("identity_mismatch", source)
		}
		return mastodonStatusResult(l, s, source, false)
	}
	var a mastodonAccount
	if err := f.getJSON(ctx, "https://"+host+"/api/v1/accounts/lookup?acct="+url.QueryEscape(name), source, &a); err != nil {
		return Result{}, err
	}
	if a.Username == nil || !strings.EqualFold(*a.Username, name) || a.Acct == nil || *a.Acct != *a.Username {
		return Result{}, failure("identity_mismatch", source)
	}
	r := newResult(l, source)
	r.Author = mastodonAuthor(a)
	r.Title = available(r.Author.Name.Value)
	var err error
	if r.Published, err = timestampField(a.CreatedAt); err != nil {
		return Result{}, err
	}
	note := mastodonTextPtr(a.Note)
	r.Text = fediText(&note)
	stat(&r, "statuses", a.StatusesCount)
	stat(&r, "followers", a.FollowersCount)
	stat(&r, "following", a.FollowingCount)
	appendBody(&r)
	return r, nil
}

func mastodonStatusResult(l Link, s mastodonStatus, source string, nested bool) (Result, error) {
	if s.ID == nil || *s.ID == "" || s.Account == nil {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	var err error
	if r.Published, err = timestampField(s.CreatedAt); err != nil {
		return Result{}, err
	}
	r.Author = mastodonAuthor(*s.Account)
	// Mastodon 也没有标题；转发的外层没有正文，内容在 Quote 里。
	r.Title = missing[string]("platform_has_no_title")

	var parts []string
	if t := mastodonTextPtr(s.Content); t != "" {
		parts = append(parts, t)
	}
	if s.Poll != nil {
		parts = append(parts, mastodonPollText(*s.Poll))
	}
	if len(parts) > 0 {
		joined := strings.Join(parts, "\n")
		r.Text = previewField(&joined)
	} else {
		r.Text = Field[string]{State: "not_returned", Reason: "empty_text"}
	}
	if s.Poll != nil && s.Poll.ExpiresAt != nil {
		r.Flags["poll_expires_at"] = available[jsonValue](*s.Poll.ExpiresAt)
	}

	spoiler := ""
	if s.SpoilerText != nil {
		spoiler = *s.SpoilerText
	}
	r.Flags["content_warning"] = available[jsonValue](spoiler)
	if s.Sensitive != nil {
		r.Flags["sensitive"] = available[jsonValue](*s.Sensitive)
	} else {
		r.Flags["sensitive"] = missing[jsonValue]("interface_omitted")
	}
	if s.Visibility != nil {
		r.Flags["visibility"] = available[jsonValue](*s.Visibility)
	}

	r.Media = missing[[]Media]("interface_omitted")
	if s.MediaAttachments != nil {
		unsupported := 0
		media := []Media{}
		for _, a := range *s.MediaAttachments {
			m, ok := mastodonMediaOf(a, s.Sensitive)
			if !ok {
				unsupported++
				continue
			}
			media = append(media, m)
		}
		r.Media = available(media)
		if unsupported > 0 {
			r.Flags["unsupported_files"] = available[jsonValue](int64(unsupported))
		}
	}

	r.LinkCard = missing[LinkPreview]("no_card")
	if s.Card != nil && s.Card.URL != nil && *s.Card.URL != "" {
		img := missing[Media]("interface_omitted")
		if s.Card.Image != nil && *s.Card.Image != "" {
			img = available(imageMetadata(available(*s.Card.Image)))
		}
		r.LinkCard = available(LinkPreview{
			URL:         available(*s.Card.URL),
			Title:       fediString(s.Card.Title),
			Description: fediString(s.Card.Description),
			Site:        fediString(s.Card.ProviderName),
			Image:       img,
		})
	}

	var quoted *Result
	if s.Reblog != nil {
		if nested {
			r.Quote = missing[*Result]("not_applicable")
		} else {
			rb := s.Reblog
			if rb.ID == nil || rb.Account == nil || rb.Account.Username == nil {
				r.Quote = Field[*Result]{State: "failed", Reason: "invalid_response"}
			} else {
				ql := Link{Platform: "mastodon", Kind: "status", ID: fediMastodonHost + "/" + *rb.ID, Clean: "https://" + fediMastodonHost + "/@" + *rb.Account.Username + "/" + *rb.ID}
				q, qerr := mastodonStatusResult(ql, *rb, source, true)
				if qerr != nil {
					r.Quote = Field[*Result]{State: "failed", Reason: "invalid_response"}
				} else {
					r.Quote = available(&q)
					quoted = &q
				}
			}
		}
	}
	if nested {
		r.MoreQuotes = available(s.Reblog != nil)
	} else {
		r.MoreQuotes = available(s.Reblog != nil && s.Reblog.Reblog != nil)
	}

	stat(&r, "favourites", s.FavouritesCount)
	stat(&r, "reblogs", s.ReblogsCount)
	stat(&r, "replies", s.RepliesCount)

	appendBody(&r)
	appendLinkCardBlock(&r)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
	if quoted != nil {
		r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: quoted})
	}
	return r, nil
}

func mastodonMediaOf(a mastodonMedia, sensitive *bool) (Media, bool) {
	typ := ""
	if a.Type != nil {
		switch *a.Type {
		case "image":
			typ = "image"
		case "gifv":
			typ = "gif"
		case "video":
			typ = "video"
		case "audio":
			typ = "audio"
		}
	}
	if typ == "" {
		return Media{}, false
	}
	m := Media{
		Type:      typ,
		URL:       fediString(a.URL),
		Thumbnail: fediString(a.PreviewURL),
		Width:     intField(a.Meta.Original.Width),
		Height:    intField(a.Meta.Original.Height),
		Duration:  floatField(a.Meta.Original.Duration),
		Size:      missing[int64]("interface_omitted"),
		Variants:  missing[[]MediaVariant]("not_applicable"),
		Sensitive: missing[bool]("interface_omitted"),
	}
	if sensitive != nil {
		m.Sensitive = available(*sensitive)
	}
	return m, true
}

func mastodonPollText(p mastodonPoll) string {
	var opts []string
	for _, o := range p.Options {
		text := ""
		if o.Title != nil {
			text = *o.Title
		}
		if o.Votes != nil {
			text += "（" + itoa64(*o.Votes) + " 票）"
		}
		opts = append(opts, text)
	}
	head := "投票"
	if p.Multiple != nil && *p.Multiple {
		head = "多选投票"
	}
	return head + "：" + strings.Join(opts, "、")
}

func mastodonAuthor(a mastodonAccount) Author {
	username := ""
	if a.Username != nil {
		username = *a.Username
	}
	name := username
	if a.DisplayName != nil && *a.DisplayName != "" {
		name = *a.DisplayName
	}
	account := username
	if a.Acct != nil && *a.Acct != "" {
		account = *a.Acct
	}
	out := Author{Name: available(name), Account: available(account), ID: missing[string]("interface_omitted"), Avatar: fediString(a.Avatar)}
	if a.ID != nil && *a.ID != "" {
		out.ID = available(*a.ID)
	}
	return out
}

// mastodonText 把 HTML 正文转成纯文本：换行标签保留为换行，其余标签去掉，再解码实体。
func mastodonText(s string) string {
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func mastodonTextPtr(p *string) string {
	if p == nil {
		return ""
	}
	return mastodonText(*p)
}

// ---- 共用 ----

func fediString(p *string) Field[string] {
	if p == nil || *p == "" {
		return missing[string]("interface_omitted")
	}
	return available(*p)
}

// fediText 给用户简介这类可能为空的正文字段：null 与空串都是 not_returned，超长按 previewField 截断。
func fediText(p *string) Field[string] {
	if p == nil || *p == "" {
		return Field[string]{State: "not_returned", Reason: "empty_text"}
	}
	return previewField(p)
}

func fediNameFromClean(clean string) string {
	u, err := url.Parse(clean)
	if err != nil {
		return ""
	}
	for _, part := range strings.Split(u.Path, "/") {
		if strings.HasPrefix(part, "@") {
			return part[1:]
		}
	}
	return ""
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
