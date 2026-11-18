package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

func TestParseDBStatus(t *testing.T) {
	o, err := parseArgs([]string{"db", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if o.req.Cmd != ctlproto.CmdDB || len(o.req.Args) != 1 || o.req.Args[0] != ctlproto.DBStatusCmd {
		t.Fatalf("解析成 %+v", o.req)
	}
}

func TestDBStatusRenderUnavailable(t *testing.T) {
	data, _ := json.Marshal(ctlproto.DBStatus{Databases: []ctlproto.DBDatabase{
		{Name: "trace", Path: `C:\data\go\trace.db`, Status: "ok", Tracks: []ctlproto.DBTrack{{Track: "trace", Version: 21, MinReader: 21, Latest: 21}}},
		{Name: "qq", Path: `C:\data\go\chat\qq.db`, Status: "unavailable", Reason: "migrate_failed",
			Detail: "第 9 号迁移失败", Snapshot: `C:\data\go\upgrade-snapshots\qq-failed.sqlite`,
			Tracks: []ctlproto.DBTrack{{Track: "chat", Version: 16, MinReader: 17, Latest: 17}}},
	}})
	var out, stderr bytes.Buffer
	renderDB(&out, &stderr, ctlproto.Reply{OK: true, Data: data})
	text := out.String()
	for _, want := range []string{"trace", "qq", "不可用（migrate_failed）", "库 16 / min_reader 17 / 程序 17", "qq-failed.sqlite"} {
		if !strings.Contains(text, want) {
			t.Fatalf("输出里没有 %q：\n%s", want, text)
		}
	}
}
