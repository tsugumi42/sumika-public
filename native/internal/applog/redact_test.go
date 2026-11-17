package applog

import (
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/filelog"
	"sumika-native/internal/paths"
)

// 规格 A4：slog 和标准库 log 的行（misc 路径）调用方没有机会打码，Handler 用注入的函数打一次。
// 令牌放在消息正文和属性值里，写出后都是 ***。
func TestSlogPathRedactsMessageAndAttrs(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "TESTTOKEN-XYZ", "***") })
	lg := r.Slog(Misc)
	lg.Warn("检查点快照失败 TESTTOKEN-XYZ", "cwd", `C:\Users\someone\TESTTOKEN-XYZ\proj`, "err", errors.New("boom TESTTOKEN-XYZ"))
	got := strings.Join(linesIn(t, file), "\n")
	if strings.Contains(got, "TESTTOKEN") {
		t.Fatalf("令牌原文进了文件：\n%s", got)
	}
	want := `2026-11-13 10:02:31 WARN misc 检查点快照失败 *** cwd=C:\Users\someone\***\proj err=boom ***`
	if got != want {
		t.Errorf("实际：\n%s\n应是：\n%s", got, want)
	}
}

// 标准库 log 包经 slog.SetDefault 进来：log.Printf 里的令牌也要被打码（桌面程序没有 stderr，这些行以前进不了文件）
func TestStdLogPathRedacts(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "TESTTOKEN-XYZ", "***") })
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	slog.SetDefault(r.Slog(Misc))
	log.Printf("审批订阅 panic：%v", "带着 TESTTOKEN-XYZ")
	got := strings.Join(linesIn(t, file), "\n")
	if strings.Contains(got, "TESTTOKEN") || !strings.Contains(got, "INFO misc 审批订阅 panic：带着 ***") {
		t.Errorf("实际：\n%s", got)
	}
}

// 属性值先打码再截断：令牌正好跨在 500 字符的截断线上时，不能留下半截令牌
func TestAttrRedactedBeforeTruncated(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "TESTTOKEN-XYZ", "***") })
	r.Slog(Misc).Warn("长值", "v", strings.Repeat("a", maxAttrValue-6)+"TESTTOKEN-XYZ"+strings.Repeat("b", 50))
	got := strings.Join(linesIn(t, file), "\n")
	if strings.Contains(got, "TESTTO") || strings.Contains(got, "TOKEN") {
		t.Errorf("截断线上留下了半截令牌：\n%s", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("令牌该被换成 ***：\n%s", got)
	}
}

// 没有注入打码函数时原样写（测试和没有数据目录的情况）：不 panic
func TestSlogWithoutRedactStillWrites(t *testing.T) {
	r, file := newRoot(t, at)
	r.Slog(Misc).Info("普通的一行", "k", "v")
	if got := strings.Join(linesIn(t, file), "\n"); got != "2026-11-13 10:02:31 INFO misc 普通的一行 k=v" {
		t.Errorf("实际 %q", got)
	}
}

// 包外能造出 Module 的零值：不写空模块名（读取侧会把它认成坏行 "?"）
func TestZeroModuleWritesAsMisc(t *testing.T) {
	r, file := newRoot(t, at)
	r.Logf(Module{})("没名字的模块")
	got := strings.Join(linesIn(t, file), "\n")
	if got != "2026-11-13 10:02:31 INFO misc 没名字的模块" {
		t.Errorf("实际 %q", got)
	}
	if e, ok := filelog.ParseLine(got); !ok || e.Module != "misc" {
		t.Errorf("该能解析成 misc：%+v %v", e, ok)
	}
}

// 规格 A10：级别没开时 Enabled、slog 的 Debug 都不分配
func TestDisabledLevelDoesNotAllocate(t *testing.T) {
	r := New(nil, nil)
	lg := r.Slog(Turn)
	if n := testing.AllocsPerRun(200, func() { _ = r.Enabled(Turn, slog.LevelDebug) }); n != 0 {
		t.Errorf("Enabled 为假时分配了 %v 次", n)
	}
	if n := testing.AllocsPerRun(200, func() { lg.Debug("每步的细节") }); n != 0 {
		t.Errorf("关着的 Debug 分配了 %v 次", n)
	}
}

func benchRoot(b *testing.B) (*Root, *filelog.Logger) {
	b.Helper()
	dir := b.TempDir()
	if _, err := paths.Init(dir); err != nil {
		b.Fatal(err)
	}
	now := func() time.Time { return at }
	lg := filelog.New(dir, "bench", io.Discard, now) // 只量格式化和转义；文件写入同基线一样都在
	b.Cleanup(func() { lg.Close() })
	return New(lg, now), lg
}

// 规格 A10：BenchmarkHandle 对比 BenchmarkFilelogPrintf，单行开销不超过 1.5 倍（实测写进规格第十四节）
func BenchmarkHandle(b *testing.B) {
	r, _ := benchRoot(b)
	lg := r.Slog(Turn)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lg.Info("回合 t-1 第 2 步完成", "ms", i)
	}
}

func BenchmarkLogfPath(b *testing.B) {
	r, _ := benchRoot(b)
	logf := r.Logf(Turn)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		logf("回合 %s 第 %d 步完成", "t-1", 2)
	}
}

func BenchmarkFilelogPrintf(b *testing.B) {
	_, lg := benchRoot(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lg.Printf("回合 %s 第 %d 步完成", "t-1", 2)
	}
}

// 直接拿 Root 写日志的入口（看门狗、桌面启动行、sumika-go 的启动行）不自己打码：Printf 形状的五个出口都对正文打一次。
// 打码函数故意写成不幂等的（每次包一层 R(…)），写出来恰好一层才说明只打了一次。
func TestPrintfExitsRedactBodyExactlyOnce(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetLevel(slog.LevelDebug)
	r.SetRedact(func(s string) string { return "R(" + strings.ReplaceAll(s, "TESTTOKEN-XYZ", "***") + ")" })
	r.Logf(Watch)("拉起 -account %s", "TESTTOKEN-XYZ")
	r.Warnf(Watch)("警告 %s", "TESTTOKEN-XYZ")
	r.Errorf(Watch)("错误 %s", "TESTTOKEN-XYZ")
	r.Debugf(Watch)("调试 %s", "TESTTOKEN-XYZ")
	r.At(Watch, slog.LevelWarn)("At %s", "TESTTOKEN-XYZ")
	want := []string{
		"2026-11-13 10:02:31 INFO watch R(拉起 -account ***)",
		"2026-11-13 10:02:31 WARN watch R(警告 ***)",
		"2026-11-13 10:02:31 ERROR watch R(错误 ***)",
		"2026-11-13 10:02:31 DEBUG watch R(调试 ***)",
		"2026-11-13 10:02:31 WARN watch R(At ***)",
	}
	got := linesIn(t, file)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("实际：\n%s\n应是：\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Masked 是给自己已经打过码的装配处用的：正文原样写，不再过注入的函数（不然那一行被打两遍）
func TestMaskedExitDoesNotRedactAgain(t *testing.T) {
	r, file := newRoot(t, at)
	r.SetRedact(func(s string) string { return "R(" + s + ")" })
	r.Masked(App, slog.LevelWarn)("已经打过码 %s", "***")
	got := strings.Join(linesIn(t, file), "\n")
	if got != "2026-11-13 10:02:31 WARN app 已经打过码 ***" {
		t.Errorf("实际：%s", got)
	}
}

// 级别没开时 Printf 形状的出口不格式化也不打码（关着的 DEBUG 不做任何工作）
func TestPrintfExitSkipsRedactWhenLevelOff(t *testing.T) {
	r, file := newRoot(t, at)
	calls := 0
	r.SetRedact(func(s string) string { calls++; return s })
	r.Debugf(App)("调试 %s", "x")
	r.At(App, slog.LevelDebug)("调试 %s", "x")
	if calls != 0 {
		t.Errorf("级别没开却调了 %d 次打码", calls)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("级别没开却写了文件")
	}
}
