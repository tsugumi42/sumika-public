package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeLocal 替真实 DPAPI：mark 变了就是“换了一台电脑 / 重装了系统”，本机那份解不开。
type fakeLocal struct {
	mark         []byte
	protectErr   error
	unprotectErr error
}

func useFakeLocal(t *testing.T) *fakeLocal {
	t.Helper()
	m := &fakeLocal{mark: []byte("本机:")}
	oldProtect, oldUnprotect := protectLocal, unprotectLocal
	protectLocal = func(b []byte) ([]byte, error) {
		if m.protectErr != nil {
			return nil, m.protectErr
		}
		return append(append([]byte{}, m.mark...), b...), nil
	}
	unprotectLocal = func(b []byte) ([]byte, error) {
		if m.unprotectErr != nil {
			return nil, m.unprotectErr
		}
		if !bytes.HasPrefix(b, m.mark) {
			return nil, errors.New("这台电脑解不开")
		}
		return bytes.Clone(b[len(m.mark):]), nil
	}
	t.Cleanup(func() { protectLocal, unprotectLocal = oldProtect, oldUnprotect })
	return m
}

// useTestCost 把 scrypt 换小（规格：测试用 N = 2¹⁰），读的上下限同步放宽到能读。
func useTestCost(t *testing.T) {
	t.Helper()
	oldCost, oldLimits := writeCost, acceptCost
	writeCost = scryptCost{N: 1 << 10, R: 8, P: 1}
	acceptCost = scryptLimits{MinN: 1 << 10, MaxN: 1 << 20, MaxR: 16, MaxP: 4}
	t.Cleanup(func() { writeCost, acceptCost = oldCost, oldLimits })
}

func newVaultEnv(t *testing.T) (*fakeLocal, string) {
	t.Helper()
	useTestCost(t)
	return useFakeLocal(t), filepath.Join(t.TempDir(), "secrets.bin")
}

func mustCreate(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create：%v", err)
	}
	return s
}

func mustSet(t *testing.T, s *Store, name, value string) {
	t.Helper()
	if err := s.Set(name, value); err != nil {
		t.Fatalf("Set(%q)：%v", name, err)
	}
}

func mustPassphrase(t *testing.T, s *Store, passphrase string) string {
	t.Helper()
	code, err := s.SetPassphrase(passphrase)
	if err != nil {
		t.Fatalf("SetPassphrase：%v", err)
	}
	return code
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读密钥文件：%v", err)
	}
	return b
}

func readShape(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(readBytes(t, path), &shape); err != nil {
		t.Fatalf("解析密钥文件：%v", err)
	}
	return shape
}

func writeShape(t *testing.T, path string, shape map[string]json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func wrapsOf(t *testing.T, shape map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var wraps map[string]json.RawMessage
	if err := json.Unmarshal(shape["wraps"], &wraps); err != nil {
		t.Fatalf("解析 wraps：%v", err)
	}
	return wraps
}

func saveWraps(t *testing.T, shape map[string]json.RawMessage, wraps map[string]json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(wraps)
	if err != nil {
		t.Fatal(err)
	}
	shape["wraps"] = b
}

// dropLocalCopy 去掉本机那份（模拟重装系统后文件里没有 DPAPI 包裹）。
func dropLocalCopy(t *testing.T, path string) {
	t.Helper()
	shape := readShape(t, path)
	wraps := wrapsOf(t, shape)
	delete(wraps, "dpapi")
	saveWraps(t, shape, wraps)
	writeShape(t, path, shape)
}

// countKDF 数 scrypt 派生被调了几次；证明超限参数没去算。
func countKDF(t *testing.T) *int {
	t.Helper()
	old := deriveKey
	calls := 0
	deriveKey = func(secret, salt []byte, n, r, p int) ([]byte, error) {
		calls++
		return old(secret, salt, n, r, p)
	}
	t.Cleanup(func() { deriveKey = old })
	return &calls
}

func requireLocked(t *testing.T, err error, wantPassphrase bool) {
	t.Helper()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("应返回 ErrLocked，得到 %v", err)
	}
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatal("锁着的错误应带 LockedError，调用方才能用 errors.As 拿到 HasPassphrase")
	}
	if locked.HasPassphrase != wantPassphrase {
		t.Errorf("HasPassphrase 应为 %t，得到 %t", wantPassphrase, locked.HasPassphrase)
	}
}

// 验收：往返（存、读、删、改名单）。
func TestSetGetDeleteRoundTrip(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	if s.HasPassphrase() {
		t.Error("新建的库不该有主密码")
	}
	if _, ok := s.Get("qq.100.token"); ok {
		t.Error("空库里不该读到名字")
	}
	mustSet(t, s, "qq.100.token", "编的令牌一")
	mustSet(t, s, "webdav.password", "编的口令")
	mustSet(t, s, "empty", "")
	if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌一" {
		t.Errorf("Get 应读到原值，存在=%t、值=%q", ok, value)
	}
	if value, ok := s.Get("empty"); !ok || value != "" {
		t.Error("空字符串也是合法值，bool 应为真")
	}
	mustSet(t, s, "qq.100.token", "改过的令牌")
	if err := s.Delete("webdav.password"); err != nil {
		t.Fatalf("Delete：%v", err)
	}
	if err := s.Delete("webdav.password"); err != nil {
		t.Fatalf("删不存在的名字应什么也不做：%v", err)
	}
	if names := s.Names(); !slices.Equal(names, []string{"empty", "qq.100.token"}) {
		t.Errorf("Names 应排好序，得到 %q", names)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开：%v", err)
	}
	if value, ok := again.Get("qq.100.token"); !ok || value != "改过的令牌" {
		t.Error("重新打开后读到的应是落盘的改后值")
	}
	if names := again.Names(); !slices.Equal(names, []string{"empty", "qq.100.token"}) {
		t.Errorf("重新打开后的名单不对：%q", names)
	}
}

// 验收：本机那份缺 / 解不开 → 主密码解开 → 本机那份包回、再开不用口令。
func TestUnlockPassphraseRewrapsLocalCopy(t *testing.T) {
	passphrase := "正确的主密码一号"
	for _, missing := range []string{"本机那份缺", "本机那份解不开"} {
		t.Run(missing, func(t *testing.T) {
			local, path := newVaultEnv(t)
			s := mustCreate(t, path)
			mustSet(t, s, "qq.100.token", "编的令牌")
			mustPassphrase(t, s, passphrase)
			if missing == "本机那份缺" {
				dropLocalCopy(t, path)
			} else {
				// 换了 Windows 用户 / 重装了系统：文件里的 DPAPI 包裹解不开。
				local.mark = []byte("另一台电脑:")
			}
			if _, err := Open(path); !errors.Is(err, ErrLocked) {
				t.Fatalf("应锁着，得到 %v", err)
			}
			locked, err := Unlock(path, passphrase, Passphrase)
			if err != nil {
				t.Fatalf("Unlock：%v", err)
			}
			if locked.LocalCopyError() != nil {
				t.Fatalf("本机副本应包回去，得到 %v", locked.LocalCopyError())
			}
			if value, ok := locked.Get("qq.100.token"); !ok || value != "编的令牌" {
				t.Error("解锁后应读到原值")
			}
			// 本机那份包回：不用口令再开。
			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("重包后应能直接打开：%v", err)
			}
			if value, ok := reopened.Get("qq.100.token"); !ok || value != "编的令牌" {
				t.Error("重包后读到的值不对")
			}
		})
	}
}

// 验收：恢复码解开（大小写、短横线、空格都不管）。
func TestUnlockWithRecoveryCode(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	code := mustPassphrase(t, s, "正确的主密码一号")
	dropLocalCopy(t, path)
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	unlocked, err := Unlock(path, typed, RecoveryCode)
	if err != nil {
		t.Fatalf("用恢复码解开：%v", err)
	}
	if value, ok := unlocked.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("用恢复码解开后应读到原值")
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("恢复码解开后本机那份应包回去：%v", err)
	}
}

// 验收：主密码错、恢复码错、恢复码格式不对各报对的错，文件字节不变。
func TestUnlockRejectsWrongSecretsWithoutTouchingFile(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	mustPassphrase(t, s, "正确的主密码一号")
	dropLocalCopy(t, path)
	before := readBytes(t, path)
	other, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func() error
		want func(error) bool
	}{
		{"主密码错", func() error {
			_, err := Unlock(path, "错误的主密码二号", Passphrase)
			return err
		}, func(err error) bool { return errors.Is(err, ErrWrongSecret) }},
		{"恢复码错", func() error {
			_, err := Unlock(path, other.display, RecoveryCode)
			return err
		}, func(err error) bool { return errors.Is(err, ErrWrongSecret) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if !tc.want(err) {
				t.Errorf("应报对应的错，得到 %v", err)
			}
			if !bytes.Equal(before, readBytes(t, path)) {
				t.Error("解不开时文件应一个字节都不变")
			}
		})
	}
	t.Run("恢复码格式不对", func(t *testing.T) {
		calls := countKDF(t)
		*calls = 0
		_, err := Unlock(path, "不是有效的恢复码", RecoveryCode)
		if !strings.Contains(err.Error(), "恢复码格式不对") {
			t.Errorf("应报恢复码格式不对，得到 %v", err)
		}
		if *calls != 0 {
			t.Errorf("恢复码格式不对时不该跑 scrypt，跑了 %d 次", *calls)
		}
		if !bytes.Equal(before, readBytes(t, path)) {
			t.Error("解不开时文件应一个字节都不变")
		}
	})
}

// 验收：改主密码后旧恢复码失效、新的能用。
func TestSetPassphraseRotatesRecoveryCode(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	oldCode := mustPassphrase(t, s, "第一个主密码八九")
	newCode := mustPassphrase(t, s, "第二个主密码八九")
	if oldCode == newCode {
		t.Fatal("改主密码应换一个新的恢复码")
	}
	dropLocalCopy(t, path)
	before := readBytes(t, path)
	if _, err := Unlock(path, oldCode, RecoveryCode); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("旧恢复码应失效，得到 %v", err)
	}
	if _, err := Unlock(path, "第一个主密码八九", Passphrase); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("旧主密码应失效，得到 %v", err)
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("输错时文件不应改")
	}
	if _, err := Unlock(path, newCode, RecoveryCode); err != nil {
		t.Errorf("新恢复码应能用：%v", err)
	}
	if _, err := Unlock(path, "第二个主密码八九", Passphrase); err != nil {
		t.Errorf("新主密码应能用：%v", err)
	}
}

// 验收：password 和 recovery 互换包裹解不开（附加数据把两份绑死）。
func TestSwappedWrapsCannotUnlock(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	passphrase := "正确的主密码一号"
	code := mustPassphrase(t, s, passphrase)
	dropLocalCopy(t, path)
	shape := readShape(t, path)
	wraps := wrapsOf(t, shape)
	wraps["password"], wraps["recovery"] = wraps["recovery"], wraps["password"]
	saveWraps(t, shape, wraps)
	writeShape(t, path, shape)
	if _, err := Unlock(path, passphrase, Passphrase); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("互换后主密码不该解开，得到 %v", err)
	}
	if _, err := Unlock(path, code, RecoveryCode); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("互换后恢复码不该解开，得到 %v", err)
	}
}

// 验收：超上限参数报格式错误且不跑 scrypt。
func TestOutOfRangeScryptParamsAreFormatErrorWithoutKDF(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	passphrase := "正确的主密码一号"
	mustPassphrase(t, s, passphrase)
	dropLocalCopy(t, path)
	original := readBytes(t, path)
	cases := []struct {
		name   string
		limits *scryptLimits
		adjust func(map[string]any)
	}{
		{"N 超过 2²⁰", nil, func(w map[string]any) { w["N"] = float64(1 << 21) }},
		// 下界照生产值 2¹⁴ 查；两份包裹都改，免得另一份先被测到。
		{"N 低于 2¹⁴", &scryptLimits{MinN: 1 << 14, MaxN: 1 << 20, MaxR: 16, MaxP: 4}, func(w map[string]any) { w["N"] = float64(1 << 13) }},
		{"N 不是 2 的幂", nil, func(w map[string]any) { w["N"] = float64(1<<10 + 1) }},
		{"r 超过 16", nil, func(w map[string]any) { w["r"] = float64(17) }},
		{"r 为 0", nil, func(w map[string]any) { w["r"] = float64(0) }},
		{"p 超过 4", nil, func(w map[string]any) { w["p"] = float64(5) }},
		{"p 为 0", nil, func(w map[string]any) { w["p"] = float64(0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.limits != nil {
				oldLimits := acceptCost
				acceptCost = *tc.limits
				t.Cleanup(func() { acceptCost = oldLimits })
			}
			calls := countKDF(t)
			*calls = 0
			shape := readShape(t, path)
			wraps := wrapsOf(t, shape)
			for _, key := range []string{"password", "recovery"} {
				var one map[string]any
				if err := json.Unmarshal(wraps[key], &one); err != nil {
					t.Fatal(err)
				}
				tc.adjust(one)
				raw, err := json.Marshal(one)
				if err != nil {
					t.Fatal(err)
				}
				wraps[key] = raw
			}
			saveWraps(t, shape, wraps)
			writeShape(t, path, shape)
			if _, err := Unlock(path, passphrase, Passphrase); !errors.Is(err, ErrFormat) {
				t.Errorf("应报格式错误，得到 %v", err)
			}
			if *calls != 0 {
				t.Errorf("超限参数不该跑 scrypt，跑了 %d 次", *calls)
			}
			// 换回原文件，下一个子测试从干净的库开始。
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// 验收：写到一半（.tmp 写失败、改名失败）原文件不变。
func TestWriteFailureKeepsOriginalFile(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	before := readBytes(t, path)
	cases := []struct {
		name    string
		install func(t *testing.T)
	}{
		{"写 .tmp 失败", func(t *testing.T) {
			old := writeTmp
			writeTmp = func(string, []byte) error { return errors.New("磁盘满了") }
			t.Cleanup(func() { writeTmp = old })
		}},
		{"改名失败", func(t *testing.T) {
			old := renameTmp
			renameTmp = func(string, string) error { return errors.New("文件被占用") }
			t.Cleanup(func() { renameTmp = old })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.install(t)
			if err := s.Set("qq.200.token", "编的令牌二"); err == nil {
				t.Fatal("落盘失败时 Set 应报错")
			}
			if !bytes.Equal(before, readBytes(t, path)) {
				t.Error("写到一半失败时原文件应不变")
			}
			if _, ok := s.Get("qq.200.token"); ok {
				t.Error("落盘失败时内存里的值也应回退")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "secrets.bin" {
					t.Errorf("失败后不该留下临时文件，目录里有 %q", entry.Name())
				}
			}
		})
	}
}

// 返工四：其余错误不重试，立刻失败、文件不变、目录里不留临时文件。
func TestRenameDoesNotRetryOtherErrors(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	before := readBytes(t, path)
	oldRename, oldWait := renameTmp, renameRetryWait
	t.Cleanup(func() { renameTmp, renameRetryWait = oldRename, oldWait })
	calls := 0
	renameTmp = func(from, to string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.Errno(1)}
	}
	renameRetryWait = func(time.Duration) { t.Error("不是可以重试的错误，不该等") }
	if err := s.Set("qq.200.token", "编的令牌二"); err == nil {
		t.Fatal("替换失败时 Set 应报错")
	}
	if calls != 1 {
		t.Errorf("不该重试，rename 调了 %d 次", calls)
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("替换失败时原文件应不变")
	}
	if _, ok := s.Get("qq.200.token"); ok {
		t.Error("失败的值不该留在内存里")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "secrets.bin" {
			t.Errorf("失败后不该留下临时文件，目录里有 %q", entry.Name())
		}
	}
}

// 返工四：目录里已有外来文件（包括像我们的临时文件的）时写入照常成功，外来文件不动。
func TestForeignTempFilesSurviveWrites(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	dir := filepath.Dir(path)
	foreign := map[string][]byte{
		"secrets.bin.tmp":              []byte("外来的临时文件"),
		"secrets.bin.tmp-0123456789ab": []byte("像我们的临时文件"),
	}
	for name, content := range foreign {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustSet(t, s, "qq.100.token", "编的令牌")
	for name, content := range foreign {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("写完之后外来文件 %q 不该消失：%v", name, err)
			continue
		}
		if !bytes.Equal(got, content) {
			t.Errorf("外来文件 %q 的内容不该被改", name)
		}
	}
}

// 验收：错误字符串里搜不到测试用的值、主密码、恢复码。
func TestErrorsNeverLeakSecrets(t *testing.T) {
	const value = "编的令牌-绝不能出现在错误里"
	const passphrase = "主密码-绝不能出现在错误里"
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", value)
	code := mustPassphrase(t, s, passphrase)
	dropLocalCopy(t, path)
	lockAndCheck := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("这里应该有错误")
		}
		text := err.Error()
		for _, secret := range []string{value, passphrase, code, strings.ReplaceAll(code, "-", "")} {
			if strings.Contains(text, secret) {
				t.Errorf("错误里出现了密钥内容：%q", text)
			}
		}
	}
	_, err := Unlock(path, "错误的主密码二号", Passphrase)
	lockAndCheck(err)
	other, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Unlock(path, other.display, RecoveryCode)
	lockAndCheck(err)
	_, err = Unlock(path, "不是有效的恢复码", RecoveryCode)
	lockAndCheck(err)
	lockAndCheck(s.Set("名字 里有空格", value))
	lockAndCheck(s.Set("qq.300.token", strings.Repeat("值", maxValueBytes/len("值")+1)))
	badPath := filepath.Join(filepath.Dir(path), "坏.bin")
	if err := os.WriteFile(badPath, []byte(`{"v":1,"wraps":{"dpapi":"`+value), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(badPath)
	lockAndCheck(err)
}

// 验收：锁着时错误里带“能不能用主密码 / 恢复码”（HasPassphrase）。
func TestLockedErrorCarriesHasPassphrase(t *testing.T) {
	local, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	mustPassphrase(t, s, "正确的主密码一号")
	dropLocalCopy(t, path)
	_, err := Open(path)
	requireLocked(t, err, true)

	plainPath := filepath.Join(t.TempDir(), "secrets.bin")
	plain := mustCreate(t, plainPath)
	mustSet(t, plain, "qq.100.token", "编的令牌")
	local.mark = []byte("另一台电脑:")
	_, err = Open(plainPath)
	requireLocked(t, err, false)
	// 非 Windows（本机加密不可用）也只有本机那份时锁着，HasPassphrase 为假。
	local.unprotectErr = ErrUnsupported
	_, err = Open(plainPath)
	requireLocked(t, err, false)
}

// 规格：本机加密不可用（非 Windows）时不能在没有主密码的情况下落盘。
func TestCreateWithoutLocalEncryptionNeedsPassphrase(t *testing.T) {
	local := useFakeLocal(t)
	useTestCost(t)
	local.protectErr = ErrUnsupported
	local.unprotectErr = ErrUnsupported
	path := filepath.Join(t.TempDir(), "secrets.bin")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create：%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("没有本机加密又没主密码时不该落盘")
	}
	if err := s.Set("qq.100.token", "编的令牌"); !errors.Is(err, ErrNeedPassphrase) {
		t.Errorf("应先要主密码，得到 %v", err)
	}
	code := mustPassphrase(t, s, "这台电脑的主密码")
	mustSet(t, s, "qq.100.token", "编的令牌")
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Errorf("没有本机那份时应锁着，得到 %v", err)
	}
	unlocked, err := Unlock(path, "这台电脑的主密码", Passphrase)
	if err != nil {
		t.Fatalf("用主密码解开：%v", err)
	}
	if value, ok := unlocked.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("解开后应读到原值")
	}
	if unlocked.LocalCopyError() != nil {
		t.Errorf("非 Windows 只有主密码那一份，不算没包上：%v", unlocked.LocalCopyError())
	}
	if _, err := Unlock(path, code, RecoveryCode); err != nil {
		t.Errorf("恢复码也应能用：%v", err)
	}
}

// 返工四：我们以为文件不存在时它已被人建好，SetPassphrase 也要拒写。
func TestSetPassphraseRefusesWhenFileAppeared(t *testing.T) {
	local := useFakeLocal(t)
	useTestCost(t)
	// 先趁本机加密可用，造一份外面的文件。
	other := filepath.Join(t.TempDir(), "other", "secrets.bin")
	external := mustCreate(t, other)
	mustSet(t, external, "qq.200.token", "外面的值")
	externalBytes := readBytes(t, other)

	local.protectErr = ErrUnsupported
	local.unprotectErr = ErrUnsupported
	path := filepath.Join(t.TempDir(), "secrets.bin")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create：%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("没有本机加密又没主密码时不该落盘")
	}
	if err := os.WriteFile(path, externalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPassphrase("这台电脑的主密码"); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("文件被外面建好时该拒写，得到 %v", err)
	}
	if !bytes.Equal(externalBytes, readBytes(t, path)) {
		t.Error("拒绝的写不该改文件")
	}
	if s.HasPassphrase() {
		t.Error("拒绝的主密码不该留在对象里")
	}
}

// 规格：Create 不覆盖已存在的库。
func TestCreateKeepsExistingVault(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	again, err := Create(path)
	if err != nil {
		t.Fatalf("Create 已存在的库：%v", err)
	}
	if value, ok := again.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("Create 不该新建空库顶掉原有的")
	}
}

// 复核第 4 轮：两个 goroutine 同时 Create 同一个路径，两边都拿到能用的库，之后 Set 都成功。
// 先拿到锁的那个建好文件；后一个进锁后看到文件已经在了，照 Open 沿用，不能把先建好的库卸载。
func TestConcurrentCreateSamePath(t *testing.T) {
	_, path := newVaultEnv(t)
	const n = 2
	stores := make([]*Store, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			stores[i], errs[i] = Create(path)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range stores {
		if errs[i] != nil {
			t.Fatalf("第 %d 个 Create：%v", i, errs[i])
		}
	}
	if stores[1] != stores[0] {
		t.Error("同一路径的 Create 应拿到同一个库对象")
	}
	for i, s := range stores {
		if err := s.Set(fmt.Sprintf("qq.%d.token", i), fmt.Sprintf("值%d", i)); err != nil {
			t.Errorf("第 %d 个 Set：%v", i, err)
		}
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开：%v", err)
	}
	if names := again.Names(); !slices.Equal(names, []string{"qq.0.token", "qq.1.token"}) {
		t.Errorf("两个值都应落盘，得到 %q", names)
	}
}


func TestCreateRebuildsDeletedFile(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "旧值")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删了文件后 Open 应报 ErrNotFound，得到 %v", err)
	}
	rebuilt, err := Create(path)
	if err != nil {
		t.Fatalf("Create 应重建：%v", err)
	}
	if rebuilt != s {
		t.Fatal("Create 应在同一个已登记对象上重建")
	}
	if names := rebuilt.Names(); len(names) != 0 {
		t.Errorf("重建后应是空库，得到 %q", names)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("本机加密可用时 Create 应落盘：%v", err)
	}
	mustSet(t, rebuilt, "qq.200.token", "新值")
	if value, ok := rebuilt.Get("qq.200.token"); !ok || value != "新值" {
		t.Error("重建后应能写入并读到新值")
	}
	if _, ok := rebuilt.Get("qq.100.token"); ok {
		t.Error("旧快照不该写回")
	}
}

// 复核第 4 轮：resetLocked 里本机加密 panic 后，对象退回没加载——持有人写不进去、
// Open 报没有文件、再 Create 能在同一个对象上重建。
func TestCreateRecoversAfterLocalPanic(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "旧值")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	oldProtect := protectLocal
	protectLocal = func([]byte) ([]byte, error) { panic("本机加密坏了") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("本机加密 panic 应传出来")
			}
		}()
		_, _ = Create(path)
	}()
	protectLocal = oldProtect

	if err := s.Set("qq.200.token", "新值"); !errors.Is(err, ErrChangedOnDisk) {
		t.Errorf("卸载后的 Set 应报 ErrChangedOnDisk，得到 %v", err)
	}
	if err := s.Delete("qq.100.token"); !errors.Is(err, ErrChangedOnDisk) {
		t.Errorf("卸载后的 Delete 应报 ErrChangedOnDisk，得到 %v", err)
	}
	if _, err := s.SetPassphrase("新的主密码一号呀"); !errors.Is(err, ErrChangedOnDisk) {
		t.Errorf("卸载后的 SetPassphrase 应报 ErrChangedOnDisk，得到 %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("panic 后文件不该建出来，Open 应报 ErrNotFound，得到 %v", err)
	}
	rebuilt, err := Create(path)
	if err != nil {
		t.Fatalf("再 Create 应能重建：%v", err)
	}
	if rebuilt != s {
		t.Error("应在同一个已登记对象上重建")
	}
	mustSet(t, rebuilt, "qq.300.token", "重建后的值")
	if names := rebuilt.Names(); !slices.Equal(names, []string{"qq.300.token"}) {
		t.Errorf("重建后应是空库加新值，得到 %q", names)
	}
}


func TestSetRejectsInvalidNamesAndValues(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	for _, name := range []string{"", "名字", "有 空格", "有/斜杠", strings.Repeat("a", 129)} {
		if err := s.Set(name, "值"); err == nil {
			t.Errorf("名字 %q 应被拒绝", name)
		}
	}
	for _, name := range []string{strings.Repeat("a", 128), "qq.100.token", "a_b-c.d", "Up9"} {
		if err := s.Set(name, "值"); err != nil {
			t.Errorf("名字 %q 应能存：%v", name, err)
		}
	}
	big := strings.Repeat("x", maxValueBytes)
	if err := s.Set("big", big); err != nil {
		t.Errorf("64 KB 的值应能存：%v", err)
	}
	if err := s.Set("bigger", big+"x"); err == nil {
		t.Error("超过 64 KB 的值应被拒绝")
	}
}

// 规格：重包本机副本写失败不影响这次解开，结果里说明。
func TestUnlockReportsLocalCopyFailureButStillUnlocks(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	passphrase := "正确的主密码一号"
	mustPassphrase(t, s, passphrase)
	dropLocalCopy(t, path)
	before := readBytes(t, path)
	oldRename, oldWrite := renameTmp, writeTmp
	renameTmp = func(string, string) error { return errors.New("文件被占用") }
	t.Cleanup(func() { renameTmp, writeTmp = oldRename, oldWrite })
	unlocked, err := Unlock(path, passphrase, Passphrase)
	if err != nil {
		t.Fatalf("写回失败不该让这次解开失败：%v", err)
	}
	if unlocked.LocalCopyError() == nil {
		t.Error("本机副本没包上时应在结果里说明")
	}
	if value, ok := unlocked.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("解锁后应读到原值")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("写回失败时文件应不变")
	}
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Errorf("文件里还是没有本机那份，应锁着，得到 %v", err)
	}
}

// 规格：不认识的包裹不挡读写，写回时原样保留。
func TestUnknownWrapsSurviveWrites(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	shape := readShape(t, path)
	wraps := wrapsOf(t, shape)
	legacy := json.RawMessage(`{"kdf":"scrypt","N":131072,"r":8,"p":1,"salt":"x","iv":"y","tag":"z","ct":"w"}`)
	wraps["future"] = legacy
	saveWraps(t, shape, wraps)
	// 另写一份再打开：同一个路径已经登记着解开的库、不会再读文件，这里要验的是一次新的解析。
	fresh := filepath.Join(t.TempDir(), "secrets.bin")
	writeShape(t, fresh, shape)

	opened, err := Open(fresh)
	if err != nil {
		t.Fatalf("带不认识的包裹也应能打开：%v", err)
	}
	mustSet(t, opened, "qq.200.token", "编的令牌二")
	kept := wrapsOf(t, readShape(t, fresh))["future"]
	if !bytes.Equal(kept, legacy) {
		t.Errorf("写回时不该改动不认识的包裹，得到 %s", kept)
	}
}

// legacyFixture 把旧守护进程写的夹具拷到临时目录（不动 testdata 原文件），
// 返回副本路径和 .txt 里记的主密码、恢复码。
func legacyFixture(t *testing.T) (path, password, code string) {
	t.Helper()
	bin, err := os.ReadFile(filepath.Join("testdata", "legacy-daemon-locked.bin"))
	if err != nil {
		t.Fatalf("读夹具：%v", err)
	}
	text, err := os.ReadFile(filepath.Join("testdata", "legacy-daemon-locked.bin.txt"))
	if err != nil {
		t.Fatalf("读夹具的口令：%v", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			switch key {
			case "password":
				password = value
			case "recovery":
				code = value
			}
		}
	}
	if password == "" || code == "" {
		t.Fatal("夹具的 .txt 里应有一行 password= 和一行 recovery=")
	}
	path = filepath.Join(t.TempDir(), "secrets.bin")
	if err := os.WriteFile(path, bin, 0o600); err != nil {
		t.Fatalf("复制夹具：%v", err)
	}
	return path, password, code
}

// allowLegacyScryptN 把读取下限临时放到 N = 2¹⁰：夹具是旧守护进程用测试参数生成的。
func allowLegacyScryptN(t *testing.T) {
	t.Helper()
	old := acceptCost
	acceptCost.MinN = 1 << 10
	t.Cleanup(func() { acceptCost = old })
}

// 验收：旧守护进程写的文件（N = 1024、password / recovery 两份、没有本机那份）能解锁。
func TestLegacyDaemonFixtureUnlocks(t *testing.T) {
	useFakeLocal(t)
	allowLegacyScryptN(t)
	path, password, code := legacyFixture(t)
	requireLocked(t, mustOpenErr(t, path), true)
	cases := []struct {
		name   string
		secret string
		kind   Kind
	}{
		{"主密码", password, Passphrase},
		{"恢复码", code, RecoveryCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Unlock(path, tc.secret, tc.kind)
			if err != nil {
				t.Fatalf("Unlock：%v", err)
			}
			for name, value := range fixtureValues {
				got, ok := s.Get(name)
				if !ok || got != value {
					t.Errorf("Get(%q) 应读到夹具里的值，存在=%t、值一致=%t", name, ok, got == value)
				}
			}
		})
	}
	// 半角写法：NFC 原样保留，只有 NFKC 才归一到当初派生口令的样子。
	halfWidth := strings.ReplaceAll(password, "ｃｏｒｒｅｃｔ", "correct")
	if halfWidth == password {
		t.Fatal("夹具主密码里应有全角字母，半角写法才能证明做了 NFKC")
	}
	if _, err := Unlock(path, halfWidth, Passphrase); err != nil {
		t.Errorf("半角写法应经 NFKC 归一到同一个口令：%v", err)
	}
}

// 验收：解开旧文件、写进一个新名字后，写回的文件用主密码、恢复码都还能开。
func TestLegacyDaemonFixtureRoundTrip(t *testing.T) {
	useFakeLocal(t)
	allowLegacyScryptN(t)
	path, password, code := legacyFixture(t)
	s, err := Unlock(path, password, Passphrase)
	if err != nil {
		t.Fatalf("Unlock：%v", err)
	}
	mustSet(t, s, "webdav.password", "编造的 WebDAV 口令")
	want := map[string]string{
		"qq.10001.token":       fixtureValues["qq.10001.token"],
		"provider.example.key": fixtureValues["provider.example.key"],
		"webdav.password":      "编造的 WebDAV 口令",
	}
	cases := []struct {
		name   string
		secret string
		kind   Kind
	}{
		{"主密码", password, Passphrase},
		{"恢复码", code, RecoveryCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Unlock(path, tc.secret, tc.kind)
			if err != nil {
				t.Fatalf("写回后用 %s 解开：%v", tc.name, err)
			}
			if names := s.Names(); !slices.Equal(names, []string{"provider.example.key", "qq.10001.token", "webdav.password"}) {
				t.Errorf("三个名字都应在，得到 %q", names)
			}
			for name, value := range want {
				got, ok := s.Get(name)
				if !ok || got != value {
					t.Errorf("Get(%q) 应读到写回后的值，存在=%t、值一致=%t", name, ok, got == value)
				}
			}
		})
	}
}

// fixtureValues 是夹具里两个密钥的名字和值（值来自生成夹具时用的才是编造的样本）。
var fixtureValues = map[string]string{
	"qq.10001.token":       "编造的令牌-abc123",
	"provider.example.key": "sk-test-编造",
}

// mustOpenErr 返回 Open 的错误；能打开就没法测“锁着”。
func mustOpenErr(t *testing.T, path string) error {
	t.Helper()
	s, err := Open(path)
	if s != nil {
		t.Fatal("夹具没有本机那份，Open 不该成功")
	}
	if err == nil {
		t.Fatal("夹具没有本机那份，Open 应返回错误")
	}
	return err
}

// forgeVault 照 5.3.3 的文件格式写一份“本机那份 + 主密码 + 恢复码”的库，用来造 Go 自己不会
// 写出的旧文件（例如不到 8 个字符的主密码）。
func forgeVault(t *testing.T, path, passphrase, recoverySecret string, values map[string]string) {
	t.Helper()
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	data, err := sealData(dek, plain)
	if err != nil {
		t.Fatal(err)
	}
	local, err := protectLocal(dek)
	if err != nil {
		t.Fatal(err)
	}
	passWrap, err := sealWrap([]byte(passphrase), aadPassword, dek)
	if err != nil {
		t.Fatal(err)
	}
	recWrap, err := sealWrap([]byte(recoverySecret), aadRecovery, dek)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"v": 1,
		"wraps": map[string]any{
			"dpapi":    base64.StdEncoding.EncodeToString(local),
			"password": passWrap,
			"recovery": recWrap,
		},
		"data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// failRenames 让改临时文件那一一步失败、返回提前恢复的函数；验证写回失败时内存和文件都回退。
func failRenames(t *testing.T) func() {
	t.Helper()
	old := renameTmp
	renameTmp = func(string, string) error { return errors.New("文件被占用") }
	t.Cleanup(func() { renameTmp = old })
	return func() { renameTmp = old }
}

// 复核第 1 条：打印 *Store 不能带出值、主密码、恢复码或数据密钥。
func TestStorePrintingNeverLeaksSecrets(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	const value = "编的令牌-打印绝不能出现"
	const passphrase = "主密码-打印绝不能出现"
	mustSet(t, s, "qq.100.token", value)
	code := mustPassphrase(t, s, passphrase)
	dek := base64.StdEncoding.EncodeToString(s.dek)
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("测试", "store", s)
	printed := fmt.Sprintf("%v|%+v|%#v|%s|%q|%s", s, s, s, s, s, logged.String())
	for _, secret := range []string{value, passphrase, code, strings.ReplaceAll(code, "-", ""), dek} {
		if strings.Contains(printed, secret) {
			t.Errorf("打印带出了密钥内容：%q", printed)
		}
	}
	if got := fmt.Sprintf("%v", s); got != storeLabel {
		t.Errorf("%%v 应只给出 %q，得到 %q", storeLabel, got)
	}
	if !strings.Contains(logged.String(), storeLabel) {
		t.Errorf("日志里应只有 %q，得到 %q", storeLabel, logged.String())
	}
}

// 复核第 2 条：Unlock 只做 NFKC、拒绝空串，不按长度拒——旧主密码可能不到 8 个字符。
func TestUnlockAcceptsShortOldPassphrase(t *testing.T) {
	_, path := newVaultEnv(t)
	const passphrase = "😀😀😀😀"
	if utf8.RuneCountInString(passphrase) >= minPassChars {
		t.Fatal("这个主密码要短于设主密码的下限，才能证明 Unlock 不按长度拒")
	}
	forgeVault(t, path, passphrase, strings.Repeat("Q", recoveryChars), map[string]string{"qq.100.token": "编的令牌"})
	calls := countKDF(t)
	*calls = 0
	if _, err := Unlock(path, "", Passphrase); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("空主密码应报错，得到 %v", err)
	}
	if *calls != 0 {
		t.Errorf("空主密码不该跑 scrypt，跑了 %d 次", *calls)
	}
	s, err := Unlock(path, passphrase, Passphrase)
	if err != nil {
		t.Fatalf("4 个 emoji 的旧主密码应能解开：%v", err)
	}
	if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("解开后应读到原值")
	}
	before := readBytes(t, path)
	if _, err := s.SetPassphrase(passphrase); err == nil {
		t.Error("设主密码时仍应查最短长度")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("设主密码被拒时文件应不变")
	}
}

// 复核第 6 条：本机那份好时，password / recovery 的参数或长度不合规不挡读，原样保留、
// 写回不改；Unlock 用到那一份时才报格式错误。
func TestBrokenKDFWrapDoesNotBlockLocalCopy(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	const passphrase = "编造的主密码八九"
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"N 越界", func(w map[string]any) { w["N"] = float64(1 << 21) }},
		{"iv 长度不对", func(w map[string]any) { w["iv"] = b64(make([]byte, 11)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, path := newVaultEnv(t)
			forgeVault(t, path, passphrase, strings.Repeat("Q", recoveryChars), map[string]string{"qq.100.token": "编的令牌"})
			shape := readShape(t, path)
			wraps := wrapsOf(t, shape)
			var broken map[string]any
			if err := json.Unmarshal(wraps["password"], &broken); err != nil {
				t.Fatal(err)
			}
			tc.change(broken)
			raw, err := json.Marshal(broken)
			if err != nil {
				t.Fatal(err)
			}
			wraps["password"] = raw
			saveWraps(t, shape, wraps)
			writeShape(t, path, shape)

			opened, err := Open(path)
			if err != nil {
				t.Fatalf("本机那份好时 Open 不该受坏包裹影响：%v", err)
			}
			if value, ok := opened.Get("qq.100.token"); !ok || value != "编的令牌" {
				t.Error("坏包裹不该挡住本机副本里的值")
			}
			mustSet(t, opened, "webdav.password", "编的口令")
			if kept := wrapsOf(t, readShape(t, path))["password"]; !bytes.Equal(kept, raw) {
				t.Errorf("坏包裹写回时应一个字节都不改，得到 %s", kept)
			}
			if _, err := Unlock(path, passphrase, Passphrase); !errors.Is(err, ErrFormat) {
				t.Errorf("用到坏包裹时应报格式错误，得到 %v", err)
			}
			if _, err := Unlock(path, strings.Repeat("Q", recoveryChars), RecoveryCode); err != nil {
				t.Errorf("另一份好的包裹不该受连累：%v", err)
			}

			// 只有一边（有 password 没 recovery）照旧算格式错误。
			delete(wraps, "recovery")
			saveWraps(t, shape, wraps)
			writeShape(t, path, shape)
			if _, err := Open(path); !errors.Is(err, ErrFormat) {
				t.Errorf("只有 password 没有 recovery 应算格式错误，得到 %v", err)
			}
		})
	}
}

// 复核第 3 条：不合法的 UTF-8 值直接拒绝，不改内存也不改文件。
func TestSetRejectsInvalidUTF8(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	before := readBytes(t, path)
	if err := s.Set("qq.100.token", string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("不合法的 UTF-8 应被拒绝")
	}
	if _, ok := s.Get("qq.100.token"); ok {
		t.Error("被拒的值不该留在内存里")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("被拒的值不该改文件")
	}
	if err := s.Set("qq.100.token", "编的令牌😀"); err != nil {
		t.Errorf("合法 UTF-8 应能存：%v", err)
	}
}

// 复核第 4 条：两个 goroutine 各 Open 一次同一路径拿到同一个库，并发写不同的名字谁也不丢。
func TestConcurrentOpenSharesOneStore(t *testing.T) {
	_, path := newVaultEnv(t)
	mustCreate(t, path)
	var wg sync.WaitGroup
	opened := make([]*Store, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range opened {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			opened[i], errs[i] = Open(path)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个 Open：%v", i, err)
		}
	}
	if opened[0] != opened[1] {
		t.Fatal("同一路径两次 Open 应拿到同一个库")
	}
	names := []string{"qq.100.token", "qq.200.token"}
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			if err := opened[i].Set(name, "值-"+name); err != nil {
				t.Errorf("Set(%q)：%v", name, err)
			}
		}(i, name)
	}
	wg.Wait()
	// 直接从磁盘读：两个名字都在，丢掉一个就是两个库各写了一份。
	v, err := load(path)
	if err != nil {
		t.Fatalf("读文件：%v", err)
	}
	dek, err := unprotectLocal(v.dpapi)
	if err != nil {
		t.Fatalf("解本机那份：%v", err)
	}
	values, err := openData(dek, v.data)
	if err != nil {
		t.Fatalf("解数据：%v", err)
	}
	for _, name := range names {
		if value, ok := values[name]; !ok || value != "值-"+name {
			t.Errorf("磁盘上应有 %q，得到 %q", name, values[name])
		}
	}
}

// 复核第 4 条：整个包在 -race 下并发 Set / Get / Names / Delete 不报错、不丢名字。
func TestConcurrentSetGetNamesDelete(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("qq.%d.token", i)
			for round := 0; round < 5; round++ {
				value := fmt.Sprintf("值-%d-%d", i, round)
				if err := s.Set(name, value); err != nil {
					t.Errorf("Set(%q)：%v", name, err)
					return
				}
				if got, ok := s.Get(name); !ok || got != value {
					t.Errorf("Get(%q) 应读到自己刚写的值，存在=%t", name, ok)
					return
				}
				s.Names()
				if err := s.Delete(name); err != nil {
					t.Errorf("Delete(%q)：%v", name, err)
					return
				}
				if _, ok := s.Get(name); ok {
					t.Errorf("%q 删掉后不该再读到", name)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if names := s.Names(); len(names) != 0 {
		t.Errorf("并发跑完应没有剩下的名字，得到 %q", names)
	}
}

// 复核第 5 条：单项上限都过、128·N·r 超内存上限的组合报格式错误，不跑 scrypt。
func TestScryptMemoryLimitIsFormatErrorWithoutKDF(t *testing.T) {
	_, path := newVaultEnv(t)
	const passphrase = "编造的主密码八九"
	forgeVault(t, path, passphrase, strings.Repeat("Q", recoveryChars), map[string]string{"qq.100.token": "编的令牌"})
	shape := readShape(t, path)
	wraps := wrapsOf(t, shape)
	var broken map[string]any
	if err := json.Unmarshal(wraps["password"], &broken); err != nil {
		t.Fatal(err)
	}
	// N = 2²⁰ 过了单项上限（测试环境 MaxN = 2²⁰）、r = 8、p = 1，内存 128·N·r = 1 GiB。
	broken["N"] = float64(1 << 20)
	if broken["r"] != float64(8) || broken["p"] != float64(1) {
		t.Fatalf("测试前提变了：r=%v、p=%v", broken["r"], broken["p"])
	}
	raw, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	wraps["password"] = raw
	saveWraps(t, shape, wraps)
	writeShape(t, path, shape)

	calls := countKDF(t)
	*calls = 0
	if _, err := Unlock(path, passphrase, Passphrase); !errors.Is(err, ErrFormat) {
		t.Errorf("超内存上限应报格式错误，得到 %v", err)
	}
	if *calls != 0 {
		t.Errorf("超上限参数不该跑 scrypt，跑了 %d 次", *calls)
	}
}

// 复核第 5 条：同一时间只跑一个 scrypt——两个不同路径同时 Unlock，替身记同时活动数。
func TestScryptDerivationIsSerialized(t *testing.T) {
	_, pathA := newVaultEnv(t)
	pathB := filepath.Join(filepath.Dir(pathA), "secrets-b.bin")
	for _, path := range []string{pathA, pathB} {
		s := mustCreate(t, path)
		mustSet(t, s, "qq.100.token", "编的令牌")
		mustPassphrase(t, s, "正确的主密码一号")
		dropLocalCopy(t, path)
	}
	var active, peak int32
	entered := make(chan struct{}, 2)
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	old := deriveKey
	deriveKey = func(secret, salt []byte, n, r, p int) ([]byte, error) {
		current := atomic.AddInt32(&active, 1)
		for {
			seen := atomic.LoadInt32(&peak)
			if current <= seen || atomic.CompareAndSwapInt32(&peak, seen, current) {
				break
			}
		}
		entered <- struct{}{}
		<-gate
		atomic.AddInt32(&active, -1)
		return old(secret, salt, n, r, p)
	}
	t.Cleanup(func() { deriveKey = old })

	var wg sync.WaitGroup
	for _, path := range []string{pathA, pathB} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if _, err := Unlock(path, "正确的主密码一号", Passphrase); err != nil {
				t.Errorf("Unlock(%s)：%v", filepath.Base(path), err)
			}
		}(path)
	}
	<-entered // 第一个已经在跑 scrypt
	if scryptMu.TryLock() {
		scryptMu.Unlock()
		t.Error("scrypt 没有串行：第一个还在跑，锁却是空的")
	}
	release() // 放行第一个
	<-entered // 第二个这时才该进来
	wg.Wait()
	if got := atomic.LoadInt32(&peak); got > 1 {
		t.Errorf("同一时间最多应只跑一个 scrypt，最多同时 %d 个", got)
	}
}

// 返工四：登记表的锁不包着建库——A 的 Unlock 卡在 scrypt 里时 B 的 Open 照常返回。
func TestRegistryLockDoesNotBlockOnScrypt(t *testing.T) {
	_, base := newVaultEnv(t)
	dir := filepath.Dir(base)
	// A 的路径不登记对象：Unlock 第一次打开它，读文件、跑 scrypt 都在登记锁外做。
	pathA := filepath.Join(dir, "secrets-a.bin")
	pathB := filepath.Join(dir, "secrets-b.bin")
	freshA := mustCreate(t, filepath.Join(t.TempDir(), "secrets.bin"))
	mustSet(t, freshA, "qq.100.token", "编的令牌")
	mustPassphrase(t, freshA, "正确的主密码一号")
	dropLocalCopy(t, freshA.path)
	if err := os.WriteFile(pathA, readBytes(t, freshA.path), 0o600); err != nil {
		t.Fatal(err)
	}
	b := mustCreate(t, pathB)
	mustSet(t, b, "qq.200.token", "编的令牌二")

	entered := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	var enteredOnce sync.Once
	old := deriveKey
	deriveKey = func(secret, salt []byte, n, r, p int) ([]byte, error) {
		enteredOnce.Do(func() { close(entered) })
		<-gate
		return old(secret, salt, n, r, p)
	}
	t.Cleanup(func() { deriveKey = old })

	unlocked := make(chan error, 1)
	go func() {
		_, err := Unlock(pathA, "正确的主密码一号", Passphrase)
		unlocked <- err
	}()
	<-entered // A 已在跑 scrypt；登记表的锁若还握着，B 就动不了
	opened := make(chan *Store, 1)
	openedErr := make(chan error, 1)
	go func() {
		s, err := Open(pathB)
		if err != nil {
			openedErr <- err
			return
		}
		opened <- s
	}()
	select {
	case s := <-opened:
		if value, ok := s.Get("qq.200.token"); !ok || value != "编的令牌二" {
			t.Error("B 应读到自己的值")
		}
	case err := <-openedErr:
		t.Fatalf("Open(B)：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Open(B) 被 A 的 scrypt 挡住了：登记表的锁不该包着建库")
	}
	release()
	if err := <-unlocked; err != nil {
		t.Fatalf("Unlock(A)：%v", err)
	}
}

// 返工五：第一次加载 panic 时锁照常放开，同一路径后来的 Open 还能再试，不被永久卡住。
func TestPanicDuringLoadDoesNotWedgePath(t *testing.T) {
	_, path := newVaultEnv(t)
	// 目标路径不能在登记表里：先在一份别的文件上建好内容，再拷过去。
	other := filepath.Join(t.TempDir(), "secrets.bin")
	source := mustCreate(t, other)
	mustSet(t, source, "qq.100.token", "编的令牌")
	if err := os.WriteFile(path, readBytes(t, other), 0o600); err != nil {
		t.Fatal(err)
	}

	old := unprotectLocal
	var once sync.Once
	unprotectLocal = func(b []byte) ([]byte, error) {
		once.Do(func() { panic("注入：第一次加载时崩") })
		return old(b)
	}
	t.Cleanup(func() { unprotectLocal = old })

	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _ = Open(path)
	}()
	select {
	case got := <-panicked:
		if got == nil {
			t.Fatal("注入的 panic 没发生")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("第一次 Open 没走到 panic")
	}

	opened := make(chan *Store, 1)
	openedErr := make(chan error, 1)
	go func() {
		s, err := Open(path)
		if err != nil {
			openedErr <- err
			return
		}
		opened <- s
	}()
	select {
	case s := <-opened:
		if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
			t.Errorf("panic 后重试应读到原值，得到 %q、存在=%t", value, ok)
		}
	case err := <-openedErr:
		t.Fatalf("panic 后同一路径的 Open 应能重试：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("panic 后同一路径的 Open 永久阻塞：加载的锁没在 panic 时放开")
	}
}

// 返工五：一个调用输错口令卡在 scrypt 里，同一路径的 Open 不受它的错误影响，自己拿到能用的库。
func TestConcurrentCallsGetOwnErrors(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	mustPassphrase(t, s, "正确的主密码一号")

	entered := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	var enteredOnce sync.Once
	old := deriveKey
	deriveKey = func(secret, salt []byte, n, r, p int) ([]byte, error) {
		enteredOnce.Do(func() { close(entered) })
		<-gate
		return old(secret, salt, n, r, p)
	}
	t.Cleanup(func() { deriveKey = old })

	unlocked := make(chan error, 1)
	go func() {
		_, err := Unlock(path, "错误的主密码八九", Passphrase)
		unlocked <- err
	}()
	<-entered // 错口令的 Unlock 已经在跑 scrypt、拿着这个路径的锁

	opened := make(chan *Store, 1)
	openedErr := make(chan error, 1)
	go func() {
		s, err := Open(path)
		if err != nil {
			openedErr <- err
			return
		}
		opened <- s
	}()
	release()
	if err := <-unlocked; !errors.Is(err, ErrWrongSecret) {
		t.Fatalf("错口令的 Unlock 应报 ErrWrongSecret，得到 %v", err)
	}
	select {
	case s := <-opened:
		if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
			t.Errorf("Open 应拿到能用的库，得到 %q、存在=%t", value, ok)
		}
	case err := <-openedErr:
		t.Fatalf("Open 拿到了别人调用留下的错误：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Open 卡住了")
	}
}

// 返工五：第一个调用还在加载时，第二个调用不会提前拿到空壳或 nil 错误，要等到加载好的库。
func TestSecondCallerWaitsForFirst(t *testing.T) {
	_, path := newVaultEnv(t)
	// 目标路径不在登记表里：另一个文件建好后拷过去，第一次 Open 会在加载里卡住。
	other := filepath.Join(t.TempDir(), "secrets.bin")
	source := mustCreate(t, other)
	mustSet(t, source, "qq.100.token", "编的令牌")
	if err := os.WriteFile(path, readBytes(t, other), 0o600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	defer release()
	var once sync.Once
	old := unprotectLocal
	unprotectLocal = func(b []byte) ([]byte, error) {
		once.Do(func() {
			close(entered)
			<-gate
		})
		return old(b)
	}
	t.Cleanup(func() { unprotectLocal = old })

	first := make(chan *Store, 1)
	firstErr := make(chan error, 1)
	go func() {
		s, err := Open(path)
		if err != nil {
			firstErr <- err
			return
		}
		first <- s
	}()
	<-entered

	second := make(chan *Store, 1)
	secondErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		s, err := Open(path)
		if err != nil {
			secondErr <- err
			return
		}
		second <- s
	}()
	<-started
	select {
	case s := <-second:
		t.Fatalf("第一个调用还没加载完，第二个调用就拿到了库：%v", s)
	case err := <-secondErr:
		t.Fatalf("第一个调用还没加载完，第二个调用就返回了错误：%v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case s := <-first:
		if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
			t.Errorf("第一个 Open 应读到原值，得到 %q、存在=%t", value, ok)
		}
	case err := <-firstErr:
		t.Fatalf("第一个 Open：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("第一个 Open 卡住了")
	}
	select {
	case s := <-second:
		if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
			t.Errorf("第二个调用应等到加载好的库，得到 %q、存在=%t", value, ok)
		}
	case err := <-secondErr:
		t.Fatalf("第二个调用应等到加载好的库：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("第二个调用没等到加载完")
	}
}

// 返工五：重建空库途中文件又被外面建好，Create 报 ErrChangedOnDisk、不覆盖，对象退回没加载；
// 随后 Open 读到外面那份。
func TestRecreateWhenFileReappears(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "旧值")
	externalPath := filepath.Join(t.TempDir(), "secrets.bin")
	external := mustCreate(t, externalPath)
	mustSet(t, external, "qq.100.token", "外面的值")
	externalBytes := readBytes(t, externalPath)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	oldProtect := protectLocal
	protectLocal = func(b []byte) ([]byte, error) {
		wrapped, err := oldProtect(b)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, externalBytes, 0o600); err != nil {
			t.Errorf("注入写外部文件：%v", err)
		}
		return wrapped, nil
	}
	t.Cleanup(func() { protectLocal = oldProtect })

	if _, err := Create(path); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("重建途中文件回来应报 ErrChangedOnDisk，得到 %v", err)
	}
	if !bytes.Equal(externalBytes, readBytes(t, path)) {
		t.Error("Create 不该覆盖外面的文件")
	}

	protectLocal = oldProtect
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	if value, ok := opened.Get("qq.100.token"); !ok || value != "外面的值" {
		t.Errorf("Open 应读到外面那份，得到 %q、存在=%t", value, ok)
	}
}

// 复核第 7 条：超过 4 MB 的文件当格式错误，不读进来再解析（后面加空格，JSON 本身照样合法）。
func TestOpenRejectsOversizedFile(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	big := append(readBytes(t, path), bytes.Repeat([]byte(" "), maxFileBytes)...)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrFormat) {
		t.Errorf("超过 4 MB 应报格式错误，得到 %v", err)
	}
	if _, err := Unlock(path, "随便的主密码", Passphrase); !errors.Is(err, ErrFormat) {
		t.Errorf("Unlock 也应报格式错误，得到 %v", err)
	}
}

// 复核补测：SetPassphrase 落盘失败时主密码和恢复码回退，旧主密码仍能解。
func TestSetPassphraseRollsBackOnWriteFailure(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	oldCode := mustPassphrase(t, s, "第一个主密码八九")
	oldPass, oldRecovery := s.password.wrap, s.recovery.wrap
	before := readBytes(t, path)
	restore := failRenames(t)
	if _, err := s.SetPassphrase("第二个主密码八九"); err == nil {
		t.Fatal("落盘失败时 SetPassphrase 应报错")
	}
	restore()
	if s.password.wrap != oldPass || s.recovery.wrap != oldRecovery {
		t.Error("落盘失败时内存里的主密码和恢复码应回退")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("落盘失败时文件应不变")
	}
	dropLocalCopy(t, path)
	if _, err := Unlock(path, "第一个主密码八九", Passphrase); err != nil {
		t.Errorf("旧主密码仍应能解：%v", err)
	}
	if _, err := Unlock(path, oldCode, RecoveryCode); err != nil {
		t.Errorf("旧恢复码仍应能用：%v", err)
	}
	if _, err := Unlock(path, "第二个主密码八九", Passphrase); !errors.Is(err, ErrWrongSecret) {
		t.Errorf("失败的新主密码不该生效，得到 %v", err)
	}
}

// 复核补测：Delete 落盘失败时名字回退，文件不变。
func TestDeleteRollsBackOnWriteFailure(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	before := readBytes(t, path)
	restore := failRenames(t)
	if err := s.Delete("qq.100.token"); err == nil {
		t.Fatal("落盘失败时 Delete 应报错")
	}
	restore()
	if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("落盘失败时名字应留在内存里")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("落盘失败时文件应不变")
	}
}

// 复核补测：设主密码的最短长度——7 个字符拒、8 个字符收。
func TestSetPassphraseMinimumLength(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	before := readBytes(t, path)
	if _, err := s.SetPassphrase("1234567"); err == nil {
		t.Fatal("7 个字符的主密码应被拒绝")
	}
	if s.HasPassphrase() {
		t.Error("被拒的主密码不该留在库里")
	}
	if !bytes.Equal(before, readBytes(t, path)) {
		t.Error("被拒时不该改文件")
	}
	if _, err := s.SetPassphrase("12345678"); err != nil {
		t.Errorf("8 个字符的主密码应能设：%v", err)
	}
	if !s.HasPassphrase() {
		t.Error("设好后 HasPassphrase 应为真")
	}
}

// 复核补测：包裹的 iv / ct / tag 长度不对各报格式错误、不留给 gcm.Open panic。
func TestWrapValidateRejectsWrongLengths(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	valid := func() *kdfWrap {
		return &kdfWrap{KDF: "scrypt", N: 1 << 14, R: 8, P: 1,
			Salt: b64(make([]byte, saltLen)), IV: b64(make([]byte, ivLen)),
			Tag: b64(make([]byte, tagLen)), CT: b64(make([]byte, dekLen))}
	}
	cases := []struct {
		name  string
		field string
		size  int
	}{
		{"iv 为 0 字节", "iv", 0},
		{"iv 为 11 字节", "iv", 11},
		{"iv 为 13 字节", "iv", 13},
		{"ct 为 31 字节", "ct", 31},
		{"tag 为 15 字节", "tag", 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := valid()
			value := b64(make([]byte, tc.size))
			switch tc.field {
			case "iv":
				w.IV = value
			case "ct":
				w.CT = value
			case "tag":
				w.Tag = value
			}
			if err := w.validate(); !errors.Is(err, ErrFormat) {
				t.Errorf("长度不对应报格式错误，得到 %v", err)
			}
		})
	}
}

// 复核补测：恢复码清理后 31、33 个字符各报格式不对，不跑 scrypt、文件不变。
func TestRecoveryCodeLengthIsRejectedWithoutKDF(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	mustPassphrase(t, s, "正确的主密码一号")
	dropLocalCopy(t, path)
	before := readBytes(t, path)
	for _, length := range []int{recoveryChars - 1, recoveryChars + 1} {
		t.Run(fmt.Sprintf("%d 个字符", length), func(t *testing.T) {
			calls := countKDF(t)
			*calls = 0
			_, err := Unlock(path, strings.Repeat("A", length), RecoveryCode)
			if err == nil || !strings.Contains(err.Error(), "恢复码格式不对") {
				t.Errorf("应报恢复码格式不对，得到 %v", err)
			}
			if *calls != 0 {
				t.Errorf("格式不对时不该跑 scrypt，跑了 %d 次", *calls)
			}
			if !bytes.Equal(before, readBytes(t, path)) {
				t.Error("格式不对时文件应不变")
			}
		})
	}
}

// 复核补测：直接调 save——没有本机那份也没有主密码时返回 ErrNeedPassphrase，不落盘。
func TestSaveWithoutAnyWrapNeedsPassphrase(t *testing.T) {
	local := useFakeLocal(t)
	useTestCost(t)
	local.protectErr = ErrUnsupported
	local.unprotectErr = ErrUnsupported
	path := filepath.Join(t.TempDir(), "secrets.bin")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create：%v", err)
	}
	if err := s.save(); !errors.Is(err, ErrNeedPassphrase) {
		t.Errorf("没有本机那份也没有主密码时 save 应报 ErrNeedPassphrase，得到 %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("不该落盘")
	}
}

// 外部程序改同一路径的文件：在别的路径建库、写内容，再把字节拷过来覆盖；返回拷过去的字节。
func externalWrite(t *testing.T, path string, values map[string]string) []byte {
	t.Helper()
	other := filepath.Join(t.TempDir(), "secrets.bin")
	store := mustCreate(t, other)
	for name, value := range values {
		mustSet(t, store, name, value)
	}
	b := readBytes(t, other)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("覆盖密钥文件：%v", err)
	}
	return b
}

// 返工三：库登记着的时候外面改了文件，再次 Open 还是在同一个对象上，值换成外面的新值。
func TestReopenSeesExternalWrite(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "旧值")
	externalWrite(t, path, map[string]string{"qq.100.token": "外部写的新值", "qq.200.token": "外部的第二条"})
	again, err := Open(path)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	if again != s {
		t.Fatal("同一路径应还是同一个库")
	}
	if value, ok := again.Get("qq.100.token"); !ok || value != "外部写的新值" {
		t.Errorf("应看到外面写的新值，得到 %q、存在=%t", value, ok)
	}
	if _, ok := again.Get("qq.200.token"); !ok {
		t.Error("外面加的名字也应看到")
	}
	// 重读后记下的是新文件，接着写不该被当成冲突。
	mustSet(t, again, "qq.300.token", "重开后我写的")
}

// 返工三：Open 之后文件被外面改过，写操作拒绝落盘，内存和文件都保持外面的样子。
func TestWriteRefusedAfterExternalChange(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "旧值")
	b := externalWrite(t, path, map[string]string{"qq.100.token": "外部写的新值"})
	cases := []struct {
		name string
		run  func() error
	}{
		{"Set 改名", func() error { return s.Set("qq.100.token", "我的新值") }},
		{"Set 加名字", func() error { return s.Set("qq.300.token", "我的第三条") }},
		{"Delete", func() error { return s.Delete("qq.100.token") }},
		{"SetPassphrase", func() error {
			_, err := s.SetPassphrase("又一个主密码八九")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, ErrChangedOnDisk) {
				t.Fatalf("应报 ErrChangedOnDisk，得到 %v", err)
			}
			if !bytes.Equal(b, readBytes(t, path)) {
				t.Error("拒绝的写不该改文件")
			}
		})
	}
	if value, ok := s.Get("qq.100.token"); !ok || value != "旧值" {
		t.Errorf("拒绝的写不该改内存，得到 %q、存在=%t", value, ok)
	}
}

// 返工三：文件被删了，Open 报 ErrNotFound，原对象上的写报错且不重建文件。
func TestReopenAfterDelete(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "值")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删了文件后 Open 应报 ErrNotFound，得到 %v", err)
	}
	if err := s.Set("qq.200.token", "值"); err == nil {
		t.Fatal("文件被删后写应报错")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("拒绝的写不该重建文件")
	}
	if _, ok := s.Get("qq.200.token"); ok {
		t.Error("拒绝的写不该改内存")
	}
}

// 返工三：外面改坏了文件、这台电脑解不开，Open 报锁着，原对象上的写也拒绝。
func TestReopenRefusesUnreadableFile(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "值")
	other := filepath.Join(t.TempDir(), "secrets.bin")
	from := mustCreate(t, other)
	mustSet(t, from, "qq.100.token", "外部的值")
	mustPassphrase(t, from, "外部设的主密码八九")
	dropLocalCopy(t, other)
	if err := os.WriteFile(path, readBytes(t, other), 0o600); err != nil {
		t.Fatal(err)
	}
	b := readBytes(t, path)
	_, err := Open(path)
	requireLocked(t, err, true)
	if err := s.Set("qq.200.token", "值"); err == nil {
		t.Fatal("重读失败后写应报错")
	}
	if !bytes.Equal(b, readBytes(t, path)) {
		t.Error("拒绝的写不该改文件")
	}
}

// 返工三：修改时间不变、只改了大小，也算外面改过。
func TestReopenDetectsSizeChangeWithSameModtime(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "小值")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("外部更长的值", 40)
	externalWrite(t, path, map[string]string{"qq.100.token": long})
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	restored, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Chtimes 没把修改时间还原：%v → %v", before.ModTime(), restored.ModTime())
	}
	if restored.Size() == before.Size() {
		t.Fatal("构造的文件应改变大小，否则测不到只看修改时间的情况")
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	if got, ok := again.Get("qq.100.token"); !ok || got != long {
		t.Error("大小变了就该当外面改过、换成新值")
	}
}

// 返工三：大小不变、只改了内容和修改时间，也算外面改过；改坏的数据报格式错误，之后的写也拒绝。
func TestReopenDetectsContentChangeWithSameSize(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	shape := readShape(t, path)
	var data sealed
	if err := json.Unmarshal(shape["data"], &data); err != nil {
		t.Fatal(err)
	}
	ct := []byte(data.CT)
	if ct[len(ct)/2] == 'A' {
		ct[len(ct)/2] = 'B'
	} else {
		ct[len(ct)/2] = 'A'
	}
	data.CT = string(ct)
	if shape["data"], err = json.Marshal(data); err != nil {
		t.Fatal(err)
	}
	writeShape(t, path, shape)
	later := before.ModTime().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Size() != before.Size() {
		t.Fatalf("这里要测大小不变的情况：%d → %d", before.Size(), changed.Size())
	}
	if changed.ModTime().Equal(before.ModTime()) {
		t.Fatal("修改时间应已改变")
	}
	bad := readBytes(t, path)
	if _, err := Open(path); !errors.Is(err, ErrFormat) {
		t.Fatalf("改坏的数据应报格式错误，得到 %v", err)
	}
	if err := s.Set("qq.200.token", "新值"); err == nil {
		t.Fatal("重读失败后写应报错")
	}
	if !bytes.Equal(bad, readBytes(t, path)) {
		t.Error("拒绝的写不该改文件")
	}
}

// 返工四：修改时间和大小都一样、内容不同，也要判成外面改过（按内容的 SHA-256 比）。
func TestReopenDetectsContentChangeWithSameStamp(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "编的令牌")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 翻 data.ct 中间一个 base64 字符：长度不变，内容变了。
	shape := readShape(t, path)
	var data sealed
	if err := json.Unmarshal(shape["data"], &data); err != nil {
		t.Fatal(err)
	}
	ct := []byte(data.CT)
	if ct[len(ct)/2] == 'A' {
		ct[len(ct)/2] = 'B'
	} else {
		ct[len(ct)/2] = 'A'
	}
	data.CT = string(ct)
	if shape["data"], err = json.Marshal(data); err != nil {
		t.Fatal(err)
	}
	writeShape(t, path, shape)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Size() != before.Size() {
		t.Fatalf("这里要测大小不变的情况：%d → %d", before.Size(), changed.Size())
	}
	if !changed.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Chtimes 没把修改时间还原：%v → %v", before.ModTime(), changed.ModTime())
	}
	bad := readBytes(t, path)
	if _, err := Open(path); !errors.Is(err, ErrFormat) {
		t.Fatalf("内容变了就该重读、报格式错误，得到 %v", err)
	}
	if err := s.Set("qq.200.token", "新值"); err == nil {
		t.Fatal("重读失败后写应报错")
	}
	if !bytes.Equal(bad, readBytes(t, path)) {
		t.Error("拒绝的写不该改文件")
	}
}

// 返工四：文件被删后不调 Open 直接 Set 也拒写，不重建文件。
func TestSetAfterDeleteWithoutOpen(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "值")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("qq.200.token", "新值"); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("文件被删后写应报 ErrChangedOnDisk，得到 %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("拒绝的写不该重建文件")
	}
	if _, ok := s.Get("qq.200.token"); ok {
		t.Error("拒绝的写不该改内存")
	}
}

// 返工四：重读时文件没变，拒写标记也要清掉（同样的样子不该一直拒写）。
func TestReopenClearsStaleWhenFileUnchanged(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	mustSet(t, s, "qq.100.token", "值")
	oldStat := statFile
	t.Cleanup(func() { statFile = oldStat })
	statFile = func(name string) (os.FileInfo, error) {
		if filepath.Clean(name) == filepath.Clean(path) {
			return nil, errors.New("磁盘一时读不到")
		}
		return oldStat(name)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Stat 失败时 Open 应报错")
	}
	statFile = oldStat
	again, err := Open(path)
	if err != nil {
		t.Fatalf("文件没变，再 Open 应成功：%v", err)
	}
	mustSet(t, again, "qq.200.token", "值二")
}
