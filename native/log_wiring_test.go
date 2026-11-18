package main

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/applog"
	"sumika-native/internal/paths"
)

// 规格-日志分级 4.2 的接线测试：入口有没有把打码接到每条写日志的路上。
// 复核时同时删掉两处 SetRedact 全部测试仍是绿的，所以这里直接从入口的做法往下断言文件里的字。

const wiringLong = "98765432101234"

func wiringLines(t *testing.T, data, prefix string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(data, "logs", prefix+"-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("logs/ 下该有且只有一个 %s-*.log：%v %v", prefix, files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 桌面的日志出口开好以后，直接拿 Root 写的 Printf 形状的行（不经宿主、外壳的打码包装）也被打码；
// 启动那几行（openDeskLog 里写的）里的用户目录也不留原文。
func TestDeskRootDirectCallsAreRedacted(t *testing.T) {
	opt := diagnosticFixture(t)
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(opt, func(hostState) {})
	sh.openLog()
	t.Cleanup(sh.closeLog)
	lg := sh.lg.Load()
	if lg == nil {
		t.Fatal("没开成日志")
	}
	lg.Logf(applog.Desk)("直调 %s，号 %s，会话 %s", diagnosticToken, wiringLong, "qq:group:987654321")
	lg.Errorf(applog.Desk)("直调错误 %s", diagnosticToken)
	sh.closeLog()
	got := wiringLines(t, opt.DataDir, deskLogPrefix)
	for _, raw := range []string{diagnosticToken, wiringLong, "987654321"} {
		if strings.Contains(got, raw) {
			t.Errorf("直调的行漏了 %q：\n%s", raw, got)
		}
	}
	if !strings.Contains(got, "INFO desk 直调 ***，号 ") || !strings.Contains(got, "ERROR desk 直调错误 ***") {
		t.Errorf("直调的行不对：\n%s", got)
	}
}

// 外壳进程入口接 slog 和标准库 log：令牌被打码，closeLog 以后默认值还原，再写不进这份日志。
// 不能并行：slog.SetDefault、log.SetOutput 是进程全局的。
func TestDeskRouteRedactsSlogAndRestoresOnClose(t *testing.T) {
	opt := diagnosticFixture(t)
	oldSlog, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(opt, func(hostState) {})
	sh.openLogAndRoute()
	t.Cleanup(sh.closeLog)
	slog.Warn("检查点快照失败 "+diagnosticToken, "cwd", wiringLong)
	log.Printf("标准库来的 %s", diagnosticToken)
	sh.closeLog()
	if slog.Default() != oldSlog || log.Writer() != oldWriter || log.Flags() != oldFlags {
		t.Fatal("closeLog 以后 slog 或 log 的默认值没还原")
	}
	slog.Warn("关了以后的 slog 行")
	log.Printf("关了以后的标准库行")
	got := wiringLines(t, opt.DataDir, deskLogPrefix)
	for _, want := range []string{"WARN misc 检查点快照失败 ***", "INFO misc 标准库来的 ***"} {
		if !strings.Contains(got, want) {
			t.Errorf("日志里没有 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, diagnosticToken) || strings.Contains(got, wiringLong) || strings.Contains(got, "关了以后") {
		t.Errorf("令牌或长号漏了，或关了以后的行还进了文件：\n%s", got)
	}
}

// 托盘建不出来时没有宿主：外壳的行也要按配置里的令牌打码，不能只靠正则（令牌不是数字，正则认不出）
func TestShellWithoutHostRedactsKnownTokens(t *testing.T) {
	opt := diagnosticFixture(t)
	sh := newShell(&app{}, opt, false) // 没有 sh.host
	sh.openLog()
	t.Cleanup(sh.closeLog)
	sh.logf("托盘建不出来：%s", diagnosticToken)
	sh.closeLog()
	got := wiringLines(t, opt.DataDir, deskLogPrefix)
	if strings.Contains(got, diagnosticToken) {
		t.Errorf("没有宿主时令牌原文进了日志：\n%s", got)
	}
	if !strings.Contains(got, "托盘建不出来：***") {
		t.Errorf("日志里没有打码后的那一行：\n%s", got)
	}
}

type countedArg struct{ n *int }

func (c countedArg) String() string { *c.n++; return "x" }

// 桌面模块的级别调高以后，宿主和外壳的行在格式化之前就判级别，不白做 Sprintf 和打码
func TestHostAndShellLogfSkipWorkWhenDeskLevelOff(t *testing.T) {
	opt := diagnosticFixture(t)
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(opt, func(hostState) {})
	sh.openLog()
	t.Cleanup(sh.closeLog)
	sh.lg.Load().SetModules(map[string]slog.Level{"desk": slog.LevelWarn})
	n := 0
	sh.host.logf("宿主 %v", countedArg{&n})
	sh.logf("外壳 %v", countedArg{&n})
	if n != 0 {
		t.Errorf("desk 模块调到 warn 以后，INFO 行还是格式化了 %d 次", n)
	}
	sh.lg.Load().SetModules(nil)
	sh.host.logf("宿主 %v", countedArg{&n})
	sh.logf("外壳 %v", countedArg{&n})
	if n != 2 {
		t.Errorf("级别开着时两行都该格式化一次，实际 %d 次", n)
	}
}

// 守护进程的日志：直调 Root 的行里的完整号和用户目录都要打码（它没有令牌，只靠不靠令牌的那几种）
func TestWatchLogRedactsDirectCalls(t *testing.T) {
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	root := openWatchLog(dir)
	root.Logf(applog.Watch)("守护进程启动，拉起 %s %v", `C:\Users\Someone\sumika.exe`, []string{"-account", wiringLong})
	root.Warnf(applog.Watchdog)("会话 %s", "qq:private:111222333:444555666")
	root.Close()
	got := wiringLines(t, dir, watchLogPrefix)
	for _, raw := range []string{"Someone", wiringLong, "111222333", "444555666"} {
		if strings.Contains(got, raw) {
			t.Errorf("守护进程的行漏了 %q：\n%s", raw, got)
		}
	}
	if !strings.Contains(got, "%USERPROFILE%") {
		t.Errorf("用户目录该换成 %%USERPROFILE%%：\n%s", got)
	}
}
