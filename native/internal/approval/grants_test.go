package approval

// "始终允许"写回文件时进程被杀（表一 A9）：写到一半失败，原来的授权一条不少；改名能替换已经有的文件

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGrantsWriteInterrupted(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config", "tool-grants.yml")
	g := LoadGrants(file)
	for _, k := range []string{"a", "b"} {
		if err := g.Put(Rule{Key: k, Label: "第 " + k + " 类", Tool: "powershell", At: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if !LoadGrants(file).Has("b") {
		t.Fatal("第二条写进去了：改名要能替换已经有的文件")
	}
	before, _ := os.ReadFile(file)
	writeFile = func(name string, b []byte, perm os.FileMode) error {
		os.WriteFile(name, b[:len(b)/3], perm) // 只写了三分之一就"被杀"
		return errors.New("写到一半被杀")
	}
	t.Cleanup(func() { writeFile = os.WriteFile })
	if err := g.Put(Rule{Key: "c", Label: "第 c 类", Tool: "powershell", At: 2}); err == nil {
		t.Fatal("写失败要报出来")
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(before) {
		t.Fatalf("写到一半失败，原来的文件要原样留着：\n%s", after)
	}
	if got := LoadGrants(file); !got.Has("a") || !got.Has("b") || got.Has("c") {
		t.Fatal("重启以后读回来的还是原来那两条")
	}
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("写失败的临时文件要删掉")
	}
	// 写失败时内存里也不留：本进程不当成已授权，再点一次真的写进文件
	if g.Has("c") {
		t.Fatal("没写成的不算授权")
	}
	writeFile = os.WriteFile
	if err := g.Put(Rule{Key: "c", Label: "第 c 类", Tool: "powershell", At: 3}); err != nil || !LoadGrants(file).Has("c") {
		t.Fatalf("再点一次要写进文件：%v", err)
	}
}
