package ctlserver

import (
	"context"
	"testing"

	"sumika-native/internal/ctlproto"
)

type dbFake struct{ Backend }

func (dbFake) DBStatus(context.Context) (ctlproto.DBStatus, error) {
	return ctlproto.DBStatus{Databases: []ctlproto.DBDatabase{{
		Name: "qq", Path: `C:\data\go\chat\qq.db`, Status: "unavailable", Reason: "migrate_failed",
		Detail: "第 9 号迁移失败：坏\n行", Snapshot: `C:\data\go\upgrade-snapshots\qq-failed.sqlite`,
		Tracks: []ctlproto.DBTrack{{Track: "chat", Version: 16, MinReader: 17, Latest: 17}},
	}}}, nil
}

func TestDBStatusReadOnlyAndCleansText(t *testing.T) {
	s := New(dbFake{}, Options{})
	r, _ := s.serve([]byte(`{"v":1,"cmd":"db","args":["status"]}`))
	if !r.OK {
		t.Fatalf("db status 失败：%s", r.Error)
	}
	v := dataOf[ctlproto.DBStatus](t, r)
	if len(v.Databases) != 1 || v.Databases[0].Name != "qq" || v.Databases[0].Reason != "migrate_failed" {
		t.Fatalf("db status 结果：%+v", v)
	}
	if v.Databases[0].Detail != "第 9 号迁移失败：坏 行" {
		t.Fatalf("详情里的换行没清掉：%q", v.Databases[0].Detail)
	}
	if !readOnly(ctlproto.Request{Cmd: ctlproto.CmdDB, Args: []string{"status"}}) {
		t.Fatal("db status 不是只读")
	}
	if readOnly(ctlproto.Request{Cmd: ctlproto.CmdDB, Args: []string{"status", "x"}}) {
		t.Fatal("db status 带多余参数还是只读")
	}
	if r, _ := s.serve([]byte(`{"v":1,"cmd":"db","args":["migrate"]}`)); r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("db migrate 没被拒：%+v", r)
	}
}
