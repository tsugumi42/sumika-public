package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// withStdin 换掉 secrets 命令读输入的地方，测试结束换回。
func withStdin(t *testing.T, text string) {
	t.Helper()
	old := secretStdin
	secretStdin = strings.NewReader(text)
	t.Cleanup(func() { secretStdin = old })
}

// 值只从标准输入读，进请求的专门字段，不进 Args（Args 会写日志、进进程列表）。
func TestSecretsSetReadsValueFromStdin(t *testing.T) {
	withStdin(t, "编造值-xyz\n")
	f := newFakeServer(t, okReply(`{"name":"qq.100.token"}`))
	code, out, errb := runWith(t, f, "secrets", "set", "qq.100.token")
	if code != 0 {
		t.Fatalf("退出码 %d：%s%s", code, out, errb)
	}
	want := ctlproto.Request{V: ctlproto.Version, Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造值-xyz"}}
	if got := f.only(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("请求不对：\n得到 %+v\n应是 %+v", got, want)
	}
	if !strings.Contains(out, "已存下 qq.100.token") || strings.Contains(out, "编造值-xyz") || strings.Contains(errb, "编造值-xyz") {
		t.Fatalf("输出不该带值，名字要显示：%q%s", out, errb)
	}
}

// 值、主密码、恢复码写在命令行参数里是用法错误：不发请求、不回显参数原文。
func TestSecretsRejectsValuesInArgs(t *testing.T) {
	for _, args := range [][]string{
		{"secrets", "set", "qq.100.token", "编造值-xyz"},
		{"secrets", "delete", "qq.100.token", "编造值-xyz"},
		{"secrets", "set-passphrase", "编造主密码xyz"},
		{"secrets", "unlock", "编造恢复码xyz"},
		{"secrets", "set", "qq.100.token", "--recovery"},
	} {
		f := newFakeServer(t, okReply(""))
		var out, errb strings.Builder
		code := run(args, &out, &errb, f.dial)
		if code != ctlproto.ExitDenied {
			t.Errorf("%q：应被拒（3），得到 %d", args, code)
		}
		f.mu.Lock()
		dials, sent := len(f.demos), len(f.reqs)
		f.mu.Unlock()
		if dials != 0 {
			t.Errorf("%q：用法错误不该连主程序", args)
		}
		if sent != 0 {
			t.Errorf("%q：用法错误不该发请求", args)
		}
		if strings.Contains(errb.String(), "编造值-xyz") || strings.Contains(errb.String(), "编造主密码xyz") || strings.Contains(errb.String(), "编造恢复码xyz") {
			t.Errorf("%q：错误里不该回显参数原文：%s", args, errb.String())
		}
		if !strings.Contains(errb.String(), "标准输入") {
			t.Errorf("%q：错误里要说值从标准输入读：%s", args, errb.String())
		}
	}
}

// 主密码读两次，不一致就不发请求；一致时只把第一次的值放进请求。
func TestSecretsPassphraseTwice(t *testing.T) {
	withStdin(t, "编造主密码一号\n编造主密码二号\n")
	f := newFakeServer(t, okReply(""))
	var out, errb strings.Builder
	if code := run([]string{"secrets", "set-passphrase"}, &out, &errb, f.dial); code != ctlproto.ExitDenied {
		t.Fatalf("两次不一致应被拒（3），得到 %d：%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "两次输入的主密码不一样") {
		t.Fatalf("应说清楚没改：%s", errb.String())
	}
	f.mu.Lock()
	sent := len(f.reqs)
	f.mu.Unlock()
	if sent != 0 {
		t.Fatalf("两次不一致不该发请求，发了 %d 个", sent)
	}

	withStdin(t, "编造主密码一号\n编造主密码一号\n")
	f2 := newFakeServer(t, okReply(`{"recoveryCode":"ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"}`))
	code, okOut, okErr := runWith(t, f2, "secrets", "set-passphrase")
	if code != 0 {
		t.Fatalf("退出码 %d：%s%s", code, okOut, okErr)
	}
	want := ctlproto.Request{V: ctlproto.Version, Cmd: "secrets", Args: []string{"set-passphrase"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造主密码一号"}}
	if got := f2.only(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("请求不对：\n得到 %+v\n应是 %+v", got, want)
	}
	if !strings.Contains(okOut, "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567") || !strings.Contains(okOut, "只显示这一次") {
		t.Fatalf("恢复码应显示一次：%q", okOut)
	}
}

// unlock --recovery 把恢复码放进请求的专门字段，并带上恢复码标记。
func TestSecretsUnlockRecovery(t *testing.T) {
	withStdin(t, "ABCD-efgh-ijkl\n")
	f := newFakeServer(t, okReply(`{"rewrapped":true}`))
	code, out, errb := runWith(t, f, "secrets", "unlock", "--recovery")
	if code != 0 {
		t.Fatalf("退出码 %d：%s%s", code, out, errb)
	}
	want := ctlproto.Request{V: ctlproto.Version, Cmd: "secrets", Args: []string{"unlock"}, Secrets: &ctlproto.SecretsArgs{Secret: "ABCD-efgh-ijkl", Recovery: true}}
	if got := f.only(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("请求不对：\n得到 %+v\n应是 %+v", got, want)
	}
	if !strings.Contains(out, "本机副本已重新包好") || !strings.Contains(out, "sumika-ctl receive start") {
		t.Fatalf("解锁成功应说明本机副本和下一步：%q", out)
	}
}

// --json 时提示只走 stderr，stdout 只有一个 JSON 对象；stdin 的值不显示。
func TestSecretsJSONKeepsStdoutClean(t *testing.T) {
	withStdin(t, "编造值-xyz\n")
	f := newFakeServer(t, okReply(`{"name":"qq.100.token"}`))
	code, out, errb := runWith(t, f, "--json", "secrets", "set", "qq.100.token")
	if code != 0 {
		t.Fatalf("退出码 %d：%s%s", code, out, errb)
	}
	var r ctlproto.Reply
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
		t.Fatalf("stdout 应是 JSON：%v\n%s", err, out)
	}
	if strings.Contains(out, "输入不显示") || strings.Contains(out, "编造值-xyz") {
		t.Fatalf("提示和值不该进 stdout：%q", out)
	}
	if !strings.Contains(errb, "输入不显示") {
		t.Fatalf("提示应写到 stderr：%q", errb)
	}
}

// 各子命令的输出：状态、名单、删除；锁着时给出下一步。
func TestRenderSecrets(t *testing.T) {
	cases := []struct {
		req  ctlproto.Request
		data string
		want []string
	}{
		{ctlproto.Request{Cmd: "secrets", Args: []string{"status"}}, `{"file":false}`, []string{"密钥文件：还没有", "secrets set"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"status"}}, `{"file":true,"hasPassphrase":true}`, []string{"状态：锁着", "secrets unlock", "主密码：已设", "本机副本：不在这台电脑"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"status"}}, `{"file":true,"unlocked":true,"localCopy":true,"names":2}`, []string{"状态：已解开（存了 2 个名字）", "主密码：没设", "本机副本：这台电脑能用"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"list"}}, `{"names":["qq.100.token","webdav.password"]}`, []string{"qq.100.token\nwebdav.password"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"list"}}, `{"names":[]}`, []string{"没有存任何密钥"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"delete", "qq.100.token"}}, `{"name":"qq.100.token"}`, []string{"已删掉 qq.100.token"}},
		{ctlproto.Request{Cmd: "secrets", Args: []string{"unlock"}}, `{"rewrapped":false,"note":"本机副本没包上：磁盘满了"}`, []string{"这次解开了", "磁盘满了", "receive start"}},
	}
	for _, c := range cases {
		var out, errb strings.Builder
		render(&out, &errb, c.req, ctlproto.Reply{OK: true, Data: []byte(c.data)})
		for _, want := range c.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%q：输出里应有 %q，得到：%s", c.req.Args, want, out.String())
			}
		}
	}
}

// 主程序没在跑：说清楚先启动 Sumika；ctl 不自己打开密钥文件，也就不会先让人输一遍。
func TestSecretsNotRunningHint(t *testing.T) {
	var out, errb strings.Builder
	dial := func(context.Context, bool) (net.Conn, error) { return nil, errors.New("管道不存在") }
	code := run([]string{"secrets", "status"}, &out, &errb, dial)
	if code != ctlproto.ExitNotRunning {
		t.Fatalf("应报没在运行（2），得到 %d：%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "先启动 Sumika") {
		t.Fatalf("应提示先启动 Sumika：%s", errb.String())
	}
}

// 不是合法 UTF-8 的输入不悄悄换成替换字符再存：直接报错，不发请求。
func TestSecretsRejectsInvalidUTF8(t *testing.T) {
	withStdin(t, "\xff\xfe\n")
	f := newFakeServer(t, okReply(""))
	var out, errb strings.Builder
	if code := run([]string{"secrets", "set", "qq.100.token"}, &out, &errb, f.dial); code != ctlproto.ExitFailed {
		t.Fatalf("应报执行失败（1），得到 %d：%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "UTF-8") {
		t.Fatalf("应说清楚要 UTF-8：%s", errb.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 0 {
		t.Fatalf("不该发请求，发了 %d 个", len(f.reqs))
	}
}

// 管道里第一行以后还有内容时拒绝：不悄悄只存第一行，也不发请求。
func TestSecretsRejectsMultipleLines(t *testing.T) {
	withStdin(t, "编造值-xyz\n第二行也是值\n")
	f := newFakeServer(t, okReply(`{"name":"qq.100.token"}`))
	var out, errb strings.Builder
	if code := run([]string{"secrets", "set", "qq.100.token"}, &out, &errb, f.dial); code != ctlproto.ExitDenied {
		t.Fatalf("多行输入应被拒（3），得到 %d：%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "值只能是一行") {
		t.Fatalf("应说清楚值只能是一行：%s", errb.String())
	}
	if strings.Contains(errb.String(), "编造值-xyz") || strings.Contains(out.String(), "编造值-xyz") {
		t.Fatalf("输出不该回显值：%q%s", out.String(), errb.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 0 {
		t.Fatalf("不该发请求，发了 %d 个", len(f.reqs))
	}
}

// 管道开头的 BOM 不是值的一部分：去掉再发；用法说明写清 PowerShell 5.1 的管道编码问题。
func TestSecretsStripsBOM(t *testing.T) {
	withStdin(t, "\uFEFF编造值-xyz\n")
	f := newFakeServer(t, okReply(`{"name":"qq.100.token"}`))
	code, out, errb := runWith(t, f, "secrets", "set", "qq.100.token")
	if code != 0 {
		t.Fatalf("退出码 %d：%s%s", code, out, errb)
	}
	want := ctlproto.Request{V: ctlproto.Version, Cmd: "secrets", Args: []string{"set", "qq.100.token"}, Secrets: &ctlproto.SecretsArgs{Secret: "编造值-xyz"}}
	if got := f.only(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("BOM 应被去掉：\n得到 %+v\n应是 %+v", got, want)
	}
	for _, s := range []string{"PowerShell 5.1", "PowerShell 7", "chcp 65001"} {
		if !strings.Contains(secretsUsage, s) {
			t.Errorf("用法里应说明编码问题，缺 %q：%s", s, secretsUsage)
		}
	}
}

// 本机副本没包上：只说这次解开、下次还要输主密码和原因，不说“这次解开照常可用”
// （文件里本机那一份还是解不开，紧接着的 list、set 会报锁着）。
func TestRenderSecretsUnlockLocalCopyFailed(t *testing.T) {
	var out, errb strings.Builder
	req := ctlproto.Request{Cmd: "secrets", Args: []string{"unlock"}}
	render(&out, &errb, req, ctlproto.Reply{OK: true, Data: []byte(`{"rewrapped":false,"note":"本机副本没包上：磁盘满了"}`)})
	got := out.String()
	for _, want := range []string{"这次解开了", "这台电脑的本机副本没包上", "下次还要输主密码", "原因：磁盘满了"} {
		if !strings.Contains(got, want) {
			t.Errorf("输出里应有 %q：%s", want, got)
		}
	}
	if strings.Contains(got, "照常可用") {
		t.Errorf("本机副本没包上时不该说这次照常可用：%s", got)
	}
	if strings.Count(got, "本机副本没包上") != 1 {
		t.Errorf("原因只该出现一次（不重复前缀）：%s", got)
	}
}
