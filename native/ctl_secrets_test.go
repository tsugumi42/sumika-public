package main

// secrets 命令行接进主程序的完整路（复核 finding）：真的 ctlserver → ctlBackend → 临时数据目录里的后台，
// 不直接调 App。Windows 上本机副本是真 DPAPI。演示和后台没开好只测后端自己的错误。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

func TestCtlSecretsSetThenListOverPipe(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	e.sh.host.start() // 只看历史（archive）就有后台：密钥库跟着正在用的那一份
	set := e.ask(t, ctlproto.Request{Cmd: ctlproto.CmdSecrets, Args: []string{ctlproto.SecretsSetCmd, "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造值-xyz"}})
	if !set.OK {
		t.Fatalf("secrets set 经控制接口应成功：%+v %s", set, set.Data)
	}
	if written := dataAs[ctlproto.SecretsWritten](t, set); written.Name != "qq.100.token" {
		t.Fatalf("存下的名字：%+v", written)
	}
	if strings.Contains(string(set.Data), "编造值-xyz") {
		t.Fatalf("回复里不该有值：%s", set.Data)
	}
	list := e.ask(t, ctlproto.Request{Cmd: ctlproto.CmdSecrets, Args: []string{ctlproto.SecretsListCmd}})
	if !list.OK {
		t.Fatalf("secrets list 经控制接口应成功：%+v %s", list, list.Data)
	}
	if names := dataAs[ctlproto.SecretsList](t, list).Names; len(names) != 1 || names[0] != "qq.100.token" {
		t.Fatalf("list 应只有刚存下的名字：%v", names)
	}
	// 文件在临时数据目录里（hostFixture 用的是 t.TempDir），不在真实的 data/
	if _, err := os.Stat(filepath.Join(e.opt.DataDir, "config", "secrets.bin")); err != nil {
		t.Fatalf("密钥文件应写在临时数据目录：%v", err)
	}
}

func TestCtlSecretsBackendDemoAndNoBackend(t *testing.T) {
	t.Parallel()
	demo := &ctlBackend{s: &shell{}}
	if ce := ctlErr(t, func() error { _, err := demo.SecretsList(context.Background()); return err }()); ce.Code != ctlproto.CodeDemo {
		t.Fatalf("演示数据模式应回 demo：%+v", ce)
	}
	b := &ctlBackend{s: &shell{host: newHost(hostFixture(t), func(hostState) {})}}
	_, err := b.SecretsStatus(context.Background())
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "读不了密钥库") {
		t.Fatalf("后台还没开好应回失败并说清：%+v", ce)
	}
}
