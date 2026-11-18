package pdftext

// 真实模型试验：只在设了 SUMIKA_REAL_PDF_OUT（输出目录）时跑，经本机 Magpie 发真请求；PDF 是测试里编的。
// 两件事：read 提取的文字交给 DeepSeek 能答对里面的事实；page 渲染出的 JPEG 交给能看图的模型能认出页面上的字。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

func ask(t *testing.T, out, label, model string, content any) string {
	t.Helper()
	req := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": content}}, "max_tokens": 400}
	body, _ := json.Marshal(req)
	hc := &http.Client{Timeout: 120 * time.Second}
	resp, err := hc.Post("http://127.0.0.1:3425/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// 请求里的图片只留长度，不把 base64 写进存档
	var slim map[string]any
	json.Unmarshal(body, &slim)
	if ms, ok := slim["messages"].([]any); ok {
		for _, m := range ms {
			if parts, ok := m.(map[string]any)["content"].([]any); ok {
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
						pm["image_url"] = map[string]any{"url": "(data URL 已省略，图另存为 page1.jpg)"}
					}
				}
			}
		}
	}
	rec, _ := json.MarshalIndent(map[string]any{"label": label, "model": model, "status": resp.StatusCode, "request": slim, "response": json.RawMessage(raw)}, "", "  ")
	os.WriteFile(filepath.Join(out, label+".json"), rec, 0o644)
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || len(r.Choices) == 0 {
		t.Fatalf("%s：%d %s", label, resp.StatusCode, raw)
	}
	return r.Choices[0].Message.Content
}

func TestRealModelReadsPDF(t *testing.T) {
	out := os.Getenv("SUMIKA_REAL_PDF_OUT")
	if out == "" {
		t.Skip("设 SUMIKA_REAL_PDF_OUT 才跑真实模型试验")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	e := New("")
	defer e.Close()
	tools.SetPDFEngine(e)
	defer tools.SetPDFEngine(nil)

	wd := t.TempDir()
	d := newDoc()
	d.helvetica()
	d.addPage(textContent("F1", "Ledger of the Maple Street tea shop, autumn quarter.", "Oolong sold: 128 jin at 900 yuan per jin.", "Puer sold: 64 jin at 450 yuan per jin.", "Shop cat name: Biscuit."), true, "")
	d.addPage(textContent("F1", "Second page: the shop closes on Tuesdays.", "Delivery partner: Willow Courier."), true, "")
	os.WriteFile(filepath.Join(wd, "ledger.pdf"), d.bytes(), 0o644)

	args, _ := jsjson.Parse(`{"path":"ledger.pdf"}`)
	res, err := tools.Execute(context.Background(), wd, "read", args)
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	answer := ask(t, out, "deepseek-读文字", "auto/deepseek-flash", "下面是 read 工具读一份 PDF 的结果。请回答：卖出的乌龙一共多少钱？店里的猫叫什么？店周几休息？只答结果。\n\n"+res.Text)
	t.Logf("DeepSeek 的回答：%s", answer)
	for _, want := range []string{"115200", "115,200", "Biscuit", "Tuesday", "周二", "星期二"} {
		if strings.Contains(answer, want) {
			goto textOK
		}
	}
	t.Errorf("回答里没有 PDF 里的事实：%s", answer)
textOK:

	args, _ = jsjson.Parse(`{"path":"ledger.pdf","page":1}`)
	res, err = tools.Execute(context.Background(), wd, "read", args)
	if err != nil || res.IsError || len(res.Images) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	t.Logf("page=1 的图片 data URL 长度 %d", len(res.Images[0].DataURL))
	if i := strings.Index(res.Images[0].DataURL, ","); i > 0 {
		if jpg, err := base64.StdEncoding.DecodeString(res.Images[0].DataURL[i+1:]); err == nil {
			os.WriteFile(filepath.Join(out, "page1.jpg"), jpg, 0o644)
		}
	}
	vision := ask(t, out, "haiku-看图", "auto/claude-haiku-5-5", []any{
		map[string]any{"type": "text", "text": "这张图是一页 PDF。图上写着哪家店的账本？乌龙卖了多少斤？只答结果。"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": res.Images[0].DataURL}},
	})
	t.Logf("看图模型的回答：%s", vision)
	if !strings.Contains(strings.ToLower(vision), "maple") || !strings.Contains(vision, "128") {
		t.Errorf("看图模型没认出页面上的字：%s", vision)
	}
}
