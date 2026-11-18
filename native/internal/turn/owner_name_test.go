package turn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/paths"
	"sumika-native/internal/persona"
)

// 四种场合拼出来的提示词都要有 owner_name：卡上写了用卡上的，没写是"用户"。
// 出厂提示词这一段还没用到 owner_name，所以用临时提示词目录把占位符放进去看填的值。
func TestPrefixOwnerNameInEveryScene(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	files := map[string]string{
		"core":             "你是{{name}}，称呼={{owner_name}}。",
		"scene-home":       "场合=本地私聊 称呼={{owner_name}}",
		"scene-home-group": "场合=本地群聊 称呼={{owner_name}}",
		"scene-private":    "场合=QQ 私聊 称呼={{owner_name}}",
		"scene-group":      "场合=QQ 群 称呼={{owner_name}}",
		"platform-qq":      "平台=QQ 称呼={{owner_name}}",
	}
	dir := filepath.Join(root, "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Root: root, Data: paths.Paths{Root: data}}
	scenes := []struct {
		name   string
		conv   string
		scenes []string
	}{
		{"本地私聊", "console:suzu", nil},
		{"本地群聊", "console:group:room", []string{"scene-home-group"}},
		{"QQ 私聊", "qq:private:u1", []string{"scene-private", "platform-qq"}},
		{"QQ 群", "qq:group:g1", []string{"scene-group", "platform-qq"}},
	}
	for _, c := range []struct{ name, owner, want string }{
		{"没写", "", "用户"},
		{"写了", "主人", "主人"},
	} {
		for _, sc := range scenes {
			t.Run(c.name+"/"+sc.name, func(t *testing.T) {
				ch := persona.Character{ID: "suzu", Name: "铃", OwnerName: c.owner}
				sections, full, err := prefixOf(cfg, ch, Input{Conversation: sc.conv, Scenes: sc.scenes})
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(full, "{{") {
					t.Fatalf("有没填的占位符：\n%s", full)
				}
				for _, s := range sections {
					if s.Text == "" {
						continue
					}
					if !strings.Contains(s.Text, "称呼="+c.want) {
						t.Errorf("段 %s 的称呼不是 %s：\n%s", s.ID, c.want, s.Text)
					}
				}
				core := sections[0]
				if !strings.Contains(core.Text, "铃") || !strings.Contains(core.Text, "称呼="+c.want) {
					t.Errorf("核心提示词里的名字或称呼不对：\n%s", core.Text)
				}
			})
		}
	}
}
