package ctlserver

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// envBackend 在 fakeBackend 上补 doctor 要的可选接口：返回给定快照，调用也记下来。
type envBackend struct {
	*fakeBackend
	snap ctlproto.DoctorSnapshot
	err  error
}

func (b *envBackend) EnvSnapshot(context.Context) (ctlproto.DoctorSnapshot, error) {
	b.rec("env-snapshot")
	return b.snap, b.err
}

func TestDoctor(t *testing.T) {
	f := &fakeBackend{}
	b := &envBackend{fakeBackend: f, snap: ctlproto.DoctorSnapshot{
		Mode: "archive",
		Items: []ctlproto.DoctorItem{{
			ID: "env.tools", Group: "外部程序", Name: "外部程序", State: "warn",
			Detail: "登记了 1 个外部程序，1 个没找到", Next: "装 PowerShell 7\n\x1b[2J", Affects: "外部程序：个别可选功能用不了",
			Sub: []ctlproto.DoctorSubItem{{Name: "pwsh", State: "warn", Detail: "没找到\r\n下一行", Next: "装 PowerShell 7"}},
		}}}}
	name, _ := start(t, b, Options{})

	r := ask(t, name, ctlproto.Request{Cmd: ctlproto.CmdDoctor})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	d := dataOf[ctlproto.DoctorSnapshot](t, r)
	if len(d.Items) != 1 || d.Items[0].ID != "env.tools" || len(d.Items[0].Sub) != 1 {
		t.Fatalf("快照字段没原样回来：%+v", d)
	}
	// 外来文字出去前清一遍：换行变空格、控制字符变问号
	if strings.ContainsAny(d.Items[0].Next, "\n\r") || strings.Contains(d.Items[0].Next, "\x1b") ||
		strings.ContainsAny(d.Items[0].Sub[0].Detail, "\n\r") {
		t.Fatalf("reply 里还有换行或控制字符：%+v", d.Items[0])
	}
	wantCalls(t, f, "env-snapshot")
}

func TestDoctorTakesNoArguments(t *testing.T) {
	f := &fakeBackend{}
	b := &envBackend{fakeBackend: f}
	name, _ := start(t, b, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: ctlproto.CmdDoctor, Args: []string{"x"}})
	if r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("doctor 带参数应被拒：%+v", r)
	}
	wantCalls(t, f)
}

func TestDoctorWithoutEnvBackend(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: ctlproto.CmdDoctor})
	if r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "环境清单不可用") {
		t.Fatalf("没接环境清单后端应是 failed：%+v", r)
	}
	wantCalls(t, f)
}

func TestDoctorBackendError(t *testing.T) {
	f := &fakeBackend{}
	b := &envBackend{fakeBackend: f, err: ctlproto.Errorf(ctlproto.CodeDemo, "演示数据模式没有环境清单")}
	name, _ := start(t, b, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: ctlproto.CmdDoctor})
	if r.OK || r.Code != ctlproto.CodeDemo {
		t.Fatalf("后端的错误码要原样回来：%+v", r)
	}
}
