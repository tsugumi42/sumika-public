package main

// 设置 · 数据 · 密钥的界面测试（规格-数据目录与备份 5.3.2 界面）：假密钥库量操作，
// 真后台 + 真内进程桥在 Windows 上走一遍“界面 → 桌面接口 → App → secrets.bin”。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm/llmtest"
)

const secretsNoPassLine = "没设主密码：换电脑或重装系统后密钥解不开"

// 32 个 Base32 字符、4 个一组：只用来核对“显示一次、关掉就丢”，不对应任何真实的恢复码。
var secretsFakeRecovery = strings.Join([]string{"TEST", "CODE", "2345", "6789", "ABCD", "EFGH", "JKLM", "NPQR"}, "-")

// secretsFake 是只带密钥库的假数据源：状态、名字在内存里，调用记下来给测试对。
type secretsFake struct {
	api.Reader
	mu       sync.Mutex
	status   ctlproto.SecretsStatus
	names    []string
	sets     []string
	deletes  []string
	pasCalls int
	unlocks  []string
	recovery []bool
}

func (f *secretsFake) SecretsStatus(context.Context) (ctlproto.SecretsStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	st.Names = len(f.names)
	return st, nil
}

func (f *secretsFake) SecretsList(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...), nil
}

func (f *secretsFake) SecretsSet(_ context.Context, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, name+"="+value)
	f.status.File, f.status.Unlocked = true, true
	for _, n := range f.names {
		if n == name {
			return nil
		}
	}
	f.names = append(f.names, name)
	sort.Strings(f.names)
	return nil
}

func (f *secretsFake) SecretsDelete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, name)
	out := f.names[:0]
	for _, n := range f.names {
		if n != name {
			out = append(out, n)
		}
	}
	f.names = out
	return nil
}

func (f *secretsFake) SecretsSetPassphrase(_ context.Context, passphrase string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pasCalls++
	f.status.File, f.status.Unlocked, f.status.HasPassphrase, f.status.LocalCopy = true, true, true, true
	return secretsFakeRecovery, nil
}

func (f *secretsFake) SecretsUnlock(_ context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks = append(f.unlocks, secret)
	f.recovery = append(f.recovery, recovery)
	f.status.Unlocked, f.status.LocalCopy = true, true
	return ctlproto.SecretsUnlocked{Rewrapped: true}, nil
}

func (f *secretsFake) snapshot() (ctlproto.SecretsStatus, []string, []string, []string, int, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, append([]string(nil), f.names...), append([]string(nil), f.sets...),
		append([]string(nil), f.deletes...), f.pasCalls, append([]bool(nil), f.recovery...)
}

// secretsUIApp 是停在“设置 → 数据”的新界面，数据源带密钥库；post 进队列，由测试在界面线程上放行。
func secretsUIApp(t *testing.T, r api.Reader) (*app, *ui.Tester, func()) {
	t.Helper()
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabData, false
	a.set = nil
	a.real = realSettingsOf(t, nil)
	a.reader = r
	var mu sync.Mutex
	var queue []func()
	a.postFn = func(f func()) { mu.Lock(); queue = append(queue, f); mu.Unlock() }
	tt := shellTester(a, 2048, 1030)
	pump := func() {
		for range 50 {
			mu.Lock()
			q := queue
			queue = nil
			mu.Unlock()
			if len(q) == 0 {
				tt.Frame()
				mu.Lock()
				empty := len(queue) == 0
				mu.Unlock()
				if empty {
					return
				}
				continue
			}
			for _, f := range q {
				f()
			}
			tt.Frame()
		}
	}
	return a, tt, pump
}

func secretsWait(t *testing.T, what string, pump func(), cond func() bool) {
	t.Helper()
	for range 2000 {
		if cond() {
			return
		}
		pump()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func fieldInfo(t *testing.T, tt *ui.Tester, name string) ui.AccessInfo {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok || info.Bounds.W <= 0 || info.Bounds.H <= 0 {
		t.Fatalf("找不到输入框 %q：%+v", name, tt.Texts())
	}
	return info
}

func typeField(t *testing.T, tt *ui.Tester, name, text string) {
	t.Helper()
	info := fieldInfo(t, tt, name)
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(text)
	tt.Frame()
}

// 没设主密码：常驻一行提示；设主密码时两次不一致不提交，一致后恢复码只出现一次，点“抄好了”就丢。
func TestSecretsUIWarningAndPassphrase(t *testing.T) {
	f := &secretsFake{}
	a, tt, pump := secretsUIApp(t, f)
	secretsWait(t, "没设主密码的提示", pump, func() bool { return tt.HasText(secretsNoPassLine) && !a.secretsUI.busy })
	t.Logf("设置 · 数据 · 密钥（没设主密码）：%d 个元素", len(uiBoxes(tt)))
	if !hasKey(tt, "secrets-set-pass") {
		t.Fatalf("说明旁边要有“设主密码”：%q", tt.Texts())
	}
	clickKey(t, tt, "secrets-set-pass")
	pump()
	if !fieldInfo(t, tt, "新的主密码").Password || !fieldInfo(t, tt, "再输一次新的主密码").Password {
		t.Fatal("主密码输入框要遮住内容")
	}
	typeField(t, tt, "新的主密码", "correct horse")
	typeField(t, tt, "再输一次新的主密码", "different horse")
	clickKey(t, tt, "action-save")
	pump()
	if _, _, _, _, calls, _ := f.snapshot(); calls != 0 {
		t.Fatalf("两次不一致不该提交：%d", calls)
	}
	if !tt.HasText("两次输入的主密码不一样") {
		t.Fatalf("不一致时要写明：%q", tt.Texts())
	}
	// 改第二次，提交成功；恢复码对话框出现
	typeField(t, tt, "再输一次新的主密码", "correct horse")
	clickKey(t, tt, "action-save")
	secretsWait(t, "恢复码对话框", pump, func() bool {
		return tt.HasText("只显示这一次，抄下来") && tt.HasText(secretsFakeRecovery)
	})
	if _, _, _, _, calls, _ := f.snapshot(); calls != 1 {
		t.Fatalf("应提交一次：%d", calls)
	}
	if a.secretsUI.recoveryCode != secretsFakeRecovery {
		t.Fatalf("恢复码应在界面状态里等主人抄：%+v", a.secretsUI)
	}
	clickKey(t, tt, "action-ok")
	tt.Advance(time.Second)
	pump()
	if a.secretsUI.recoveryCode != "" || tt.HasText(secretsFakeRecovery) {
		t.Fatalf("点“抄好了”后要丢掉恢复码：状态 %q 文字 %q", a.secretsUI.recoveryCode, tt.Texts())
	}
	if tt.HasText(secretsNoPassLine) {
		t.Fatal("设了主密码后不再有那一行提示")
	}
}

// openSecretsRecovery 走完“设主密码 → 恢复码对话框出现”，两次输入一致。
func openSecretsRecovery(t *testing.T, a *app, tt *ui.Tester, pump func()) {
	t.Helper()
	secretsWait(t, "没设主密码的提示", pump, func() bool { return tt.HasText(secretsNoPassLine) && !a.secretsUI.busy })
	clickKey(t, tt, "secrets-set-pass")
	pump()
	typeField(t, tt, "新的主密码", "correct horse")
	typeField(t, tt, "再输一次新的主密码", "correct horse")
	clickKey(t, tt, "action-save")
	secretsWait(t, "恢复码对话框", pump, func() bool {
		return tt.HasText("只显示这一次，抄下来") && tt.HasText(secretsFakeRecovery)
	})
	// 设主密码的对话框有 160 毫秒退场，拷贝里还带着它的关闭钮；等它走完再量恢复码对话框。
	tt.Advance(time.Second)
	tt.Frame()
}

// 恢复码只显示一次：右上角没有关闭钮，Esc、点遮罩都关不掉，切到别的设置标签再回来还在；
// 只有点“抄好了”关得掉，关掉就清码（清码不靠设置页停在哪个标签）。
func TestSecretsUIRecoveryDialogOnlyClosesByButton(t *testing.T) {
	f := &secretsFake{}
	a, tt, pump := secretsUIApp(t, f)
	openSecretsRecovery(t, a, tt, pump)
	if hasKey(tt, "dialog-close") {
		t.Fatal("锁住的恢复码对话框不该有右上角关闭钮")
	}
	tt.Key(0, ui.KeyEscape)
	pump()
	tt.Frame()
	if !a.secretsUI.recoveryOpen || a.secretsUI.recoveryCode != secretsFakeRecovery || !tt.HasText(secretsFakeRecovery) {
		t.Fatalf("Esc 关掉了恢复码对话框：open=%v code=%q", a.secretsUI.recoveryOpen, a.secretsUI.recoveryCode)
	}
	tt.ClickAt(8, 8) // 对话框居中，左上角这点在面板外，点到的是遮罩
	pump()
	tt.Frame()
	if !a.secretsUI.recoveryOpen || a.secretsUI.recoveryCode != secretsFakeRecovery || !tt.HasText(secretsFakeRecovery) {
		t.Fatalf("点遮罩关掉了恢复码对话框：open=%v code=%q", a.secretsUI.recoveryOpen, a.secretsUI.recoveryCode)
	}
	a.realTab = tabLook
	pump()
	if !a.secretsUI.recoveryOpen || a.secretsUI.recoveryCode != secretsFakeRecovery {
		t.Fatalf("切标签把锁着的对话框关掉或清码了：open=%v code=%q", a.secretsUI.recoveryOpen, a.secretsUI.recoveryCode)
	}
	a.realTab = tabData
	pump()
	if !tt.HasText(secretsFakeRecovery) {
		t.Fatalf("切回数据页后恢复码对话框要还在：%q", tt.Texts())
	}
	clickKey(t, tt, "action-ok")
	pump()
	if a.secretsUI.recoveryOpen || a.secretsUI.recoveryCode != "" || tt.HasText(secretsFakeRecovery) {
		t.Fatalf("点“抄好了”后要关掉并丢掉恢复码：open=%v code=%q", a.secretsUI.recoveryOpen, a.secretsUI.recoveryCode)
	}
}

// 锁着的时候顶上有解锁输入框，可以切“用恢复码”；解开后框消失、状态回到已解开。
func TestSecretsUILockedUnlocks(t *testing.T) {
	f := &secretsFake{status: ctlproto.SecretsStatus{File: true, HasPassphrase: true}}
	a, tt, pump := secretsUIApp(t, f)
	secretsWait(t, "锁着时的解锁框", pump, func() bool {
		info, ok := tt.Access("主密码或恢复码")
		return ok && info.Password && info.Bounds.W > 0 && !a.secretsUI.busy
	})
	if !tt.HasText("锁着") || !tt.HasText("设了主密码") || !tt.HasText("本机副本：不在") {
		t.Fatalf("状态一行要写清锁着、主密码、本机副本：%q", tt.Texts())
	}
	clickKey(t, tt, "secrets-unlock-recovery")
	typeField(t, tt, "主密码或恢复码", secretsFakeRecovery)
	clickKey(t, tt, "secrets-unlock-btn")
	secretsWait(t, "解开", pump, func() bool {
		_, _, _, _, _, recovery := f.snapshot()
		return len(recovery) == 1 && recovery[0] && a.secretsUI.status.Unlocked
	})
	if _, _, _, _, _, recovery := f.snapshot(); !recovery[0] {
		t.Fatal("应把“用恢复码”传下去")
	}
	if _, ok := tt.Access("主密码或恢复码"); ok {
		t.Fatal("解开后不该再有输入框")
	}
}

// 名字只写“已存”，值不显示；删除先问一次，点了删除才真的调后台。
func TestSecretsUIDeleteNeedsConfirm(t *testing.T) {
	f := &secretsFake{status: ctlproto.SecretsStatus{File: true, Unlocked: true, HasPassphrase: true, LocalCopy: true}, names: []string{"qq.100.token"}}
	a, tt, pump := secretsUIApp(t, f)
	secretsWait(t, "名字列表", pump, func() bool { return tt.HasText("qq.100.token") && tt.HasText("已存") && !a.secretsUI.busy })
	clickKey(t, tt, "secrets-del-qq.100.token")
	pump()
	if _, _, _, del, _, _ := f.snapshot(); len(del) != 0 {
		t.Fatalf("还没确认就删了：%v", del)
	}
	if !tt.HasText("删除密钥？") || !hasKey(tt, "action-delete") {
		t.Fatalf("要二次确认：%q", tt.Texts())
	}
	clickKey(t, tt, "action-delete")
	secretsWait(t, "删除", pump, func() bool {
		_, _, _, del, _, _ := f.snapshot()
		return len(del) == 1 && del[0] == "qq.100.token"
	})
	tt.Advance(time.Second)
	pump()
	if len(a.secretsUI.names) != 0 {
		t.Fatalf("删掉后列表要少一行：%v", a.secretsUI.names)
	}
}

// 完整路径：界面 → 内进程桥（内嵌 DesktopReader）→ App → 临时目录的 secrets.bin。
// Windows 上本机加密是真的 DPAPI；其他系统没有本机加密，单靠 DPAPI 存不了，跳过。
func TestSecretsUIAddWritesRealSecretsFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本机加密是 Windows DPAPI，非 Windows 没有")
	}
	e := newBridgeEnv(t, llmtest.Script{})
	_, b := e.open(t, nil)
	a, tt, pump := secretsUIApp(t, b)
	secretsWait(t, "密钥库读回来", pump, func() bool { return tt.HasText("还没有密钥文件") && !a.secretsUI.busy })
	clickKey(t, tt, "secrets-add")
	pump()
	if info, ok := tt.Access("密钥名字"); !ok || info.Password {
		t.Fatalf("添加对话框缺名字输入框（或它被遮住了）：%+v", info)
	}
	if info, ok := tt.Access("密钥值"); !ok || !info.Password {
		t.Fatalf("添加对话框缺遮住内容的值的输入框：%+v", info)
	}
	const name, value = "qq.100.token", "编造的值-不该出现在界面或日志里"
	typeField(t, tt, "密钥名字", name)
	typeField(t, tt, "密钥值", value)
	clickKey(t, tt, "action-add")
	secretsWait(t, "列表里出现新名字", pump, func() bool {
		return tt.HasText(name) && !a.secretsUI.busy
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	names, err := b.SecretsList(ctx)
	if err != nil || len(names) != 1 || names[0] != name {
		t.Fatalf("桌面接口读回的名字：%v %v", names, err)
	}
	file := filepath.Join(e.opt.DataDir, "config", "secrets.bin")
	bin, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("secrets.bin 没写出来：%v", err)
	}
	if strings.Contains(string(bin), value) {
		t.Fatal("文件里出现了明文值")
	}
	if strings.Contains(strings.Join(tt.Texts(), "|"), value) {
		t.Fatal("界面上出现了值")
	}
}
