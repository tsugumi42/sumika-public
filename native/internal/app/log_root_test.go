package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/applog"
	"sumika-native/internal/config"
	"sumika-native/internal/filelog"
	"sumika-native/internal/paths"
)

// 规格-日志分级 A4：经新路径（Root 写进文件）写出的行，令牌和模型错误模板仍被打码，格式是 `时间 级别 模块 消息`
func TestRootLineStaysMaskedAndFormatted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 10, 2, 31, 0, time.Local)
	now := func() time.Time { return at }
	root := applog.New(filelog.New(dir, "desk", nil, now), now)
	t.Cleanup(func() { root.Close() }) // Windows 上文件开着时临时目录删不掉
	a := &App{opt: Options{Logf: root.Logf(applog.App)}, tokens: []string{"TESTTOKEN-SECRET-XYZ"}}
	logf := a.newLogf()
	logf("回合 %s 第 %d 步的模型请求出错：%s", "turn-7", 2, "400: 编的聊天原文 TESTTOKEN-SECRET-XYZ")
	logf("连上了，令牌 %s，号 %s", "TESTTOKEN-SECRET-XYZ", "123456789")
	b, err := os.ReadFile(filepath.Join(dir, "logs", filelog.FileName("desk", at)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{"SECRET", "编的聊天原文", "123456789"} {
		if strings.Contains(text, bad) {
			t.Errorf("经 Root 写出的行漏了 %q：\n%s", bad, text)
		}
	}
	want := []string{
		"2026-11-13 10:02:31 INFO app 回合 turn-7 第 2 步的模型请求出错：网关 HTTP 错误，状态码 400",
		"2026-11-13 10:02:31 INFO app 连上了，令牌 ***，号 ",
	}
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("要 2 行，实际 %q", lines)
	}
	if lines[0] != want[0] || !strings.HasPrefix(lines[1], want[1]) {
		t.Errorf("实际 %q", lines)
	}
}

// 规格-日志分级 A6（简化版）：opt.Logs 设了以后，组件拿到的出口按模块写进 applog，名单里每个模块都能写、标签对得上
func TestLogfForWritesItsModule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 10, 2, 31, 0, time.Local)
	now := func() time.Time { return at }
	root := applog.New(filelog.New(dir, "desk", nil, now), now)
	t.Cleanup(func() { root.Close() })
	a := &App{opt: Options{Logs: root}, tokens: []string{"TESTTOKEN-SECRET-XYZ"}}
	for _, name := range applog.Names() {
		m, ok := applog.ByName(name)
		if !ok {
			t.Fatalf("名单里的 %s 找不到", name)
		}
		a.logfFor(m)("模块 %s 的一行，令牌 %s", name, "TESTTOKEN-SECRET-XYZ")
	}
	b, err := os.ReadFile(filepath.Join(dir, "logs", filelog.FileName("desk", at)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	names := applog.Names()
	if len(lines) != len(names) {
		t.Fatalf("要 %d 行，实际 %d 行", len(names), len(lines))
	}
	for i, name := range names {
		want := "INFO " + name + " 模块 " + name + " 的一行，令牌 ***"
		if !strings.HasSuffix(lines[i], want) || strings.Contains(lines[i], "SECRET") {
			t.Errorf("第 %d 行：%q，要以 %q 结尾且不含令牌", i, lines[i], want)
		}
	}
}

// 规格-日志分级第六节：logs.level 设全局，logs.modules 按模块覆盖；名单外的模块名忽略
func TestApplyLogSettingsLevelAndModules(t *testing.T) {
	t.Parallel()
	root := applog.New(nil, nil)
	ApplyLogSettings(root, config.LogSettings{Level: "warn", Modules: map[string]string{"turn": "debug", "nope": "debug"}})
	if !root.Enabled(applog.Turn, slog.LevelDebug) {
		t.Error("turn 按模块覆盖成 debug，该放行")
	}
	if root.Enabled(applog.Channel, slog.LevelInfo) {
		t.Error("全局 warn，channel 的 INFO 该挡住")
	}
	if !root.Enabled(applog.Channel, slog.LevelWarn) {
		t.Error("全局 warn，channel 的 WARN 该放行")
	}
}
