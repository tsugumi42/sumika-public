package tools

// read 的分流和大文件窗口（规格-读文件和文档）。数字的来源写在规格第二节。

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/media"
	"sumika-native/internal/mediastore"
)

const (
	sniffBytes      = 8 * 1024  // 判断文件类型看的头部
	bufferedTextMax = 4 << 20   // 这么大以内整读，语义和 Bun 录像一样；再大流式
	streamChunk     = 256 << 10 // 流式读的块
	countLinesMax   = 64 << 20  // 这么大以内才数总行数；再大提示里写文件大小
)

func read(ctx context.Context, cwd string, args jsjson.Value) (string, []media.Image, error) {
	path, _ := argString(args, "path")
	abs := resolveReadPath(path, cwd)
	// 先 Stat 再打开：FIFO 在打开时就会卡住，设备（CON）读的时候卡住
	fi, err := os.Stat(abs)
	if err != nil {
		return "", nil, readOpenError(err, abs)
	}
	if fi.IsDir() {
		return "", nil, fmt.Errorf("这是目录，不是文件：%s。看目录用 ls。", path)
	}
	if fi.Mode()&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
		return "", nil, fmt.Errorf("这不是普通文件（设备或管道），读它会一直卡住：%s", path)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", nil, readOpenError(err, abs)
	}
	defer f.Close()
	if fi, err = f.Stat(); err != nil {
		return "", nil, err
	}
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", nil, err
	}
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	if looksLikeImage(head) {
		// 长边缩到 1024 内、转成 PNG 随结果交给模型；是否真的发出去由请求拼装按模型能否看图决定。
		// PrepareImage 自己限 20MB，文件再大也不会读进内存
		data, mime, err := mediastore.PrepareImage(f)
		if err != nil {
			return "", nil, fmt.Errorf("读不了这张图片：%s：%w", path, err)
		}
		return "Read image file [" + mime + "]", []media.Image{{DataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}, nil
	}
	if looksLikePDF(head) {
		rememberFile(ctx, abs, fi)
		return readPDF(ctx, abs, path, fi.Size(), args)
	}
	if bytes.HasPrefix(head, []byte("PK")) {
		if text, ok, err := readOffice(ctx, f, abs, path, fi.Size(), args); ok {
			if err == nil {
				rememberFile(ctx, abs, fi)
			}
			return text, nil, err
		}
		// 普通压缩包或损坏的 ZIP：文件头就是 PK，当文本读只会得到半截乱码，一律按二进制说明
		return "", nil, errors.New(describeBinary(path, head, fi.Size()))
	}
	if _, ok := argNum(args, "page"); ok {
		return "", nil, fmt.Errorf("page 只用于 PDF，%s 不是 PDF。文本用 offset、limit 分段。", path)
	}
	rememberFile(ctx, abs, fi) // 打开时的版本：读的过程中又被改了，下次写前也会要求重读
	enc := detectEncoding(head, int64(n) >= fi.Size())
	if enc == encBinary {
		return "", nil, errors.New(describeBinary(path, head, fi.Size()))
	}
	text, err := readText(ctx, f, fi.Size(), enc, path, args)
	if err != nil {
		return "", nil, err
	}
	return text, nil, nil
}

// looksLikePDF：%PDF- 在文件开头；规范允许前面有最多 1KB 的杂字节，那种情况头部必然有 NUL 或非文本字节，
// 避免把正文里提到 %PDF- 的源码当 PDF。
func looksLikePDF(head []byte) bool {
	i := bytes.Index(head[:min(len(head), 1024+5)], []byte("%PDF-"))
	return i == 0 || i > 0 && (bytes.IndexByte(head[:i], 0) >= 0 || !utf8.Valid(head[:i]))
}

func readOpenError(err error, abs string) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ENOENT: no such file or directory, access '%s'", abs) // node 的 fs.access 报错原文
	}
	return err
}

// ───────────── 文本编码 ─────────────

type textEncoding int

const (
	encUTF8 textEncoding = iota
	encUTF16
	encShiftJIS
	encGB18030
	encWin1252
	encBinary
)

// detectEncoding 看头部定编码：有 BOM 听 BOM，含 NUL 是二进制，合法 UTF-8 是文本；
// 不合法的不直接判二进制（GBK、Shift-JIS 的文本在这台机器上很常见），试着解一次，解得像才算文本。
func detectEncoding(head []byte, wholeFile bool) textEncoding {
	if bytes.HasPrefix(head, []byte{0xFF, 0xFE}) || bytes.HasPrefix(head, []byte{0xFE, 0xFF}) {
		return encUTF16
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return encBinary
	}
	sample := head
	if !wholeFile {
		sample = trimCutRune(sample)
	}
	if utf8.Valid(sample) {
		return encUTF8
	}
	// 假名是 Shift-JIS 最强的信号：GBK 的字节按 Shift-JIS 解只会出半角假名和生僻汉字，全角假名极少
	if legacyTextScore(japanese.ShiftJIS, head, wholeFile, isKana, 2) {
		return encShiftJIS
	}
	if legacyTextScore(simplifiedchinese.GB18030, head, wholeFile, isHanOrFullwidth, 7) {
		return encGB18030
	}
	// 有 NUL 或控制符多才是二进制；西欧语言的老文本、夹了几个坏字节的小文本按 Windows-1252 读，
	// 以前 os.ReadFile 直接交给模型这类文件能读，不能因为猜不出编码就让模型无路可走
	if controlRatioLow(head) {
		return encWin1252
	}
	return encBinary
}

// controlRatioLow：除制表、换行、回车、换页、ESC 外的控制字节不超过 2%。文本里几乎没有，二进制噪声里约一成。
func controlRatioLow(head []byte) bool {
	ctrl := 0
	for _, b := range head {
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' && b != 0x1b) || b == 0x7f {
			ctrl++
		}
	}
	return ctrl*50 <= len(head)
}

// trimCutRune 去掉头部末尾被 8KB 边界切开的半个 UTF-8 字。
func trimCutRune(b []byte) []byte {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// legacyTextScore 说这段字节按这个编码解出来像不像文本：不能有解不出的字、控制符、私用区，
// 非 ASCII 字符里 good 至少占 tenths 成。
func legacyTextScore(codec encoding.Encoding, head []byte, wholeFile bool, good func(rune) bool, tenths int) bool {
	decoded, err := codec.NewDecoder().Bytes(head)
	if err != nil {
		return false
	}
	bad, wide, hit := 0, 0, 0
	for _, r := range string(decoded) {
		switch {
		case r == utf8.RuneError || (r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' && r != 0x1b) || r == 0x7f || (r >= 0xE000 && r <= 0xF8FF):
			bad++
		case r >= 0x80:
			wide++
			if good(r) {
				hit++
			}
		}
	}
	allowed := 0
	if !wholeFile {
		allowed = 1 // 8KB 边界切开的半个字
	}
	return bad <= allowed && wide > 0 && hit*10 >= wide*tenths
}

func isKana(r rune) bool { return r >= 0x3040 && r <= 0x30FF }

func isHanOrFullwidth(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

func decodeText(f io.Reader, enc textEncoding) io.Reader {
	switch enc {
	case encUTF16:
		return transform.NewReader(f, unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder())
	case encShiftJIS:
		return transform.NewReader(f, japanese.ShiftJIS.NewDecoder())
	case encGB18030:
		return transform.NewReader(f, simplifiedchinese.GB18030.NewDecoder())
	case encWin1252:
		return transform.NewReader(f, charmap.Windows1252.NewDecoder())
	}
	return f
}

// encodingNote 写在结果末尾，不插在正文前面，行号才和文件对得上。
func encodingNote(enc textEncoding) string {
	name := ""
	switch enc {
	case encUTF16:
		name = "UTF-16"
	case encShiftJIS:
		name = "Shift-JIS"
	case encGB18030:
		name = "GB18030"
	case encWin1252:
		return "\n\n[编码不明，按 Windows-1252 显示；其中的字可能不对。edit 匹配不到它；write 会把它存成 UTF-8。]"
	default:
		return ""
	}
	return "\n\n[这个文件是 " + name + " 编码，上面是解码后的文字。edit 匹配不到它；write 会把它存成 UTF-8。]"
}

// describeBinary 按文件头说是什么、多大，不把字节交给模型。
func describeBinary(path string, head []byte, size int64) string {
	kind, advice := "二进制文件", ""
	has := func(p string) bool { return bytes.HasPrefix(head, []byte(p)) }
	switch {
	case has("PK\x03\x04"), has("PK\x05\x06"):
		kind = "ZIP 压缩包"
	case has("\x1f\x8b"):
		kind = "gzip 压缩文件"
	case has("7z\xbc\xaf\x27\x1c"):
		kind = "7z 压缩包"
	case has("Rar!"):
		kind = "RAR 压缩包"
	case has("MZ"):
		kind = "Windows 可执行文件或 DLL"
	case has("\x7fELF"):
		kind = "Linux 可执行文件"
	case has("\xd0\xcf\x11\xe0"):
		kind, advice = "老式 Office 文档（.doc、.xls、.ppt）", "。提取不了文字，另存为 .docx、.xlsx 后再读"
	case has("SQLite format 3\x00"):
		kind, advice = "SQLite 数据库", "。用 powershell 跑 sqlite3 查"
	case has("\x00asm"):
		kind = "WebAssembly 模块"
	case has("OggS"), has("fLaC"), has("ID3"):
		kind = "音频文件"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		kind = "视频文件"
	}
	return fmt.Sprintf("%s 是%s（%s），不能当文本读%s。", path, kind, formatSize(int(size)), advice)
}

// cleanWindow 是所有文字结果的最后一道：NUL 说明头部是文本、后面是二进制；半个字换成替换符。
func cleanWindow(text, path string) (string, error) {
	if strings.IndexByte(text, 0) >= 0 {
		return "", fmt.Errorf("%s 这一段里有 NUL 字节，后面是二进制内容，不能当文本读。", path)
	}
	return strings.ToValidUTF8(text, "�"), nil
}

// cutUTF8 切出最多 n 字节，不切开字。
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// longLineHint：一行超过上限时给开头那段，并说明 offset 进不去这一行里面。
func longLineHint(line, size string, more bool, next string) string {
	s := fmt.Sprintf("[Line %s is %s, exceeds %s limit; showing its first %s. offset can't go inside a line: use grep for what you need, or powershell to cut a range.", line, size, formatSize(maxBytes), formatSize(maxBytes))
	if more {
		s += " Use offset=" + next + " for the next line."
	}
	return s + "]"
}

// ───────────── 文本窗口 ─────────────

func readText(ctx context.Context, f io.Reader, size int64, enc textEncoding, path string, args jsjson.Value) (string, error) {
	r := decodeText(f, enc)
	var text string
	if size <= bufferedTextMax {
		b, err := io.ReadAll(r)
		if err != nil {
			return "", err
		}
		// 整读时全文都在内存，头部是文本、后面是二进制的文件在这里就能认出来，不必等它落进窗口
		if bytes.IndexByte(b, 0) >= 0 {
			return "", fmt.Errorf("%s 里有 NUL 字节，后面是二进制内容，不能当文本读。", path)
		}
		text, err = windowText(string(b), path, args)
		if err != nil {
			return "", err
		}
	} else {
		var err error
		text, err = streamWindow(ctx, r, size, path, args)
		if err != nil {
			return "", err
		}
	}
	return text + encodingNote(enc), nil
}

type windowStop int

const (
	stopEOF windowStop = iota
	stopLines
	stopBytes
	stopUser
	stopLongLine
)

// lineWindow 是流式扫一遍的结果。
type lineWindow struct {
	content      string
	outLines     int64
	total        int64 // 总行数，没数是 -1（和 strings.Split 一样算：结尾的换行后面还有一个空行）
	stop         windowStop
	longLineSize int64 // 超长那一行的字节数，没扫完是 -1
	more         bool  // 窗口后面还有内容
}

// scanWindow 从头扫到窗口，内存只留窗口。countAll 为真时扫到文件尾数总行数，否则窗口满了就停。
func scanWindow(ctx context.Context, r io.Reader, start, lineLimit int64, userLimited, countAll bool) (lineWindow, error) {
	w := lineWindow{total: -1, longLineSize: -1}
	var out strings.Builder
	var cur []byte
	var line, curLen int64
	full := false
	buf := make([]byte, streamChunk)

	// finish 处理完整的一行（可能是文件尾没有换行的最后一行）
	finish := func() {
		if !full && line >= start {
			extra := 0
			if w.outLines > 0 {
				extra = 1
			}
			switch {
			case curLen > maxBytes && w.outLines == 0:
				out.WriteString(cutUTF8(string(cur), maxBytes))
				w.outLines, w.stop, w.longLineSize, full = 1, stopLongLine, curLen, true
			case curLen > maxBytes || out.Len()+extra+len(cur) > maxBytes:
				w.stop, full = stopBytes, true
			default:
				if extra == 1 {
					out.WriteByte('\n')
				}
				out.Write(cur)
				if w.outLines++; w.outLines >= lineLimit {
					w.stop, full = stopLines, true
					if userLimited {
						w.stop = stopUser
					}
				}
			}
		}
		line++
		cur, curLen = cur[:0], 0
	}

	for {
		if err := ctx.Err(); err != nil {
			return w, errAborted
		}
		n, rerr := r.Read(buf)
		chunk := buf[:n]
		for len(chunk) > 0 {
			if full && !countAll {
				w.more = true
				w.content = out.String()
				return w, nil
			}
			i := bytes.IndexByte(chunk, '\n')
			seg := chunk
			if i >= 0 {
				seg = chunk[:i]
			}
			curLen += int64(len(seg))
			if !full && line >= start && len(cur) <= maxBytes {
				cur = append(cur, seg[:min(len(seg), maxBytes+1-len(cur))]...)
			}
			if i < 0 {
				chunk = nil
				// 这一行已经超了，又不用数总行数：不必把它扫完（单行几 GB 的文件）
				if !full && !countAll && line >= start && curLen > maxBytes {
					if w.outLines == 0 {
						out.WriteString(cutUTF8(string(cur), maxBytes))
						w.outLines, w.stop, full = 1, stopLongLine, true
					} else {
						w.stop, full = stopBytes, true
					}
				}
				continue
			}
			chunk = chunk[i+1:]
			finish()
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return w, rerr
			}
			break
		}
	}
	finish() // 最后一行：可能没有换行，也可能是换行后面的空行；窗口已满时只计数
	w.total = line
	w.content = out.String()
	w.more = w.stop != stopEOF && w.total > start+w.outLines
	return w, nil
}

// streamWindow 是超过整读上限的文本。offset、limit 在这里取整，limit 小于 1 当没写。
func streamWindow(ctx context.Context, r io.Reader, size int64, path string, args jsjson.Value) (string, error) {
	var start int64
	if offset, ok := argNum(args, "offset"); ok && offset > 1 {
		start = int64(math.Trunc(offset)) - 1
	}
	lineLimit, userLimited := int64(maxLines), false
	if limit, ok := argNum(args, "limit"); ok && limit >= 1 {
		if l := int64(math.Trunc(limit)); l <= maxLines {
			lineLimit, userLimited = l, true
		}
	}
	countAll := size <= countLinesMax
	w, err := scanWindow(ctx, r, start, lineLimit, userLimited, countAll)
	if err != nil {
		return "", err
	}
	if w.outLines == 0 && w.stop == stopEOF {
		return "", fmt.Errorf("Offset %d is beyond end of file (%d lines total)", start+1, w.total)
	}
	first, last := start+1, start+w.outLines
	var hint string
	switch {
	case w.stop == stopLongLine:
		lineSize := "over " + formatSize(maxBytes)
		if w.longLineSize >= 0 {
			lineSize = formatSize(int(w.longLineSize))
		}
		hint = longLineHint(fmt.Sprint(first), lineSize, w.more, fmt.Sprint(first+1))
	case !w.more:
	case w.stop == stopUser && w.total >= 0:
		hint = fmt.Sprintf("[%d more lines in file. Use offset=%d to continue.]", w.total-last, last+1)
	case w.stop == stopUser:
		hint = fmt.Sprintf("[More lines in file. Use offset=%d to continue.]", last+1)
	default:
		of := fmt.Sprintf("of %d", w.total)
		if w.total < 0 {
			of = "of a " + formatSize(int(size)) + " file"
		}
		limited := ""
		if w.stop == stopBytes {
			limited = " (" + formatSize(maxBytes) + " limit)"
		}
		hint = fmt.Sprintf("[Showing lines %d-%d %s%s. Use offset=%d to continue.", first, last, of, limited, last+1)
		if w.total < 0 {
			hint += " grep finds things faster in a file this big."
		}
		hint += "]"
	}
	text := w.content
	if hint != "" {
		text += "\n\n" + hint
	}
	return cleanWindow(text, path)
}
