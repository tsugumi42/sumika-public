package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// 工具一启动（拷库之前）就在标准错误里说：报告只由主人打开，读了内容会进 Anthropic / OpenAI。
// 用一个拿不到数据锁的空目录让运行在第一步就失败，提醒还是得先出现
func TestStartupNoticeComesBeforeAnythingElse(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	code := realMain(context.Background(), []string{"-data", t.TempDir(), "-char", "x"}, &out, &errb)
	if code != 1 {
		t.Fatalf("空目录该在拿数据锁时失败（退出码 1）：%d %s", code, errb.String())
	}
	got := errb.String()
	notice := strings.Index(got, startupNotice)
	failure := strings.Index(got, "失败：")
	if notice < 0 || failure < 0 || notice > failure {
		t.Fatalf("启动提醒该在失败信息之前出现：%q", got)
	}
	for _, want := range []string{"只由主人自己打开", "Anthropic", "OpenAI", "summary.json"} {
		if !strings.Contains(startupNotice, want) {
			t.Errorf("启动提醒里该有 %q", want)
		}
	}
	if !strings.Contains(reportWarning, "Anthropic") || !strings.Contains(reportWarning, "OpenAI") {
		t.Error("报告第一行的提醒该点名 Anthropic、OpenAI")
	}
}

// 参数不对、还没碰数据时不用吓人：没有启动提醒
func TestNoNoticeWhenNothingIsCopied(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := realMain(context.Background(), []string{"-char", "x"}, &out, &errb); code != 2 {
		t.Fatalf("缺 -data 该是参数错误（退出码 2）：%d", code)
	}
	if strings.Contains(errb.String(), startupNotice) {
		t.Errorf("参数不对时不用打印提醒：%q", errb.String())
	}
}

// -gen real 关着，拒绝的原因要和 gen.go 的 realGenReady 注释说的一样：场合门已经接上，缺的是别的
func TestGenRealRefusalSaysWhatIsActuallyMissing(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	code := realMain(context.Background(), []string{"-data", t.TempDir(), "-char", "x", "-gen", "real", "-spend", "5"}, &out, &errb)
	if code != 2 {
		t.Fatalf("-gen real 关着该是参数错误（退出码 2）：%d %s", code, errb.String())
	}
	got := errb.String()
	if strings.Contains(got, "场合门（SkipPlace）合进") {
		t.Errorf("场合门已经接上，不该再说在等它：%q", got)
	}
	for _, want := range []string{"真实生成器", "-spend"} {
		if !strings.Contains(got, want) {
			t.Errorf("拒绝的原因里该有 %q：%q", want, got)
		}
	}
	if strings.Contains(got, startupNotice) {
		t.Errorf("被拒绝在参数检查，还没碰数据，不用打印提醒：%q", got)
	}
}
