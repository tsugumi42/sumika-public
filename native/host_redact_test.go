package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
)

const diagnosticToken = "fake-token-ABC"

var diagnosticShapes = []struct{ name, raw, want string }{
	{"已知令牌", "打开失败：" + diagnosticToken + "；fake-env-token-XYZ", "打开失败：***；***"},
	{"长号", "打开失败：123456789", "打开失败：***89"},
	{"会话号", "打开失败：qq:private:123456789:987654321；qq:group:123", "打开失败：qq:private:***89:***21；qq:group:***23"},
	{"路径里的号", `打开失败：C:\编造目录\123456789\cache`, `打开失败：C:\编造目录\***89\cache`},
	{"空", "", ""},
	{"普通中文", "打开失败：磁盘空间不足", "打开失败：磁盘空间不足"},
}

func diagnosticFixture(t *testing.T) backend.Options {
	t.Helper()
	opt := hostFixture(t)
	opt.Account = "90001"
	t.Setenv("SUMIKA_TEST_HOST_TOKEN", "fake-env-token-XYZ")
	writeDiagnosticConfig(t, opt, "a")
	return opt
}

func writeDiagnosticConfig(t *testing.T, opt backend.Options, character string) {
	t.Helper()
	cfg := fmt.Sprintf("qq:\n  accounts:\n    - id: '90001'\n      character: %q\n      token: %q\n    - id: '90002'\n      character: b\n      token: '${SUMIKA_TEST_HOST_TOKEN}'\n", character, diagnosticToken)
	if err := os.WriteFile(filepath.Join(opt.DataDir, "config", "sumika.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireSafeDiagnostic(t *testing.T, text string) {
	t.Helper()
	for _, raw := range []string{diagnosticToken, "fake-env-token-XYZ", "123456789", "987654321", "qq:group:123"} {
		if strings.Contains(text, raw) {
			t.Fatalf("诊断出口泄漏编造的令牌或号码：%s", text)
		}
	}
}

// 独立日志和状态日志都落到真实 filelog；logs 尾读只能读回已脱敏的行。
func TestHostLogRedactsDiagnostics(t *testing.T) {
	opt := diagnosticFixture(t)
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(opt, func(hostState) {})
	sh.openLog()
	t.Cleanup(sh.closeLog)
	sh.host.start()
	t.Cleanup(func() { sh.host.shutdown() })
	b := &ctlBackend{s: sh}
	for _, tc := range diagnosticShapes {
		t.Run(tc.name, func(t *testing.T) {
			sh.host.logf("独立错误：%s", tc.raw)
			sh.logf("外壳错误：%s", tc.raw)
			sh.host.set(hostState{Phase: phaseBlocked, Reason: tc.raw})
			logs, err := b.Logs(context.Background(), 3, ctlproto.LogFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Lines) != 3 || !strings.HasSuffix(logs.Lines[0], "独立错误："+tc.want) || !strings.HasSuffix(logs.Lines[1], "外壳错误："+tc.want) {
				t.Fatalf("日志类别或详情丢失：%+v", logs)
			}
			requireSafeDiagnostic(t, strings.Join(logs.Lines, "\n"))
		})
	}
	sh.closeLog()
	requireSafeDiagnostic(t, strings.Join(deskLogLines(t, opt.DataDir), "\n"))
}

// 走测试专用的真实命名管道，同时核对界面回调和 Desktop.Publish 的快照。
func TestHostStatusRedactsDiagnostics(t *testing.T) {
	e := newCtlEnvWith(t, false, diagnosticFixture(t))
	e.sh.host.start()
	for _, tc := range diagnosticShapes {
		t.Run(tc.name, func(t *testing.T) {
			e.sh.host.noRecv = tc.raw
			e.sh.host.set(hostState{Phase: phaseArchive, Mode: backend.Archive, Reason: tc.raw})
			r := e.ask(t, ctlproto.Request{Cmd: "status"})
			st := dataAs[ctlproto.Status](t, r)
			if !r.OK || st.Reason != tc.want || st.NoRecv != tc.want {
				t.Fatalf("status 类别或详情不对：%+v", r)
			}
			requireSafeDiagnostic(t, string(r.Data))
			if got := e.log.last(); got.Reason != tc.want || got.NoRecv != tc.want {
				t.Fatalf("界面收到原始原因：%+v", got)
			}
			sub, err := e.sh.host.desk.Reader.WatchRuntime(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if snap := <-sub.Updates(); snap.Result.Value.Reason != tc.want {
				t.Fatalf("运行状态订阅收到原始原因：%+v", snap.Result.Value)
			}
			r = e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}})
			if rc := dataAs[ctlproto.Receive](t, r); !r.OK || rc.Reason != tc.want {
				t.Fatalf("receive stop 类别或详情不对：%+v", r)
			}
			requireSafeDiagnostic(t, string(r.Data))
		})
	}
}

// 缺失的临时数据目录让真实 Open 失败；目录名携带虚构令牌和长号，不连接协议端。
func TestHostReceiveStartRedactsFailure(t *testing.T) {
	e := newCtlEnvWith(t, false, diagnosticFixture(t))
	e.sh.host.start()
	e.sh.host.beforeOpen = func(mode backend.Mode) {
		if mode == backend.Receive {
			e.sh.host.opt.DataDir = filepath.Join(e.opt.DataDir, diagnosticToken+"-123456789")
		}
	}
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "30s"})
	rc := dataAs[ctlproto.Receive](t, r)
	if r.OK || r.Code != ctlproto.CodeFailed || !rc.Settled || rc.Receiving || !strings.Contains(r.Error, "开始接收失败") || !strings.Contains(r.Error, "后台没在运行") {
		t.Fatalf("应保留失败码、接收失败和后台受阻类别：%+v", r)
	}
	all, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	requireSafeDiagnostic(t, string(all))
	if r.Error != rc.Reason {
		t.Fatalf("error 和 data.reason 应为同一安全原因：%+v", r)
	}
}
