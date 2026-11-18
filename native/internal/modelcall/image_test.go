package modelcall

import (
	"strings"
	"testing"

	"sumika-native/internal/llm/chat"
)

func TestInputEstimateImagePayload(t *testing.T) {
	plain := `[{"role":"user","content":"hello"}]`
	req := chat.Request{Style: chat.StyleForeground, Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("hello")}}}}
	got, err := InputEstimateChat(req)
	if err != nil || got != int64(len(plain)+16) {
		t.Fatalf("plain %d %v", got, err)
	}
	url := "data:image/png;base64," + strings.Repeat("a", 100000)
	req.Messages[0].Blocks = append(req.Messages[0].Blocks, chat.Image{DataURL: url}, chat.Image{DataURL: url})
	got, err = InputEstimateChat(req)
	messages := `[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"` + url + `"}},{"type":"image_url","image_url":{"url":"` + url + `"}}]}]`
	want := int64(len(messages) - 2*(len(url)+2) + 6000 + 16)
	if err != nil || got != want {
		t.Fatalf("images %d want %d: %v", got, want, err)
	}
}
