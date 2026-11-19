package private

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/devtools/gitrun"
)

// 第四轮复核：data/ 进了 git、名单被跟踪、名单缺失或为空、改道名单的环境变量、UTF-16 文件、非 UTF-8 的提交说明。

func TestRunDataPathInGit(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "data/x.txt"), "普通\n")
	gitIn(t, dir, "x@example.com", "add", "-f", "data/x.txt")
	if code, out := run(dir, "--staged"); code != 1 || !strings.Contains(out, "data/") {
		t.Errorf("暂存了 data/ 下的文件要拦：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "不该提交的")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 || !strings.Contains(out, "data/") {
		t.Errorf("范围里新增了 data/ 下的文件要拦：code=%d out=%s", code, out)
	}
	// 把它从 git 里拿掉（删除）不拦：那是补救
	gitIn(t, dir, "x@example.com", "rm", "-q", "--cached", "data/x.txt")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "拿掉")
	if code, out := run(dir, "--range", "HEAD~1..HEAD"); code != 0 {
		t.Errorf("删除 data/ 下的文件不拦：code=%d out=%s", code, out)
	}
}

func TestRunTrackedListRefused(t *testing.T) {
	dir := repo(t)
	gitIn(t, dir, "x@example.com", "add", "-f", "data/dev/private-strings.json")
	if code, out := run(dir, "--staged"); code != 2 || !strings.Contains(out, "跟踪") {
		t.Errorf("名单文件被 git 跟踪：拒绝读、红：code=%d out=%s", code, out)
	}
}

func TestRunRangeNeedsLists(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "b.md"), "普通\n")
	gitIn(t, dir, "x@example.com", "add", "b.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "普通")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 0 {
		t.Fatalf("名单齐全：code=%d out=%s", code, out)
	}
	groups := filepath.Join(dir, "data/dev/human-replies/groups.json")
	priv := filepath.Join(dir, "data/dev/private-strings.json")
	privOld := mustFile(t, priv)
	// 群名表缺失
	if err := os.Remove(groups); err != nil {
		t.Fatal(err)
	}
	if code, out := run(dir, "--range", base+"..HEAD"); code != 2 || !strings.Contains(out, "群名表") {
		t.Errorf("推送前群名表缺失：红：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--range", base+"..HEAD", "--allow-missing"); code != 2 {
		t.Errorf("--allow-missing 绕不过推送前的名单要求：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--staged"); code != 0 {
		t.Errorf("提交时（--staged）名单缺了照旧只提示：code=%d out=%s", code, out)
	}
	// 私人字符串名单为空
	write(t, groups, `{"6767898":{"name":"编的群表名"}}`)
	write(t, priv, "[]")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 2 || !strings.Contains(out, "私人字符串名单") {
		t.Errorf("推送前私人字符串名单为空：红：code=%d out=%s", code, out)
	}
	write(t, priv, privOld)
	if code, out := run(dir, "--range", base+"..HEAD"); code != 0 {
		t.Errorf("名单恢复后照常：code=%d out=%s", code, out)
	}
}

// 生产构建不认 SUMIKA_SCAN_DATA、SUMIKA_SCAN_MAIBOT：设了就拒绝（红）。测试里把“允许测试钩子”改成 false 来模拟生产构建。
func TestRunProductionRejectsScanEnv(t *testing.T) {
	dir := repo(t)
	old := testHooksAllowed
	testHooksAllowed = func() bool { return false }
	t.Cleanup(func() { testHooksAllowed = old })
	names := []string{"SUMIKA_SCAN_DATA", "SUMIKA_SCAN_MAIBOT"}
	if runtime.GOOS == "windows" {
		names = append(names, "sumika_scan_data", "Sumika_Scan_Maibot") // Windows 的环境变量名不分大小写，os.Getenv 也一样
	}
	for _, name := range names {
		t.Setenv(name, dir)
		if code, out := run(dir, "--staged"); code != 2 || !strings.Contains(out, "拒绝扫描") {
			t.Errorf("%s：生产构建见到就拒绝：code=%d out=%s", name, code, out)
		}
		t.Setenv(name, "")
	}
	t.Setenv("SUMIKA_SCAN_MAIBOT", "")
	if code, out := run(dir, "--staged"); code != 0 {
		t.Errorf("没设时照常：code=%d out=%s", code, out)
	}
	// 生产环境本来就不配 MaiBot 库：输出里不算“缺 MaiBot 库”
	if _, out := run(dir, "--staged"); strings.Contains(out, "缺 MaiBot 库") {
		t.Errorf("没配 MaiBot 库不算缺失：%s", out)
	}
}

func TestRunTreeUTF16(t *testing.T) {
	dir := repo(t)
	tree := t.TempDir()
	bom := string(rune(0xFEFF))
	for name, content := range map[string]string{
		"le.txt": utf16Bytes(bom+"号 1111222\r\n又一行\r\n", false),
		"be.txt": utf16Bytes(bom+"号 1111222\r\n又一行\r\n", true),
	} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := run(dir, "--tree", tree); code != 1 || strings.Contains(out, "1111222") || !strings.Contains(out, "拦 2 组") && !strings.Contains(out, "拦 1 组") {
		t.Errorf("--tree 里 UTF-16 文件里的号码要拦（真正的二进制才跳过）：code=%d out=%s", code, out)
	}
}

// 提交说明用别的编码存（头里有 encoding gbk，正文是 GBK 字节）：原始字节里的中文名字对不上，另外用 git log --encoding=UTF-8 转一份再扫
func TestRunNonUTF8Message(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	tree := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD^{tree}"))
	ident := "a <x@example.com> 1700000000 +0000"
	gbk := []byte{0xB1, 0xE0, 0xB5, 0xC4, 0xC8, 0xBA, 0xC3, 0xFB} // “编的群名”（名单里的名字）的 GBK 字节
	obj := append([]byte("tree "+tree+"\nparent "+base+"\nauthor "+ident+"\ncommitter "+ident+"\nencoding gbk\n\n"), gbk...)
	obj = append(obj, '\n')
	out, code, err := gitrun.Run(dir, nil, obj, "hash-object", "-t", "commit", "--literally", "-w", "--stdin")
	if err != nil || code != 0 {
		t.Fatalf("造提交失败：code=%d err=%v out=%s", code, err, out)
	}
	c := strings.TrimSpace(string(out))
	if code, out := run(dir, "--range", c, "--not", base); code != 1 || !strings.Contains(out, "真实名字") {
		t.Errorf("gbk 存的提交说明里的名字要拦：code=%d out=%s", code, out)
	}
}
