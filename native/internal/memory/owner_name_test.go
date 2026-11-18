package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/prompts"
)

// 后台整理提示词也要填 owner_name：卡上的称呼，没配是"用户"。
// 出厂提示词这一段还没用到它，用临时提示词目录放占位符。
func TestSummaryOwnerName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, owner, want string }{
		{"没配", "", "用户"},
		{"配了", "主人", "主人"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			body := "称呼={{owner_name}} 名字={{name}} 候选={{candidates}} 现在={{now}} 已知={{known}} 说话人={{speakers}} 记录={{transcript}}"
			if err := os.WriteFile(filepath.Join(dir, "summarize-v2.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			r := newRig(t, "console:suzune", rigOpts{mod: func(o *Options) {
				o.Prompts = prompts.Store{Root: repoRoot, UserDir: dir}
				o.OwnerName = c.owner
			}})
			r.say(ownerA, "我下周三考试。")
			r.reply("no", "")
			if res := r.run(); res.Skipped != "" {
				t.Fatalf("该整理一批，跳过了：%s", res.Skipped)
			}
			got := r.gen.last().prompt
			if strings.Contains(got, "{{") || !strings.Contains(got, "称呼="+c.want) {
				t.Fatalf("称呼没填成 %s：\n%s", c.want, got)
			}
		})
	}
}
