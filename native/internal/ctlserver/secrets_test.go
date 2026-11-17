package ctlserver

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

type secretsFake struct {
	fakeBackend
	gotName     string
	gotValue    string
	gotRecovery bool
	gotPass     string
}

func (f *secretsFake) SecretsStatus(context.Context) (ctlproto.SecretsStatus, error) {
	f.rec("secrets-status")
	return ctlproto.SecretsStatus{File: true, Unlocked: true, HasPassphrase: true, LocalCopy: true, Names: 2, Note: "注\n释"}, f.err
}

func (f *secretsFake) SecretsList(context.Context) (ctlproto.SecretsList, error) {
	f.rec("secrets-list")
	return ctlproto.SecretsList{Names: []string{"qq.100.token", "webdav.password"}}, f.err
}

func (f *secretsFake) SecretsSet(_ context.Context, name, value string) (ctlproto.SecretsWritten, error) {
	f.rec("secrets-set")
	f.gotName, f.gotValue = name, value
	return ctlproto.SecretsWritten{Name: name}, f.err
}

func (f *secretsFake) SecretsDelete(_ context.Context, name string) (ctlproto.SecretsWritten, error) {
	f.rec("secrets-delete")
	f.gotName = name
	return ctlproto.SecretsWritten{Name: name}, f.err
}

func (f *secretsFake) SecretsSetPassphrase(_ context.Context, passphrase string) (ctlproto.SecretsPassphrase, error) {
	f.rec("secrets-set-passphrase")
	f.gotPass = passphrase
	return ctlproto.SecretsPassphrase{RecoveryCode: "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"}, f.err
}

func (f *secretsFake) SecretsUnlock(_ context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error) {
	f.rec("secrets-unlock")
	f.gotValue, f.gotRecovery = secret, recovery
	return ctlproto.SecretsUnlocked{Rewrapped: !recovery}, f.err
}

// status、list 只读：不写改状态的日志；后端拿到请求、回复字段原样交出去（note 清过控制字符）。
func TestSecretsStatusAndListAreReadOnly(t *testing.T) {
	lc := &logCapture{}
	f := &secretsFake{}
	name, _ := start(t, f, Options{Logf: lc.logf})
	r := ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"status"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	st := dataOf[ctlproto.SecretsStatus](t, r)
	if !st.File || !st.Unlocked || !st.HasPassphrase || !st.LocalCopy || st.Names != 2 {
		t.Fatalf("%+v", st)
	}
	if strings.ContainsAny(st.Note, "\n\r") {
		t.Errorf("note 里的控制字符该清掉：%q", st.Note)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"list"}}); !r.OK {
		t.Fatalf("%+v", r)
	} else if l := dataOf[ctlproto.SecretsList](t, r); len(l.Names) != 2 {
		t.Fatalf("%+v", l)
	}
	if got := lc.all(); got != "" {
		t.Fatalf("status、list 只读，不该写日志：%q", got)
	}
	wantCalls(t, &f.fakeBackend, "secrets-status", "secrets-list")
}

// 形状 × 入口：缺名字、名字非法、值超长、多余参数、值放在 Args 里、恢复码标记放错、别的命令带这个字段，
// 一律 invalid、一个后端函数都不调。
func TestSecretsRejectsBadShapes(t *testing.T) {
	value := &ctlproto.SecretsArgs{Secret: "编造值-xyz"}
	for name, req := range map[string]ctlproto.Request{
		"没有子命令":                 {Cmd: "secrets"},
		"不认识的子命令":               {Cmd: "secrets", Args: []string{"show"}},
		"set 没写名字":              {Cmd: "secrets", Args: []string{"set"}, Secrets: value},
		"set 名字带中文":             {Cmd: "secrets", Args: []string{"set", "密钥"}, Secrets: value},
		"set 名字带空格":             {Cmd: "secrets", Args: []string{"set", "qq 100"}, Secrets: value},
		"set 名字带路径":             {Cmd: "secrets", Args: []string{"set", "../x"}, Secrets: value},
		"set 名字太长":              {Cmd: "secrets", Args: []string{"set", strings.Repeat("a", ctlproto.SecretsMaxNameBytes+1)}, Secrets: value},
		"set 没有值字段":             {Cmd: "secrets", Args: []string{"set", "qq.100.token"}},
		"set 值是空的":              {Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: ""}},
		"set 值超长":               {Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: strings.Repeat("x", ctlproto.SecretsMaxValueBytes+1)}},
		"set 值放在 Args 里":        {Cmd: "secrets", Args: []string{"set", "qq.100.token", "编造值-xyz"}},
		"set 多余参数":              {Cmd: "secrets", Args: []string{"set", "qq.100.token", "多余的"}, Secrets: value},
		"set 带恢复码标记":            {Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "x", Recovery: true}},
		"delete 没写名字":           {Cmd: "secrets", Args: []string{"delete"}},
		"delete 名字非法":           {Cmd: "secrets", Args: []string{"delete", "密钥"}},
		"delete 带了值":            {Cmd: "secrets", Args: []string{"delete", "qq.100.token"}, Secrets: value},
		"status 带参数":            {Cmd: "secrets", Args: []string{"status", "x"}},
		"status 带了值":            {Cmd: "secrets", Args: []string{"status"}, Secrets: value},
		"list 带了值":              {Cmd: "secrets", Args: []string{"list"}, Secrets: value},
		"set-passphrase 多余参数":   {Cmd: "secrets", Args: []string{"set-passphrase", "x"}, Secrets: value},
		"set-passphrase 没有输入":   {Cmd: "secrets", Args: []string{"set-passphrase"}},
		"set-passphrase 带恢复码标记": {Cmd: "secrets", Args: []string{"set-passphrase"}, Secrets: &ctlproto.SecretsArgs{Secret: "x", Recovery: true}},
		"unlock 多余参数":           {Cmd: "secrets", Args: []string{"unlock", "x"}, Secrets: value},
		"unlock 把恢复码写在 Args 里":  {Cmd: "secrets", Args: []string{"unlock", "--recovery", "ABCD-EFGH"}},
		"unlock 没有输入":           {Cmd: "secrets", Args: []string{"unlock"}},
		"unlock 输入是空的":          {Cmd: "secrets", Args: []string{"unlock"}, Secrets: &ctlproto.SecretsArgs{Secret: ""}},
		"别的命令带值字段":              {Cmd: "backup", Args: []string{"list"}, Secrets: value},
		"别的命令带恢复码标记":            {Cmd: "mcp", Args: []string{"status"}, Secrets: &ctlproto.SecretsArgs{Recovery: true}},
		"顶层字段形状不认识（多余参数进 Args）": {Cmd: "secrets", Args: []string{"unlock", "--recovery"}},
	} {
		f := &secretsFake{}
		n, _ := start(t, f, Options{})
		if r := ask(t, n, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("%s：该 invalid，得到 %+v", name, r)
		}
		if got := f.got(); len(got) != 0 {
			t.Errorf("%s：被拒的请求调到了后端：%v", name, got)
		}
	}
}

// 校验过的值原样交给后端；恢复码标记只有 unlock 收得到。
func TestSecretsForwardsValidatedInput(t *testing.T) {
	f := &secretsFake{}
	name, _ := start(t, f, Options{})
	ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造值-xyz"}})
	if f.gotName != "qq.100.token" || f.gotValue != "编造值-xyz" {
		t.Fatalf("set 收到的名字或值不对：%q %q", f.gotName, f.gotValue)
	}
	ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"unlock"}, Secrets: &ctlproto.SecretsArgs{Secret: "ABCD-EFGH", Recovery: true}})
	if f.gotValue != "ABCD-EFGH" || !f.gotRecovery {
		t.Fatalf("unlock 收到的恢复码或标记不对：%q %t", f.gotValue, f.gotRecovery)
	}
	r := ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"set-passphrase"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造主密码"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	if v := dataOf[ctlproto.SecretsPassphrase](t, r); v.RecoveryCode != "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567" {
		t.Fatalf("恢复码应原样回给客户端：%+v", v)
	}
}

// 日志不泄露：值、主密码、恢复码（包括 set-passphrase 回复里的恢复码）一个都不进日志；
// 会改状态的子命令各记一行，只记命令名和子命令。
func TestSecretsNeverLogValues(t *testing.T) {
	lc := &logCapture{}
	f := &secretsFake{}
	name, _ := start(t, f, Options{Logf: lc.logf})
	ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造值-xyz"}})
	ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"set-passphrase"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造主密码-xyz"}})
	ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{"unlock"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造恢复码-xyz", Recovery: true}})
	got := lc.all()
	want := "控制接口：secrets set → 成功\n控制接口：secrets set-passphrase → 成功\n控制接口：secrets unlock → 成功"
	if got != want {
		t.Fatalf("日志：\n%s\n应是\n%s", got, want)
	}
	for _, secret := range []string{"编造值-xyz", "编造主密码-xyz", "编造恢复码-xyz", "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"} {
		if strings.Contains(got, secret) {
			t.Errorf("日志里出现了密钥材料 %q：%q", secret, got)
		}
	}
}

// 演示数据模式一律不做，一个后端函数都不调。
func TestSecretsRefuseDemo(t *testing.T) {
	for _, sub := range []string{"status", "list", "set", "delete", "set-passphrase", "unlock"} {
		f := &secretsFake{}
		n, _ := start(t, f, Options{Demo: true})
		req := ctlproto.Request{Cmd: "secrets", Args: []string{sub}}
		switch sub {
		case "set":
			req.Args = append(req.Args, "qq.100.token")
			req.Secrets = &ctlproto.SecretsArgs{Secret: "x"}
		case "delete":
			req.Args = append(req.Args, "qq.100.token")
		case "set-passphrase", "unlock":
			req.Secrets = &ctlproto.SecretsArgs{Secret: "x"}
		}
		if r := ask(t, n, req); r.OK || r.Code != ctlproto.CodeDemo {
			t.Errorf("%s：演示模式该被拒，得到 %+v", sub, r)
		}
		if got := f.got(); len(got) != 0 {
			t.Errorf("%s：演示模式不该调到后端：%v", sub, got)
		}
	}
}

// 后端不实现 SecretsBackend 时回“不可用”，不是崩溃。
func TestSecretsWithoutBackendExtension(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	for _, sub := range []string{"status", "list"} {
		r := ask(t, name, ctlproto.Request{Cmd: "secrets", Args: []string{sub}})
		if r.OK || r.Code != ctlproto.CodeFailed {
			t.Fatalf("%s：得到 %+v", sub, r)
		}
	}
}
