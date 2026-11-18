package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
)

// secretsTestApp 是一个只带数据目录的空壳 App：secrets 这几个方法只碰 paths 和日志。
func secretsTestApp(t *testing.T) *App {
	t.Helper()
	return &App{paths: paths.Paths{Root: t.TempDir()}, logf: func(string, ...any) {}}
}

// copyLockedFixture 把“旧守护进程设过主密码、没有本机副本”的夹具放进这个数据目录。
func copyLockedFixture(t *testing.T, a *App) {
	t.Helper()
	bin, err := os.ReadFile(filepath.Join("..", "secrets", "testdata", "legacy-daemon-locked.bin"))
	if err != nil {
		t.Fatalf("读夹具：%v", err)
	}
	path := a.paths.SecretsFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bin, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsStatusWithoutFile(t *testing.T) {
	a := secretsTestApp(t)
	st, err := a.SecretsStatus(context.Background())
	if err != nil {
		t.Fatalf("没有文件不算错误：%v", err)
	}
	if st.File || st.Unlocked || st.HasPassphrase || st.LocalCopy || st.Names != 0 {
		t.Fatalf("没有文件时应全空：%+v", st)
	}
	if _, err := a.SecretsList(context.Background()); err == nil || !strings.Contains(err.Error(), "sumika-ctl secrets set") {
		t.Fatalf("list 没有文件时应指向 ctl 怎么存：%v", err)
	}
	if _, err := a.SecretsDelete(context.Background(), "qq.100.token"); err == nil {
		t.Fatal("delete 没有文件时应失败，不该因此新建文件")
	}
	if _, err := os.Stat(a.paths.SecretsFile()); !os.IsNotExist(err) {
		t.Fatalf("delete 不该新建密钥文件：%v", err)
	}
	if _, err := a.SecretsUnlock(context.Background(), "随便的主密码", false); err == nil || !strings.Contains(err.Error(), "还没有密钥文件") {
		t.Fatalf("unlock 没有文件时应说清楚：%v", err)
	}
}

// 锁着的时候：status 照实报，set/delete 一个字节都不改，错误指向 unlock。
func TestSecretsLockedStatusAndWrites(t *testing.T) {
	a := secretsTestApp(t)
	copyLockedFixture(t, a)
	before, err := os.ReadFile(a.paths.SecretsFile())
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.SecretsStatus(context.Background())
	if err != nil {
		t.Fatalf("Status：%v", err)
	}
	if !st.File || st.Unlocked || !st.HasPassphrase || st.LocalCopy || st.Names != 0 {
		t.Fatalf("锁着时该报文件在、没解开、有主密码、本机副本不可用：%+v", st)
	}
	for name, call := range map[string]func() error{
		"list": func() error { _, err := a.SecretsList(context.Background()); return err },
		"set": func() error {
			_, err := a.SecretsSet(context.Background(), "qq.100.token", "编造值-xyz")
			return err
		},
		"delete": func() error {
			_, err := a.SecretsDelete(context.Background(), "qq.100.token")
			return err
		},
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "sumika-ctl secrets unlock") {
			t.Errorf("%s 锁着时应指向 unlock，得到 %v", name, err)
		}
		if strings.Contains(err.Error(), "编造值-xyz") {
			t.Errorf("%s 的错误里出现了值：%v", name, err)
		}
	}
	after, err := os.ReadFile(a.paths.SecretsFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("锁着时不该改文件")
	}
}

// 取令牌失败时的提示指向 ctl，号照旧打码，不再指向 Bun；
// 提示里的命令用占位符，不带打码的号（复制了也跑不了）。
func TestTokenForPointsAtCtl(t *testing.T) {
	a := secretsTestApp(t)
	acct := config.QQAccount{ID: config.QQID("10001")}
	_, err := a.tokenFor(acct)
	if err == nil {
		t.Fatal("没有令牌应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sumika-ctl secrets list") || !strings.Contains(msg, "sumika-ctl secrets set qq.<这个账号的号>.token") {
		t.Errorf("没有令牌时应指向 ctl 的 list 和 set：%v", err)
	}
	if !strings.Contains(msg, "***01") {
		t.Errorf("错误里应指明是哪个账号（打码）：%v", err)
	}
	if strings.Contains(msg, "qq.***01.token") {
		t.Errorf("提示里的命令不该带打码的号（复制了也跑不了）：%v", err)
	}
	if strings.Contains(msg, "10001") {
		t.Errorf("号应打码：%v", err)
	}
	if strings.Contains(msg, "Bun") {
		t.Errorf("不该再指向 Bun：%v", err)
	}

	copyLockedFixture(t, a)
	_, err = a.tokenFor(acct)
	if err == nil || !strings.Contains(err.Error(), "sumika-ctl secrets unlock") {
		t.Errorf("锁着时应指向 unlock：%v", err)
	}
	if strings.Contains(err.Error(), "10001") {
		t.Errorf("锁着时的错误里号应打码：%v", err)
	}
}

// Windows 上用真的 DPAPI 做一次往返：set 建库、list 有名字、status 已解开、delete 删掉。
func TestSecretsRoundTripWithRealLocalCopy(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本机副本只有 Windows 的 DPAPI")
	}
	a := secretsTestApp(t)
	ctx := context.Background()
	if _, err := a.SecretsSet(ctx, "qq.100.token", "编造值-xyz"); err != nil {
		t.Fatalf("Set：%v", err)
	}
	st, err := a.SecretsStatus(ctx)
	if err != nil {
		t.Fatalf("Status：%v", err)
	}
	if !st.File || !st.Unlocked || !st.LocalCopy || st.HasPassphrase || st.Names != 1 {
		t.Fatalf("刚存完应是：文件在、已解开、本机副本可用、没设主密码、1 个名字：%+v", st)
	}
	l, err := a.SecretsList(ctx)
	if err != nil || len(l.Names) != 1 || l.Names[0] != "qq.100.token" {
		t.Fatalf("list：%+v %v", l, err)
	}
	if _, err := a.SecretsDelete(ctx, "qq.100.token"); err != nil {
		t.Fatalf("Delete：%v", err)
	}
	if l, err := a.SecretsList(ctx); err != nil || len(l.Names) != 0 {
		t.Fatalf("删掉后 list 应为空：%+v %v", l, err)
	}
	if _, err := a.tokenFor(config.QQAccount{ID: config.QQID("10001")}); err == nil || !strings.Contains(err.Error(), "sumika-ctl secrets set") {
		t.Fatalf("库解开了但没有这个名字时应指向 set：%v", err)
	}
}

// 日志里写 qq.<号>.token 时号要照界面规则打码：完整号码不进日志。
func TestSecretsLogMasksQQNumber(t *testing.T) {
	var logs []string
	a := &App{paths: paths.Paths{Root: t.TempDir()}, logf: func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	}}
	ctx := context.Background()
	// 非 Windows 没有本机加密，先设主密码才写得进去；这一条也顺带走一遍设主密码的日志（里面没有号）。
	if _, err := a.SecretsSetPassphrase(ctx, "编造的主密码一号"); err != nil {
		t.Fatalf("SetPassphrase：%v", err)
	}
	if _, err := a.SecretsSet(ctx, "qq.100.token", "编造值-xyz"); err != nil {
		t.Fatalf("Set：%v", err)
	}
	if _, err := a.SecretsDelete(ctx, "qq.100.token"); err != nil {
		t.Fatalf("Delete：%v", err)
	}
	got := strings.Join(logs, "\n")
	if strings.Contains(got, "100") {
		t.Fatalf("日志里不该有完整号码：%s", got)
	}
	if !strings.Contains(got, "qq.***00.token") {
		t.Fatalf("日志里的号应打码：%s", got)
	}
}
