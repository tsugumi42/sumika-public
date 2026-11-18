package gitrun

import (
	"strings"
	"testing"
)

func TestCleanEnvDropsGitVars(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "x")
	t.Setenv("git_dir", "y")
	t.Setenv("KEEP_ME", "z")
	var gotKeep bool
	for _, kv := range CleanEnv() {
		if strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			t.Fatalf("GIT_* 没清掉：%s", kv)
		}
		if kv == "KEEP_ME=z" {
			gotKeep = true
		}
	}
	if !gotKeep {
		t.Fatal("别的变量不该被清")
	}
}

func TestRunReportsExitCodeNotError(t *testing.T) {
	_, code, err := Run(t.TempDir(), nil, nil, "rev-parse", "--is-inside-work-tree")
	if err != nil || code == 0 {
		t.Fatalf("不在仓库里应是非零退出码、不是 err：code=%d err=%v", code, err)
	}
}
