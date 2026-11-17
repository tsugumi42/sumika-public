package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// writeLines 写 n 行（甲 + 行号），每行补到 width 字节，返回文件大小。
func writeLines(t *testing.T, path string, n, width int) int64 {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b bytes.Buffer
	var size int64
	for i := 1; i <= n; i++ {
		s := fmt.Sprintf("甲%d", i)
		b.WriteString(s)
		if pad := width - len(s) - 1; pad > 0 {
			b.WriteString(strings.Repeat("x", pad))
		}
		b.WriteByte('\n')
		if b.Len() > 1<<20 {
			size += int64(b.Len())
			if _, err := f.Write(b.Bytes()); err != nil {
				t.Fatal(err)
			}
			b.Reset()
		}
	}
	size += int64(b.Len())
	if _, err := f.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	return size
}

// 旧代码先 os.ReadFile 再切行：70MiB 的文件要分配 70MiB 以上。这里只许窗口那么多。
func TestReadLargeFileStaysSmallInMemory(t *testing.T) {
	wd := t.TempDir()
	size := writeLines(t, filepath.Join(wd, "big.log"), 700_000, 100)
	if size <= countLinesMax {
		t.Fatalf("样本要超过数行上限才走不数总行数的路：%d", size)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := runTool(t, wd, "read", map[string]any{"path": "big.log"})
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 8<<20 {
		t.Fatalf("读 %d 字节的文件分配了 %d 字节，窗口之外不该进内存", size, used)
	}
	if r.IsError || !strings.HasPrefix(r.Text, "甲1x") {
		t.Fatalf("开头不对：%.80q", r.Text)
	}
	if !strings.Contains(r.Text, "(50.0KB limit)") || !strings.Contains(r.Text, "of a 66.8MB file") || !strings.Contains(r.Text, "grep") {
		t.Fatalf("超大文件的提示不对：%q", r.Text[len(r.Text)-160:])
	}
	if strings.Contains(r.Text, "lines total") {
		t.Fatal("没数总行数就不该说总行数")
	}
}

// 分段续读行号要衔接：按提示里的 offset 读下去，正好读完全部行，不重不漏。
func TestReadStreamPagesWholeFile(t *testing.T) {
	wd := t.TempDir()
	const lines = 120_000
	size := writeLines(t, filepath.Join(wd, "mid.txt"), lines, 50)
	if size <= bufferedTextMax || size > countLinesMax {
		t.Fatalf("样本要落在流式、数总行数的区间：%d", size)
	}
	offset, seen := 1, 0
	for round := 0; ; round++ {
		if round > 200 {
			t.Fatal("读不完")
		}
		r := runTool(t, wd, "read", map[string]any{"path": "mid.txt", "offset": offset})
		if r.IsError {
			t.Fatalf("offset=%d：%s", offset, r.Text)
		}
		body, hint, more := strings.Cut(r.Text, "\n\n[")
		for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
			seen++
			if want := fmt.Sprintf("甲%d", seen); !strings.HasPrefix(line, want+"x") {
				t.Fatalf("第 %d 行是 %q", seen, line)
			}
		}
		if !more {
			break
		}
		if !strings.Contains(hint, fmt.Sprintf("of %d", lines+1)) {
			t.Fatalf("总行数提示不对：%q", hint)
		}
		if _, err := fmt.Sscanf(hint[strings.Index(hint, "offset=")+len("offset="):], "%d", &offset); err != nil {
			t.Fatalf("提示里没有 offset：%q", hint)
		}
	}
	if seen != lines {
		t.Fatalf("读到 %d 行", seen)
	}
}

func TestReadStreamOffsetAndLimit(t *testing.T) {
	wd := t.TempDir()
	writeLines(t, filepath.Join(wd, "mid.txt"), 120_000, 50)
	r := runTool(t, wd, "read", map[string]any{"path": "mid.txt", "offset": 100_000, "limit": 2})
	if r.IsError || !strings.HasPrefix(r.Text, "甲100000x") || !strings.Contains(r.Text, "\n甲100001x") {
		t.Fatalf("窗口不对：%.120q", r.Text)
	}
	if !strings.HasSuffix(r.Text, "[20000 more lines in file. Use offset=100002 to continue.]") {
		t.Fatalf("提示不对：%q", r.Text[len(r.Text)-80:])
	}
	// 小数取整，limit 小于 1 当没写，和整读路线的 JS 语义不同，但都不崩
	r = runTool(t, wd, "read", map[string]any{"path": "mid.txt", "offset": 2.5, "limit": -3})
	if r.IsError || !strings.HasPrefix(r.Text, "甲2x") {
		t.Fatalf("小数和负数：%.80q", r.Text)
	}
	r = runTool(t, wd, "read", map[string]any{"path": "mid.txt", "offset": 500_000})
	if !r.IsError || !strings.Contains(r.Text, "beyond end of file (120001 lines total)") {
		t.Fatalf("超过文件尾：%q", r.Text)
	}
}

// 不数总行数的路上 limit 到了也要给续读提示，读到文件尾不能说还有。
func TestReadUncountedLimitHints(t *testing.T) {
	var buf bytes.Buffer
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "第%d行\n", i)
	}
	w, err := scanWindow(t.Context(), bytes.NewReader(buf.Bytes()), 1, 2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.content != "第2行\n第3行" || w.stop != stopUser || !w.more || w.total != -1 {
		t.Fatalf("%+v", w)
	}
	// 窗口正好到最后一行后面的空行：没有更多
	w, err = scanWindow(t.Context(), strings.NewReader("甲\n乙"), 0, 2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.content != "甲\n乙" || w.more {
		t.Fatalf("%+v", w)
	}
}

// 单行超过上限：给开头，不切开字；旧代码只回一句 bash 的 sed 提示。
func TestReadLongLineShowsItsHead(t *testing.T) {
	wd := t.TempDir()
	long := strings.Repeat("字", maxBytes) // 3 倍上限
	cases := map[string]string{
		"小文件": long + "\n第二行\n",
		"大文件": long + "\n第二行\n" + strings.Repeat("填充\n", bufferedTextMax/6),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(wd, "long.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			r := runTool(t, wd, "read", map[string]any{"path": "long.txt"})
			if r.IsError {
				t.Fatal(r.Text)
			}
			body, hint, ok := strings.Cut(r.Text, "\n\n[")
			if !ok || len(body) > maxBytes || !strings.HasPrefix(body, "字字") || strings.ContainsRune(body, '�') {
				t.Fatalf("开头不对：%d 字节", len(body))
			}
			if !strings.Contains(hint, "Line 1 is 150.0KB") || !strings.Contains(hint, "offset=2") {
				t.Fatalf("提示不对：%q", hint)
			}
			if strings.Contains(hint, "sed") {
				t.Fatalf("这里只有 powershell，不能提 sed：%q", hint)
			}
			r = runTool(t, wd, "read", map[string]any{"path": "long.txt", "offset": 2, "limit": 1})
			if r.IsError || !strings.HasPrefix(r.Text, "第二行") {
				t.Fatalf("下一行：%.60q", r.Text)
			}
		})
	}
}

// 超大单行不数总行数时不把这一行扫完，也不把它读进内存。
func TestReadHugeSingleLineStopsEarly(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "one.min.js")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("a", 1<<20))
	for range countLinesMax>>20 + 2 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := runTool(t, wd, "read", map[string]any{"path": "one.min.js"})
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 8<<20 {
		t.Fatalf("分配了 %d 字节", used)
	}
	if r.IsError || !strings.Contains(r.Text, "Line 1 is over 50.0KB") {
		t.Fatalf("%q", r.Text[len(r.Text)-200:])
	}
}

// 旧代码把这些字节原样切成“文本”塞给模型。
func TestReadBinaryGivesPlainWords(t *testing.T) {
	wd := t.TempDir()
	cases := []struct {
		name, want string
		data       []byte
	}{
		{"a.zip", "ZIP 压缩包", append([]byte("PK\x03\x04\x14\x00\x00"), make([]byte, 64)...)},
		{"a.exe", "Windows 可执行文件", append([]byte("MZ\x90\x00\x03"), make([]byte, 64)...)},
		{"a.doc", "老式 Office 文档", append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 64)...)},
		{"a.db", "SQLite", append([]byte("SQLite format 3\x00"), make([]byte, 64)...)},
		{"a.bin", "二进制文件", []byte{0x13, 0x00, 0xfe, 0x7a, 0x00, 0x01}},
		{"noise.bin", "二进制文件", []byte{0xff, 0x13, 0x90, 0x82, 0xc1, 0x05, 0xf7, 0xff, 0x80}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(wd, c.name), c.data, 0o644); err != nil {
				t.Fatal(err)
			}
			r := runTool(t, wd, "read", map[string]any{"path": c.name})
			if !r.IsError || !strings.Contains(r.Text, c.want) || !strings.Contains(r.Text, "不能当文本读") {
				t.Fatalf("%+v", r)
			}
			if !strings.Contains(r.Text, formatSize(len(c.data))) {
				t.Fatalf("要写大小：%q", r.Text)
			}
		})
	}
}

// 头部是文本、后面才是二进制：窗口读到的时候也要报，不交半截乱码。
func TestReadBinaryAfterTextHead(t *testing.T) {
	wd := t.TempDir()
	data := append([]byte(strings.Repeat("正常的一行\n", 2000)), 0, 1, 2, 3)
	if err := os.WriteFile(filepath.Join(wd, "mixed.dat"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	r := runTool(t, wd, "read", map[string]any{"path": "mixed.dat"})
	if !r.IsError || !strings.Contains(r.Text, "NUL") {
		t.Fatalf("%.120q", r.Text)
	}
}

func TestReadLegacyEncodings(t *testing.T) {
	wd := t.TempDir()
	const zh, ja = "今天的账本：龙井一百二十八斤，普洱六十四斤。\n第二行也是中文。\n", "こんにちは、今日もいい天気ですね。\nゲームのデータを読みます。\n"
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String(zh)
	if err != nil {
		t.Fatal(err)
	}
	sjis, err := japanese.ShiftJIS.NewEncoder().String(ja)
	if err != nil {
		t.Fatal(err)
	}
	utf16, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().String(zh)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, data, want, note string }{
		{"gbk.txt", gbk, zh, "GB18030"},
		{"sjis.txt", sjis, ja, "Shift-JIS"},
		{"utf16.txt", utf16, zh, "UTF-16"},
		{"utf8.txt", zh, zh, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(wd, c.name), []byte(c.data), 0o644); err != nil {
				t.Fatal(err)
			}
			r := runTool(t, wd, "read", map[string]any{"path": c.name})
			if r.IsError || !strings.HasPrefix(r.Text, c.want) {
				t.Fatalf("%+v", r)
			}
			if c.note == "" && strings.Contains(r.Text, "编码") {
				t.Fatalf("UTF-8 不用说明：%q", r.Text)
			}
			if c.note != "" && !strings.Contains(r.Text, c.note+" 编码") {
				t.Fatalf("要说明编码：%q", r.Text)
			}
		})
	}
}

// 8KB 边界切开一个字时，不能把 UTF-8 文本判成二进制。
func TestReadSniffBoundaryCutsARune(t *testing.T) {
	wd := t.TempDir()
	data := strings.Repeat("a", sniffBytes-1) + strings.Repeat("字", 10)
	if err := os.WriteFile(filepath.Join(wd, "cut.txt"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runTool(t, wd, "read", map[string]any{"path": "cut.txt"})
	if r.IsError || r.Text != data {
		t.Fatalf("%.80q", r.Text)
	}
}

// 文件尾半个 UTF-8 字：换成替换符，不悄悄丢（边界清单 388b6977）。
func TestReadHalfRuneAtEnd(t *testing.T) {
	wd := t.TempDir()
	data := strings.Repeat("甲\n", 5000) + "尾\xe5\xad"
	if err := os.WriteFile(filepath.Join(wd, "half.txt"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runTool(t, wd, "read", map[string]any{"path": "half.txt", "offset": 5001})
	if r.IsError || r.Text != "尾�" {
		t.Fatalf("%q", r.Text)
	}
}

func TestReadDirectoryAndDevice(t *testing.T) {
	wd := t.TempDir()
	if err := os.Mkdir(filepath.Join(wd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "sub"}); !r.IsError || !strings.Contains(r.Text, "ls") {
		t.Fatalf("目录：%+v", r)
	}
	// 设备读起来会卡住，到不了这里就要拒绝。NUL 是 Windows 的，别的系统上 /dev/null
	dev := os.DevNull
	r := runTool(t, wd, "read", map[string]any{"path": dev})
	if !r.IsError || !strings.Contains(r.Text, "不是普通文件") {
		t.Fatalf("设备：%+v", r)
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "missing.txt"}); !r.IsError || !strings.Contains(r.Text, "ENOENT") {
		t.Fatalf("不存在：%+v", r)
	}
	// 空文件是空字符串，不是错误
	if err := os.WriteFile(filepath.Join(wd, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "empty.txt"}); r.IsError || r.Text != "" {
		t.Fatalf("空文件：%+v", r)
	}
}

// 几 GB 的图片不能读进内存：PrepareImage 限 20MB，拿到的是文件句柄。
func TestReadHugeImageRefusedWithoutReadingIt(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "huge.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("\x89PNG\r\n\x1a\n")); err != nil {
		t.Fatal(err)
	}
	// 稀疏文件：逻辑上 3GB，不占盘
	if err := f.Truncate(3 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := runTool(t, wd, "read", map[string]any{"path": "huge.png"})
	runtime.ReadMemStats(&after)
	if !r.IsError || !strings.Contains(r.Text, "读不了这张图片") {
		t.Fatalf("%+v", r)
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > 256<<20 {
		t.Fatalf("分配了 %d 字节", used)
	}
}

// 西欧语言的老文本、夹了坏字节的小文本：旧代码能读，不能因为猜不出编码就判二进制。
func TestReadLegacyEncodedTextIsNotBinary(t *testing.T) {
	wd := t.TempDir()
	cases := []struct{ name, data, want string }{
		{"a.csv", "name,city\ncaf\xe9,Paris\nna\xefve,Nice\n", "café,Paris"},
		{"b.txt", "hello\n\xff\xfe broken \x80\x81 tail\n", "broken"},
	}
	for _, c := range cases {
		if err := os.WriteFile(filepath.Join(wd, c.name), []byte(c.data), 0o644); err != nil {
			t.Fatal(err)
		}
		r := runTool(t, wd, "read", map[string]any{"path": c.name})
		if r.IsError || !strings.Contains(r.Text, c.want) || !strings.Contains(r.Text, "Windows-1252") {
			t.Fatalf("%s：%+v", c.name, r)
		}
	}
}
