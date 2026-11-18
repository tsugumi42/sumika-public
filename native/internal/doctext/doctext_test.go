package doctext

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// 样本全在测试里编出来：名字、内容都是编的。

func makeZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func convert(t *testing.T, parts map[string]string) (string, Kind, error) {
	t.Helper()
	b := makeZip(t, parts)
	return Convert(bytes.NewReader(b), int64(len(b)))
}

const wns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func docxParts(body string, extra map[string]string) map[string]string {
	parts := map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document ` + wns + `><w:body>` + body + `</w:body></w:document>`,
		"word/styles.xml": `<w:styles ` + wns + `><w:style w:styleId="H1"><w:name w:val="heading 1"/></w:style><w:style w:styleId="H2"><w:name w:val="Heading 2"/></w:style>` +
			`<w:style w:styleId="T"><w:name w:val="Title"/></w:style><w:style w:styleId="Body"><w:name w:val="Normal"/></w:style></w:styles>`,
	}
	for k, v := range extra {
		parts[k] = v
	}
	return parts
}

func p(text string) string {
	return `<w:p><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func TestDocxStructure(t *testing.T) {
	body := `<w:p><w:pPr><w:pStyle w:val="T"/></w:pPr><w:r><w:t>茶叶账本</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="H2"/></w:pPr><w:r><w:t>第二节</w:t></w:r></w:p>` +
		p("普通段落，有 tab：") + `<w:p><w:r><w:t>甲</w:t><w:tab/><w:t>乙</w:t><w:br/><w:t>换行后</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>列表一</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:numId w:val="0"/></w:numPr></w:pPr><w:r><w:t>numId 0 不是列表</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc>` + p("品名") + `</w:tc><w:tc>` + p("斤数") + `</w:tc></w:tr><w:tr><w:tc>` + p("龙井") + `</w:tc><w:tc>` + p("128") + `</w:tc></w:tr></w:tbl>` +
		`<w:p><w:r><w:drawing><a:graphic><a:graphicData><a:blip r:embed="rId9"/></a:graphicData></a:graphic></w:drawing></w:r></w:p>` +
		`<w:p><w:r><w:t>漢</w:t></w:r><w:r><w:rPr/><w:ruby><w:rubyPr/><w:rt><w:r><w:t>かん</w:t></w:r></w:rt><w:rubyBase><w:r><w:t>字</w:t></w:r></w:rubyBase></w:ruby></w:r></w:p>` +
		`<w:p><w:del><w:r><w:delText>删掉的字</w:delText></w:r></w:del><w:ins><w:r><w:t>新加的字</w:t></w:r></w:ins><w:r><w:instrText> HYPERLINK "http://x" </w:instrText></w:r><w:r><w:t>链接文字</w:t></w:r></w:p>` +
		`<w:p><w:r><mc:AlternateContent><mc:Choice><w:drawing><wps:txbx><w:txbxContent>` + p("文本框里的字") + `</w:txbxContent></wps:txbx></w:drawing></mc:Choice>` +
		`<mc:Fallback><w:pict><w:txbxContent>` + p("文本框里的字") + `</w:txbxContent></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>`
	text, kind, err := convert(t, docxParts(body, nil))
	if err != nil || kind != KindDocx {
		t.Fatal(kind, err)
	}
	for _, want := range []string{"# 茶叶账本", "## 第二节", "甲\t乙\n换行后", "- 列表一", "\nnumId 0 不是列表", "品名 | 斤数", "龙井 | 128", "[图片]", "漢字", "新加的字链接文字"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q：\n%s", want, text)
		}
	}
	for _, bad := range []string{"かん", "删掉的字", "HYPERLINK", "- numId 0"} {
		if strings.Contains(text, bad) {
			t.Errorf("不该有 %q：\n%s", bad, text)
		}
	}
	if n := strings.Count(text, "文本框里的字"); n != 1 {
		t.Errorf("文本框的字出现 %d 次（Fallback 要跳过）", n)
	}
}

func TestDocxNestedTableFootnotesComments(t *testing.T) {
	inner := `<w:tbl><w:tr><w:tc>` + p("内一") + `</w:tc><w:tc>` + p("内二") + `</w:tc></w:tr></w:tbl>`
	body := `<w:tbl><w:tr><w:tc>` + p("外格") + inner + `</w:tc><w:tc>` + p("右格") + `</w:tc></w:tr></w:tbl>` + p("结尾段落")
	fn := `<w:footnotes ` + wns + `><w:footnote w:type="separator" w:id="-1">` + p("分隔线不要") + `</w:footnote><w:footnote w:id="1">` + p("脚注一的内容") + `</w:footnote></w:footnotes>`
	text, _, err := convert(t, docxParts(body, map[string]string{"word/footnotes.xml": fn, "word/comments.xml": `<w:comments ` + wns + `/>`}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "外格 内一 | 内二 | 右格") {
		t.Errorf("嵌套表格：\n%s", text)
	}
	for _, want := range []string{"结尾段落", "[脚注]", "脚注一的内容", "批注"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "分隔线不要") {
		t.Error("脚注分隔线要跳过")
	}
}

func TestPptxAndPlainZipAndBroken(t *testing.T) {
	if _, kind, err := convert(t, map[string]string{"ppt/presentation.xml": "<p/>"}); kind != KindPptx || err == nil || !strings.Contains(err.Error(), "pptx") {
		t.Errorf("pptx：%v %v", kind, err)
	}
	if _, kind, _ := convert(t, map[string]string{"a.txt": "hi"}); kind != KindUnknown {
		t.Errorf("普通 ZIP：%v", kind)
	}
	if _, _, err := convert(t, map[string]string{"word/document.xml": "<w:document><w:body><w:p>"}); err == nil {
		t.Error("坏 XML 应该报错")
	}
	if _, _, err := Convert(bytes.NewReader([]byte("not a zip at all")), 16); err == nil {
		t.Error("不是 ZIP 应该报错")
	}
}

// 压缩炸弹：解压后超过部件上限就停，不信 ZIP 头里的大小；自定义实体不展开。
func TestDocxBombAndEntities(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	io.WriteString(w, `<w:document `+wns+`><w:body>`)
	chunk := bytes.Repeat([]byte(" "), 1<<20)
	for i := 0; i < 260; i++ {
		w.Write(chunk)
	}
	io.WriteString(w, `</w:body></w:document>`)
	zw.Close()
	_, _, err := Convert(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err == nil || !strings.Contains(err.Error(), "太大") {
		t.Fatalf("炸弹要报体积：%v", err)
	}
	t.Logf("260MiB 的部件压缩后 %d 字节，被拒", buf.Len())

	ent := `<!DOCTYPE d [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]><w:document ` + wns + `><w:body><w:p><w:r><w:t>&b;</w:t></w:r></w:p></w:body></w:document>`
	text, _, err := convert(t, map[string]string{"word/document.xml": ent})
	if err == nil && strings.Count(text, "a") > 100 {
		t.Fatalf("实体被展开了：%d", len(text))
	}
}

func TestDocxTextCap(t *testing.T) {
	line := p(strings.Repeat("字", 1000))
	body := strings.Repeat(line, 6000) // 约 18MB 文字
	text, _, err := convert(t, docxParts(body, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "超过 16 MiB") || len(text) > MaxTextBytes+1024 {
		t.Fatalf("文字上限没生效：%d 字节", len(text))
	}
}

// ---------------------------------------------------------------- xlsx

const xns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func xlsxParts(wbExtra string, sheets map[string]string, shared string, styles string) map[string]string {
	parts := map[string]string{
		"xl/workbook.xml": `<workbook ` + xns + `><workbookPr` + wbExtra + `/><sheets>` +
			`<sheet name="总账" sheetId="1" r:id="rId2"/><sheet name="隐藏页" sheetId="2" state="hidden" r:id="rId1"/><sheet name="明细" sheetId="3" r:id="rId3"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Target="/xl/worksheets/sheet3.xml"/></Relationships>`,
		"xl/sharedStrings.xml": shared,
		"xl/styles.xml":        styles,
	}
	for k, v := range sheets {
		parts["xl/worksheets/"+k] = `<worksheet ` + xns + `><sheetData>` + v + `</sheetData></worksheet>`
	}
	return parts
}

const sharedXML = `<sst ` + xns + `><si><t>品名</t></si><si><r><t>龙</t></r><r><t>井</t></r></si><si><t xml:space="preserve">带
换行	和制表</t></si><si><t>汉字</t><rPh sb="0" eb="2"><t>かんじ</t></rPh></si></sst>`

const stylesXML = `<styleSheet ` + xns + `><numFmts count="2"><numFmt numFmtId="164" formatCode="yyyy&quot;年&quot;m&quot;月&quot;"/><numFmt numFmtId="165" formatCode="0.00&quot;d&quot;"/></numFmts>` +
	`<cellStyleXfs count="1"><xf numFmtId="14"/></cellStyleXfs><cellXfs count="4"><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/><xf numFmtId="165"/></cellXfs></styleSheet>`

func TestXlsx(t *testing.T) {
	s2 := `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="D1" t="s"><v>2</v></c></row>` +
		`<row r="2"><c r="A2"><v>128.5</v></c><c r="B2" s="1"><v>46335</v></c><c r="C2" s="1"><v>46335.75</v></c><c r="D2" t="b"><v>1</v></c></row>` +
		`<row r="4"><c r="A4"><f>SUM(A2:A3)</f><v>128.5</v></c><c r="B4"><f>NOW()</f></c><c r="C4" t="inlineStr"><is><t>内联字符串</t></is></c><c r="D4" t="e"><v>#DIV/0!</v></c><c r="E4" s="3"><v>1.5</v></c><c r="F4" s="2"><v>46335</v></c></row>` +
		`<row r="5"><c r="A5" t="s"><v>3</v></c><c r="B5" s="1"><v>0.5</v></c></row>`
	parts := xlsxParts("", map[string]string{"sheet1.xml": `<row r="1"><c r="A1"><v>1</v></c></row>`, "sheet2.xml": s2, "sheet3.xml": `<row r="1"><c r="A1" t="inlineStr"><is><t>明细表第一格</t></is></c></row>`}, sharedXML, stylesXML)
	text, kind, err := convert(t, parts)
	if err != nil || kind != KindXlsx {
		t.Fatal(kind, err)
	}
	for _, want := range []string{"## 工作表：总账", "品名\t龙井\t\t带 换行 和制表", "128.5\t2026-11-09\t2026-11-09 18:00:00\tTRUE", "128.5\t=NOW()\t内联字符串\t#DIV/0!\t1.5\t2026-11-09", "汉字\t12:00:00", "## 工作表：明细", "明细表第一格", "跳过了隐藏的工作表：隐藏页"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %q：\n%s", want, text)
		}
	}
	if strings.Index(text, "总账") > strings.Index(text, "明细") {
		t.Error("要按工作簿顺序")
	}
	if strings.Contains(text, "かんじ") {
		t.Error("拼音要跳过")
	}
	if strings.Contains(text, "## 工作表：隐藏页") {
		t.Error("隐藏表要跳过")
	}
}

func TestXlsxDate1904AndColumnCap(t *testing.T) {
	var cells strings.Builder
	cells.WriteString(`<row r="1"><c r="A1" s="1"><v>1</v></c>`)
	for i := 0; i < 300; i++ {
		cells.WriteString(`<c><v>7</v></c>`)
	}
	cells.WriteString(`</row>`)
	parts := xlsxParts(` date1904="1"`, map[string]string{"sheet2.xml": cells.String(), "sheet1.xml": "", "sheet3.xml": ""}, sharedXML, stylesXML)
	text, _, err := convert(t, parts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "1904-01-02") {
		t.Errorf("date1904：%s", text[:min(len(text), 200)])
	}
	row := strings.Split(strings.Split(text, "\n")[1], "\t")
	if len(row) != maxXlsxCols {
		t.Errorf("列数 %d，想要上限 %d", len(row), maxXlsxCols)
	}
}

func TestXlsxBadAndMissing(t *testing.T) {
	if _, _, err := convert(t, map[string]string{"xl/workbook.xml": "<workbook><sheets>"}); err == nil {
		t.Error("坏工作簿应该报错")
	}
	// 引用了不存在的表部件：跳过，不崩
	parts := xlsxParts("", map[string]string{"sheet2.xml": `<row r="1"><c r="A1"><v>1</v></c></row>`}, sharedXML, stylesXML)
	if _, _, err := convert(t, parts); err != nil {
		t.Error(err)
	}
}
