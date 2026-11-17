package filelog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TailLines 按行数取：一个比 1 MB 大得多的文件里，Tail 只给得出最后一段，TailLines 给够 n 行
// （sumika-ctl logs 过滤要扫最近 20000 行，不是最近 1 MB；DEBUG 打开后一天的日志也不能让更早的 ERROR 读不到）
func TestTailLinesIsByLinesNotBytes(t *testing.T) {
	dir := initDir(t)
	var b strings.Builder
	pad := strings.Repeat("x", 120) // 每行约 150 字节，30000 行约 4.5 MB
	for i := 1; i <= 30000; i++ {
		fmt.Fprintf(&b, "2026-11-13 10:00:00 INFO app 第%d行 %s\n", i, pad)
	}
	writeLog(t, dir, "desk-2026-11-13.log", b.String())
	l := New(dir, "desk", nil, time.Now)

	short, err := l.Tail(20000)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) >= 20000 {
		t.Fatalf("Tail 只读最后 1 MB，不该凑得够 20000 行（实际 %d）：这条用例的前提不成立", len(short))
	}

	got, err := l.TailLines(20000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20000 {
		t.Fatalf("TailLines(20000) 给了 %d 行", len(got))
	}
	if !strings.Contains(got[0], "第10001行 ") || !strings.Contains(got[len(got)-1], "第30000行 ") {
		t.Errorf("该是第 10001 到 30000 行，首尾是 %.40q … %.40q", got[0], got[len(got)-1])
	}
	for i, line := range got {
		if !strings.HasPrefix(line, "2026-11-13 10:00:00 INFO app 第") {
			t.Fatalf("第 %d 行被切断了：%.60q", i, line)
		}
	}
}

// 行数比文件里的少、文件很小、跨两天：和 Tail 同样的结果
func TestTailLinesMatchesTailOnSmallFiles(t *testing.T) {
	dir := initDir(t)
	writeLog(t, dir, "desk-2026-10-30.log", numbered("昨", 1, 5))
	writeLog(t, dir, "desk-2026-10-31.log", numbered("今", 1, 3))
	l := New(dir, "desk", nil, time.Now)
	for _, n := range []int{1, 3, 5, 8, 500} {
		a, _ := l.Tail(n)
		b, err := l.TailLines(n)
		if err != nil || strings.Join(a, "|") != strings.Join(b, "|") {
			t.Errorf("n=%d：Tail %q，TailLines %q %v", n, a, b, err)
		}
	}
}
