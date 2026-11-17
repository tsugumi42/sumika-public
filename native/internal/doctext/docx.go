package doctext

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var headingRe = regexp.MustCompile(`^heading\s*([1-9])$`)

var errStop = errors.New("stop")

// Docx 转 word/document.xml（和脚注、尾注）为文字：标题写成 #，列表写成 "- "，表格每行用 " | " 分隔单元格，
// 图片写 [图片]。跳过注音、域代码、修订里删掉的字和 mc:Fallback（否则文本框的字会重复）。
// 页眉页脚不取；编号列表的序号要跑 Word 的编号引擎才算得出，这里只给 "- "。
func Docx(zr *zip.Reader) (string, error) {
	p := newPkg(zr)
	styles := docxStyles(p)
	out := &limitedBuilder{}
	main, err := p.open("word/document.xml")
	if err != nil {
		return "", err
	}
	if main == nil {
		return "", errors.New("缺 word/document.xml")
	}
	lines, err := docxLines(main, styles)
	truncated := errors.Is(err, errStop)
	if err != nil && !truncated {
		return "", wrapXML(err)
	}
	writeLines(out, lines)
	for _, part := range []struct{ name, title string }{{"word/footnotes.xml", "脚注"}, {"word/endnotes.xml", "尾注"}} {
		if truncated {
			break
		}
		r, err := p.open(part.name)
		if err != nil || r == nil {
			continue
		}
		fl, err := docxLines(r, styles)
		if err != nil && !errors.Is(err, errStop) {
			continue
		}
		if len(fl) > 0 {
			writeLines(out, append([]string{"", "[" + part.title + "]"}, fl...))
		}
	}
	if p.files["word/comments.xml"] != nil {
		out.WriteString("\n\n[文档里有批注，没有提取。]")
	}
	if truncated || out.stopped {
		out.b.WriteString("\n\n[文字超过 16 MiB，只转换到这里。]")
	}
	return strings.TrimSpace(collapseBlank(out.b.String())), nil
}

func wrapXML(err error) error {
	if errors.Is(err, errTooBig) {
		return err
	}
	return errors.New("文档内容损坏：" + err.Error())
}

func writeLines(out *limitedBuilder, lines []string) {
	for _, l := range lines {
		if !out.WriteString(l + "\n") {
			return
		}
	}
}

func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// docxStyles 读 styles.xml：styleId → 标题级别（0 不是标题）。
func docxStyles(p *pkg) map[string]int {
	out := map[string]int{}
	b, err := p.readAll("word/styles.xml")
	if err != nil || b == nil {
		return out
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	cur := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "style":
				cur = attr(se, "styleId")
			case "name":
				if cur == "" {
					break
				}
				n := strings.ToLower(attr(se, "val"))
				if m := headingRe.FindStringSubmatch(n); m != nil {
					out[cur], _ = strconv.Atoi(m[1])
				} else if n == "title" {
					out[cur] = 1
				}
			}
		}
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "style" {
			cur = ""
		}
	}
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

type docPara struct {
	text  strings.Builder
	level int
	list  bool
}

type docTable struct {
	rows [][]string
	row  []string
}

// docxLines 流式解析一个部件，返回按行的文字。
func docxLines(r io.Reader, styles map[string]int) ([]string, error) {
	dec := xml.NewDecoder(r)
	var (
		sinks  = [][]string{nil} // 最外层是正文；每个单元格再压一层
		paras  []*docPara
		tables []*docTable
		skip   = 0 // >0：正在跳过一棵子树，值是嵌套深度
		inT    bool
		size   int
	)
	emit := func(line string) error {
		top := len(sinks) - 1
		sinks[top] = append(sinks[top], line)
		if top == 0 {
			if size += len(line) + 1; size > MaxTextBytes {
				return errStop
			}
		}
		return nil
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sinks[0], err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch t.Name.Local {
			case "rt", "Fallback", "instrText", "delText", "pPrChange", "rPrChange":
				skip = 1
			case "footnote", "endnote":
				if ty := attr(t, "type"); ty == "separator" || ty == "continuationSeparator" || ty == "continuationNotice" {
					skip = 1
				}
			case "p":
				paras = append(paras, &docPara{})
			case "pStyle":
				if len(paras) > 0 {
					paras[len(paras)-1].level = styles[attr(t, "val")]
				}
			case "numPr":
				if len(paras) > 0 {
					paras[len(paras)-1].list = true
				}
			case "numId":
				if len(paras) > 0 && attr(t, "val") == "0" {
					paras[len(paras)-1].list = false
				}
			case "t":
				inT = true
			case "tab":
				addText(paras, "\t")
			case "br", "cr":
				addText(paras, "\n")
			case "noBreakHyphen":
				addText(paras, "-")
			case "blip", "imagedata":
				addText(paras, "[图片]")
			case "tbl":
				tables = append(tables, &docTable{})
			case "tr":
				if n := len(tables); n > 0 {
					tables[n-1].row = nil
				}
			case "tc":
				sinks = append(sinks, nil)
			}
		case xml.CharData:
			if skip == 0 && inT {
				addText(paras, string(t))
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				n := len(paras)
				if n == 0 {
					break
				}
				p := paras[n-1]
				paras = paras[:n-1]
				text := strings.TrimRight(p.text.String(), " \t\n")
				line := text
				switch {
				case text == "":
				case p.level > 0:
					line = strings.Repeat("#", min(p.level, 6)) + " " + text
				case p.list:
					line = "- " + text
				}
				if line == "" && len(sinks) == 1 {
					// 空段落只保留一个空行
					if l := sinks[0]; len(l) > 0 && l[len(l)-1] == "" {
						break
					}
				}
				if line == "" && len(sinks) > 1 {
					break
				}
				if err := emit(line); err != nil {
					return sinks[0], err
				}
			case "tc":
				n := len(sinks)
				if n < 2 {
					break
				}
				lines := sinks[n-1]
				sinks = sinks[:n-1]
				cell := strings.TrimSpace(strings.Join(lines, " "))
				if tn := len(tables); tn > 0 {
					tables[tn-1].row = append(tables[tn-1].row, cell)
				}
			case "tr":
				if tn := len(tables); tn > 0 {
					t := tables[tn-1]
					t.rows = append(t.rows, t.row)
					t.row = nil
				}
			case "tbl":
				tn := len(tables)
				if tn == 0 {
					break
				}
				t := tables[tn-1]
				tables = tables[:tn-1]
				for _, row := range t.rows {
					if err := emit(strings.Join(row, " | ")); err != nil {
						return sinks[0], err
					}
				}
				if len(sinks) == 1 {
					if err := emit(""); err != nil {
						return sinks[0], err
					}
				}
			}
		}
	}
	return sinks[0], nil
}

func addText(paras []*docPara, s string) {
	if n := len(paras); n > 0 {
		paras[n-1].text.WriteString(s)
	}
}
