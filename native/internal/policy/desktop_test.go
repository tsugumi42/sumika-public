package policy

import (
	"testing"

	"sumika-native/internal/jsjson"
)

// 剪贴板、打开、通知的审批判定：读剪贴板和通知不问；写剪贴板只在 auto 下直接做；打开任何模式都问；QQ 和群聊一律拒绝
func TestDesktopToolDecisions(t *testing.T) {
	local := Audience{Channel: "console", PrivateOk: true}
	cases := []struct {
		name, tool, args, mode string
		aud                    Audience
		want                   string
	}{
		{"读剪贴板·manual", "clipboard", `{"action":"read"}`, "manual", local, "allow"},
		{"读剪贴板·auto", "clipboard", `{"action":"read"}`, "auto", local, "allow"},
		{"读剪贴板·yolo", "clipboard", `{"action":"read"}`, "yolo", local, "allow"},
		{"写剪贴板·manual", "clipboard", `{"action":"write","text":"a"}`, "manual", local, "ask"},
		{"写剪贴板·auto", "clipboard", `{"action":"write","text":"a"}`, "auto", local, "allow"},
		{"写剪贴板·yolo", "clipboard", `{"action":"write","text":"a"}`, "yolo", local, "ask"},
		{"打开·manual", "open", `{"target":"https://example.com"}`, "manual", local, "ask"},
		{"打开·auto", "open", `{"target":"https://example.com"}`, "auto", local, "ask"},
		{"打开·yolo", "open", `{"target":"https://example.com"}`, "yolo", local, "ask"},
		{"通知·manual", "notify", `{"title":"x"}`, "manual", local, "allow"},
		{"通知·auto", "notify", `{"title":"x"}`, "auto", local, "allow"},
		{"QQ 私聊读剪贴板", "clipboard", `{"action":"read"}`, "auto", Audience{Channel: "qq", PrivateOk: true}, "deny"},
		{"QQ 私聊打开", "open", `{"target":"https://example.com"}`, "yolo", Audience{Channel: "qq", PrivateOk: true}, "deny"},
		{"本地群聊通知", "notify", `{"title":"x"}`, "auto", Audience{Channel: "console"}, "deny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, err := jsjson.Parse(c.args)
			if err != nil {
				t.Fatal(err)
			}
			d := Decide(c.tool, Specs[c.tool], args, Env{Mode: c.mode, Declared: true, Workdir: t.TempDir(), Audience: c.aud})
			if d.Kind != c.want {
				t.Fatalf("%s：Decide = %s（%s），要 %s", c.tool, d.Kind, d.Reason, c.want)
			}
		})
	}
}
