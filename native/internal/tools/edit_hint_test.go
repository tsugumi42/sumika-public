package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

func hintEditArgs(t *testing.T, path, oldText, newText string) jsjson.Value {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"path": path, "edits": []any{map[string]any{"oldText": oldText, "newText": newText}}})
	v, err := jsjson.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 提示只在内核打开时追加；原报错的字原样在前面；成功的编辑不受影响
func TestEditMissHintOnlyWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("func main() {\n\trun(cfg, 3)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := hintEditArgs(t, "a.go", "    run(cfg, 3)", "    run(cfg, 4)")
	_, _, plain := edit(context.Background(), dir, args)
	if plain == nil || strings.Contains(plain.Error(), "提示") {
		t.Fatalf("没开不该有提示：%v", plain)
	}
	_, _, hinted := edit(WithEditHints(context.Background()), dir, args)
	if hinted == nil || !strings.HasPrefix(hinted.Error(), plain.Error()+"\n\n提示：") || !strings.Contains(hinted.Error(), "第 2～2 行") {
		t.Fatalf("开了要在原报错后面追加：%v", hinted)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.go")); string(got) != "func main() {\n\trun(cfg, 3)\n}\n" {
		t.Fatal("未命中不能改文件")
	}
	text, _, err := edit(WithEditHints(context.Background()), dir, hintEditArgs(t, "a.go", "\trun(cfg, 3)", "\trun(cfg, 4)"))
	if err != nil || !strings.Contains(text, "Successfully") {
		t.Fatalf("%v %v", text, err)
	}
}
