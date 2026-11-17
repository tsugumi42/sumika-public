package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/paths"
)

func TestMemoryPathWithoutImportMarker(t *testing.T) {
	data := t.TempDir()
	want := filepath.Join(data, "go", "characters", "fixture", "memory.db")
	if err := os.MkdirAll(filepath.Dir(want), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("synthetic memory"), 0600); err != nil {
		t.Fatal(err)
	}
	got := (paths.Paths{Root: data}).MemoryDB("fixture")
	if got != want {
		t.Errorf("无导入标记时路径 = %q，要 %q", got, want)
	}
	if err := paths.CheckMemoryPath(data, got); err != nil {
		t.Errorf("固定记忆库路径未获写入守卫放行：%v", err)
	}
	// 留着旧的导入标记（真实数据里还在），路径也不能变（ADR-308 第 2、3 条）
	if err := os.WriteFile(filepath.Join(data, "go", "import-active.json"), []byte(`{"run":"real-20261102c"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if again := (paths.Paths{Root: data}).MemoryDB("fixture"); again != want {
		t.Errorf("有旧导入标记时路径 = %q，要 %q", again, want)
	}
}
