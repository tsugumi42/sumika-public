package filelog

// Tail（sumika-ctl logs 用）：只读自己前缀的、日期样子的文件；当天不够就接着读前一天；很大的文件只读最后一段。
// 日期和内容都是编的，目录用 t.TempDir()。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func numbered(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

func TestTailLastLinesAcrossDays(t *testing.T) {
	dir := initDir(t)
	writeLog(t, dir, "desk-2026-10-30.log", numbered("昨", 1, 5))
	writeLog(t, dir, "desk-2026-10-31.log", numbered("今", 1, 3))
	l := New(dir, "desk", nil, time.Now)
	for _, c := range []struct {
		n    int
		want string
	}{
		{1, "今3"},
		{3, "今1 今2 今3"},
		{5, "昨4 昨5 今1 今2 今3"},
		{8, "昨1 昨2 昨3 昨4 昨5 今1 今2 今3"},
		{500, "昨1 昨2 昨3 昨4 昨5 今1 今2 今3"}, // 不够就给全部
	} {
		got, err := l.Tail(c.n)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("Tail(%d) = %q %v，应是 %q", c.n, got, err, c.want)
		}
	}
	if got, err := l.Tail(0); got != nil || err != nil {
		t.Errorf("Tail(0) = %v %v", got, err)
	}
}

// 只读两个最近的文件；别的前缀、别的名字样子、子目录里的一概不碰
func TestTailOnlyReadsItsOwnDatedFiles(t *testing.T) {
	dir := initDir(t)
	writeLog(t, dir, "desk-2026-10-29.log", "太旧\n")
	writeLog(t, dir, "desk-2026-10-30.log", "前天\n")
	writeLog(t, dir, "desk-2026-10-31.log", "昨天\n")
	writeLog(t, dir, "go-2026-10-31.log", "诊断入口的\n")
	writeLog(t, dir, "desk-2026-10-31.log.bak", "备份\n")
	writeLog(t, dir, "desk-latest.log", "不是日期\n")
	writeLog(t, dir, "xdesk-2026-10-31.log", "前缀不对\n")
	writeLog(t, dir, "desk-2026-10-31.logx", "后缀不对\n")
	if err := os.MkdirAll(filepath.Join(dir, "logs", "desk-2026-11-01.log"), 0o755); err != nil { // 目录冒充文件
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sumika-secret.txt"), []byte("机密\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir, "desk", nil, time.Now).Tail(100)
	if err != nil || strings.Join(got, " ") != "前天 昨天" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestTailEmptyAndMissing(t *testing.T) {
	dir := initDir(t)
	l := New(dir, "desk", nil, time.Now)
	if got, err := l.Tail(10); len(got) != 0 || err != nil { // 没有 logs/
		t.Fatalf("没有 logs 目录：%v %v", got, err)
	}
	writeLog(t, dir, "desk-2026-10-31.log", "")
	if got, err := l.Tail(10); len(got) != 0 || err != nil {
		t.Fatalf("空文件：%v %v", got, err)
	}
	writeLog(t, dir, "desk-2026-10-31.log", "一行没有换行")
	if got, err := l.Tail(10); len(got) != 1 || got[0] != "一行没有换行" || err != nil {
		t.Fatalf("没有结尾换行：%v %v", got, err)
	}
	writeLog(t, dir, "desk-2026-10-31.log", "甲\r\n乙\r\n")
	if got, _ := l.Tail(10); strings.Join(got, "|") != "甲|乙" {
		t.Fatalf("CRLF 行尾：%q", got)
	}
}

// 很大的文件只读最后一段，而且不会把被截断的半行当成一行
func TestTailBigFileOnlyReadsTheEnd(t *testing.T) {
	dir := initDir(t)
	line := strings.Repeat("长", 100) // 300 字节
	n := (tailBytes / 300) * 3       // 约 3 MB
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%06d %s\n", i, line)
	}
	writeLog(t, dir, "desk-2026-10-31.log", b.String())
	got, err := New(dir, "desk", nil, time.Now).Tail(500)
	if err != nil || len(got) != 500 {
		t.Fatalf("%d %v", len(got), err)
	}
	if !strings.HasPrefix(got[499], fmt.Sprintf("%06d ", n-1)) || !strings.HasPrefix(got[0], fmt.Sprintf("%06d ", n-500)) {
		t.Fatalf("最后 500 行不对：%.20s … %.20s", got[0], got[499])
	}
	for _, l := range got {
		if len(l) != 7+300 {
			t.Fatalf("有半截的行：%d 字节", len(l))
		}
	}
}

// 正在写的 Logger 和 Tail 一起用：Tail 读到的是写进去的最新几行
func TestTailSeesWhatLoggerWrote(t *testing.T) {
	dir := initDir(t)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.Local)
	l := New(dir, "desk", nil, func() time.Time { return now })
	defer l.Close()
	for i := 1; i <= 5; i++ {
		l.Printf("第 %d 行", i)
	}
	got, err := l.Tail(2)
	if err != nil || len(got) != 2 || !strings.HasSuffix(got[0], "第 4 行") || !strings.HasSuffix(got[1], "第 5 行") {
		t.Fatalf("%q %v", got, err)
	}
}

// 日志里的第三方文字（群名、对方发来的话、协议端的报错）带换行时，Printf 写进去的仍是一行：
// 不然能在 desk-*.log 里伪造出整行假的日志，sumika-ctl logs 会把假的当真的交给 Agent
func TestPrintfKeepsOneRecordOnOneLine(t *testing.T) {
	dir := initDir(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	l := New(dir, "desk", nil, func() time.Time { return now })
	l.Printf("后台状态：%s", "接收中\n2026-11-01 12:00:00 后台状态：接收中\r\n伪造的第三行\r结尾")
	l.Printf("下一条正常的")
	l.Close()
	got, err := l.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("两次 Printf 应该是两行，读到 %d 行：%q", len(got), got)
	}
	if !strings.Contains(got[0], "伪造的第三行") || !strings.HasSuffix(got[1], "下一条正常的") {
		t.Errorf("内容不该丢：%q", got)
	}
	if strings.Count(got[0], `\n`) != 3 {
		t.Errorf("三处换行应换成 \n 两个字符：%q", got[0])
	}
}
