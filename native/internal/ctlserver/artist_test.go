package ctlserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 测试里的账号 ID、名字都是编的。
type artistFake struct {
	fakeBackend
	calls []string
	err   error
}

func (f *artistFake) ArtistList(_ context.Context, platform, status string, n int) (ctlproto.ArtistListResult, error) {
	f.rec("artist-list")
	f.calls = append(f.calls, "list "+platform+" "+status+" "+itoa(n))
	return ctlproto.ArtistListResult{Platform: platform, Status: status, Counts: map[string]int{"pending": 1},
		Rows: []ctlproto.ArtistRow{{ID: "1001", Handle: "paper\x1b[31m_cloud", Name: "纸\n云", Status: "pending", Source: "auto", Clues: []string{"自称\x07绘师"}}}}, nil
}
func (f *artistFake) ArtistImport(_ context.Context, platform, path string) (ctlproto.ArtistChange, error) {
	f.rec("artist-import")
	f.calls = append(f.calls, "import "+platform+" "+path)
	return ctlproto.ArtistChange{Platform: platform, Batch: "imp-20261113100000", Added: 2}, f.err
}
func (f *artistFake) ArtistUndo(_ context.Context, platform, batch string) (ctlproto.ArtistChange, error) {
	f.rec("artist-undo")
	f.calls = append(f.calls, "undo "+platform+" "+batch)
	return ctlproto.ArtistChange{Platform: platform, Deleted: 2}, nil
}
func (f *artistFake) ArtistConfirm(_ context.Context, platform string, ids []string) (ctlproto.ArtistChange, error) {
	f.rec("artist-confirm")
	f.calls = append(f.calls, "confirm "+platform+" "+strings.Join(ids, ","))
	return ctlproto.ArtistChange{Platform: platform, Confirmed: len(ids)}, nil
}
func (f *artistFake) ArtistRemove(_ context.Context, platform string, ids []string) (ctlproto.ArtistChange, error) {
	f.rec("artist-remove")
	f.calls = append(f.calls, "remove "+platform+" "+strings.Join(ids, ","))
	return ctlproto.ArtistChange{Platform: platform, Removed: len(ids)}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestArtistListIsReadOnlyAndCleansExternalText(t *testing.T) {
	f := &artistFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "artist", Args: []string{"list"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	if len(f.calls) != 1 || f.calls[0] != "list x pending 100" {
		t.Fatalf("默认应该是 x、pending、100：%v", f.calls)
	}
	v := dataOf[ctlproto.ArtistListResult](t, r)
	row := v.Rows[0]
	if strings.ContainsAny(row.Handle+row.Name+row.Clues[0], "\x1b\n\x07") {
		t.Fatalf("外来文字里的控制字符没清掉：%+v", row)
	}
	if len(logs) != 0 {
		t.Fatalf("list 是只读命令，不该写“改状态”的日志：%v", logs)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "artist", Args: []string{"list"}, N: 5, Artist: &ctlproto.ArtistArgs{Status: "all", Platform: "x"}})
	if !r.OK || f.calls[1] != "list x all 5" {
		t.Fatalf("%+v %v", r, f.calls)
	}
}

func TestArtistWritesAreLoggedAndRouted(t *testing.T) {
	f := &artistFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	reqs := []ctlproto.Request{
		{Cmd: "artist", Args: []string{"import", `D:\x\artists-confirmed.json`}},
		{Cmd: "artist", Args: []string{"import"}, Artist: &ctlproto.ArtistArgs{Undo: "imp-20261113100000"}},
		{Cmd: "artist", Args: []string{"confirm", "1001", "1002"}},
		{Cmd: "artist", Args: []string{"remove", "1001"}},
	}
	for _, req := range reqs {
		if r := ask(t, name, req); !r.OK {
			t.Fatalf("%+v：%+v", req, r)
		}
	}
	want := []string{`import x D:\x\artists-confirmed.json`, "undo x imp-20261113100000", "confirm x 1001,1002", "remove x 1001"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("后端收到的不对：%v", f.calls)
	}
	if len(logs) != len(reqs) {
		t.Fatalf("会改表的命令每个都要记一行：%d 行 %v", len(logs), logs)
	}
	for _, l := range logs {
		if strings.Contains(l, "D:") || strings.Contains(l, "1001") {
			t.Fatalf("日志里不该带路径和账号：%q", l)
		}
	}
}

func TestArtistReadOnlyTable(t *testing.T) {
	cases := map[string]bool{"list": true, "import": false, "confirm": false, "remove": false, "": false}
	for sub, want := range cases {
		args := []string{sub}
		if sub == "" {
			args = nil
		}
		if got := readOnly(ctlproto.Request{Cmd: "artist", Args: args}); got != want {
			t.Errorf("artist %q 的只读判断是 %v，应该是 %v", sub, got, want)
		}
	}
	if readOnly(ctlproto.Request{Cmd: "artist", Args: []string{"list", "x"}}) {
		t.Error("带多余参数的 list 不算只读")
	}
}

func TestArtistRejectsBadRequests(t *testing.T) {
	f := &artistFake{}
	name, _ := start(t, f, Options{})
	many := make([]string, ctlproto.MaxArtistIDs+1)
	for i := range many {
		many[i] = "1001"
	}
	bad := []ctlproto.Request{
		{Cmd: "artist"},
		{Cmd: "artist", Args: []string{"nope"}},
		{Cmd: "artist", Args: []string{"list", "多余"}},
		{Cmd: "artist", Args: []string{"list"}, N: -1},
		{Cmd: "artist", Args: []string{"list"}, N: ctlproto.MaxArtistRows + 1},
		{Cmd: "artist", Args: []string{"list"}, Artist: &ctlproto.ArtistArgs{Status: "vip"}},
		{Cmd: "artist", Args: []string{"list"}, Artist: &ctlproto.ArtistArgs{Platform: "X;DROP"}},
		{Cmd: "artist", Args: []string{"list"}, Artist: &ctlproto.ArtistArgs{Undo: "imp-1"}},
		{Cmd: "artist", Args: []string{"import"}},
		{Cmd: "artist", Args: []string{"import", "a", "b"}},
		{Cmd: "artist", Args: []string{"import", ""}},
		{Cmd: "artist", Args: []string{"import", "a\nb"}},
		{Cmd: "artist", Args: []string{"import", strings.Repeat("a", ctlproto.MaxArtistPath+1)}},
		{Cmd: "artist", Args: []string{"import", "文件.json"}, Artist: &ctlproto.ArtistArgs{Undo: "imp-1"}},
		{Cmd: "artist", Args: []string{"import"}, Artist: &ctlproto.ArtistArgs{Undo: "没有前缀"}},
		{Cmd: "artist", Args: []string{"import"}, Artist: &ctlproto.ArtistArgs{Undo: "imp-1; rm"}},
		{Cmd: "artist", Args: []string{"confirm"}},
		{Cmd: "artist", Args: append([]string{"confirm"}, many...)},
		{Cmd: "artist", Args: []string{"remove", "10 01"}},
		{Cmd: "artist", Args: []string{"remove", "1001\x00"}},
		{Cmd: "artist", Args: []string{"remove", ""}},
		{Cmd: "artist", Args: []string{"confirm", "1001"}, N: 5},
		{Cmd: "artist", Args: []string{"confirm", "1001"}, Artist: &ctlproto.ArtistArgs{Status: "all"}},
		{Cmd: "status", Artist: &ctlproto.ArtistArgs{Platform: "x"}},
	}
	for _, req := range bad {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("%+v 应当 invalid：%+v", req, r)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("不合格的请求不该碰后端：%v", f.calls)
	}
	wantCalls(t, &f.fakeBackend)
}

func TestArtistModes(t *testing.T) {
	for _, req := range []ctlproto.Request{
		{Cmd: "artist", Args: []string{"list"}},
		{Cmd: "artist", Args: []string{"confirm", "1001"}},
	} {
		name, _ := start(t, &artistFake{}, Options{Demo: true})
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
			t.Errorf("演示模式 %v：%+v", req.Args, r)
		}
		name, _ = start(t, &fakeBackend{}, Options{})
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
			t.Errorf("没接画师表 %v：%+v", req.Args, r)
		}
	}
}

func TestArtistBackendErrorsKeepTheirCode(t *testing.T) {
	f := &artistFake{err: ctlproto.Errorf(ctlproto.CodeInvalid, "文件太大")}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "artist", Args: []string{"import", `D:\x.json`}})
	if r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("%+v", r)
	}
	f.err = errors.New("写盘失败")
	r = ask(t, name, ctlproto.Request{Cmd: "artist", Args: []string{"import", `D:\x.json`}})
	if r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
}
