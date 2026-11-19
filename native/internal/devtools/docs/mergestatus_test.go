package docs

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// D14：文档里写着没合、提到的分支其实已合进 main。判断本身用造好的表测，查 git 那一步用 t.TempDir 里的临时仓库测（gitrun 清掉 GIT_*）。

// mergeIn 从 main 开 branch、提交一个文件、用 msg 合回 main（--no-ff），返回合并提交的短哈希。
func mergeIn(t *testing.T, root, branch, msg string) string {
	t.Helper()
	git(t, root, "checkout", "-q", "-b", branch)
	put(t, root, "work/"+strings.ReplaceAll(branch, "/", "_"), branch+"\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "做 "+branch)
	git(t, root, "checkout", "-q", "main")
	git(t, root, "merge", "-q", "--no-ff", "-m", msg, branch)
	return strings.TrimSpace(git(t, root, "rev-parse", "--short", "HEAD"))
}

func d14s(found []Finding) []Finding {
	var out []Finding
	for _, f := range found {
		if strings.HasPrefix(f.Check, "D14") {
			out = append(out, f)
		}
	}
	return out
}

const staleSub = "# 子系统\n\n## 某功能（feat/done，未合 main）\n"
const staleSpec = "# 规格：甲\n\n> 状态：在分支 `feat/done` 上（2026-11-19）。\n"

func staleRepo(t *testing.T) (root, hash string) {
	t.Helper()
	root = mkRepo(t, with(base(),
		"docs/README.md", "# 索引\n\n[甲](规格-甲.md)\n",
		"docs/规格-甲.md", staleSpec,
		"docs/subsystems/x.md", staleSub,
	))
	return root, mergeIn(t, root, "feat/done", "Merge branch 'feat/done'")
}

// 改坏检查用的就是这个用例：去掉 D14 或把范围缩回规格状态头，子系统参考那一行就不再被报。
func TestD14ReportsStaleLinesWithHash(t *testing.T) {
	root, h := staleRepo(t)
	got := d14s(check(t, root, false))
	if len(got) != 2 {
		t.Fatalf("应有两条 D14：%+v", got)
	}
	wheres := []string{got[0].Where, got[1].Where}
	if !reflect.DeepEqual(wheres, []string{"docs/subsystems/x.md:3", "docs/规格-甲.md:3"}) && !reflect.DeepEqual(wheres, []string{"docs/规格-甲.md:3", "docs/subsystems/x.md:3"}) {
		t.Fatalf("位置不对：%v", wheres)
	}
	for _, f := range got {
		if f.Level != LevelWarn || !strings.Contains(f.What, "feat/done 已合进 main（"+h+"）") || !strings.Contains(f.What, "改成“已合 main（"+h+"）”") {
			t.Fatalf("内容不对：%+v", f)
		}
	}
	// 只是提醒：退出码 0
	var out, errb bytes.Buffer
	if code := Run(nil, root, &out, &errb); code != 0 || !strings.Contains(out.String(), "· [D14 合并状态没跟上] docs/subsystems/x.md:3：") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	// CRLF 的文件：同一行、同样的提醒
	put(t, root, "docs/subsystems/x.md", strings.ReplaceAll(staleSub, "\n", "\r\n"))
	crlf := d14s(check(t, root, false))
	if len(crlf) != 2 || !reflect.DeepEqual(crlf, got) {
		t.Fatalf("CRLF 和 LF 不一样：%+v / %+v", crlf, got)
	}
	// 没有任何 main（CI 的浅克隆停在分离头指针、或主分支不叫 main）：不报、不出错
	git(t, root, "branch", "-q", "-m", "main", "trunk")
	if got := d14s(check(t, root, false)); len(got) != 0 {
		t.Fatalf("没有 main 不该报：%+v", got)
	}
}

func TestD14MergedSourceFromSubjects(t *testing.T) {
	for _, c := range []struct{ subj, branch, pr string }{
		{"Merge pull request #19 from tsugumi42/feat/notice-go", "feat/notice-go", "19"},
		{"Merge branch 'feat/a'", "feat/a", ""},
		{"Merge branch 'feat/a' into night/b", "feat/a", ""},
		{"Merge remote-tracking branch 'origin/fix/x'", "fix/x", ""},
		{"合并 fix/link-format-1113-e：链接排版", "fix/link-format-1113-e", ""},
		{"合并 qq-media-backfill", "qq-media-backfill", ""},
		{"Merge branch 'main' into feat/notice-go", "", ""},
		{"Merge remote-tracking branch 'origin/main' into fix/ci-flaky-2", "", ""},
		{"合并 main", "", ""},
		{"合并 W4", "", ""}, // 不带 / 或 - 的不当分支
		{"feat(写文件): 她写文件时自己选对换行", "", ""},
	} {
		if b, pr := mergedSource(c.subj); b != c.branch || pr != c.pr {
			t.Errorf("%q → %q %q，应为 %q %q", c.subj, b, pr, c.branch, c.pr)
		}
	}
}

func TestD14StaleBranchesPerLine(t *testing.T) {
	m := map[string]mergeInfo{"feat/done": {hash: "abc1234"}, "qq-media-backfill": {hash: "def5678"}}
	for _, c := range []struct {
		line string
		want []string
	}{
		{"> 状态：在分支 feat/done 上（未合 main）", []string{"feat/done"}},
		{"## 某功能（feat/done，未合 main）\r", []string{"feat/done"}},
		{"在 `feat/done` 上做完、待合并", []string{"feat/done"}},
		{"分支 qq-media-backfill 上做完、没合进 main", []string{"qq-media-backfill"}},
		{"`feat/a`、`feat/done` 都未合 main", []string{"feat/done"}}, // 部分已合也要改
		{"`feat/done` 和 `feat/done` 未合并", []string{"feat/done"}}, // 去重
		{"在分支 `feat/done` 上（见第四节）", []string{"feat/done"}},
		// 不报的
		{"`feat/open` 未合 main", nil},
		{"`feat/done-pub` 未合 main，`feat/done2` 也是", nil}, // 按整段比
		{"`feat/done` 已合 main（abc1234），`feat/open` 待合并", nil},
		{"在分支 `feat/done` 上做，11-07 已合 main", nil},
		{"`feat/done` 上线了", nil},
		{"做在分支 `feat/done`，用法见下", nil},
		{"", nil},
	} {
		if got := staleBranches(c.line, m); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q → %v，应为 %v", c.line, got, c.want)
		}
	}
}

func TestD14Scope(t *testing.T) {
	for f, want := range map[string]bool{
		"AGENTS.md": true, "README.zh-CN.md": true, "docs/待办.md": true, "docs/规格-甲.md": true, "docs/subsystems/x.md": true,
		"docs/notes/调研.md": false, "docs/archive/规格/旧.md": false, "docs/inbox/x.md": false, "docs/决策记录.md": false,
		"native/README.md": false, "docs/a.html": false,
	} {
		if got := mergeStatusScope(f); got != want {
			t.Errorf("%s → %v，应为 %v", f, got, want)
		}
	}
}

// 本地 main 落后 origin/main、另有一段不相交的存档历史（private/main）：三处的合并都算；
// 同一分支几处都有时取 origin/main 的；PR 合并带上号。
func TestD14MergedBranchesAcrossMains(t *testing.T) {
	root := mkRepo(t, base())
	pr := mergeIn(t, root, "fix/pr-x", "Merge pull request #7 from someone/fix/pr-x")
	git(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, root, "reset", "-q", "--hard", "HEAD~1") // 本地 main 回到合并前
	// 不相交的存档历史
	git(t, root, "checkout", "-q", "--orphan", "old")
	git(t, root, "commit", "-q", "-m", "存档起点")
	git(t, root, "checkout", "-q", "-b", "feat/old")
	put(t, root, "old.txt", "1\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "旧活")
	git(t, root, "checkout", "-q", "old")
	git(t, root, "merge", "-q", "--no-ff", "-m", "合并 feat/old：旧活", "feat/old")
	old := strings.TrimSpace(git(t, root, "rev-parse", "--short", "HEAD"))
	git(t, root, "update-ref", "refs/remotes/private/main", "HEAD")
	git(t, root, "update-ref", "refs/remotes/private/x/main", "HEAD~1") // 不是“远端/main”的不算
	git(t, root, "checkout", "-q", "main")
	git(t, root, "branch", "-q", "-D", "old", "feat/old", "fix/pr-x") // 合完删掉也照样认

	got, err := mergedBranches(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]mergeInfo{
		"fix/pr-x": {hash: pr, pr: "7", ref: "origin/main", rank: 0},
		"feat/old": {hash: old, ref: "private/main", rank: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v\n应为 %+v", got, want)
	}
	if s := got["fix/pr-x"].where(); s != "PR #7，"+pr {
		t.Fatalf("%q", s)
	}
	if s := got["feat/old"].where(); s != "private/main 上的 "+old {
		t.Fatalf("%q", s)
	}
}

// 提交钩子在 worktree 里跑：--staged 继承钩子给的 GIT_DIR、GIT_INDEX_FILE 读暂存区，查合并记录那一步清掉 GIT_* 靠目录找仓库，
// 两边都要对：只暂存了的那一行被报，主仓库的工作区模式看不到 worktree 里的改动。
func TestD14StagedInWorktreeLikeHook(t *testing.T) {
	root, h := staleRepo(t)
	put(t, root, "docs/subsystems/x.md", "# 子系统\n")
	put(t, root, "docs/规格-甲.md", "# 规格：甲\n")
	git(t, root, "commit", "-q", "-am", "改好")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat/next", wt)
	put(t, wt, "docs/subsystems/x.md", staleSub)
	git(t, wt, "add", "docs/subsystems/x.md")
	put(t, wt, "docs/规格-甲.md", staleSpec) // 只在工作区、没暂存：--staged 不该报
	gitDir := strings.TrimSpace(git(t, wt, "rev-parse", "--absolute-git-dir"))
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(gitDir, "index"))
	got := d14s(check(t, wt, true))
	if len(got) != 1 || got[0].Where != "docs/subsystems/x.md:3" || !strings.Contains(got[0].What, h) {
		t.Fatalf("%+v", got)
	}
	if got := d14s(check(t, root, false)); len(got) != 0 {
		t.Fatalf("主仓库不该看到 worktree 的改动：%+v", got)
	}
}
