package archcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// go.mod 的 replace 不许把干净克隆带不走的东西当依赖（2026-11-14，X-06）：native/go.mod 曾经写着
// `replace github.com/tsugumi42/MewTypeUI => <MewTypeUI 源码目录>`，干净克隆（和云端）编不出主程序，
// 而云端只测几个不依赖它的包，一直是绿的。replace 的目标必须是下面两种之一：
//   - “模块 版本”的模块替换；
//   - 相对目录（./x、../x），解析出来要落在 git 仓库里面，并且那个目录的 go.mod 已被 git 跟踪。
//     光禁绝对路径不够：`../../MewTypeUI` 在这台机器上正好等于原来的绝对路径，本机能编、干净克隆编不出。
//
// 不自己切 go.mod 的词：括号、引号、注释、成块写法的规则 go 自己最清楚，手写的分词器漏判过
// （`ignore "("` 后面的 replace、块里的 `")" => ./x`）。统一让 `go mod edit -json` 解析，再判它给出的结果；
// go 读不懂的 go.mod 一律算红。判断分两层：replaceProblems 是对解析结果的纯函数，“在不在仓库里、有没有被跟踪”做成回调注入，
// 可以表驱动；真实入口用 git 回答这两个问题（repoDirChecker）。

// goModReplace 是 go.mod 里的一条 replace，字段和 `go mod edit -json` 的 Replace 一一对应。
type goModReplace struct{ OldPath, OldVersion, NewPath, NewVersion string }

func (r goModReplace) String() string {
	left, right := r.OldPath, r.NewPath
	if r.OldVersion != "" {
		left += " " + r.OldVersion
	}
	if r.NewVersion != "" {
		right += " " + r.NewVersion
	}
	return "replace " + left + " => " + right
}

// replaceTargetProblem 判断 replace 右边的写法行不行；行就返回空串，不行返回原因。
// 右边只有路径、没有版本号：是目录替换，只许 ./x、../x、.、..（正斜杠）。
// 右边带版本号：是模块替换，路径不能长得像本机路径。
func replaceTargetProblem(path, version string) string {
	switch {
	case path == "":
		return "右边是空的"
	case strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`):
		return "绝对路径（/ 或 \\ 开头，含 UNC）"
	case strings.HasPrefix(path, "~"):
		return "~ 开头的家目录路径"
	case len(path) >= 2 && path[1] == ':' && (path[0] >= 'a' && path[0] <= 'z' || path[0] >= 'A' && path[0] <= 'Z'):
		return "带盘符的路径"
	case version != "":
		return ""
	case strings.Contains(path, `\`):
		return `用了反斜杠（别的系统上不是路径分隔符，只许 ./x 这种正斜杠写法）`
	case path == "." || path == ".." || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../"):
		return ""
	}
	return "不是相对目录（目录替换只许 ./ 或 ../ 开头）"
}

// replaceProblems 返回不合格的 replace，每条形如“replace a => b：原因”。modDir 是 go.mod 所在目录，相对目录按它解析；
// checkDir 回答“这个绝对目录在不在仓库里、里面的 go.mod 有没有被跟踪”，没问题返回空串。没有问题返回 nil。
func replaceProblems(modDir string, reps []goModReplace, checkDir func(absDir string) string) []string {
	var problems []string
	for _, r := range reps {
		why := replaceTargetProblem(r.NewPath, r.NewVersion)
		if why == "" && r.NewVersion == "" {
			why = checkDir(filepath.Join(modDir, filepath.FromSlash(r.NewPath)))
		}
		if why != "" {
			problems = append(problems, r.String()+"："+why)
		}
	}
	return problems
}

// goModReplaces 用 `go mod edit -json` 读出 go.mod 里全部 replace。go 报错（go.mod 读不懂）原样返回错误。
func goModReplaces(file string) ([]goModReplace, error) {
	cmd := exec.Command("go", "mod", "edit", "-json", file)
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			msg += "：" + strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("go mod edit -json 读不懂 %s（%s）", file, msg)
	}
	var parsed struct {
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("go mod edit -json 的输出读不懂 %s：%v", file, err)
	}
	var reps []goModReplace
	for _, r := range parsed.Replace {
		reps = append(reps, goModReplace{r.Old.Path, r.Old.Version, r.New.Path, r.New.Version})
	}
	return reps, nil
}

// goModProblems 查一个 go.mod 文件：go 读不懂也算一条问题。
func goModProblems(file string, checkDir func(absDir string) string) []string {
	reps, err := goModReplaces(file)
	if err != nil {
		return []string{err.Error()}
	}
	return replaceProblems(filepath.Dir(file), reps, checkDir)
}

// gitOut 在 dir 里跑 git。显式清掉环境里的 GIT_*：钩子里继承的 GIT_DIR、GIT_INDEX_FILE 会让 git 认错仓库（LESSONS）。
// --literal-pathspecs：路径里的 * [ 不当通配符。
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--literal-pathspecs"}, args...)...)
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(strings.ToUpper(e), "GIT_") })
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%v：%s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s（在 %s）：%w", strings.Join(args, " "), dir, err)
	}
	return string(out), nil
}

// canonical 把路径里已存在的那段解析成真实路径（短文件名、符号链接、联接），不存在的尾巴原样接上。
func canonical(p string) string {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// repoRoot 是 dir 所在 git 仓库（或 worktree）的根目录。
func repoRoot(t *testing.T, dir string) string {
	t.Helper()
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("找不到 git 仓库根：%v", err)
	}
	return canonical(filepath.FromSlash(strings.TrimSpace(top)))
}

// repoDirChecker 是真实入口用的 checkDir：目录必须落在 dir 所在的仓库里，目录里的 go.mod 必须已被 git 跟踪
// （被忽略、没提交、不存在的都不算：干净克隆里没有它们）。
func repoDirChecker(t *testing.T, dir string) func(absDir string) string {
	root := repoRoot(t, dir)
	return func(absDir string) string {
		abs := canonical(absDir)
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Sprintf("目标目录 %s 在 git 仓库（%s）外面，干净克隆里没有它", abs, root)
		}
		out, err := gitOut(root, "ls-files", "-z", "--", filepath.ToSlash(filepath.Join(rel, "go.mod")))
		if err != nil {
			return err.Error()
		}
		if out == "" {
			return fmt.Sprintf("目标目录里的 go.mod 没有被 git 跟踪（不存在、被忽略或没提交）：%s", filepath.Join(rel, "go.mod"))
		}
		return ""
	}
}

// goWorkFiles 挑出 go.work、go.work.sum：有了它们 native/ 的构建会吃到里面的 use / replace，绝对路径可以绕过上面的检查。
func goWorkFiles(tracked []string) []string {
	var bad []string
	for _, f := range tracked {
		if b := path.Base(filepath.ToSlash(f)); b == "go.work" || b == "go.work.sum" {
			bad = append(bad, f)
		}
	}
	return bad
}

// ---- 测试 ----

func TestReplaceTargetProblem(t *testing.T) {
	cases := []struct {
		name, path, version string
		want                string // 空串表示要过；否则是原因里应含的词
	}{
		// 要红
		{"盘符正斜杠", "D:/x/y", "", "盘符"},
		{"盘符反斜杠", `D:\x\y`, "", "盘符"},
		{"小写盘符", "d:/x", "", "盘符"},
		{"盘符不带斜杠", "D:x", "", "盘符"},
		{"无版本的盘符路径要直接报盘符", "C:/Users/x/MewTypeUI", "", "盘符"},
		{"盘符路径后面带版本号也不行", "D:/x", "v1.0.0", "盘符"},
		{"根开头的绝对路径", "/abs/y", "", "绝对路径"},
		{"反斜杠开头", `\x`, "", "绝对路径"},
		{"UNC", `\\server\share\x`, "", "绝对路径"},
		{"UNC 正斜杠", "//server/share/x", "", "绝对路径"},
		{"带版本号的绝对路径", "/abs/y", "v1.2.3", "绝对路径"},
		{"家目录", "~/x", "", "家目录"},
		{"只有波浪线", "~", "", "家目录"},
		{"目录替换不以点开头", "third_party/x", "", "相对目录"},
		{"家目录变量不会被展开", "$HOME/x", "", "相对目录"},
		{"目录替换用反斜杠", `.\third_party\x`, "", "反斜杠"},
		{"右边是空的", "", "", "空"},
		// 要过
		{"当前目录相对写法", "./x", "", ""},
		{"上级目录相对写法", "../x/y", "", ""},
		{"多级相对", "../../x", "", ""},
		{"点点本身", "..", "", ""},
		{"点本身", ".", "", ""},
		{"含空格的相对路径", "./with space/x", "", ""},
		{"模块替换", "c/d", "v1.2.3", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := replaceTargetProblem(c.path, c.version)
			if (got == "") != (c.want == "") || !strings.Contains(got, c.want) {
				t.Fatalf("replaceTargetProblem(%q, %q) = %q，想要含 %q", c.path, c.version, got, c.want)
			}
		})
	}
}

func TestReplaceProblems(t *testing.T) {
	var asked []string
	check := func(abs string) string {
		asked = append(asked, filepath.ToSlash(abs))
		if strings.HasSuffix(filepath.ToSlash(abs), "/bad") {
			return "目录不行"
		}
		return ""
	}
	reps := []goModReplace{
		{"a/b", "", "./ok", ""},        // 目录替换，目录行：问回调，解析到 mod/ok
		{"c/d", "v1.0.0", "./bad", ""}, // 左边带版本号不影响：问回调，回调说不行
		{"e/f", "", "g/h", "v1.2.3"},   // 模块替换：不问回调
		{"i/j", "", "D:/x", ""},        // 写法就不行：不再问回调
	}
	got := replaceProblems("mod", reps, check)
	want := []string{"replace c/d v1.0.0 => ./bad：目录不行", "replace i/j => D:/x：带盘符的路径"}
	if !slices.Equal(got, want) {
		t.Fatalf("得到 %q，想要 %q", got, want)
	}
	if wantAsked := []string{"mod/ok", "mod/bad"}; !slices.Equal(asked, wantAsked) {
		t.Fatalf("回调被问了 %q，想要 %q", asked, wantAsked)
	}
	if got := replaceProblems("mod", nil, check); got != nil {
		t.Fatalf("没有 replace 应该返回 nil，得到 %q", got)
	}
}

func TestGoWorkFiles(t *testing.T) {
	got := goWorkFiles([]string{"native/go.mod", "go.work", "native/sub/go.work.sum", "docs/go.work.md", "native/go.workspace", `a\go.work`})
	want := []string{"go.work", "native/sub/go.work.sum", `a\go.work`}
	if !slices.Equal(got, want) {
		t.Fatalf("得到 %q，想要 %q", got, want)
	}
}

// replaceFixture 在临时目录里搭一个小仓库：
//
//	<tmp>/MewTypeUI/go.mod      仓库外面的同名目录（本机能找到、干净克隆里没有）
//	<tmp>/repo/ok/go.mod        已跟踪的目录
//	<tmp>/repo/ign/go.mod       被 .gitignore 忽略的目录
//
// 每个用例的 go.mod 放在 <tmp>/repo/cNN/，所以 ../ok、../ign 在仓库里，../../MewTypeUI 在仓库外。
func replaceFixture(t *testing.T) (repo string) {
	t.Helper()
	tmp := t.TempDir()
	repo = filepath.Join(tmp, "repo")
	write := func(p, s string) {
		p = filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const m = "module example.com/m\n\ngo 1.21\n"
	write("MewTypeUI/go.mod", m)
	write("repo/ok/go.mod", m)
	write("repo/ign/go.mod", m)
	write("repo/.gitignore", "ign/\n")
	if _, err := gitOut(repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOut(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	return repo
}

// 从 go.mod 文本到判定的整条路径：真起 go mod edit -json，真问 git。
func TestGoModTextEndToEnd(t *testing.T) {
	const header = "module example.com/fixture\n\ngo 1.21\n\n"
	cases := []struct {
		name string
		text string // 接在 header 后面
		raw  bool   // true：text 就是整个文件，前面不加 header
		bad  int    // 期望的问题条数
		want string // 非空：问题里应含这段话
	}{
		// 要红：写法
		{name: "盘符正斜杠", text: "replace a/b => D:/x/y\n", bad: 1, want: "带盘符"},
		{name: "盘符反斜杠", text: "replace a/b => D:\\x\\y\n", bad: 1, want: "带盘符"},
		{name: "绝对路径", text: "replace a/b => /abs/y\n", bad: 1, want: "绝对路径"},
		{name: "UNC", text: "replace a/b => \\\\server\\share\\x\n", bad: 1, want: "绝对路径"},
		{name: "家目录", text: "replace a/b => ~/x\n", bad: 1, want: "go mod edit"},
		{name: "带引号的盘符路径", text: "replace a/b => \"D:/Program Files/x\"\n", bad: 1, want: "带盘符"},
		{name: "反引号的盘符路径", text: "replace a/b => `D:\\x`\n", bad: 1, want: "go mod edit"},
		{name: "成块里的绝对路径", text: "replace (\n\ta/b => ../ok\n\tc/d => D:/x\n)\n", bad: 1, want: "c/d"},
		{name: "成块里两条都坏", text: "replace (\n\ta/b => D:/x\n\tc/d => /abs\n)\n", bad: 2},
		{name: "行尾注释前面的绝对路径", text: "replace a/b => D:/x // 临时\n", bad: 1},
		{name: "多空格和制表符", text: "replace\t\ta/b   =>\t D:/x\n", bad: 1},
		{name: "CRLF 换行", text: "module example.com/fixture\r\n\r\ngo 1.21\r\n\r\nreplace a/b => D:/x\r\n", raw: true, bad: 1},
		{name: "目录替换不以点开头", text: "replace a/b => ok\n", bad: 1, want: "go mod edit"},
		// 要红：写法对，但干净克隆里没有那个目录
		{name: "跳出仓库的相对路径", text: "replace a/b => ../../MewTypeUI\n", bad: 1, want: "外面"},
		{name: "被忽略的目录", text: "replace a/b => ../ign\n", bad: 1, want: "没有被 git 跟踪"},
		{name: "不存在的目录", text: "replace a/b => ./nothing\n", bad: 1, want: "没有被 git 跟踪"},
		{name: "仓库外面的相对路径写在块里", text: "replace (\n\ta/b => ../../MewTypeUI\n)\n", bad: 1, want: "外面"},
		// 要红：手写分词器漏判过的写法
		{name: "带引号的左括号指令后面的 replace", text: "ignore \"(\"\nreplace real/mod => D:/x\n", bad: 1, want: "real/mod"},
		{name: "块里带引号的右括号", text: "replace (\n\t\")\" => ./x\n\treal/mod => D:/x\n)\n", bad: 2, want: "real/mod"},
		// 要红：go 读不懂就不放行
		{name: "引号没闭合", text: "replace a/b => \"D:/x\n", bad: 1, want: "go mod edit"},
		{name: "没有 =>", text: "replace a/b ./ok\n", bad: 1, want: "go mod edit"},
		{name: "开头带 BOM", text: "\ufeffmodule example.com/fixture\n\ngo 1.21\n\nreplace a/b => D:/x\n", raw: true, bad: 1},
		// 要过
		{name: "仓库里已跟踪的相对目录", text: "replace a/b => ../ok\n", bad: 0},
		{name: "左边带版本号", text: "replace a/b v1.2.3 => ../ok\n", bad: 0},
		{name: "带引号的相对路径", text: "replace a/b => \"../ok\"\n", bad: 0},
		{name: "模块替换", text: "replace a/b => c/d v1.2.3\n", bad: 0},
		{name: "左右都带版本号的模块替换", text: "replace a/b v1.0.0 => c/d v1.2.3\n", bad: 0},
		{name: "成块的相对写法和模块替换", text: "replace (\n\ta/b => ../ok\n\tc/d => e/f v0.1.0 // 注释\n)\n", bad: 0},
		{name: "成块夹空行和整行注释", text: "replace (\n\n\t// 说明：D:/x 只是注释\n\ta/b => ../ok\n)\n", bad: 0},
		{name: "整行被注释掉的绝对路径", text: "// replace a/b => D:/x\n", bad: 0},
		{name: "行尾注释里的绝对路径", text: "replace a/b => ../ok // 以前是 D:/x\n", bad: 0},
		{name: "空块", text: "replace ()\n", bad: 0},
		{name: "别的指令里写着盘符", text: "require a/b v1.0.0\nexclude a/b v0.9.0\n", bad: 0},
		{name: "没有 replace", text: "", bad: 0},
	}
	repo := replaceFixture(t)
	check := repoDirChecker(t, repo)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(repo, fmt.Sprintf("c%02d", i))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			text := header + c.text
			if c.raw {
				text = c.text
			}
			file := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			got := goModProblems(file, check)
			if len(got) != c.bad {
				t.Fatalf("期望 %d 条问题，得到 %d 条：%q\n输入：%q", c.bad, len(got), got, text)
			}
			if c.want != "" && !strings.Contains(strings.Join(got, "|"), c.want) {
				t.Fatalf("问题 %q 里应含 %q", got, c.want)
			}
		})
	}
}

// git 已跟踪的文件列表，路径相对 dir。
func trackedFiles(t *testing.T, dir string) []string {
	t.Helper()
	out, err := gitOut(dir, "ls-files", "-z", "--", ".")
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 })
}

// 真仓库：native/ 下 git 已跟踪的每个 go.mod（含 third_party 里导进来的独立模块）都过。
// 不查 scripts/bench-t2 的题库夹具：那是评测要改的题目，不是构建输入，里面也没有 replace。
func TestGoModReplacesStayInRepo(t *testing.T) {
	root := moduleRoot(t)
	check := repoDirChecker(t, root)
	var files []string
	for _, f := range trackedFiles(t, root) {
		if path.Base(f) == "go.mod" {
			files = append(files, filepath.Join(root, filepath.FromSlash(f)))
		}
	}
	if !slices.Contains(files, filepath.Join(root, "go.mod")) {
		t.Fatalf("git 跟踪的文件里没找到主模块的 go.mod：这个测试什么也没查（%q）", files)
	}
	for _, f := range files {
		for _, p := range goModProblems(f, check) {
			t.Errorf("%s\n  %s\nreplace 要么写“模块 版本”，要么写仓库里已跟踪的相对目录（./third_party/x）；写本机路径或跳出仓库的路径，干净克隆编不出来", f, p)
		}
	}
}

// 仓库里不放 go.work、go.work.sum：有了它们，native/ 的构建会吃到里面的 use 和 replace，上面的检查管不到。
func TestNoGoWorkTracked(t *testing.T) {
	root := repoRoot(t, moduleRoot(t))
	if bad := goWorkFiles(trackedFiles(t, root)); len(bad) > 0 {
		t.Errorf("仓库里不许有 go.work：%q（它的 use / replace 不在 go.mod 里，检查看不到）", bad)
	}
}
