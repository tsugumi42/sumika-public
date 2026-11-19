package docs

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/devtools/gitrun"
)

// 全部用 t.TempDir 造小仓库，不碰真实仓库、真实记忆目录。

func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "") // 先登记恢复
			os.Unsetenv(k)
		}
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, code, err := gitrun.Run(root, nil, nil, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil || code != 0 {
		t.Fatalf("git %v 失败：code=%d err=%v out=%s", args, code, err, out)
	}
	return string(out)
}

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const adrOK = "# 决策记录\n\n## ADR-001：第一条\n\n日期：2026-01-01\n\n正文\n\n## ADR-002：第二条\n\n日期：2026-01-02\n\n正文\n"

// base 是能全过的最小仓库内容。
func base() map[string]string {
	return map[string]string{
		"docs/决策记录.md":     adrOK,
		"docs/README.md":   "# 索引\n\n[决策](决策记录.md) [待办](待办.md)\n",
		"scripts/keep.txt": "x\n",
		"docs/待办.md":       "# 待办\n\n- [ ] 一件事\n",
	}
}

func mkRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	clearGitEnv(t)
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	for k, v := range files {
		if k == "docs/README.md" { // 决策记录和待办这两份基础文档自己也要进索引，各个用例不用都写
			v += "\n[决策](决策记录.md) [待办](待办.md)\n"
		}
		put(t, root, k, v)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "init")
	return root
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func memNone(t *testing.T) string { return filepath.Join(t.TempDir(), "没有记忆目录") }

func check(t *testing.T, root string, staged bool) []Finding {
	t.Helper()
	res, err := Check(Options{Root: root, Staged: staged, MemDir: memNone(t)})
	if err != nil {
		t.Fatalf("Check 出错：%v", err)
	}
	return res.Found
}

// want 断言找到恰好一条该编号的发现，且级别对；也可以 wantNone 断言没有该编号。
func want(t *testing.T, found []Finding, check string, level Level) Finding {
	t.Helper()
	var hit []Finding
	for _, f := range found {
		if strings.HasPrefix(f.Check, check) {
			hit = append(hit, f)
		}
	}
	if len(hit) != 1 || hit[0].Level != level {
		t.Fatalf("应有一条 %s（%s），实际：%+v\n全部：%+v", check, level, hit, found)
	}
	return hit[0]
}

func wantNone(t *testing.T, found []Finding, check string) {
	t.Helper()
	for _, f := range found {
		if strings.HasPrefix(f.Check, check) {
			t.Fatalf("不该有 %s，实际：%+v", check, f)
		}
	}
}

func TestAllPassOnCleanRepo(t *testing.T) {
	root := mkRepo(t, base())
	if f := check(t, root, false); len(f) != 0 {
		t.Fatalf("干净仓库不该有发现：%+v", f)
	}
	var out, errb bytes.Buffer
	if code := Run(nil, root, &out, &errb); code != 0 || out.String() != "文档检查都过了\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

// ———— D1 ————
func TestD1(t *testing.T) {
	bad := with(base(), "docs/决策记录.md", "## ADR-001：旧的\n\n日期：2026-01-01\n\n## ADR-002：新的\n\n日期：2026-01-02\n\n这条修正 ADR-001。\n")
	f := want(t, check(t, mkRepo(t, bad), false), "D1", LevelError)
	if f.Where != "决策记录.md:1" || !strings.Contains(f.What, "ADR-001 被 ADR-002 修正") {
		t.Fatalf("%+v", f)
	}
	ok := with(base(), "docs/决策记录.md", "## ADR-001：旧的（已被 ADR-002 修正）\n\n日期：2026-01-01\n\n## ADR-002：新的\n\n日期：2026-01-02\n\n这条修正 ADR-001。\n")
	wantNone(t, check(t, mkRepo(t, ok), false), "D1")
	// 标题里号连写：已被 ADR-002、003 修正
	multi := with(base(), "docs/决策记录.md", "## ADR-001：旧的（已被 ADR-002、003 修正）\n\n日期：2026-01-01\n\n## ADR-002：新的\n\n日期：2026-01-02\n\n修正 ADR-001。\n\n## ADR-003：再新的\n\n日期：2026-01-03\n\n推翻 ADR-001。\n")
	wantNone(t, check(t, mkRepo(t, multi), false), "D1")
	// 全角空格、冒号也认（JS 的 \s 含全角空格）
	wide := with(base(), "docs/决策记录.md", "## ADR-001：旧的\n\n日期：2026-01-01\n\n## ADR-002：新的\n\n日期：2026-01-02\n\n取代了　：ADR-001。\n")
	want(t, check(t, mkRepo(t, wide), false), "D1", LevelError)
	// 自己指自己、指向更晚的号不算
	self := with(base(), "docs/决策记录.md", "## ADR-001：旧的\n\n日期：2026-01-01\n\n修正 ADR-002。\n\n## ADR-002：新的\n\n日期：2026-01-02\n")
	wantNone(t, check(t, mkRepo(t, self), false), "D1")
}

// ———— D2 ————
func TestD2(t *testing.T) {
	gap := with(base(), "docs/决策记录.md", "## ADR-001：a\n\n日期：2026-01-01\n\n## ADR-003：b\n\n日期：2026-01-02\n")
	f := want(t, check(t, mkRepo(t, gap), false), "D2 ADR 编号", LevelError)
	if f.Where != "决策记录.md:5" {
		t.Fatalf("%+v", f)
	}
	future := with(base(), "docs/决策记录.md", "## ADR-001：a\n\n日期：2999-01-01\n")
	want(t, check(t, mkRepo(t, future), false), "D2 ADR 日期", LevelError)
	back := with(base(), "docs/决策记录.md", "## ADR-001：a\n\n日期：2026-01-05\n\n## ADR-002：b\n\n日期：2026-01-02\n")
	want(t, check(t, mkRepo(t, back), false), "D2 ADR 日期", LevelWarn)
	wantNone(t, check(t, mkRepo(t, base()), false), "D2")
	// 注入的“今天”：同一份日期在今天之前不报、之后报
	root := mkRepo(t, with(base(), "docs/决策记录.md", "## ADR-001：a\n\n日期：2026-06-01\n"))
	for _, c := range []struct {
		day string
		n   int
	}{{"2026-06-01", 0}, {"2026-05-31", 1}} {
		tm, _ := time.Parse("2006-01-02", c.day)
		res, err := Check(Options{Root: root, Now: func() time.Time { return tm }, MemDir: memNone(t)})
		if err != nil || len(res.Found) != c.n {
			t.Fatalf("今天=%s：%+v %v", c.day, res.Found, err)
		}
	}
}

// ———— D4 / D10 ————
func TestD4IndexTodoAndD10(t *testing.T) {
	noIndex := with(base(), "docs/规格-x.md", "# x\n")
	f := want(t, check(t, mkRepo(t, noIndex), false), "D4 文档没进索引", LevelError)
	if f.Where != "docs/规格-x.md" {
		t.Fatalf("%+v", f)
	}
	inIndex := with(noIndex, "docs/README.md", "# 索引\n\n| [规格](规格-x.md) | x |\n")
	wantNone(t, check(t, mkRepo(t, inIndex), false), "D4")
	done := with(base(), "docs/待办.md", "# 待办\n\n- [ ] 没做\n  - [x] 做完没删\n")
	f = want(t, check(t, mkRepo(t, done), false), "D4 待办", LevelError)
	if f.Where != "待办.md:4" {
		t.Fatalf("%+v", f)
	}
	// 子目录、notes 里的文档不要求进索引
	wantNone(t, check(t, mkRepo(t, with(base(), "docs/subsystems/a.md", "# a\n")), false), "D4")

	tmp := with(base(), "docs/规格-y.md", "# y\n\n见会话临时目录里的东西\n", "docs/README.md", "[y](规格-y.md)\n")
	f = want(t, check(t, mkRepo(t, tmp), false), "D10", LevelWarn)
	if f.Where != "docs/规格-y.md:3" {
		t.Fatalf("%+v", f)
	}
	for _, line := range []string{"会话临时目录已经找不回", "会话临时目录没进仓库", "别再引用会话临时目录（不引用）"} {
		ok := with(base(), "docs/规格-y.md", "# y\n\n"+line+"\n", "docs/README.md", "[y](规格-y.md)\n")
		wantNone(t, check(t, mkRepo(t, ok), false), "D10")
	}
	// notes 里不查
	wantNone(t, check(t, mkRepo(t, with(base(), "docs/notes/n.md", "会话临时目录\n", "docs/待办.md", "# 待办\n\n参考 n.md\n")), false), "D10")
}

// ———— D13 ————
func TestD13(t *testing.T) {
	adrs := "## ADR-001：旧（已被 ADR-002 修正）\n\n日期：2026-01-01\n\n## ADR-002：新\n\n日期：2026-01-02\n\n修正 ADR-001。\n"
	doc := "# 规格\n\n> 这里已被 ADR-001 改为别的写法\n"
	repo := with(base(), "docs/决策记录.md", adrs, "docs/规格-z.md", doc, "docs/README.md", "[z](规格-z.md)\n")
	f := want(t, check(t, mkRepo(t, repo), false), "D13", LevelWarn)
	if f.Where != "docs/规格-z.md:3" || !strings.Contains(f.What, "ADR-001 后来又被 ADR-002 修正") {
		t.Fatalf("%+v", f)
	}
	mentioned := with(repo, "docs/规格-z.md", doc+"\n后来 ADR-002 又改了。\n")
	wantNone(t, check(t, mkRepo(t, mentioned), false), "D13")
	// AGENTS.md 也查，docs/inbox 不查
	repo2 := with(repo, "docs/规格-z.md", "# z\n", "AGENTS.md", "被 ADR-001 改为……\n", "docs/inbox/i.md", "被 ADR-001 改为……\n")
	f = want(t, check(t, mkRepo(t, repo2), false), "D13", LevelWarn)
	if f.Where != "AGENTS.md:1" {
		t.Fatalf("%+v", f)
	}
	// 第二种写法：“ADR-001 改为”
	repo3 := with(repo, "docs/规格-z.md", "# z\n\nADR-001 取代了旧做法\n")
	want(t, check(t, mkRepo(t, repo3), false), "D13", LevelWarn)
}

// ———— D12 ————
func TestD12(t *testing.T) {
	repo := with(base(), "docs/notes/调研-甲 乙.md", "# 调研\n")
	f := want(t, check(t, mkRepo(t, repo), false), "D12", LevelWarn)
	if f.Where != "docs/notes/调研-甲 乙.md" {
		t.Fatalf("%+v", f)
	}
	// 被外面直接链到（原名、encodeURI 后的名字都算）
	wantNone(t, check(t, mkRepo(t, with(repo, "docs/待办.md", "- [ ] 参考：调研-甲 乙.md\n")), false), "D12")
	wantNone(t, check(t, mkRepo(t, with(repo, "docs/待办.md", "- [ ] 参考：[x](notes/"+encodeURI("调研-甲 乙.md")+")\n")), false), "D12")
	// 前 5 行写“不挂：”
	wantNone(t, check(t, mkRepo(t, with(repo, "docs/notes/调研-甲 乙.md", "# 调研\n\n不挂：一次性排查\n")), false), "D12")
	// 不挂写在第 6 行以后不算
	late := with(repo, "docs/notes/调研-甲 乙.md", "1\n2\n3\n4\n5\n不挂：太晚\n")
	want(t, check(t, mkRepo(t, late), false), "D12", LevelWarn)
	// 经已挂上的笔记链过来也算；只被 archive 链到不算
	chain := with(base(), "docs/notes/a.md", "# a\n见 b.md\n", "docs/notes/b.md", "# b\n", "docs/待办.md", "参考：a.md\n")
	wantNone(t, check(t, mkRepo(t, chain), false), "D12")
	arch := with(base(), "docs/notes/a.md", "# a\n", "docs/archive/x.md", "见 a.md\n")
	want(t, check(t, mkRepo(t, arch), false), "D12", LevelWarn)
}

func TestEncodeURI(t *testing.T) {
	cases := map[string]string{
		"abc-1.md":     "abc-1.md",
		"调研 甲.md":      "%E8%B0%83%E7%A0%94%20%E7%94%B2.md",
		"a[b]":         "a%5Bb%5D",
		"x(1)!~*'.md":  "x(1)!~*'.md",
		"a;/?:@&=+$,#": "a;/?:@&=+$,#",
	}
	for in, want := range cases {
		if got := encodeURI(in); got != want {
			t.Errorf("encodeURI(%q)=%q 想要 %q", in, got, want)
		}
	}
}

// ———— D14 ————
func TestD14(t *testing.T) {
	specMerged := "# 规格\n\n> 状态：做完了，待合并 `feat/a`\n"
	repo := with(base(), "docs/规格-m.md", specMerged, "docs/README.md", "[m](规格-m.md)\n")
	root := mkRepo(t, repo)
	h := mergeIn(t, root, "feat/a", "Merge pull request #3 from someone/feat/a")
	f := want(t, check(t, root, false), "D14", LevelWarn)
	if f.Where != "docs/规格-m.md:3" || !strings.Contains(f.What, "feat/a 已合进 main（PR #3，"+h+"）") {
		t.Fatalf("%+v", f)
	}
	// 反例：刚从 main 开出来、还没提交的分支是 main 的祖先，但没合过（旧版按祖先判断会误报）
	root2 := mkRepo(t, repo)
	git(t, root2, "branch", "feat/a")
	wantNone(t, check(t, root2, false), "D14")
	// 反例：分支不存在、也没有合并记录（CI 和新克隆里本来就没有那些分支；旧版按“已删”提醒）
	wantNone(t, check(t, mkRepo(t, repo), false), "D14")
	// 已经写成“已合”的不报，见 TestD14StaleBranchesPerLine
}

// ———— D8 / --update ————
func TestD8AndUpdate(t *testing.T) {
	repo := with(base(), "docs/规格-n.md", "# n\n\n家里 家里 数据中心\n", "docs/README.md", "[n](规格-n.md)\n", "x.ts", "// 在家\n")
	root := mkRepo(t, repo)
	f := check(t, root, false)
	if len(f) != 3 {
		t.Fatalf("没有基线时三处旧名（家里、数据中心、在家）都该报错：%+v", f)
	}
	for _, x := range f {
		if x.Check != "D8 旧名变多了" || x.Level != LevelError {
			t.Fatalf("%+v", x)
		}
	}
	if !strings.Contains(f[0].What, "“家里” 0 → 2 处") && !strings.Contains(f[0].What, "\"家里\" 0 → 2 处") {
		t.Fatalf("%+v", f[0])
	}
	// --update 写基线，格式固定，之后通过
	var out, errb bytes.Buffer
	if code := Run([]string{"--update"}, root, &out, &errb); code != 0 || out.String() != "基线已写到 scripts/docs-baseline.json：旧名 4 处\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	b, _ := os.ReadFile(filepath.Join(root, "scripts/docs-baseline.json"))
	wantJSON := "{\n  \"names\": {\n    \"家里\": {\n      \"docs/规格-n.md\": 2\n    },\n    \"数据中心\": {\n      \"docs/规格-n.md\": 1\n    },\n    \"在家\": {\n      \"x.ts\": 1\n    }\n  }\n}\n"
	_ = wantJSON
	if !strings.HasPrefix(string(b), "{\n  \"names\": {\n    \"") || !strings.HasSuffix(string(b), "}\n  }\n}\n") {
		t.Fatalf("基线格式不对：%q", b)
	}
	git(t, root, "add", "-A")
	wantNone(t, check(t, root, false), "D8")
	// 变多了才报：在家 +1
	put(t, root, "x.ts", "// 在家 在家\n")
	f = check(t, root, false)
	if len(f) != 1 || !strings.Contains(f[0].What, "1 → 2") || f[0].Where != "x.ts" {
		t.Fatalf("%+v", f)
	}
	// 变少不报
	put(t, root, "x.ts", "// \n")
	wantNone(t, check(t, root, false), "D8")
	// 范围外（notes、archive、决策记录、LESSONS）不数
	out2 := with(base(), "docs/notes/a.md", "家里\n", "docs/archive/b.md", "家里\n", "LESSONS.md", "家里\n", "docs/待办.md", "# 待办\n\n参考 a.md\n")
	wantNone(t, check(t, mkRepo(t, out2), false), "D8")
}

// 基线 JSON 的键顺序：外层按旧名第一次出现的顺序（文件在前、旧名在后遍历），内层按文件顺序。
func TestBaselineKeyOrderMatchesFirstSeen(t *testing.T) {
	repo := with(base(), "a.md", "数据中心\n", "b.md", "家里 数据中心\n")
	root := mkRepo(t, repo)
	var out, errb bytes.Buffer
	if code := Run([]string{"--update"}, root, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	b, _ := os.ReadFile(filepath.Join(root, "scripts/docs-baseline.json"))
	want := "{\n  \"names\": {\n    \"数据中心\": {\n      \"a.md\": 1,\n      \"b.md\": 1\n    },\n    \"家里\": {\n      \"b.md\": 1\n    }\n  }\n}\n"
	if string(b) != want {
		t.Fatalf("基线：\n%s\n想要：\n%s", b, want)
	}
	// 一个旧名都没有：names 是 {}
	root2 := mkRepo(t, base())
	Run([]string{"--update"}, root2, &out, &errb)
	b, _ = os.ReadFile(filepath.Join(root2, "scripts/docs-baseline.json"))
	if string(b) != "{\n  \"names\": {}\n}\n" {
		t.Fatalf("空基线：%q", b)
	}
}

// ———— D11 ————
func TestD11(t *testing.T) {
	root := mkRepo(t, base())
	mem := t.TempDir()
	put(t, mem, "a.md", "见 [[b]] 和 [[gone-file]]\n")
	put(t, mem, "b.md", "# b\n")
	put(t, mem, "c.txt", "[[不查]]\n")
	res, err := Check(Options{Root: root, MemDir: mem})
	if err != nil {
		t.Fatal(err)
	}
	f := want(t, res.Found, "D11", LevelWarn)
	if f.Where != "记忆/a.md" || !strings.Contains(f.What, "[[gone-file]]") {
		t.Fatalf("%+v", f)
	}
	// 反例：链接都在；记忆目录不存在就跳过
	put(t, mem, "gone-file.md", "x\n")
	res, _ = Check(Options{Root: root, MemDir: mem})
	wantNone(t, res.Found, "D11")
	res, _ = Check(Options{Root: root, MemDir: memNone(t)})
	wantNone(t, res.Found, "D11")
}

// ———— --staged ————
func TestStagedReadsIndexNotWorktree(t *testing.T) {
	root := mkRepo(t, base())
	// 工作区改坏（待办里写了删除线），没暂存：--staged 不拦，不带 --staged 拦
	put(t, root, "docs/待办.md", "# 待办\n\n- [x] 做完没删\n")
	wantNone(t, check(t, root, true), "D4")
	want(t, check(t, root, false), "D4 待办", LevelError)
	// 暂存以后 --staged 拦
	git(t, root, "add", "docs/待办.md")
	want(t, check(t, root, true), "D4 待办", LevelError)
	// 反过来：暂存的是好的、工作区是坏的
	root2 := mkRepo(t, base())
	put(t, root2, "docs/待办.md", "# 待办\n\n- [x] 坏\n")
	git(t, root2, "add", "docs/待办.md")
	put(t, root2, "docs/待办.md", "# 待办\n\n- [ ] 好\n")
	want(t, check(t, root2, true), "D4 待办", LevelError)
	wantNone(t, check(t, root2, false), "D4")
}

// 钩子里 GIT_INDEX_FILE 指向“这次要提交的索引”（部分提交时是临时索引）：--staged 必须读它；不带 --staged 要无视它。
func TestStagedHonorsGitIndexFileButWorkspaceModeIgnoresIt(t *testing.T) {
	root := mkRepo(t, base())
	// 做一份临时索引，里面的待办是坏的
	tmpIdx := filepath.Join(t.TempDir(), "idx")
	git(t, root, "read-tree", "HEAD") // 主索引原样
	cmd := func(env []string, stdin []byte, args ...string) string {
		out, code, err := gitrun.Run(root, env, stdin, args...)
		if err != nil || code != 0 {
			t.Fatalf("git %v：%d %v %s", args, code, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	envIdx := append(os.Environ(), "GIT_INDEX_FILE="+tmpIdx)
	cmd(envIdx, nil, "read-tree", "HEAD")
	put(t, root, "bad.md", "# 坏待办\n\n- [x] 删除线\n")
	h := cmd(nil, nil, "hash-object", "-w", "bad.md")
	os.Remove(filepath.Join(root, "bad.md"))
	cmd(envIdx, nil, "update-index", "--cacheinfo", "100644,"+h+",docs/待办.md")
	t.Setenv("GIT_INDEX_FILE", tmpIdx)
	want(t, check(t, root, true), "D4 待办", LevelError)
	wantNone(t, check(t, root, false), "D4") // 工作区模式：清掉 GIT_*，读的是工作区文件，工作区没改坏
}

// 暂存区里没有的文件（读不到）：检查没法继续，返回错误而不是悄悄放过。
func TestStagedMissingFileIsError(t *testing.T) {
	files := with(base())
	delete(files, "docs/README.md")
	root := mkRepo(t, files)
	if _, err := Check(Options{Root: root, Staged: true, MemDir: memNone(t)}); err == nil || !strings.Contains(err.Error(), "docs/README.md") {
		t.Fatalf("应报读不到 docs/README.md：%v", err)
	}
	if _, err := Check(Options{Root: root, MemDir: memNone(t)}); err == nil {
		t.Fatal("工作区模式读不到文件也应报错")
	}
}

// CRLF 的工作区文件（Windows autocrlf）：标题行不带 \r，结论和 LF 一样。
func TestCRLFWorktreeSameAsLF(t *testing.T) {
	doc := "## ADR-001：旧的\n\n日期：2026-01-01\n\n## ADR-002：新的\n\n日期：2026-01-02\n\n修正 ADR-001。\n"
	root := mkRepo(t, with(base(), "docs/决策记录.md", doc))
	lf := check(t, root, false)
	put(t, root, "docs/决策记录.md", strings.ReplaceAll(doc, "\n", "\r\n"))
	crlf := check(t, root, false)
	if len(lf) != 1 || len(crlf) != 1 || lf[0] != crlf[0] {
		t.Fatalf("LF=%+v CRLF=%+v", lf, crlf)
	}
}

func TestRunArgsAndExitCodes(t *testing.T) {
	root := mkRepo(t, with(base(), "docs/待办.md", "- [x] 坏\n"))
	var out, errb bytes.Buffer
	if code := Run([]string{"--wat"}, root, &out, &errb); code != 2 {
		t.Fatalf("未知参数应退出 2：%d", code)
	}
	out.Reset()
	if code := Run(nil, root, &out, &errb); code != 1 {
		t.Fatalf("有错应退出 1：%d", code)
	}
	if want := "✗ [D4 待办] 待办.md:1：做完的条目要删掉，不留删除线\n1 个错、0 个提醒\n"; out.String() != want {
		t.Fatalf("输出：%q", out.String())
	}
	// 只有提醒：退出码 0，输出带“·”和“0 个错、1 个提醒”
	out.Reset()
	root2 := mkRepo(t, with(base(), "docs/notes/n.md", "x\n"))
	if code := Run(nil, root2, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "· [D12 调研没挂到用处上]") || !strings.HasSuffix(out.String(), "0 个错、1 个提醒\n") {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

// D11 的提醒顺序和旧版一样：按系统给的目录顺序（旧版 Node readdirSync 的顺序），不按字节排序。
func TestD11OrderFollowsDirectoryOrder(t *testing.T) {
	root := mkRepo(t, base())
	mem := t.TempDir()
	names := []string{"B", "a", "_x", "Z", "m1", "-z", "7", "A1"}
	for _, n := range names {
		put(t, mem, n+".md", "[[gone-"+n+"]]\n")
	}
	res, err := Check(Options{Root: root, MemDir: mem})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.Found {
		if strings.HasPrefix(f.Check, "D11") {
			got = append(got, strings.TrimSuffix(strings.TrimPrefix(f.Where, "记忆/"), ".md"))
		}
	}
	// 系统自己的目录顺序
	d, _ := os.Open(mem)
	ents, _ := d.ReadDir(-1)
	d.Close()
	var sys []string
	for _, e := range ents {
		sys = append(sys, strings.TrimSuffix(e.Name(), ".md"))
	}
	if strings.Join(got, ",") != strings.Join(sys, ",") {
		t.Fatalf("顺序和系统目录顺序不同：%v / %v", got, sys)
	}
	// NTFS（Windows）上旧版实测的顺序；按字节排序会是 -z,7,A1,B,Z,_x,a,m1
	if runtime.GOOS == "windows" && strings.Join(got, ",") != "-z,7,a,A1,B,m1,Z,_x" {
		t.Fatalf("NTFS 上应是 -z,7,a,A1,B,m1,Z,_x：%v", got)
	}
}
