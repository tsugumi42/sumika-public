package linkplugin

// 展开时的按需判断（规格-画师原图 第七节）：像画师的带图原创推文作者记 pending，不影响这一次的发送。夹具在 artist_test.go。

import (
	"strings"
	"testing"

	"sumika-native/internal/artists"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
)

// 按需判断：像画师的带图原创推文作者记 pending，这一次的发送不变、日志不带账号名简介；同一作者第二次不再判。
func TestObserveRecordsPendingWithoutAffectingThisSend(t *testing.T) {
	t.Parallel()
	book := newArtistBook(t)
	f := &artistFetcher{id: artistID, account: artistAcct, images: 2, profile: true, bio: artistBio, links: []string{"https://skeb.jp/@paper_artist_x"}}
	d, r := runArtist(t, f, book, g1, nil)
	if got := kindsOf(d); got != "text,image,image" {
		t.Fatalf("pending 不影响这一次的发送：%s", got)
	}
	st, _ := book.Platform("x")
	e, ok := st.Lookup(artistID)
	if !ok || e.Status != artists.StatusPending || e.Source != artists.SourceAuto || len(e.Clues) == 0 || e.Handle != artistAcct {
		t.Fatalf("应该记一行待确认：%+v %v", e, ok)
	}
	if st.Confirmed(artistID) {
		t.Fatal("待确认不能生效")
	}
	for _, l := range r.log {
		if strings.Contains(l, artistName) || strings.Contains(l, artistAcct) || strings.Contains(l, "skeb") || strings.Contains(l, artistID) {
			t.Fatalf("日志里不该有账号、名字、简介：%q", l)
		}
	}
	if !r.logged("待确认 +1") {
		t.Fatalf("应该记一行计数：%v", r.log)
	}
	// 同一作者再来：不再判断（状态、分数、时间都不变）；只有存的 @账号 跟着更新成新的（ADR-372）
	before := e
	exp, drafts := draftCapture()
	exp.Artists = book
	r2 := newRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: exp}, &artistFetcher{id: artistID, account: "renamed_acct", images: 3, profile: true, bio: "全然関係ない説明"})
	r2.p.OnStored(storedMsg(32, []types.Seg{{Kind: types.SegText, Text: "https://x.com/renamed_acct/status/3102"}}))
	nextDraft(t, drafts)
	r2.idle()
	after, _ := st.Lookup(artistID)
	if !after.At.Equal(before.At) || after.Status != before.Status || after.Score != before.Score {
		t.Fatalf("已有的行不该再判断：%+v → %+v", before, after)
	}
	if after.Handle != "renamed_acct" {
		t.Fatalf("带 ID 的结果应该把存的账号名更新成新的：%q", after.Handle)
	}
}

// 按需判断的前提：不像、没资料、回复、没有图、功能关着、已经有行，各一格。
func TestObserveConditions(t *testing.T) {
	t.Parallel()
	artistLike := func() *artistFetcher {
		return &artistFetcher{id: artistID, account: artistAcct, images: 1, profile: true, bio: artistBio, links: []string{"https://skeb.jp/@paper_artist_x"}}
	}
	cases := []struct {
		name string
		mod  func(*artistFetcher)
		mut  func(*config.LinkExpand)
		want string // 表里这一行的状态；空串是没有这一行
	}{
		{"像画师", func(*artistFetcher) {}, nil, artists.StatusPending},
		{"不像：写真账号", func(f *artistFetcher) { f.bio, f.links = "毎日の散歩の写真を載せています", nil }, nil, artists.StatusDeclined},
		{"没资料（vx、瘦作者对象）", func(f *artistFetcher) { f.profile = false }, nil, ""},
		{"线路没给 ID", func(f *artistFetcher) { f.id = "" }, nil, ""},
		{"回复", func(f *artistFetcher) { f.reply = true }, nil, ""},
		{"没有自己的图", func(f *artistFetcher) { f.images = 0 }, nil, ""},
		{"autoDetect 关", func(*artistFetcher) {}, func(e *config.LinkExpand) { e.Artist.AutoDetect = false }, ""},
	}
	for _, c := range cases {
		book := newArtistBook(t)
		f := artistLike()
		c.mod(f)
		runArtist(t, f, book, g1, c.mut)
		st, _ := book.Platform("x")
		e, ok := st.Lookup(artistID)
		got := ""
		if ok {
			got = e.Status
		}
		if got != c.want {
			t.Errorf("%s：表里是 %q，想要 %q", c.name, got, c.want)
		}
	}
}

// 不判断的格子里，已经确认或拒绝过的作者也不会被按需判断改动（declined 的行不会被再次提）。
func TestObserveNeverTouchesExistingRows(t *testing.T) {
	t.Parallel()
	for _, status := range []string{artists.StatusConfirmed, artists.StatusPending, artists.StatusDeclined} {
		book := newArtistBook(t)
		seedArtist(t, book, status)
		st, _ := book.Platform("x")
		before, _ := st.Lookup(artistID)
		runArtist(t, &artistFetcher{id: artistID, account: artistAcct, images: 1, profile: true, bio: artistBio, links: []string{"https://skeb.jp/@paper_artist_x"}}, book, g1, nil)
		after, _ := st.Lookup(artistID)
		if after.Status != before.Status || !after.At.Equal(before.At) {
			t.Errorf("%s 的行被动了：%+v → %+v", status, before, after)
		}
	}
}

// 判断不发请求：每条链接只取一次数（取数之外没有任何别的网络调用）。
func TestObserveMakesNoExtraFetch(t *testing.T) {
	t.Parallel()
	f := &artistFetcher{id: artistID, account: artistAcct, images: 1, profile: true, bio: artistBio, links: []string{"https://skeb.jp/@paper_artist_x"}}
	runArtist(t, f, newArtistBook(t), g1, nil)
	if f.calls != 1 {
		t.Fatalf("取数 %d 次，应该只有 1 次", f.calls)
	}
}
