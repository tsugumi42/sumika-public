package commitmsg

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/devtools/gitrun"
)

// 测试里起 git：环境里的 GIT_* 一律清掉（LESSONS：钩子里继承的 GIT_DIR 会让 git 改到真仓库）；
// GIT_CEILING_DIRECTORIES 放在清完之后：git 不许往临时目录上面找仓库，免得撞上祖先目录里别的仓库。
func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// check 把 msg 写进临时文件当参数跑一遍，返回退出码和输出。
func check(t *testing.T, msg string) (int, string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "msg.txt")
	write(t, f, msg)
	var out bytes.Buffer
	return Run([]string{f}, "", &out, &out), out.String()
}

func TestMergeQueueTitlesWithADRBranchNamesPass(t *testing.T) {
	for _, m := range []string{"合并 docs/adr-359-go-mygo\n\n正文\n", "Merge branch 'main' into x\n", "Revert \"改了；又改\"\n", "fixup! ADR-359：x\n", "squash! 甲；乙\n"} {
		if c, out := check(t, m); c != 0 {
			t.Fatalf("%q 应放行：%d %q", m, c, out)
		}
	}
}

func TestOrdinaryTitlesWithADRSemicolonOrTooLongAreBlocked(t *testing.T) {
	cases := []struct {
		msg  string
		code int
		want string // 输出里要有的话
	}{
		{"ADR-359：后台回到 Go\n", 1, "标题里有 ADR 编号"},
		{"改了一处；又改一处\n", 1, "标题里有分号"},
		{"改了一处;又改一处\n", 1, "标题里有分号"},
		{strings.Repeat("一", 41) + "\n", 1, "标题 41 个字，超过 40：只写一件事、写\"现在会怎样\"，编号、理由、数字放正文\n"}, // 引号是旧版的直引号
		{strings.Repeat("一", 40) + "\n", 0, ""},
		{"后台回到 Go、界面留 mygo\n", 0, ""},
		{"feat(模块)!: " + strings.Repeat("一", 40) + "\n", 0, ""}, // 字数只算“类型(模块): ”后面的
		{"feat(模块)!: " + strings.Repeat("一", 41) + "\n", 1, "标题 41 个字"},
		{"adr 359 不是编号\n", 0, ""},
		{"改 adr359 的写法\n", 1, "标题里有 ADR 编号"},
		{"fix(adr-12): 模块名里也不许写\n", 1, "标题里有 ADR 编号"},
		{"# 注释\n\n  后台回到 Go  \n正文；有分号不查\n", 0, ""},
		{"", 0, ""}, // 空说明：旧版不拦
	}
	for _, c := range cases {
		code, out := check(t, c.msg)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%q：退出码 %d（要 %d），输出 %q（要含 %q）", c.msg, code, c.code, out, c.want)
		}
	}
	// 输出格式：标题行加每条两格缩进
	_, out := check(t, "甲；乙 ADR-1\n")
	if !strings.HasPrefix(out, "提交说明的标题：甲；乙 ADR-1\n  标题里有分号") || !strings.Contains(out, "\n  标题里有 ADR 编号") {
		t.Errorf("输出格式不对：%q", out)
	}
}

func tempRepo(t *testing.T, commitEditMsg string) string {
	t.Helper()
	clearGitEnv(t)
	repo := t.TempDir()
	if _, code, err := gitrun.Run(repo, nil, nil, "init", "-q"); err != nil || code != 0 {
		t.Fatalf("git init：%d %v", code, err)
	}
	write(t, filepath.Join(repo, ".git", "COMMIT_EDITMSG"), commitEditMsg)
	return repo
}

func checkIn(t *testing.T, repo, arg string) int {
	t.Helper()
	t.Chdir(repo)
	var out bytes.Buffer
	return Run([]string{arg}, "", &out, &out)
}

const eaten = `D:ProjectsCodesumika.gitworktreesx` // 反斜杠被 sh 吃掉以后的样子

func TestEatenBackslashPathFallsBackToGitMessageFile(t *testing.T) {
	bad := tempRepo(t, "ADR-359：标题带编号\n")
	if c := checkIn(t, bad, eaten+"COMMIT_EDITMSG"); c != 1 {
		t.Fatalf("应读 git 报的说明文件并拦下：%d", c)
	}
	good := tempRepo(t, "后台回到 Go\n")
	if c := checkIn(t, good, eaten+"COMMIT_EDITMSG"); c != 0 {
		t.Fatalf("合格标题应放行：%d", c)
	}
}

func TestNoMessageFileFailsClosed(t *testing.T) {
	clearGitEnv(t)
	notRepo := t.TempDir()
	if c := checkIn(t, notRepo, eaten+"COMMIT_EDITMSG"); c != 2 { // 结尾对，但这里不是仓库
		t.Fatalf("不在仓库里应失败 2：%d", c)
	}
	if c := checkIn(t, notRepo, "nowhere.txt"); c != 2 { // 结尾既不是 COMMIT_EDITMSG 也不是 MERGE_MSG
		t.Fatalf("找不到应失败 2：%d", c)
	}
	var out bytes.Buffer
	if c := Run(nil, "", &out, &out); c != 2 || !strings.Contains(out.String(), "用法：") {
		t.Fatalf("没参数：%d %q", c, out.String())
	}
	if c := Run([]string{""}, "", &out, &out); c != 2 {
		t.Fatalf("空参数：%d", c)
	}
}

func TestOmpTempMessageFile(t *testing.T) {
	repo := tempRepo(t, "后台回到 Go\n") // 上一次留下的合格标题：读错文件就会误放行
	write(t, filepath.Join(repo, ".git", ".omp-commit-msg-4242-1763390000-0"), "ADR-359：标题带编号\n")
	write(t, filepath.Join(repo, ".git", ".omp-commit-msg-4242-1763390001-0"), "后台回到 Go\n")
	if c := checkIn(t, repo, eaten+".omp-commit-msg-4242-1763390000-0"); c != 1 {
		t.Fatalf("应读到第一个文件并拦下：%d", c)
	}
	if c := checkIn(t, repo, eaten+".omp-commit-msg-4242-1763390001-0"); c != 0 {
		t.Fatalf("应读到第二个文件并放行：%d", c)
	}
	if c := checkIn(t, repo, eaten+".omp-commit-msg-9999-1-0"); c != 2 { // 没有这个文件：不拿 COMMIT_EDITMSG 顶替
		t.Fatalf("没有这个文件应失败 2：%d", c)
	}
	// 路径被吃掉以后点也没了（omp-commit-msg 前面没有 .）
	if c := checkIn(t, repo, eaten+"omp-commit-msg-4242-1763390000-0"); c != 1 {
		t.Fatalf("没有前导点也应补上点去找：%d", c)
	}
}

func TestMergeMsgUsedForMergeCommits(t *testing.T) {
	repo := tempRepo(t, "后台回到 Go\n")                                 // 上一次普通提交留下的合格标题
	write(t, filepath.Join(repo, ".git", "MERGE_MSG"), "后台改了；又改了\n") // 这次合并的标题带分号，该拦
	if c := checkIn(t, repo, eaten+"MERGE_MSG"); c != 1 {
		t.Fatalf("应读 MERGE_MSG、不是上一次的 COMMIT_EDITMSG：%d", c)
	}
}

func TestDecodeAndWhitespaceRules(t *testing.T) {
	// 40 个字加一个坏字节：坏字节算一个字（U+FFFD）
	f := filepath.Join(t.TempDir(), "m.txt")
	if err := os.WriteFile(f, append([]byte(strings.Repeat("一", 40)), 0xff, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if c := Run([]string{f}, "", &out, &out); c != 1 || !strings.Contains(out.String(), "标题 41 个字") {
		t.Fatalf("坏字节按一个字算：%d %q", c, out.String())
	}
	// 前缀后面的全角空格（JS 的 \s 认）不计字数
	if c, out := check(t, "fix:\u3000"+strings.Repeat("一", 40)+"\n"); c != 0 {
		t.Fatalf("前缀后的全角空格不算字：%d %q", c, out)
	}
}
