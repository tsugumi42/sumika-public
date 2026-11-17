package doctext

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxXlsxCols = 256

type sheetInfo struct {
	name, rid, state string
}

// Xlsx 转成文字：按工作簿顺序，每张表一段，一行一行，单元格用制表符分隔（里面的换行和制表符换成空格）。
// 隐藏的表跳过并在结尾写名字；日期按样式转成 2006-01-02，处理 date1904；公式显示缓存值，没缓存显示 =公式。
func Xlsx(zr *zip.Reader) (string, error) {
	p := newPkg(zr)
	wb, err := p.readAll("xl/workbook.xml")
	if err != nil {
		return "", wrapXML(err)
	}
	if wb == nil {
		return "", errors.New("缺 xl/workbook.xml")
	}
	sheets, date1904, err := parseWorkbook(wb)
	if err != nil {
		return "", wrapXML(err)
	}
	rels, err := parseRels(p, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", wrapXML(err)
	}
	shared, err := parseShared(p)
	if err != nil {
		return "", wrapXML(err)
	}
	dateStyles := parseDateStyles(p)

	out := &limitedBuilder{}
	var hidden []string
	truncated := ""
	for _, sh := range sheets {
		if sh.state == "hidden" || sh.state == "veryHidden" {
			hidden = append(hidden, sh.name)
			continue
		}
		target := rels[sh.rid]
		if target == "" {
			continue
		}
		part := target
		if strings.HasPrefix(target, "/") {
			part = strings.TrimPrefix(target, "/")
		} else {
			part = path.Join("xl", target)
		}
		r, err := p.open(part)
		if err != nil {
			return "", wrapXML(err)
		}
		if r == nil {
			continue
		}
		if !out.WriteString("## 工作表：" + sh.name + "\n") {
			truncated = sh.name
			break
		}
		stopRow, err := writeSheet(out, r, shared, dateStyles, date1904)
		if err != nil {
			return "", wrapXML(err)
		}
		if stopRow > 0 {
			truncated = fmt.Sprintf("%s 第 %d 行", sh.name, stopRow)
			break
		}
		out.WriteString("\n")
	}
	if truncated != "" {
		out.b.WriteString("\n[文字超过 16 MiB，只转换到 " + truncated + "。]")
	}
	if len(hidden) > 0 {
		out.b.WriteString("\n[跳过了隐藏的工作表：" + strings.Join(hidden, "、") + "。]")
	}
	return strings.TrimSpace(out.b.String()), nil
}

func parseWorkbook(b []byte) (sheets []sheetInfo, date1904 bool, err error) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			return
		}
		if e != nil {
			return nil, false, e
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "workbookPr":
				v := attr(se, "date1904")
				date1904 = v == "1" || v == "true"
			case "sheet":
				sheets = append(sheets, sheetInfo{name: attr(se, "name"), rid: attr(se, "id"), state: attr(se, "state")})
			}
		}
	}
}

func parseRels(p *pkg, name string) (map[string]string, error) {
	out := map[string]string{}
	b, err := p.readAll(name)
	if err != nil || b == nil {
		return out, err
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			return out, nil
		}
		if e != nil {
			return nil, e
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			out[attr(se, "Id")] = attr(se, "Target")
		}
	}
}

// parseShared 读共享字符串：每个 si 里所有 t 连起来，跳过拼音（rPh）。
func parseShared(p *pkg) ([]string, error) {
	r, err := p.open("xl/sharedStrings.xml")
	if err != nil || r == nil {
		return nil, err
	}
	dec := xml.NewDecoder(r)
	var out []string
	var cur strings.Builder
	inSI, inT, skip := false, false, 0
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			return out, nil
		}
		if e != nil {
			return nil, e
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "rPh":
				skip = 1
			case "t":
				inT = inSI
			}
		case xml.CharData:
			if inT && skip == 0 {
				cur.Write(t)
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				inSI = false
			}
		}
	}
}

// parseDateStyles：cellXfs 里哪些样式序号是日期格式。
func parseDateStyles(p *pkg) map[int]bool {
	out := map[int]bool{}
	b, err := p.readAll("xl/styles.xml")
	if err != nil || b == nil {
		return out
	}
	custom := map[int]string{}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	inXfs := false
	idx := 0
	for {
		tok, e := dec.Token()
		if e != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmt":
				if id, err := strconv.Atoi(attr(t, "numFmtId")); err == nil {
					custom[id] = attr(t, "formatCode")
				}
			case "cellXfs":
				inXfs = true
			case "xf":
				if inXfs {
					if id, err := strconv.Atoi(attr(t, "numFmtId")); err == nil && isDateFormat(id, custom) {
						out[idx] = true
					}
					idx++
				}
			}
		case xml.EndElement:
			if t.Name.Local == "cellXfs" {
				return out
			}
		}
	}
}

func isDateFormat(id int, custom map[int]string) bool {
	switch {
	case id >= 14 && id <= 22, id >= 27 && id <= 36, id >= 45 && id <= 47, id >= 50 && id <= 58:
		return true
	}
	code, ok := custom[id]
	if !ok {
		return false
	}
	// 去掉引号里的字面量和方括号里的颜色、条件，剩下的有 y m d h s 就是日期
	var b strings.Builder
	inQ, inB := false, false
	for _, r := range code {
		switch {
		case r == '"':
			inQ = !inQ
		case inQ:
		case r == '[':
			inB = true
		case r == ']':
			inB = false
		case inB:
		default:
			b.WriteRune(r)
		}
	}
	return strings.ContainsAny(strings.ToLower(b.String()), "ymdhs")
}

func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			n = n*26 + int(r-'A'+1)
		} else if r >= 'a' && r <= 'z' {
			n = n*26 + int(r-'a'+1)
		} else {
			break
		}
	}
	return n - 1
}

func serialToString(v string, date1904 bool) string {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 2958465 {
		return v
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := math.Floor(f)
	t := base.AddDate(0, 0, int(days)).Add(time.Duration(math.Round((f-days)*86400)) * time.Second)
	switch {
	case f < 1:
		return t.Format("15:04:05")
	case f == days:
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

var cellSpace = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// writeSheet 流式写一张表。返回非 0 表示写到这一行时文字到了上限。
func writeSheet(out *limitedBuilder, r io.Reader, shared []string, dateStyles map[int]bool, date1904 bool) (stopRow int, err error) {
	dec := xml.NewDecoder(r)
	var (
		cells         []string
		rowNum        int
		col           int
		cellType      string
		cellStyle     int
		val, formula  string
		inline        strings.Builder
		inV, inF, inT bool
		inIS          bool
	)
	setCell := func() {
		text := ""
		switch cellType {
		case "s":
			if i, e := strconv.Atoi(strings.TrimSpace(val)); e == nil && i >= 0 && i < len(shared) {
				text = shared[i]
			}
		case "inlineStr":
			text = inline.String()
		case "b":
			if strings.TrimSpace(val) == "1" {
				text = "TRUE"
			} else {
				text = "FALSE"
			}
		case "str", "e":
			text = val
		default:
			text = strings.TrimSpace(val)
			if text != "" && dateStyles[cellStyle] {
				text = serialToString(text, date1904)
			}
		}
		if text == "" && formula != "" && val == "" {
			text = "=" + formula
		}
		if col >= maxXlsxCols {
			return
		}
		for len(cells) <= col {
			cells = append(cells, "")
		}
		cells[col] = cellSpace.Replace(text)
	}
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			return 0, nil
		}
		if e != nil {
			return 0, e
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				cells = cells[:0]
				if n, e := strconv.Atoi(attr(t, "r")); e == nil {
					rowNum = n
				} else {
					rowNum++
				}
			case "c":
				if ref := attr(t, "r"); ref != "" {
					col = colIndex(ref)
				} else {
					col++
				}
				cellType = attr(t, "t")
				cellStyle, _ = strconv.Atoi(attr(t, "s"))
				val, formula = "", ""
				inline.Reset()
			case "v":
				inV = true
			case "f":
				inF = true
			case "is":
				inIS = true
			case "t":
				inT = inIS
			case "rPh":
				// 拼音：inline 里的 rPh 很少见，t 照常收会重复，这里整体不特殊处理
			}
		case xml.CharData:
			switch {
			case inV:
				val += string(t)
			case inF:
				formula += string(t)
			case inT:
				inline.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "f":
				inF = false
			case "t":
				inT = false
			case "is":
				inIS = false
			case "c":
				setCell()
			case "row":
				end := len(cells)
				for end > 0 && cells[end-1] == "" {
					end--
				}
				if end > 0 {
					if !out.WriteString(strings.Join(cells[:end], "\t") + "\n") {
						return rowNum, nil
					}
				}
			}
		}
	}
}
