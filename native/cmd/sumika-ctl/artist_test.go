package main

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// artist 的命令行选项：--platform、--status、--undo、-n 进到请求里，不混进位置参数
func TestParseArtistArgs(t *testing.T) {
	o, err := parseArgs([]string{"artist", "list", "--status", "all", "--platform=x", "-n", "20"})
	if err != nil {
		t.Fatal(err)
	}
	if o.req.Cmd != ctlproto.CmdArtist || len(o.req.Args) != 1 || o.req.Args[0] != "list" || o.req.Artist == nil || o.req.Artist.Status != "all" || o.req.Artist.Platform != "x" || o.req.N != 20 {
		t.Fatalf("%+v %+v", o.req, o.req.Artist)
	}
	o, err = parseArgs([]string{"artist", "import", "--undo", "imp-20261113100000"})
	if err != nil || o.req.Artist == nil || o.req.Artist.Undo != "imp-20261113100000" || len(o.req.Args) != 1 {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"artist", "confirm", "1001", "1002"})
	if err != nil || o.req.Artist != nil || len(o.req.Args) != 3 {
		t.Fatalf("没写选项时不该带 Artist：%v %+v", err, o.req)
	}
	for _, flag := range []string{"--status", "--platform", "--undo"} {
		if _, err := parseArgs([]string{"artist", "list", flag}); err == nil {
			t.Errorf("%s 后面没有内容应当报错", flag)
		}
	}
}

func TestRenderArtistListCleansExternalText(t *testing.T) {
	var out, errOut strings.Builder
	r := ctlproto.Reply{OK: true, Data: []byte(`{"platform":"x","status":"pending","counts":{"confirmed":3,"pending":1},"rows":[{"id":"1001","handle":"paper\u001b[31m_cloud","name":"纸\n云","status":"pending","source":"auto","score":4.5,"clues":["自称绘师"],"at":1763000000000}]}`)}
	renderArtist(&out, &errOut, ctlproto.Request{Cmd: "artist", Args: []string{"list"}}, r)
	got := out.String()
	if !strings.Contains(got, "生效 3、待确认 1") || !strings.Contains(got, "1001") || !strings.Contains(got, "待确认（按需发现）") || !strings.Contains(got, "分数 4.5") || !strings.Contains(got, "线索：自称绘师") {
		t.Fatalf("%q", got)
	}
	if strings.ContainsAny(got[:len(got)-1], "\x1b") || strings.Count(got, "\n") != 2 {
		t.Fatalf("控制字符和换行没清：%q", got)
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
}

func TestRenderArtistChanges(t *testing.T) {
	cases := []struct {
		req  ctlproto.Request
		data string
		want []string
	}{
		{ctlproto.Request{Cmd: "artist", Args: []string{"import", `D:\x.json`}}, `{"batch":"imp-20261113100000","added":2,"upgraded":1}`, []string{"批次 imp-20261113100000", "新增 2", "--undo imp-20261113100000"}},
		{ctlproto.Request{Cmd: "artist", Args: []string{"import", `D:\x.json`}}, `{"already":2}`, []string{"没有变化", "本来就生效 2"}},
		{ctlproto.Request{Cmd: "artist", Args: []string{"import"}, Artist: &ctlproto.ArtistArgs{Undo: "imp-1"}}, `{"restored":1,"deleted":2}`, []string{"恢复 1 行", "删除 2 行"}},
		{ctlproto.Request{Cmd: "artist", Args: []string{"confirm", "1"}}, `{"confirmed":1}`, []string{"确认 1 个"}},
		{ctlproto.Request{Cmd: "artist", Args: []string{"remove", "1"}}, `{"removed":1,"already":1}`, []string{"移除 1 个"}},
	}
	for _, c := range cases {
		var out, errOut strings.Builder
		renderArtist(&out, &errOut, c.req, ctlproto.Reply{OK: true, Data: []byte(c.data)})
		for _, w := range c.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%v：输出里没有 %q：%q", c.req.Args, w, out.String())
			}
		}
	}
}
