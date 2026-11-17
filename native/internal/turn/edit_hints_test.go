package turn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// 编辑未命中的提示带着文件内容：只对"读它本来就允许"的文件打开。受保护的路径、联接指进去的、认不准的都不开
func TestEditHintsOnlyForReadableFiles(t *testing.T) {
	t.Parallel()
	work, _ := filepath.EvalSymlinks(t.TempDir())
	secret, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := Config{Gate: &approval.Gate{ProtectedDirs: []string{secret}}}
	ws := Workspace{CWD: work, Root: work}
	write := func(path string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	call := func(name, path string) PreparedCall {
		args, _ := jsjson.Parse(`{"path":` + jsonStr(path) + `}`)
		return PreparedCall{Name: name, Args: args}
	}
	on := func(c Config, pc PreparedCall) bool {
		return tools.EditHintsEnabled(withEditHintsIfAllowed(context.Background(), c, ws, pc))
	}
	normal := write(filepath.Join(work, "a.go"))
	if !on(cfg, call("edit", normal)) || !on(cfg, call("edit", "a.go")) {
		t.Fatal("工作区里的普通文件要给提示")
	}
	if on(cfg, call("write", normal)) {
		t.Fatal("不是 edit 不开")
	}
	if on(Config{}, call("edit", normal)) {
		t.Fatal("没接审批门不开")
	}
	if on(cfg, call("edit", "")) || on(cfg, call("edit", filepath.Join(work, "missing.go"))) {
		t.Fatal("没路径、文件不存在（认不准真实位置）不开")
	}
	if on(cfg, call("edit", write(filepath.Join(secret, "keys.txt")))) {
		t.Fatal("受保护目录里的文件不给提示")
	}
	if on(cfg, call("edit", write(filepath.Join(work, ".env")))) || on(cfg, call("edit", write(filepath.Join(work, ".env.local")))) {
		t.Fatal(".env 一类不给提示")
	}
	link := filepath.Join(work, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, secret).CombinedOutput(); err != nil {
		t.Skipf("建不了联接：%v %s", err, out)
	}
	if on(cfg, call("edit", filepath.Join(link, "keys.txt"))) {
		t.Fatal("联接指进受保护目录：真实路径在里面，不给提示")
	}
}

func jsonStr(s string) string {
	return string(jsjson.Stringify(jsjson.Value{Kind: jsjson.String, Str: s}))
}
