package pdftext

// 测试用的 PDF 生成器：样本全在测试里编出来，不入库、不用真实文件。

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"crypto/rc4"
	"encoding/hex"
	"fmt"
	"strings"
)

type pobj struct {
	dict   string // 不含 /Length 的字典内容，如 "/Type /Page ..."；没有流时是完整对象体
	stream []byte
	raw    bool // true：dict 是完整对象体（不加 << >>）
	plain  bool // true：加密时不动它（Encrypt 字典）
}

type pdfDoc struct {
	objs   []pobj
	pages  []int // 页对象号
	enc    *encSpec
	fonts  string // 页资源里的字体字典内容
	xobjs  string
	header string
}

type encSpec struct{ user, owner string }

func (d *pdfDoc) add(o pobj) int { d.objs = append(d.objs, o); return len(d.objs) }

func flate(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(s)
}

// newDoc 预留 1 号（目录）、2 号（页树）。
func newDoc() *pdfDoc {
	d := &pdfDoc{header: "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"}
	d.add(pobj{}) // 1 目录，最后填
	d.add(pobj{}) // 2 页树，最后填
	return d
}

// addPage 加一页：content 是内容流；compress 为真则 Flate 压缩。
func (d *pdfDoc) addPage(content string, compress bool, extra string) {
	cs := pobj{stream: []byte(content)}
	if compress {
		cs.stream = flate([]byte(content))
		cs.dict = "/Filter /FlateDecode"
	}
	c := d.add(cs)
	res := fmt.Sprintf("/Resources << /Font << %s >> /XObject << %s >> >>", d.fonts, d.xobjs)
	p := d.add(pobj{dict: fmt.Sprintf("/Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R %s %s", c, res, extra)})
	d.pages = append(d.pages, p)
}

func (d *pdfDoc) helvetica() {
	f := d.add(pobj{dict: "/Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding"})
	d.fonts += fmt.Sprintf("/F1 %d 0 R ", f)
}

// cidFont 加一个 Identity-H 的 Type0 字体（不嵌字体），ToUnicode 把码 1..n 映射到 chars。
func (d *pdfDoc) cidFont(name string, chars []rune) {
	var cm strings.Builder
	cm.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n/CMapName /Adobe-Identity-UCS def /CMapType 2 def\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	fmt.Fprintf(&cm, "%d beginbfchar\n", len(chars))
	for i, r := range chars {
		fmt.Fprintf(&cm, "<%04X> <%04X>\n", i+1, r)
	}
	cm.WriteString("endbfchar\nendcmap CMapName currentdict /CMap defineresource pop end end\n")
	tu := d.add(pobj{stream: []byte(cm.String())})
	desc := d.add(pobj{dict: "/Type /FontDescriptor /FontName /TestSong /Flags 4 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80"})
	cid := d.add(pobj{dict: fmt.Sprintf("/Type /Font /Subtype /CIDFontType2 /BaseFont /TestSong /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /DW 1000", desc)})
	f := d.add(pobj{dict: fmt.Sprintf("/Type /Font /Subtype /Type0 /BaseFont /TestSong /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R", cid, tu)})
	d.fonts += fmt.Sprintf("/%s %d 0 R ", name, f)
}

// image 加一个 8x8 灰度图 XObject，页里用 /Im1 引用。
func (d *pdfDoc) image() {
	raw := bytes.Repeat([]byte{0x80, 0x90, 0xA0}, 64)
	o := d.add(pobj{stream: raw, dict: "/Type /XObject /Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceRGB /BitsPerComponent 8"})
	d.xobjs += fmt.Sprintf("/Im1 %d 0 R ", o)
}

func hexOf(code []rune) string {
	var b strings.Builder
	b.WriteString("<")
	for _, c := range code {
		fmt.Fprintf(&b, "%04X", c)
	}
	b.WriteString(">")
	return b.String()
}

var padStr = []byte{0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A}

func padPw(p string) []byte {
	b := append([]byte(p), padStr...)
	return b[:32]
}

func rc4Do(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key)
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// 标准安全处理器 V1 R2（RC4 40 位）。
func (d *pdfDoc) encryptParams() (key, o, u []byte) {
	ownerKey := md5.Sum(padPw(d.enc.owner))
	o = rc4Do(ownerKey[:5], padPw(d.enc.user))
	id := []byte("0123456789abcdef")
	h := md5.New()
	h.Write(padPw(d.enc.user))
	h.Write(o)
	h.Write([]byte{0xFC, 0xFF, 0xFF, 0xFF})
	h.Write(id)
	key = h.Sum(nil)[:5]
	u = rc4Do(key, padStr)
	return
}

func objKey(key []byte, num int) []byte {
	h := md5.New()
	h.Write(key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), 0, 0})
	return h.Sum(nil)[:10]
}

func (d *pdfDoc) bytes() []byte {
	var kids []string
	for _, p := range d.pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", p))
	}
	d.objs[0] = pobj{dict: "/Type /Catalog /Pages 2 0 R"}
	d.objs[1] = pobj{dict: fmt.Sprintf("/Type /Pages /Kids [%s] /Count %d", strings.Join(kids, " "), len(d.pages))}
	var key []byte
	trailerExtra := ""
	if d.enc != nil {
		k, o, u := d.encryptParams()
		key = k
		n := d.add(pobj{dict: fmt.Sprintf("/Filter /Standard /V 1 /R 2 /O <%s> /U <%s> /P -4", hex.EncodeToString(o), hex.EncodeToString(u)), plain: true})
		trailerExtra = fmt.Sprintf(" /Encrypt %d 0 R /ID [<%s><%s>]", n, hex.EncodeToString([]byte("0123456789abcdef")), hex.EncodeToString([]byte("0123456789abcdef")))
	}
	var out bytes.Buffer
	out.WriteString(d.header)
	offs := make([]int, len(d.objs))
	for i, o := range d.objs {
		offs[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		switch {
		case o.raw:
			out.WriteString(o.dict)
		case o.stream != nil:
			data := o.stream
			if key != nil && !o.plain {
				data = rc4Do(objKey(key, i+1), data)
			}
			fmt.Fprintf(&out, "<< %s /Length %d >>\nstream\n", o.dict, len(data))
			out.Write(data)
			out.WriteString("\nendstream")
		default:
			fmt.Fprintf(&out, "<< %s >>", o.dict)
		}
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(d.objs)+1)
	for _, off := range offs {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", len(d.objs)+1, trailerExtra, xref)
	return out.Bytes()
}

// 常用样本 ----------------------------------------------------------------

func textContent(font string, lines ...string) string {
	var b strings.Builder
	b.WriteString("BT /" + font + " 12 Tf 72 720 Td 16 TL\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "(%s) Tj T*\n", esc(l))
	}
	b.WriteString("ET\n")
	return b.String()
}

const scanContent = "q 612 0 0 792 0 0 cm /Im1 Do Q\n"

func helloPDF(compress bool, pages ...string) []byte {
	d := newDoc()
	d.helvetica()
	for _, p := range pages {
		d.addPage(textContent("F1", strings.Split(p, "\n")...), compress, "")
	}
	return d.bytes()
}

func chinesePDF(lines ...string) []byte {
	d := newDoc()
	set := map[rune]int{}
	var chars []rune
	for _, l := range lines {
		for _, r := range l {
			if _, ok := set[r]; !ok {
				chars = append(chars, r)
				set[r] = len(chars)
			}
		}
	}
	d.cidFont("F2", chars)
	var b strings.Builder
	b.WriteString("BT /F2 14 Tf 72 720 Td 18 TL\n")
	for _, l := range lines {
		var codes []rune
		for _, r := range l {
			codes = append(codes, rune(set[r]))
		}
		fmt.Fprintf(&b, "%s Tj T*\n", hexOf(codes))
	}
	b.WriteString("ET\n")
	d.addPage(b.String(), true, "")
	return d.bytes()
}

func scanPDF(pages int) []byte {
	d := newDoc()
	d.image()
	for i := 0; i < pages; i++ {
		d.addPage(scanContent, true, "")
	}
	return d.bytes()
}
