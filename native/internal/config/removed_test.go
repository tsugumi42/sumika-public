package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 配置里还写着已删的 backend 段：读得进来、不报错；backend.local 写 bun 时说明点明“Bun 后台已移除、现在由 Go 处理”，
// 其他写法和另外两个键也各有一行；没写这些键就没有说明
func TestRemovedBackendKeys(t *testing.T) {
	cases := []struct {
		name, yml string
		keys      []string
		want      map[string]string // 键 → 说明里必须有的话
	}{
		{"全写", "backend:\n  local: bun\n  bun: 'D:/tools/bun.exe'\n  server: D:/repo/server\n", []string{"backend.local", "backend.bun", "backend.server"},
			map[string]string{"backend.local": "Bun 后台已移除，现在由 Go 处理", "backend.bun": "Bun 后台已移除", "backend.server": "Bun 后台已移除"}},
		{"大写 Bun 也认", "backend:\n  local: Bun\n", []string{"backend.local"}, map[string]string{"backend.local": "现在由 Go 处理"}},
		{"写 go", "backend:\n  local: go\n", []string{"backend.local"}, map[string]string{"backend.local": "本地私聊一直由 Go 处理"}},
		{"写错的值也不再报错", "backend:\n  local: \"off\"\n", []string{"backend.local"}, map[string]string{"backend.local": "已不再使用"}},
		{"空值", "backend:\n  local:\n", []string{"backend.local"}, map[string]string{"backend.local": "已不再使用"}},
		{"只写 server", "backend:\n  server: x\n", []string{"backend.server"}, nil},
		{"没写 backend", "console: { port: 7720 }\n", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUMIKA_CONFIG", write(t, tc.yml))
			c, err := Load(t.TempDir())
			if err != nil {
				t.Fatalf("已删键不该让读配置失败：%v", err)
			}
			if !slices.Equal(c.RemovedKeys, tc.keys) || len(c.RemovedNotes) != len(tc.keys) {
				t.Fatalf("已删键 %v，说明 %d 条，想要 %v", c.RemovedKeys, len(c.RemovedNotes), tc.keys)
			}
			for i, k := range c.RemovedKeys {
				note := c.RemovedNotes[i]
				if !strings.HasPrefix(note, k) || !strings.Contains(note, "删掉") {
					t.Errorf("说明要点名键并写怎么解除：%q", note)
				}
				if want := tc.want[k]; want != "" && !strings.Contains(note, want) {
					t.Errorf("%s 的说明缺“%s”：%q", k, want, note)
				}
			}
		})
	}
}

// 示例配置不再写已删的键（写了，照抄的人一开机就看到一条“已删除”）
func TestExampleHasNoRemovedKeys(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", filepath.Join("..", "..", "..", "sumika.example.yml"))
	c, err := Load(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RemovedKeys) != 0 {
		t.Fatalf("示例配置不该再写已删的键：%v", c.RemovedKeys)
	}
}
