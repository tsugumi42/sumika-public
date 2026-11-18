package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/artists"
	"sumika-native/internal/linkreader"
)

// 画师表的命令行入口（sumika-ctl artist）在后台这一半：导入的就是链接插件和预览用的那一份表，立刻生效。
// 账号、名字、ID 都是编的。

// idFetcher 给 pictureFetcher 的推文补上作者数字 ID（fx_v2 线路才有）
type idFetcher struct{ id string }

func (f idFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	r, err := pictureFetcher{}.Fetch(ctx, l)
	r.Author.ID = linkreader.Field[string]{State: "available", Value: f.id}
	return r, err
}
func (idFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

func writeImport(t *testing.T, ids ...string) string {
	t.Helper()
	var rows []string
	for _, id := range ids {
		rows = append(rows, `{"id":"`+id+`","screen_name":"user_`+id+`","name":"名字`+id+`"}`)
	}
	p := filepath.Join(t.TempDir(), "artists-confirmed.json")
	if err := os.WriteFile(p, []byte(`{"confirmedArtists":[`+strings.Join(rows, ",")+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArtistImportMakesPreviewUseOriginals(t *testing.T) {
	t.Parallel()
	_, a, _, _ := newExpandHost(t, "off", idFetcher{id: "7001"})
	d := a.Desktop().Reader
	ctx := context.Background()
	const url = "https://x.com/paper/status/74001"
	before, err := d.LinkPreview(ctx, LinkPreviewRequest{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range before.Notes {
		if strings.Contains(n, "画师") {
			t.Fatalf("没导入时不该有画师提示：%v", before.Notes)
		}
	}
	rep, err := d.ArtistImport(ctx, "x", writeImport(t, "7001"))
	if err != nil || rep.Added != 1 || rep.Batch == "" {
		t.Fatalf("%+v %v", rep, err)
	}
	after, err := d.LinkPreview(ctx, LinkPreviewRequest{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(after.Notes, "\n"), "确认过的画师") || strings.Join(after.Messages, "\n") == strings.Join(before.Messages, "\n") {
		t.Fatalf("导入后预览应该换成画师的发法：%v\n%v\n%v", after.Notes, before.Messages, after.Messages)
	}
	// 撤回后又回到原样
	if _, err := d.ArtistUndo(ctx, "x", rep.Batch); err != nil {
		t.Fatal(err)
	}
	undone, _ := d.LinkPreview(ctx, LinkPreviewRequest{URL: url})
	if strings.Join(undone.Messages, "\n") != strings.Join(before.Messages, "\n") {
		t.Fatalf("撤回后应该和导入前一样")
	}
}

func TestArtistReaderCommands(t *testing.T) {
	t.Parallel()
	_, a, _, _ := newExpandHost(t, "off", idFetcher{id: "7001"})
	d := a.Desktop().Reader
	ctx := context.Background()
	if out, err := d.ArtistList(ctx, "x", "pending", 10); err != nil || len(out.Rows) != 0 {
		t.Fatalf("空表：%+v %v", out, err)
	}
	// 表里按需发现一行待确认，再用命令确认、移除
	st, _ := a.artistBook().Platform("x")
	if _, err := st.AddAuto("7002", "paper_artist", "纸", artists.Judgement{Score: 5, Clues: []string{"自称绘师"}}); err != nil {
		t.Fatal(err)
	}
	out, err := d.ArtistList(ctx, "x", "pending", 10)
	if err != nil || len(out.Rows) != 1 || out.Rows[0].ID != "7002" || out.Counts["pending"] != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if rep, err := d.ArtistConfirm(ctx, "x", []string{"7002"}); err != nil || rep.Confirmed != 1 || !st.Confirmed("7002") {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep, err := d.ArtistRemove(ctx, "x", []string{"7002"}); err != nil || rep.Removed != 1 || st.Confirmed("7002") {
		t.Fatalf("%+v %v", rep, err)
	}
	// 请求本身不对按 invalid 报：平台没做、ID 格式不对、导入文件不对
	if _, err := d.ArtistList(ctx, "pixiv", "pending", 10); !errors.Is(err, ErrArtistInvalid) {
		t.Errorf("平台没做：%v", err)
	}
	if _, err := d.ArtistConfirm(ctx, "x", []string{"abc"}); !errors.Is(err, ErrArtistInvalid) {
		t.Errorf("ID 格式不对：%v", err)
	}
	if _, err := d.ArtistImport(ctx, "x", "相对路径.json"); !errors.Is(err, ErrArtistInvalid) {
		t.Errorf("相对路径：%v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("不是 JSON"), 0o644)
	if _, err := d.ArtistImport(ctx, "x", bad); !errors.Is(err, ErrArtistInvalid) {
		t.Errorf("不是 JSON：%v", err)
	}
	// 表里没有的账号：报错、不新增
	if _, err := d.ArtistConfirm(ctx, "x", []string{"9999"}); err == nil {
		t.Errorf("表里没有的账号应该报错：%v", err)
	}
	if _, ok := st.Lookup("9999"); ok {
		t.Error("confirm 不能凭空新增")
	}
}
