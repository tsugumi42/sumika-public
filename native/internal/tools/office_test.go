package tools

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, dir, name string, parts map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, body := range parts {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	zw.Close()
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const officeWNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func TestReadDocxXlsxPptx(t *testing.T) {
	wd := t.TempDir()
	writeZip(t, wd, "a.docx", map[string]string{"word/document.xml": `<w:document ` + officeWNS + `><w:body><w:p><w:r><w:t>第一段</w:t></w:r></w:p><w:p><w:r><w:t>第二段</w:t></w:r></w:p></w:body></w:document>`})
	writeZip(t, wd, "b.xlsx", map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="表一" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>甲</t></is></c><c r="B1"><v>2</v></c></row></sheetData></worksheet>`,
	})
	writeZip(t, wd, "c.pptx", map[string]string{"ppt/presentation.xml": "<p/>"})
	writeZip(t, wd, "d.zip", map[string]string{"x.txt": "hello"})

	r := runTool(t, wd, "read", map[string]any{"path": "a.docx"})
	if r.IsError || !strings.Contains(r.Text, "第一段\n第二段") {
		t.Fatalf("docx：%+v", r)
	}
	r = runTool(t, wd, "read", map[string]any{"path": "a.docx", "offset": 2})
	if r.IsError || strings.Contains(r.Text, "第一段") || !strings.Contains(r.Text, "第二段") {
		t.Fatalf("docx 走行窗口：%+v", r)
	}
	r = runTool(t, wd, "read", map[string]any{"path": "b.xlsx"})
	if r.IsError || !strings.Contains(r.Text, "## 工作表：表一") || !strings.Contains(r.Text, "甲\t2") {
		t.Fatalf("xlsx：%+v", r)
	}
	if r = runTool(t, wd, "read", map[string]any{"path": "c.pptx"}); !r.IsError || !strings.Contains(r.Text, "pptx") {
		t.Fatalf("pptx 要明说不支持：%+v", r)
	}
	if r = runTool(t, wd, "read", map[string]any{"path": "d.zip"}); !r.IsError || !strings.Contains(r.Text, "ZIP 压缩包") || !strings.Contains(r.Text, "不能当文本读") {
		t.Fatalf("普通 ZIP 按二进制说明：%+v", r)
	}
	if r = runTool(t, wd, "read", map[string]any{"path": "a.docx", "page": 1}); !r.IsError || !strings.Contains(r.Text, "page 只用于 PDF") {
		t.Fatalf("%+v", r)
	}
	// 损坏的 docx：明说读不了，不退回当文本
	os.WriteFile(filepath.Join(wd, "bad.docx"), []byte("PK\x03\x04garbage garbage garbage"), 0o644)
	if r = runTool(t, wd, "read", map[string]any{"path": "bad.docx"}); !r.IsError || strings.Contains(r.Text, "garbage garbage") {
		t.Fatalf("损坏的 zip：%+v", r)
	}
	// 读过的 docx 之后被改，write 要拒绝
	os.WriteFile(filepath.Join(wd, "a.docx"), []byte("changed outside, longer than before"), 0o644)
	if r = runTool(t, wd, "write", map[string]any{"path": "a.docx", "content": "x"}); !r.IsError {
		t.Fatalf("读过 docx 之后被改，应该拒绝写：%+v", r)
	}
}
