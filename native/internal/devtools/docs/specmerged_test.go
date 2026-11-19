package docs

import (
	"reflect"
	"strings"
	"testing"
)

// 规格状态头“待合并”提醒：判断本身用注入的函数测，查 git 那一步用临时仓库测（gitrun 清掉 GIT_*）。
// 照搬自 scripts/check-spec-merged.test.ts。

func spec(head string) string {
	return "# 规格：某功能\n\n" + head + "\n\n正文 待合并 在分支上\n"
}

func yes(string) bool { return true }
func no(string) bool  { return false }

func TestStatusHeadTakesFirstQuoteBlockOnly(t *testing.T) {
	if got := statusHead(spec("> 状态：a\n>\n> 原话：b")); got != "> 状态：a\n>\n> 原话：b" {
		t.Fatalf("%q", got)
	}
	if got := statusHead("# 标题\n\n没有引用块\n"); got != "" {
		t.Fatalf("%q", got)
	}
	// CRLF 也行；超过 15 行的不看
	if got := statusHead("# t\r\n\r\n> 状态：x\r\n"); got != "> 状态：x" {
		t.Fatalf("%q", got)
	}
	far := strings.Repeat("\n", 16) + "> 状态：太靠后\n"
	if got := statusHead(far); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestStaleWhenPendingAndAllBranchesMerged(t *testing.T) {
	if got := staleMergeNote(spec("> 状态：B 在 `feat/a` 上做完，待合并"), yes); !reflect.DeepEqual(got, []string{"feat/a"}) {
		t.Fatalf("%v", got)
	}
	if got := staleMergeNote(spec("> 状态：在分支上 `fix/x`、`feat/y`"), yes); !reflect.DeepEqual(got, []string{"fix/x", "feat/y"}) {
		t.Fatalf("%v", got)
	}
}

func TestNotStaleWhenBranchNotMergedOrOneNotMerged(t *testing.T) {
	if got := staleMergeNote(spec("> 状态：待合并 `feat/a`"), no); got != nil {
		t.Fatalf("%v", got)
	}
	if got := staleMergeNote(spec("> 状态：待合并 `feat/a` `feat/b`"), func(b string) bool { return b == "feat/a" }); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestNotStaleWhenNoPendingWordNoBranchOrWordOnlyInBody(t *testing.T) {
	for _, text := range []string{
		spec("> 状态：已合 main（abc1234）`feat/a`"),
		spec("> 状态：待合并"),
		"# t\n\n正文 待合并 `feat/a`\n",
		"",
	} {
		if got := staleMergeNote(text, yes); got != nil {
			t.Fatalf("%q → %v", text, got)
		}
	}
}

func TestOrdinaryPathsInBackticksAreNotBranches(t *testing.T) {
	got := branchesIn("> 状态：待合并，见 `native/internal/x`、`docs/规格-y.md`、`feat/z`")
	if !reflect.DeepEqual(got, []string{"feat/z"}) {
		t.Fatalf("%v", got)
	}
	// 去重
	if got := branchesIn("`feat/a` `feat/a`"); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestMergedIntoMainQueriesGit(t *testing.T) {
	root := mkRepo(t, map[string]string{"a": "1"})
	git(t, root, "branch", "feat/merged")
	git(t, root, "checkout", "-q", "-b", "feat/ahead")
	put(t, root, "b", "2")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "b")
	git(t, root, "checkout", "-q", "main")
	if !mergedIntoMain(root, "feat/merged", "main") {
		t.Fatal("祖先应算已合")
	}
	if mergedIntoMain(root, "feat/ahead", "main") {
		t.Fatal("领先的不算已合")
	}
	if !mergedIntoMain(root, "feat/deleted", "main") {
		t.Fatal("不存在的按已合或已删")
	}
}
