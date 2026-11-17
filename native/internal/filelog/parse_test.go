package filelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

// TestParseLine 覆盖规格 A1：旧行、新行、缺模块的行、级别小写的行、时间错误的行
func TestParseLine(t *testing.T) {
	cases := []struct {
		name, line string
		ok         bool
		level, mod string
		msg        string
	}{
		{"新行", "2026-11-13 10:02:31 WARN snowluma 连不上协议端，按退避重连: 连接被拒绝", true, "WARN", "snowluma", "连不上协议端，按退避重连: 连接被拒绝"},
		{"旧行保留前缀", "2026-11-12 19:37:00 snowluma: 连不上协议端（旧行，无级别，继续可读）", true, "", "", "snowluma: 连不上协议端（旧行，无级别，继续可读）"},
		{"级别小写按旧行", "2026-11-13 10:02:31 warn snowluma 小写", true, "", "", "warn snowluma 小写"},
		{"模块不合法是坏行", "2026-11-13 10:02:31 ERROR Snow-Luma 大写模块", true, "ERROR", "?", "Snow-Luma 大写模块"},
		{"只有级别词没有空格后的内容按旧行", "2026-11-13 10:02:31 INFO", true, "", "", "INFO"},
		{"时间不合法", "2026-13-45 10:02:31 INFO app x", false, "", "", ""},
		{"太短", "2026-11-13", false, "", "", ""},
		{"没有空格分隔时间", "2026-11-13 10:02:31x INFO app y", false, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := ParseLine(c.line)
			if ok != c.ok {
				t.Fatalf("ok = %v，要 %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if e.Level != c.level || e.Module != c.mod || e.Msg != c.msg || e.Raw != c.line {
				t.Errorf("得到 level=%q mod=%q msg=%q，要 level=%q mod=%q msg=%q", e.Level, e.Module, e.Msg, c.level, c.mod, c.msg)
			}
		})
	}
}

// Line 的换行转义：一条记录写出来仍是一行（规格 A2，和 Printf 同一规则）
func TestLineKeepsOneRecordOnOneLine(t *testing.T) {
	dir := initDir(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	l := New(dir, "desk", nil, func() time.Time { return now })
	defer l.Close()
	l.Line(now, "ERROR channel 第一行\r\nINFO snowluma 伪造的一行\n第三行")
	got := linesOf(t, dir, FileName("desk", now))
	if len(got) != 1 {
		t.Fatalf("一条记录写成了 %d 行：%q", len(got), got)
	}
	if !strings.HasSuffix(got[0], `ERROR channel 第一行\nINFO snowluma 伪造的一行\n第三行`) {
		t.Errorf("换行没转义：%q", got[0])
	}
}

// 早期缓冲满了：先到的行被丢，写进文件时先补一行说明（规格 4.3）
func TestEarlyBufferDropsOldest(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	l := New(dir, "desk", nil, func() time.Time { return now })
	defer l.Close()
	for i := 0; i < 250; i++ {
		l.Line(now, fmt.Sprintf("INFO app 早期 %03d", i))
	}
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	l.Line(now, "INFO app 之后")
	got := linesOf(t, dir, FileName("desk", now))
	if len(got) != 202 {
		t.Fatalf("要 1 行说明 + 200 行缓冲 + 1 行新的，实际 %d 行", len(got))
	}
	if !strings.Contains(got[0], "WARN applog 早期日志缓冲满，丢了 50 行") {
		t.Errorf("第一行该是丢行说明：%q", got[0])
	}
	if !strings.HasSuffix(got[1], "早期 050") || !strings.HasSuffix(got[200], "早期 249") {
		t.Errorf("该丢最旧的，保留 050..249：首 %q 末 %q", got[1], got[200])
	}
}

// 旧的 Tail 读到的仍是原文（规格 A11）：Line 写出的行和旧 Printf 一样是 `时间 消息`
func TestLineFileNameUsesRecordDay(t *testing.T) {
	dir := initDir(t)
	day := time.Date(2026, 11, 13, 23, 59, 59, 0, time.Local)
	l := New(dir, "go", nil, func() time.Time { return day })
	defer l.Close()
	l.Line(day.Add(2*time.Second), "INFO app 跨零点")
	if _, err := os.Stat(filepath.Join(dir, "logs", FileName("go", day.Add(2*time.Second)))); err != nil {
		t.Fatalf("应按记录的时间选文件：%v", err)
	}
}
