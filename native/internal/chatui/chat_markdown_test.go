package chatui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 聊天里的 @ 和 #123 是普通字：不按 GitHub 引用读，量宽时也不能少字。
func TestChatMarkdownKeepsMentions(t *testing.T) {
	d := parser.New(chatMarkdown)
	d.Set("问问 @铃花 看 #123，还有 @someone")
	var text strings.Builder
	for i := range d.Children(0) {
		for _, n := range d.Inlines(i) {
			if n.Kind == parser.Mention || n.Kind == parser.Reference {
				t.Fatalf("不该按 GitHub 引用读：%+v", n)
			}
			text.WriteString(n.Text)
		}
	}
	for _, want := range []string{"@铃花", "#123", "@someone"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("少了 %q：%q", want, text.String())
		}
	}
}
