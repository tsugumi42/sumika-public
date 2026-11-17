package applog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/filelog"
	"sumika-native/internal/paths"
)

// newRoot 造一个写进临时数据目录的出口，返回出口和它写的当天文件路径
func newRoot(t *testing.T, now time.Time) (*Root, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	lg := filelog.New(dir, "desk", nil, func() time.Time { return now })
	t.Cleanup(func() { lg.Close() })
	return New(lg, func() time.Time { return now }), filepath.Join(dir, "logs", filelog.FileName("desk", now))
}

func linesIn(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
}

var at = time.Date(2026, 11, 13, 10, 2, 31, 0, time.Local)

// 规格第三节的样例：时间 级别 模块 消息
func TestLogfWritesLevelAndModule(t *testing.T) {
	r, file := newRoot(t, at)
	r.Warnf(Snowluma)("连不上协议端，按退避重连: %s", "连接被拒绝")
	r.Errorf(Channel)("接收端未收下消息")
	r.Logf(App)("启动完成")
	got := linesIn(t, file)
	want := []string{
		"2026-11-13 10:02:31 WARN snowluma 连不上协议端，按退避重连: 连接被拒绝",
		"2026-11-13 10:02:31 ERROR channel 接收端未收下消息",
		"2026-11-13 10:02:31 INFO app 启动完成",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("实际：\n%s", strings.Join(got, "\n"))
	}
}

// 规格 A3：级别不够时不做格式化，参数的 String 不会被调用
type countStringer struct{ n *int }

func (c countStringer) String() string { *c.n++; return "x" }

func TestDebugNotFormattedWhenDisabled(t *testing.T) {
	r, _ := newRoot(t, at)
	n := 0
	r.Debugf(Turn)("每步 %v", countStringer{&n})
	if n != 0 {
		t.Errorf("默认 INFO 下 Debugf 不该格式化参数，String 被调了 %d 次", n)
	}
	r.SetModules(map[string]slog.Level{"turn": slog.LevelDebug})
	r.Debugf(Turn)("每步 %v", countStringer{&n})
	if n != 1 {
		t.Errorf("模块覆盖成 debug 后该写，String 调用 %d 次", n)
	}
}

// 按模块覆盖：只有这个模块放宽，别的模块仍按全局
func TestModuleOverrideAndGlobal(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetLevel(slog.LevelWarn)
	r.SetModules(map[string]slog.Level{"turn": slog.LevelDebug})
	r.Logf(Channel)("信息被全局挡住")
	r.Debugf(Turn)("细节放行")
	r.Logf(Turn)("普通信息也放行（模块覆盖为 debug）")
	got := linesIn(t, file)
	if len(got) != 2 || !strings.Contains(got[0], "turn 细节放行") || !strings.Contains(got[1], "turn 普通信息") {
		t.Errorf("实际 %q", got)
	}
}

// slog 路径：属性写成 key=value，值超过 500 字符截断
func TestSlogHandlerAttrs(t *testing.T) {
	r, file := newRoot(t, at)
	r.Slog(Channel).Warn("重连", "次数", 3, "原因", strings.Repeat("字", 600))
	got := linesIn(t, file)
	if len(got) != 1 || !strings.HasPrefix(got[0], "2026-11-13 10:02:31 WARN channel 重连 次数=3 原因=") {
		t.Fatalf("实际 %q", got)
	}
	if !strings.HasSuffix(got[0], "…") || strings.Count(got[0], "字") != 500 {
		t.Errorf("属性值该截到 500 字符加省略号，实际长度 %d", len([]rune(got[0])))
	}
}

// 换行转义在 slog 路径同样生效（规格 A2）
func TestSlogNewlineStaysOneLine(t *testing.T) {
	r, file := newRoot(t, at)
	r.Slog(Snowluma).Info("第一行\nWARN app 伪造")
	got := linesIn(t, file)
	if len(got) != 1 || !strings.HasSuffix(got[0], `第一行\nWARN app 伪造`) {
		t.Errorf("伪造行没挡住：%q", got)
	}
}

func TestEnabledHandlerRespectsLevel(t *testing.T) {
	r, _ := newRoot(t, at)
	h := r.Slog(Turn).Handler()
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("默认 INFO 下 debug 不该 Enabled")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error 该 Enabled")
	}
}

func TestNamesAndByName(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range Names() {
		if seen[n] {
			t.Errorf("模块名重复：%s", n)
		}
		seen[n] = true
		if m, ok := ByName(n); !ok || m.String() != n {
			t.Errorf("ByName(%q) 找不到", n)
		}
	}
	if _, ok := ByName("Turn"); ok {
		t.Error("大写不该在名单里")
	}
	if _, ok := ByName("x y"); ok {
		t.Error("带空格的不该在名单里")
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "Error": slog.LevelError} {
		if got, ok := ParseLevel(in); !ok || got != want {
			t.Errorf("ParseLevel(%q) = %v %v", in, got, ok)
		}
	}
	if _, ok := ParseLevel("trace"); ok {
		t.Error("trace 不是级别")
	}
}

// 自定义级别也要有级别词：行里级别位是空的，读取侧会把它当旧格式行，--level 和 --errors 都过滤不到
func TestCustomLevelsGetLevelWords(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetLevel(slog.Level(-100))
	lg := r.Slog(Misc)
	ctx := context.Background()
	for _, c := range []struct {
		level slog.Level
		word  string
	}{
		{slog.Level(12), "ERROR"}, {slog.LevelError + 1, "ERROR"}, {slog.LevelError, "ERROR"},
		{slog.LevelWarn + 1, "WARN"}, {slog.LevelWarn, "WARN"}, {slog.Level(7), "WARN"},
		{slog.Level(2), "INFO"}, {slog.LevelInfo, "INFO"}, {slog.Level(-1), "INFO"},
		{slog.LevelDebug, "DEBUG"}, {slog.Level(-8), "DEBUG"},
	} {
		lg.Log(ctx, c.level, "自定义")
		got := linesIn(t, file)
		line := got[len(got)-1]
		if !strings.Contains(line, " "+c.word+" misc 自定义") {
			t.Errorf("级别 %d 该写 %s：%q", c.level, c.word, line)
		}
		if e, ok := filelog.ParseLine(line); !ok || e.Level != c.word {
			t.Errorf("级别 %d 写出的行读取侧认不出级别：%q → %+v", c.level, line, e)
		}
	}
}

type lazyValue struct{ s string }

func (l lazyValue) LogValue() slog.Value { return slog.StringValue(l.s) }

// 属性名也打码、LogValuer 先展开再写：以前 key 原样写入、LogValuer 写成占位的 {}
func TestAttrKeyRedactedAndLogValuerResolved(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "SECRETTOKEN", "***") })
	r.Slog(Misc).Warn("属性", "SECRETTOKEN", "keyleak", "lv", lazyValue{"展开的值 SECRETTOKEN"})
	got := strings.Join(linesIn(t, file), "\n")
	if strings.Contains(got, "SECRETTOKEN") {
		t.Errorf("属性名或展开的值漏了令牌：\n%s", got)
	}
	if !strings.Contains(got, " ***=keyleak lv=展开的值 ***") {
		t.Errorf("实际：\n%s", got)
	}
}
