package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// backend.local 默认 go；写 bun 读得进来；写错报出键名；示例配置里这一段和默认值一致
func TestBackendConfig(t *testing.T) {
	if d := Defaults().Backend; d.Local != LocalByGo || d.Bun != "" || d.Server != "" || d.LocalOwnedByBun() {
		t.Fatalf("默认该是 go：%+v", d)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "backend:\n  local: bun\n  bun: 'D:/tools/bun.exe'\n  server: D:/repo/server\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Backend; !b.LocalOwnedByBun() || b.Bun != "D:/tools/bun.exe" || b.Server != "D:/repo/server" {
		t.Fatalf("读出来不对：%+v", b)
	}
	// 只写了别的段：local 仍是默认 go
	t.Setenv("SUMIKA_CONFIG", write(t, "backend:\n  bun: x\n"))
	if c, err = Load(t.TempDir()); err != nil || c.Backend.Local != LocalByGo {
		t.Fatalf("没写 local 该是 go：%+v %v", c.Backend, err)
	}
	for _, bad := range []string{"Bun", "BUN", "off", "true", ""} {
		t.Setenv("SUMIKA_CONFIG", write(t, "backend:\n  local: \""+bad+"\"\n"))
		_, err := Load(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "backend.local") {
			t.Errorf("backend.local=%q 该报错并带键名：%v", bad, err)
		}
	}
}

func TestExampleBackend(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", filepath.Join("..", "..", "..", "sumika.example.yml"))
	c, err := Load(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != DefaultBackend() || !c.Present["backend.local"] {
		t.Fatalf("示例里的 backend 该写着默认值：%+v", c.Backend)
	}
}
