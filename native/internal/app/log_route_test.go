package app

import (
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/applog"
)

// 规格-日志分级 4.2：RouteLogs 把 Printf 形状和 slog、标准库 log 三条路都接上同一个打码函数；
// 还原函数把 slog 和 log 的默认值（含 log.SetOutput、SetFlags）换回去，之后再写不进这个 root。
// 不能并行：slog.SetDefault、log.SetOutput 是进程全局的。
func TestRouteLogsRedactsEveryPathAndRestores(t *testing.T) {
	root, file := logRootFor(t)
	oldSlog, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	redact := func(s string) string { return strings.ReplaceAll(s, "TESTTOKEN-SECRET-XYZ", "***") }
	restore := RouteLogs(root, redact)

	root.Logf(applog.Watch)("直调 %s", "TESTTOKEN-SECRET-XYZ")
	slog.Warn("slog 来的 TESTTOKEN-SECRET-XYZ", "k", "TESTTOKEN-SECRET-XYZ")
	log.Printf("标准库来的 %s", "TESTTOKEN-SECRET-XYZ")
	restore()

	if slog.Default() != oldSlog || log.Writer() != oldWriter || log.Flags() != oldFlags {
		t.Fatal("还原后 slog 或 log 的默认值不是原来的")
	}
	slog.Warn("还原之后的 slog 行")
	log.Printf("还原之后的标准库行")

	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"INFO watch 直调 ***", "WARN misc slog 来的 *** k=***", "INFO misc 标准库来的 ***"} {
		if !strings.Contains(got, want) {
			t.Errorf("日志里没有 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "TESTTOKEN") {
		t.Errorf("令牌原文进了文件：\n%s", got)
	}
	if strings.Contains(got, "还原之后") {
		t.Errorf("还原以后的行还进了这个 root：\n%s", got)
	}
}

// 用户目录前缀换成 %USERPROFILE%（用户名常是真名），其余路径不动；打两遍结果不变
func TestRedactLogMasksUserProfilePath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{`数据目录 C:\Users\Someone\AppData\Local\Sumika，配置 D:\Projects\demo\x.yml`, `数据目录 %USERPROFILE%\AppData\Local\Sumika，配置 D:\Projects\demo\x.yml`},
		{`cwd=c:/users/someone/proj/src`, `cwd=%USERPROFILE%/proj/src`},
		{`exe "C:\Users\Someone"`, `exe "%USERPROFILE%"`},
		{`没有用户目录 E:\data\x`, `没有用户目录 E:\data\x`},
	} {
		got := RedactLog(c.in, nil)
		if got != c.want {
			t.Errorf("%q\n实际 %q\n应是 %q", c.in, got, c.want)
		}
		if again := RedactLog(got, nil); again != got {
			t.Errorf("打两遍不一样：%q → %q", got, again)
		}
	}
}

// 装配出口（logfFor 等）自己按令牌表打过码，Root 不能再打第二遍：注入的函数故意写成不幂等的，
// 行里出现 R( 就说明 levelFor 走了会再打码的出口。
func TestAssembledExitsMaskOnlyOnce(t *testing.T) {
	t.Parallel()
	root, file := logRootFor(t)
	root.SetRedact(func(s string) string { return "R(" + s + ")" })
	a := &App{opt: Options{Logs: root}, tokens: []string{"TESTTOKEN-SECRET-XYZ"}}
	a.logfFor(applog.Turn)("令牌 %s", "TESTTOKEN-SECRET-XYZ")
	a.warnFor(applog.Turn)("令牌 %s", "TESTTOKEN-SECRET-XYZ")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "R(") || strings.Contains(got, "TESTTOKEN") || strings.Count(got, "令牌 ***") != 2 {
		t.Errorf("装配出口该只按令牌表打一次码：\n%s", got)
	}
}
