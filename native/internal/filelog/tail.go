package filelog

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// tailBytes 是 Tail 每个文件最多读它的最后多少字节：日志每行几百字节，500 行远远用不到 1 MB
const tailBytes = 1 << 20

// scanBytes 是 TailLines 每个文件的字节保险丝：它按行数取，只有一个文件大到这个数时才会少于要的行数
// （实测 desk 日志一天 10～84 KB，DEBUG 全开也在几十 MB 以内）
const scanBytes = 64 << 20

// chunkBytes 是从文件尾往前读的块大小
const chunkBytes = 64 << 10

// Tail 是日志的最后 n 行（旧的在前），从当天的文件往前凑，不够就接着读前一天的，最多读这个前缀最近的两个文件。
// 读的只有 <数据目录>/logs/<前缀>-<日期>.log（文件名按固定样子认，别的文件、别的目录一概不碰），调用方给不出路径；
// 日志在写入时已经打过码，这里原样给。目录不存在是空，不报错
func (l *Logger) Tail(n int) ([]string, error) { return l.tail(n, tailBytes) }

// TailLines 同 Tail，但以行数为准：每个文件往前读到凑够 n 行为止，字节上限放宽到 scanBytes。
// sumika-ctl logs 过滤时用它（规格-日志分级第八节：最近 20000 行，不是最近 1 MB）；DEBUG 打开后一天的日志也不会因为 1 MB 读不到更早的 ERROR
func (l *Logger) TailLines(n int) ([]string, error) { return l.tail(n, scanBytes) }

func (l *Logger) tail(n int, maxBytes int64) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	dir := filepath.Join(l.root, "logs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	name := regexp.MustCompile(`^` + regexp.QuoteMeta(l.prefix) + `-\d{4}-\d{2}-\d{2}\.log$`)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && name.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // 文件名里是年-月-日，字典序就是时间序
	if len(files) > 2 {
		files = files[:2]
	}
	var out []string
	for _, f := range files {
		lines, err := lastLines(filepath.Join(dir, f), n-len(out), maxBytes)
		if err != nil {
			return nil, err
		}
		out = append(lines, out...) // 前一天的在前
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// lastLines 读一个文件的最后 n 行：从文件尾往前一块一块读，凑够 n+1 个换行（多一个是为了确认最早那行是整行）或读满 maxBytes 就停。
// 停在文件中间时第一行可能是半截，丢掉
func lastLines(file string, n int, maxBytes int64) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var chunks [][]byte // 尾部的块在前
	pos, got, newlines := st.Size(), int64(0), 0
	for pos > 0 && got < maxBytes && newlines <= n {
		step := min(int64(chunkBytes), pos, maxBytes-got)
		pos -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, pos); err != nil && err != io.EOF {
			return nil, err
		}
		chunks = append(chunks, b)
		got += step
		newlines += bytes.Count(b, []byte{'\n'})
	}
	b := make([]byte, 0, got)
	for i := len(chunks) - 1; i >= 0; i-- {
		b = append(b, chunks[i]...)
	}
	if pos > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			return nil, nil
		}
	}
	text := strings.TrimRight(string(b), "\r\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
