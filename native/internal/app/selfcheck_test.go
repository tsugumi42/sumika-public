package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/selfcheck"
)

// 运行自检接进后台：开着时 Run 起来写出当天文件（状态行马上能读），关机最后刷新一次并把 running 记成 false；
// 关掉时整个不建，状态行说"已关闭"，也不写目录。
func TestSelfcheckWiredAndShutDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	if a.selfcheck.runner == nil || a.selfcheck.counters == nil {
		t.Fatal("默认应开着运行自检")
	}
	a.runSelfcheck()
	if got := a.SelfcheckLine(); got != "启动中，还没有评估" {
		t.Fatalf("刚启动的状态行：%q", got)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var f selfcheck.DayFile
	matches, _ := filepath.Glob(filepath.Join(a.paths.SelfcheckDir(), "20*.json"))
	if len(matches) != 1 {
		t.Fatalf("应有当天一个文件：%v", matches)
	}
	b, _ := os.ReadFile(matches[0])
	if err := json.Unmarshal(b, &f); err != nil || f.Running {
		t.Fatalf("关机后文件应是 running:false：%v %s", err, b)
	}
	if len(a.selfcheck.dbs) != 0 {
		t.Fatal("只读连接应已关掉")
	}
}

func TestSelfcheckDisabled(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = "selfcheck:\n  enabled: false\n"
	e.writeConfig(testToken)
	a := e.open()
	defer a.Close()
	if a.selfcheck.runner != nil {
		t.Fatal("enabled: false 不该建评估循环")
	}
	a.runSelfcheck()
	if got := a.SelfcheckLine(); got != "已关闭" {
		t.Fatalf("状态行：%q", got)
	}
	if _, err := os.Stat(a.paths.SelfcheckDir()); err == nil {
		t.Fatal("关掉时不该写自检目录")
	}
}
