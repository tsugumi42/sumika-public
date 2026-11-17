package embedinputs

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "in.json")
	want := File{
		Items:     []string{"编的条目一", "编的条目二 <&>"},
		Queries:   []string{"编的查询"},
		Group:     []string{"编的群消息"},
		Sentences: []string{"编的句子。"},
		Rerank:    []RerankInput{{Query: "查询", Docs: []string{"甲", "乙"}}},
	}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	want.Version = Version
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("读回来的和写进去的不一样：%+v", got)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "\\u003c") {
		t.Error("文本里的 < & > 被转义成 \\u003c 了，parity_ref.py 读得回但人看不懂")
	}
	if n := want.Texts(); n != 5 {
		t.Errorf("Texts() = %d", n)
	}
}

func TestReadRejectsWrongVersionAndGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"v0.json": `{"version":0}`, "v2.json": `{"version":2}`, "bad.json": `[`} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(p); err == nil {
			t.Errorf("%s 该读不进来", name)
		}
	}
	if _, err := Read(filepath.Join(dir, "none.json")); err == nil {
		t.Error("不存在的文件该报错")
	}
}

// 路径检查要三种格：不在仓库里（放行）、在仓库里没被忽略（拒绝）、在仓库里被忽略（放行）；路径指向还没建的子目录也要问对。
func TestCheckPrivatePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("机器上没有 git")
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%v\n%s", args, err, out)
		}
	}
	outside := t.TempDir() // 临时目录不在任何仓库里（测试机的 TEMP 不在工作树里；在的话下面这格会提前红，说明环境不对）
	if err := CheckPrivatePath(filepath.Join(outside, "a", "b.json")); err != nil {
		t.Fatalf("不在仓库里的路径被拒绝了：%v", err)
	}
	repo := t.TempDir()
	run(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("private/\n*.secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rel     string
		allowed bool
	}{
		{"in.json", false},
		{"sub/dir/in.json", false}, // 目录还不存在
		{"private/in.json", true},  // 被忽略的目录，还没建
		{"x.secret", true},
		{".git/in.json", false},
	} {
		err := CheckPrivatePath(filepath.Join(repo, filepath.FromSlash(c.rel)))
		if (err == nil) != c.allowed {
			t.Errorf("%s：allowed=%v，得到 %v", c.rel, c.allowed, err)
		}
		// 拒绝要拒绝得对：是"没被忽略"，不是 git 报了别的错才拒绝的
		if c.rel == "in.json" || c.rel == "sub/dir/in.json" {
			if err == nil || !strings.Contains(err.Error(), ".gitignore") {
				t.Errorf("%s：该因为没被 .gitignore 忽略而拒绝，得到 %v", c.rel, err)
			}
		}
	}
	if err := Write(filepath.Join(repo, "in.json"), File{}); err == nil {
		t.Error("Write 没拦住工作树里没被忽略的路径")
	} else if _, statErr := os.Stat(filepath.Join(repo, "in.json")); statErr == nil {
		t.Error("拒绝了却还是写出了文件")
	}
	if err := Write(filepath.Join(repo, "private", "in.json"), File{}); err != nil {
		t.Errorf("被忽略的路径该能写：%v", err)
	}
}
