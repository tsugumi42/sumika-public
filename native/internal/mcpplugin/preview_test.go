package mcpplugin

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func previewFor(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return previewOf(r, raw)
}

func TestPreviewShortTextIsPlain(t *testing.T) {
	got := previewFor(t, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "第一段"}, &mcp.TextContent{Text: "第二段"}}})
	if got != "第一段\n第二段" {
		t.Fatalf("短结果该是文字原样：%q", got)
	}
}

// 超过预算不能把内容全丢掉（主人 2026-11-09：结果超过预览预算，请按引用读取）。
func TestPreviewLongTextKeepsHeadAndTail(t *testing.T) {
	for _, pad := range []string{"字", "a字", "ab字😀"} { // 换几种长度，切点落在多字节字符不同位置
		body := "开头标记" + strings.Repeat(pad, 30000) + "结尾标记"
		got := previewFor(t, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}})
		if len(got) > previewBudget {
			t.Fatalf("超预算：%d", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatal("切出了半个字符")
		}
		if !strings.HasPrefix(got, "开头标记") || !strings.HasSuffix(got, "结尾标记") || !strings.Contains(got, "mcp_result_read") {
			t.Fatalf("该留前后并说明怎么读全文：%q…%q", got[:40], got[len(got)-40:])
		}
		if len(got) < previewBudget-200 {
			t.Fatalf("预算没用足：%d", len(got))
		}
	}
}

func TestPreviewNoTextFallsBackToJSON(t *testing.T) {
	got := previewFor(t, &mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: map[string]any{"条数": 3}})
	if !strings.Contains(got, "条数") {
		t.Fatalf("只有结构化内容该给 JSON：%q", got)
	}
	got = previewFor(t, &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte("x")}}})
	if !strings.Contains(got, "image/png") || strings.Contains(got, "eA==") {
		t.Fatalf("图片不该把数据塞进预览：%q", got)
	}
}
