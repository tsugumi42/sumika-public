// Package doctext 把 docx、xlsx 转成文字，只用 archive/zip 和 encoding/xml（规格-读文件和文档第四节）。
// 不信 ZIP 头里的大小：每个部件走 io.LimitReader，总量也有上限；encoding/xml 不展开自定义实体。
package doctext

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxPartBytes  = 256 << 20 // 单个 XML 部件解压后的上限
	maxTotalBytes = 512 << 20 // 一份文件总共解压的上限
	MaxTextBytes  = 16 << 20  // 输出文字的上限，到了就停并写明
)

// Kind 是 ZIP 里认出的文档类型。
type Kind int

const (
	KindUnknown Kind = iota
	KindDocx
	KindXlsx
	KindPptx
)

// Detect 看 ZIP 里有哪个主部件。
func Detect(zr *zip.Reader) Kind {
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			return KindDocx
		case "xl/workbook.xml":
			return KindXlsx
		case "ppt/presentation.xml":
			return KindPptx
		}
	}
	return KindUnknown
}

type pkg struct {
	zr    *zip.Reader
	files map[string]*zip.File
	total int64
}

func newPkg(zr *zip.Reader) *pkg {
	p := &pkg{zr: zr, files: map[string]*zip.File{}}
	for _, f := range zr.File {
		p.files[strings.ToLower(f.Name)] = f
	}
	return p
}

// ErrCorruptZip：文件头是 ZIP 但打不开（损坏或被截断）。
var ErrCorruptZip = errors.New("ZIP 文件损坏")

var errTooBig = errors.New("文档解压后太大，不读")

// open 打开一个部件，没有返回 nil。读超过上限时 Read 会报 errTooBig。
func (p *pkg) open(name string) (io.Reader, error) {
	f := p.files[strings.ToLower(name)]
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return &capReader{rc: rc, p: p}, nil
}

type capReader struct {
	rc   io.ReadCloser
	p    *pkg
	read int64
}

func (c *capReader) Read(b []byte) (int, error) {
	n, err := c.rc.Read(b)
	c.read += int64(n)
	c.p.total += int64(n)
	if c.read > maxPartBytes || c.p.total > maxTotalBytes {
		c.rc.Close()
		return n, errTooBig
	}
	if err != nil {
		c.rc.Close()
	}
	return n, err
}

func (p *pkg) readAll(name string) ([]byte, error) {
	r, err := p.open(name)
	if err != nil || r == nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// Convert 按类型转成文字。pptx 和未知类型返回说明性错误。
func Convert(ra io.ReaderAt, size int64) (text string, kind Kind, err error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return "", KindUnknown, fmt.Errorf("%w：%v", ErrCorruptZip, err)
	}
	kind = Detect(zr)
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("文档结构异常：%v", r)
		}
	}()
	switch kind {
	case KindDocx:
		text, err = Docx(zr)
	case KindXlsx:
		text, err = Xlsx(zr)
	case KindPptx:
		err = errors.New("这是 PowerPoint（.pptx），目前不支持提取文字。另存为 PDF 后可以读。")
	default:
		err = errors.New("是 ZIP 压缩包，不是 Office 文档")
	}
	return text, kind, err
}

// 输出拼接，带上限。
type limitedBuilder struct {
	b       bytes.Buffer
	stopped bool
}

func (l *limitedBuilder) WriteString(s string) bool {
	if l.stopped {
		return false
	}
	if l.b.Len()+len(s) > MaxTextBytes {
		l.stopped = true
		return false
	}
	l.b.WriteString(s)
	return true
}
