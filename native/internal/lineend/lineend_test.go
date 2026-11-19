package lineend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// useSetting 把 tools.lineEnding 设成 mode，测试结束还原
func useSetting(t *testing.T, mode string) {
	t.Helper()
	old := Default()
	SetDefault(mode)
	t.Cleanup(func() { SetDefault(old) })
}

func TestExistingCRLFKeptEvenIfContentIsLF(t *testing.T) {
	useSetting(t, "lf") // 配置要 LF，已有文件是 CRLF：沿用文件的
	abs := filepath.Join(tmp(t), "a.txt")
	if got := Final(abs, []byte("旧\r\n的\r\n"), "新\n正文\n"); got != "新\r\n正文\r\n" {
		t.Fatalf("%q", got)
	}
}

func TestExistingLFKeptEvenIfSettingCRLF(t *testing.T) {
	useSetting(t, "crlf")
	abs := filepath.Join(tmp(t), "a.txt")
	if got := Final(abs, []byte("旧\n的\n"), "新\r\n正文\r\n"); got != "新\n正文\n" {
		t.Fatalf("%q", got)
	}
}

func TestExistingFirstNewlineDecides(t *testing.T) {
	useSetting(t, "lf")
	abs := filepath.Join(tmp(t), "a.txt")
	if got := Final(abs, []byte("a\r\nb\nc\n"), "x\ny"); got != "x\r\ny" {
		t.Fatalf("混合换行看第一个：%q", got)
	}
}

func TestExistingWithoutNewlineOrBinaryFallsThrough(t *testing.T) {
	useSetting(t, "crlf")
	abs := filepath.Join(tmp(t), "a.txt")
	for name, old := range map[string][]byte{
		"空文件": {}, "一行没换行": []byte("只有一行"), "二进制": []byte("a\x00b\nc\n"),
	} {
		if got := Final(abs, old, "x\ny"); got != "x\r\ny" {
			t.Errorf("%s：该落到配置的 CRLF：%q", name, got)
		}
	}
}

func TestSettingDecidesWhenNothingElse(t *testing.T) {
	abs := filepath.Join(tmp(t), "new.txt")
	useSetting(t, "lf")
	if got := Final(abs, nil, "甲\r\n乙\r丙\n"); got != "甲\n乙\n丙\n" {
		t.Fatalf("lf：%q", got)
	}
	useSetting(t, "crlf")
	if got := Final(abs, nil, "甲\r\n乙\r丙\n"); got != "甲\r\n乙\r\n丙\r\n" {
		t.Fatalf("crlf：%q", got)
	}
}

func TestAutoFollowsSystem(t *testing.T) {
	useSetting(t, "auto")
	want := "x\ny"
	if runtime.GOOS == "windows" {
		want = "x\r\ny"
	}
	if got := Final(filepath.Join(tmp(t), "n.txt"), nil, "x\ny"); got != want {
		t.Fatalf("%q，系统 %s", got, runtime.GOOS)
	}
	SetDefault("乱写的")
	if Default() != "auto" {
		t.Fatalf("不认识的值要当 auto：%q", Default())
	}
}

func TestBOM(t *testing.T) {
	useSetting(t, "lf")
	abs := filepath.Join(tmp(t), "a.txt")
	if got := Final(abs, []byte("\uFEFF旧\r\n"), "新\n"); got != "\uFEFF新\r\n" {
		t.Errorf("原文件带 BOM 要保留：%q", got)
	}
	if got := Final(abs, nil, "\uFEFF新\r\n行"); got != "\uFEFF新\n行" {
		t.Errorf("正文自带 BOM 照留：%q", got)
	}
	if got := Final(abs, []byte("旧\n"), "新\n"); got != "新\n" {
		t.Errorf("原文件没有就不加：%q", got)
	}
}

func TestReadExisting(t *testing.T) {
	dir := tmp(t)
	if ReadExisting(filepath.Join(dir, "none.txt")) != nil || ReadExisting(dir) != nil {
		t.Fatal("不存在的、目录都该是 nil")
	}
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("a\r\nb"), 0o644)
	if got := string(ReadExisting(p)); got != "a\r\nb" {
		t.Fatalf("%q", got)
	}
}

// tmp 是一个和外面隔开的临时目录：放了 root = true 的 .editorconfig，往上不会查到本机别处的设置
func tmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n")
	return dir
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 第二步：.editorconfig 的 end_of_line 压过系统默认和配置
func TestEditorconfigBeatsSystemDefault(t *testing.T) {
	for eol, want := range map[string]string{"lf": "甲\n乙\n", "crlf": "甲\r\n乙\r\n"} {
		for _, setting := range []string{"auto", "lf", "crlf"} {
			useSetting(t, setting)
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n[*]\nend_of_line = "+eol+"\n")
			if got := Final(filepath.Join(dir, "sub", "a.md"), nil, "甲\r\n乙\n"); got != want {
				t.Errorf("end_of_line=%s tools.lineEnding=%s：%q", eol, setting, got)
			}
		}
	}
}

// 已有文件排在 .editorconfig 前面；按路径匹配；root = true 往上不看
func TestEditorconfigOrderAndScope(t *testing.T) {
	useSetting(t, "auto")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n[*.sh]\nend_of_line = lf\n[*.bat]\nend_of_line = crlf\n")
	if got := Final(filepath.Join(dir, "a.sh"), nil, "x\ny"); got != "x\ny" {
		t.Errorf("*.sh 写 lf：%q", got)
	}
	if got := Final(filepath.Join(dir, "a.bat"), nil, "x\ny"); got != "x\r\ny" {
		t.Errorf("*.bat 写 crlf：%q", got)
	}
	if got := Final(filepath.Join(dir, "a.sh"), []byte("旧\r\n"), "x\ny"); got != "x\r\ny" {
		t.Errorf("已有文件是 CRLF，排在 .editorconfig 前：%q", got)
	}
	// 里层 root = true 挡住外层
	inner := filepath.Join(dir, "inner")
	writeFile(t, filepath.Join(inner, ".editorconfig"), "root = true\n")
	useSetting(t, "crlf")
	if got := Final(filepath.Join(inner, "a.sh"), nil, "x\ny"); got != "x\r\ny" {
		t.Errorf("里层 root 挡住外层的 *.sh：%q", got)
	}
}

func TestEditorconfigCRFallsBackToLF(t *testing.T) {
	useSetting(t, "crlf")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n[*]\nend_of_line = cr\n")
	if got := Final(filepath.Join(dir, "a.txt"), nil, "x\r\ny"); got != "x\ny" {
		t.Fatalf("cr 不支持，按 LF：%q", got)
	}
}

func TestEditorconfigUnsetFallsThrough(t *testing.T) {
	useSetting(t, "lf")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n[*]\nend_of_line = unset\n")
	if got := Final(filepath.Join(dir, "a.txt"), nil, "x\r\ny"); got != "x\ny" {
		t.Fatalf("unset 当没写，落到配置：%q", got)
	}
}
