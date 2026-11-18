package turn

// read 读 PDF 的完整路径：真回合循环 + 假网关 + 真 PDF 引擎，
// 提取的文字、page 渲染出的图片都进入下一次模型请求（规格-读文件和文档第九节）。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/pdftext"
	"sumika-native/internal/tools"
)

// miniPDF 手写一份单页 PDF：content 是内容流，image 为真时带一张 8x8 图。
func miniPDF(content string, image bool) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	if image {
		raw := bytes.Repeat([]byte{0x80, 0x90, 0xA0}, 64)
		objs = append(objs, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(raw), raw))
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return out.Bytes()
}

func TestReadPDFFullPath(t *testing.T) {
	t.Parallel()
	eng := pdftext.New("")
	defer eng.Close()
	tools.SetPDFEngine(eng)
	defer tools.SetPDFEngine(nil)

	wd := t.TempDir()
	text := "BT /F1 12 Tf 72 720 Td (Quarterly tea ledger: oolong 128 jin, puer 64 jin, total looks fine) Tj ET"
	os.WriteFile(filepath.Join(wd, "ledger.pdf"), miniPDF(text, false), 0o644)
	os.WriteFile(filepath.Join(wd, "scan.pdf"), miniPDF("q 612 0 0 792 0 0 cm /Im1 Do Q", true), 0o644)

	h := newHarnessOpts(t, harnessOpts{
		wd: wd, mode: "auto",
		// 真 PDF 引擎在 -race 加满载时第一次提取能超过 30 秒；这里测的是文字和图进下一次请求，不是工具超时，上限只防卡死
		mutate: func(c *Config) {
			c.ToolTimeout = func(string, time.Duration) time.Duration { return 5 * time.Minute }
		},
		replies: `[` + callReply("read", `{"path":"ledger.pdf"}`) + `,` + callReply("read", `{"path":"scan.pdf"}`) + `,` +
			callReply("read", `{"path":"scan.pdf","page":1}`) + `,` + textReply + `]`,
	})
	for i := range h.hub.Models { // 假网关的模型默认不看图：让它声明能看图，图片才会跟着工具结果发出去
		h.hub.Models[i].Modalities = &struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		}{Input: []string{"text", "image"}}
	}
	h.send("看看这两份 PDF")

	// 第二次请求：文字版 PDF 的提取结果在工具结果里
	byID, _ := h.toolMsgs(1)
	if got := byID["c1"]; !strings.Contains(got, "Quarterly tea ledger") || !strings.Contains(got, "PDF 共 1 页") {
		t.Fatalf("文字没进下一次请求：%q", got)
	}
	// 第三次请求：扫描件明说没有文字，并给出看图的办法
	byID, _ = h.toolMsgs(2)
	var scan string
	for _, v := range byID {
		if strings.Contains(v, "没有可用的文字层") {
			scan = v
		}
	}
	if !strings.Contains(scan, "page=1") {
		t.Fatalf("扫描件没有说明看图办法：%v", byID)
	}
	// 第四次请求：page=1 的结果带文字说明，图片随结果交给模型
	byID, _ = h.toolMsgs(3)
	var paged string
	for _, v := range byID {
		if strings.Contains(v, "已转成图片") {
			paged = v
		}
	}
	if paged == "" {
		t.Fatalf("page=1 的结果没进请求：%v", byID)
	}
	if !strings.Contains(h.body(3), "data:image/jpeg;base64,") {
		t.Fatal("渲染出的图片没有进入下一次请求")
	}
}
