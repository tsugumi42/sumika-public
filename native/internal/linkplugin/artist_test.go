package linkplugin

// 画师的推文发原图（规格-画师原图 第五、七节）在链接插件里的装配：取数 → 查表 → 画师发法 → 交给准备的草稿；
// 以及展开时按需判断。账号 ID、名字、简介、链接都是编的；表放临时目录；出口是假的，不碰协议端。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/artists"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

const (
	artistID   = "7001"
	artistName = "编的名字"
	artistAcct = "paper_artist_x"
	artistBio  = "イラストレーター。お仕事のご依頼はDMへ https://skeb.jp/@paper_artist_x"
)

// artistFetcher 返回编的 X 推文：作者数字 ID、资料、图数、是不是回复都能调。
type artistFetcher struct {
	id      string // 作者数字 ID；空就是线路没给（vx）
	account string
	bio     string
	links   []string // 简介里展开后的链接
	profile bool
	images  int
	reply   bool
	calls   int
}

func (f *artistFetcher) Fetch(_ context.Context, l linkreader.Link) (linkreader.Result, error) {
	f.calls++
	av := func(s string) linkreader.Field[string] { return linkreader.Field[string]{State: "available", Value: s} }
	r := linkreader.Result{Platform: "x", Kind: "post", ID: l.ID, CleanURL: l.Clean, Source: "fake", FetchedAt: time.Now().UTC(), Stats: map[string]linkreader.Field[int64]{}}
	r.Author.Name, r.Author.Account = av(artistName), av(f.account)
	if f.id != "" {
		r.Author.ID = av(f.id)
	}
	if f.profile {
		r.Author.Profile = &linkreader.AuthorProfile{Bio: f.bio, Links: f.links, Followers: 1200, MediaCount: 400, Statuses: 600}
	}
	r.Article = linkreader.Field[string]{State: "not_returned", Reason: "not_applicable"}
	r.Text = av("编的作品说明")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "编的作品说明"}}
	var ms []linkreader.Media
	for i := 1; i <= f.images; i++ {
		ms = append(ms, linkreader.Media{Type: "image", URL: av("https://pbs.example.invalid/media/" + string(rune('a'+i)) + ".jpg?name=orig")})
	}
	r.Media = linkreader.Field[[]linkreader.Media]{State: "available", Value: ms}
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "image", Media: &r.Media.Value[i]})
	}
	if f.reply {
		r.ReplyTo = av("6999")
	}
	return r, nil
}
func (f *artistFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

func newArtistBook(t *testing.T) *artists.Book {
	t.Helper()
	return artists.NewBook(t.TempDir(), artists.Options{})
}

func seedArtist(t *testing.T, book *artists.Book, status string) {
	t.Helper()
	st, err := book.Platform("x")
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case artists.StatusPending:
		if _, err := st.AddAuto(artistID, artistAcct, artistName, artists.Judgement{Score: 5}); err != nil {
			t.Fatal(err)
		}
	case artists.StatusDeclined:
		if _, err := st.AddAuto(artistID, artistAcct, artistName, artists.Judgement{Score: 0}); err != nil {
			t.Fatal(err)
		}
	case artists.StatusConfirmed:
		if _, err := st.AddAuto(artistID, artistAcct, artistName, artists.Judgement{Score: 5}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Confirm([]string{artistID}); err != nil {
			t.Fatal(err)
		}
	}
}

// runArtist 让插件处理一条带这个作者推文的消息，返回交给准备的草稿；conv 是群还是私聊由调用方给。
func runArtist(t *testing.T, f *artistFetcher, book *artists.Book, conv types.Conversation, mut func(*config.LinkExpand)) (linkexpand.Draft, *rig) {
	t.Helper()
	exp, drafts := draftCapture()
	exp.Artists = book
	set := expandSettings(func(e *config.LinkExpand) {
		e.Conversations = []string{conv.String()}
		if mut != nil {
			mut(e)
		}
	})
	r := newRig(t, &fakeFetcher{}, Options{Settings: set, Expand: exp}, f)
	s := storedMsg(31, []types.Seg{{Kind: types.SegText, Text: "https://x.com/" + artistAcct + "/status/3101"}})
	s.Inbound.Conv = conv
	r.p.OnStored(s)
	d := nextDraft(t, drafts)
	r.idle()
	return d, r
}

func qqPrivate() types.Conversation {
	return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "30001", Self: acct}
}

func kindsOf(d linkexpand.Draft) string {
	if len(d.Parts) == 0 {
		return ""
	}
	var ks []string
	for _, it := range d.Parts[0].Body {
		ks = append(ks, it.Kind)
	}
	return strings.Join(ks, ",")
}

func draftJSON(t *testing.T, d linkexpand.Draft) string {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 确认过的画师：QQ 群、QQ 私聊各一份完整路径（取数 → 查表 → 发法 → 草稿）。默认卡片加原图，originalsOnly 只发链接和原图。
func TestConfirmedArtistGetsOriginalsInGroupAndPrivate(t *testing.T) {
	t.Parallel()
	for name, conv := range map[string]types.Conversation{"QQ 群": g1, "QQ 私聊": qqPrivate()} {
		book := newArtistBook(t)
		seedArtist(t, book, artists.StatusConfirmed)
		d, _ := runArtist(t, &artistFetcher{id: artistID, account: artistAcct, images: 2}, book, conv, nil)
		if got := kindsOf(d); got != "card,text,image,image" {
			t.Fatalf("%s 默认应该卡片加原图：%s", name, got)
		}
		book2 := newArtistBook(t)
		seedArtist(t, book2, artists.StatusConfirmed)
		d, _ = runArtist(t, &artistFetcher{id: artistID, account: artistAcct, images: 2}, book2, conv, func(e *config.LinkExpand) { e.Artist.Mode = config.ArtistOriginalsOnly })
		if got := kindsOf(d); got != "text,image,image" {
			t.Fatalf("%s originalsOnly：%s", name, got)
		}
	}
}

// 不命中的各种情形，草稿和空表时逐字节相同：没有这一行、待确认、不是、模式 off、线路没给 ID 且账号名对不上、
// 线路给了格式不对的 ID（不拿 @账号 兜底）。“没有 ID 但账号名一样”从 ADR-372 起命中，见 TestHandleFallback。
func TestNonConfirmedAndNoIDStayAsBefore(t *testing.T) {
	t.Parallel()
	base, _ := runArtist(t, &artistFetcher{id: artistID, account: artistAcct, images: 2}, newArtistBook(t), g1, nil)
	baseJSON := draftJSON(t, base)
	if kindsOf(base) != "text,image,image" {
		t.Fatalf("对照：没有画师时是普通的文字加图：%s", kindsOf(base))
	}
	cases := map[string]struct {
		status string
		f      *artistFetcher
		mut    func(*config.LinkExpand)
	}{
		"待确认":    {artists.StatusPending, &artistFetcher{id: artistID, account: artistAcct, images: 2}, nil},
		"不是":     {artists.StatusDeclined, &artistFetcher{id: artistID, account: artistAcct, images: 2}, nil},
		"别人的 ID": {artists.StatusConfirmed, &artistFetcher{id: "7002", account: artistAcct, images: 2}, nil},
		"模式 off": {artists.StatusConfirmed, &artistFetcher{id: artistID, account: artistAcct, images: 2}, func(e *config.LinkExpand) { e.Artist.Mode = config.ArtistOff }},
		"线路没给 ID，账号名也对不上": {artists.StatusConfirmed, &artistFetcher{id: "", account: "someone_else", images: 2}, nil},
		"ID 是 0":  {artists.StatusConfirmed, &artistFetcher{id: "0", account: artistAcct, images: 2}, nil},
		"ID 不是数字": {artists.StatusConfirmed, &artistFetcher{id: "abc", account: artistAcct, images: 2}, nil},
	}
	for name, c := range cases {
		want := baseJSON
		if c.f.account != artistAcct { // 账号名不同，文字里的作者行本来就不同：拿同一个账号在空表时的结果对照
			b, _ := runArtist(t, c.f, newArtistBook(t), g1, func(e *config.LinkExpand) { e.Artist.AutoDetect = false })
			want = draftJSON(t, b)
		}
		book := newArtistBook(t)
		seedArtist(t, book, c.status)
		d, _ := runArtist(t, c.f, book, g1, func(e *config.LinkExpand) {
			e.Artist.AutoDetect = false // 这一组只看发法，不要按需判断插嘴
			if c.mut != nil {
				c.mut(e)
			}
		})
		if got := draftJSON(t, d); got != want {
			t.Errorf("%s：应该和空表时一样\n%s\n%s", name, want, got)
		}
	}
	// 没有 Artists（功能没接）：一样
	d, _ := runArtist(t, &artistFetcher{id: artistID, account: artistAcct, images: 2}, nil, g1, nil)
	if got := draftJSON(t, d); got != baseJSON {
		t.Errorf("没接画师表：应该和空表时一样")
	}
}

// ArtistMode 的格子：配置模式 × 表状态 × 作者 ID。
func TestArtistModeTable(t *testing.T) {
	t.Parallel()
	mk := func(id string) *linkreader.Result {
		r := &linkreader.Result{Platform: "x", Kind: "post"}
		if id != "" {
			r.Author.ID = linkreader.Field[string]{State: "available", Value: id}
		}
		return r
	}
	book := newArtistBook(t)
	seedArtist(t, book, artists.StatusConfirmed)
	pend := newArtistBook(t)
	seedArtist(t, pend, artists.StatusPending)
	cfg := func(m string) config.LinkArtist { return config.LinkArtist{Mode: m} }
	cases := []struct {
		name string
		book *artists.Book
		r    *linkreader.Result
		cfg  config.LinkArtist
		want string
	}{
		{"默认", book, mk(artistID), cfg(""), linkexpand.ArtistCardAndOriginals},
		{"卡片加原图", book, mk(artistID), cfg(config.ArtistCardAndOriginals), linkexpand.ArtistCardAndOriginals},
		{"只发原图", book, mk(artistID), cfg(config.ArtistOriginalsOnly), linkexpand.ArtistOriginalsOnly},
		{"off", book, mk(artistID), cfg(config.ArtistOff), ""},
		{"待确认", pend, mk(artistID), cfg(""), ""},
		{"没有表", nil, mk(artistID), cfg(""), ""},
		{"没有 ID", book, mk(""), cfg(""), ""},
		{"别的 ID", book, mk("7002"), cfg(""), ""},
		{"空 ID", book, mk(" "), cfg(""), ""},
		{"nil 结果", book, nil, cfg(""), ""},
		{"不是 X", book, &linkreader.Result{Platform: "pixiv", Kind: "artwork", Author: linkreader.Author{ID: linkreader.Field[string]{State: "available", Value: artistID}}}, cfg(""), ""},
		{"X 文章以外的页面类型", book, &linkreader.Result{Platform: "x", Kind: "user", Author: linkreader.Author{ID: linkreader.Field[string]{State: "available", Value: artistID}}}, cfg(""), ""},
		{"ID 状态不是 available", book, &linkreader.Result{Platform: "x", Kind: "post", Author: linkreader.Author{ID: linkreader.Field[string]{State: "not_returned", Value: artistID}}}, cfg(""), ""},
	}
	for _, c := range cases {
		if got := ArtistMode(c.book, c.r, c.cfg); got != c.want {
			t.Errorf("%s：得到 %q，想要 %q", c.name, got, c.want)
		}
	}
}

var _ = channel.Stored{}

// ADR-372：vx 线路没给数字 ID 时按 @账号 找确认过的画师；fx 带 ID 的结果顺手更新表里存的 @账号。
func TestHandleFallback(t *testing.T) {
	t.Parallel()
	noDetect := func(e *config.LinkExpand) { e.Artist.AutoDetect = false }
	for name, acct := range map[string]string{"一样的账号名": artistAcct, "大小写不同": "PAPER_ARTIST_X"} {
		book := newArtistBook(t)
		seedArtist(t, book, artists.StatusConfirmed)
		d, _ := runArtist(t, &artistFetcher{id: "", account: acct, images: 2}, book, g1, noDetect)
		if got := kindsOf(d); got != "card,text,image,image" {
			t.Errorf("%s：vx 没给 ID，账号名对上确认过的画师应该照画师发：%s", name, got)
		}
	}
	// 待确认的不按账号名命中
	pend := newArtistBook(t)
	seedArtist(t, pend, artists.StatusPending)
	if d, _ := runArtist(t, &artistFetcher{id: "", account: artistAcct, images: 2}, pend, g1, noDetect); kindsOf(d) != "text,image,image" {
		t.Errorf("待确认不该按账号名命中：%s", kindsOf(d))
	}
	// 改名：fx 带着 ID 和新账号名来过一次以后，vx 按新名字认得出、按旧名字认不出
	book := newArtistBook(t)
	seedArtist(t, book, artists.StatusConfirmed)
	runArtist(t, &artistFetcher{id: artistID, account: "renamed_artist_x", images: 2}, book, g1, nil)
	st, err := book.Platform("x")
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := st.Lookup(artistID); e.Handle != "renamed_artist_x" || e.Status != artists.StatusConfirmed {
		t.Fatalf("fx 带 ID 的结果应该把存的账号名更新成新的，状态不变：%+v", e)
	}
	if d, _ := runArtist(t, &artistFetcher{id: "", account: "renamed_artist_x", images: 2}, book, g1, noDetect); kindsOf(d) != "card,text,image,image" {
		t.Errorf("改名后按新名字应该认得出：%s", kindsOf(d))
	}
	if d, _ := runArtist(t, &artistFetcher{id: "", account: artistAcct, images: 2}, book, g1, noDetect); kindsOf(d) != "text,image,image" {
		t.Errorf("改名后旧名字不该再认成画师：%s", kindsOf(d))
	}
}
