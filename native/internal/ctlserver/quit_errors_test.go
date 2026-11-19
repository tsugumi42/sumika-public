package ctlserver

import (
	"errors"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 退出开始以后在途的命令失败：failed、timeout 的原因是"正在退出"，不是命令自己带的旧阶段；
// 和退出无关的码（readonly、invalid、demo、denied、version）照原样。没在退出时一律原样
func TestErrorsWhileQuittingSayQuitting(t *testing.T) {
	codes := []string{ctlproto.CodeFailed, ctlproto.CodeTimeout, ctlproto.CodeReadOnly, ctlproto.CodeInvalid, ctlproto.CodeDemo, ctlproto.CodeDenied, ctlproto.CodeVersion}
	for _, quitting := range []bool{false, true} {
		for _, code := range codes {
			srv := New(&fakeBackend{}, Options{})
			if quitting {
				srv.Close()
			}
			r := srv.fromErr(&ctlproto.Error{Code: code, Msg: "只看历史", Data: ctlproto.Receive{Phase: "archive", Reason: "只看历史"}})
			wantQuit := quitting && (code == ctlproto.CodeFailed || code == ctlproto.CodeTimeout)
			switch {
			case wantQuit && (r.Code != ctlproto.CodeFailed || r.Error != "桌面程序正在退出" || len(r.Data) != 0):
				t.Errorf("退出中、%s：应是“桌面程序正在退出”，得到 %+v", code, r)
			case !wantQuit && (r.Code != code || r.Error != "只看历史" || len(r.Data) == 0):
				t.Errorf("quitting=%v、%s：应照原样，得到 %+v", quitting, code, r)
			}
		}
	}
	// 没带码的错误：退出中说正在退出（原有行为）
	srv := New(&fakeBackend{}, Options{})
	srv.Close()
	if r := srv.fromErr(errors.New("随便")); r.Error != "桌面程序正在退出" {
		t.Errorf("没带码的错误：%+v", r)
	}
}
