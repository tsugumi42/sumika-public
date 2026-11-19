// Package uiskill 是 sumika-dev check ui-skill：查 .claude/skills/mygo-ui/SKILL.md 有没有和 native/ 里的代码对不上
// （原 scripts/check-ui-skill.ts，lefthook.yml 的“界面技能核对”，2026-11-14 质检 X-13）：
//  1. 超过 150 行；
//  2. 反引号里的 internal/… 路径在 native/ 下没有对应目录；
//  3. 反引号里的 包名.名字 在 native/internal/<包名>/ 的非 _test.go 文件里找不到 func 或 type；
//     包名不是本仓库的（例如 ui）跳过，输出“没核对”；
//  4. 写的 mygo vX.Y.Z 和 native/go.mod 里 github.com/egoist/mygo 的版本不一致，或一处都没写。
//
// 读的是工作区的文件，不是暂存区：入口页很少和别的改动同时半改，先不做暂存区版本（和旧脚本一样）。
package uiskill

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sumika-native/internal/devtools/jstext"
)

// Deps 是核对要问的两件事，方便用编的数据测。
type Deps struct {
	PathExists func(p string) bool         // p 相对仓库根，例如 native/internal/winshell；必须是目录
	HasName    func(pkg, name string) bool // pkg 包的非测试 .go 里有没有 func 或 type name
}

// Result 是核对结果。
type Result struct {
	Errors   []string
	Skipped  []string
	Packages int
	Names    int
}

// jsSpace 是 JS 的 \s 认的空白，放进方括号用（旧脚本的正则在 JS 里，\s 比 Go 的宽）。
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	codeSpan     = regexp.MustCompile("`([^`\n]+)`")
	nameSpan     = regexp.MustCompile(`^([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)$`)
	internalPath = regexp.MustCompile(`^internal/[A-Za-z0-9_./-]+$`)
	modVersion   = regexp.MustCompile(`github\.com/egoist/mygo[` + jsSpace + `]+v([0-9]+\.[0-9]+\.[0-9]+)`)
	skillVersion = regexp.MustCompile(`mygo[` + jsSpace + `]+v([0-9]+\.[0-9]+\.[0-9]+)`)
)

// orderedSet 是按首次加入顺序的集合（对应 JS 的 Set）。
type orderedSet struct {
	seen map[string]bool
	list []string
}

func (s *orderedSet) add(v string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if !s.seen[v] {
		s.seen[v] = true
		s.list = append(s.list, v)
	}
}

// Check 只读文本和两个回调。
func Check(skill, goMod string, deps Deps) Result {
	var res Result
	skipped, pkgs, names := &orderedSet{}, &orderedSet{}, &orderedSet{}
	lineOf := func(index int) int { return strings.Count(skill[:index], "\n") + 1 }

	trimmed := strings.TrimSuffix(skill, "\n")
	if strings.HasSuffix(trimmed, "\r") && len(trimmed) != len(skill) {
		trimmed = strings.TrimSuffix(trimmed, "\r")
	}
	if n := strings.Count(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") + 1; n > 150 {
		res.Errors = append(res.Errors, fmt.Sprintf("入口页 %d 行，超过 150 行", n))
	}

	type span struct {
		text  string
		index int
	}
	var spans []span
	for _, m := range codeSpan.FindAllStringSubmatchIndex(skill, -1) {
		spans = append(spans, span{jstext.Trim(skill[m[2]:m[3]]), m[0]})
	}

	seenPaths := map[string]bool{}
	for _, s := range spans {
		if !internalPath.MatchString(s.text) {
			continue
		}
		p := "native/" + strings.TrimSuffix(s.text, "/")
		if seenPaths[p] {
			continue
		}
		seenPaths[p] = true
		if !deps.PathExists(p) {
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行：%s 在 native/ 下不存在", lineOf(s.index), s.text))
		} else {
			pkgs.add(strings.TrimSuffix(strings.TrimPrefix(s.text, "internal/"), "/"))
		}
	}

	for _, s := range spans {
		m := nameSpan.FindStringSubmatch(s.text)
		if m == nil {
			continue
		}
		pkg, name := m[1], m[2]
		if !deps.PathExists("native/internal/" + pkg) {
			skipped.add(s.text)
			continue
		}
		if !deps.HasName(pkg, name) {
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行：%s 在 native/internal/%s/ 下找不到", lineOf(s.index), s.text, pkg))
			continue
		}
		pkgs.add(pkg)
		names.add(s.text)
	}

	if m := modVersion.FindStringSubmatch(goMod); m == nil {
		res.Errors = append(res.Errors, "native/go.mod 里找不到 github.com/egoist/mygo 的版本")
	} else {
		want := m[1]
		found := skillVersion.FindAllStringSubmatchIndex(skill, -1)
		if len(found) == 0 {
			res.Errors = append(res.Errors, "入口页没写版本，应写 mygo vX.Y.Z")
		}
		for _, f := range found {
			if got := skill[f[2]:f[3]]; got != want {
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行：mygo v%s 和 go.mod 的 v%s 不一致", lineOf(f[0]), got, want))
			}
		}
	}

	res.Skipped = skipped.list
	res.Packages = len(pkgs.list)
	res.Names = len(names.list)
	return res
}

// hasDecl：目录里的非测试 .go 文件有没有 func（含方法）或 type 声明这个名字；读不了目录就当没有。
// 按目录顺序读，读到一个读不了的 .go 条目（目录、没权限）就返回错误，和旧版 readFileSync 抛错退出 1 一致，不悄悄跳过。
func hasDecl(dir, name string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, nil
	}
	funcRe := regexp.MustCompile(`\bfunc[` + jsSpace + `]+(?:\([^)]*\)[` + jsSpace + `]*)?` + name + `[` + jsSpace + `]*\(`)
	typeRe := regexp.MustCompile(`\btype[` + jsSpace + `]+` + name + `[` + jsSpace + `]`)
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return false, err
		}
		text := jstext.DecodeLossy(raw)
		if funcRe.MatchString(text) || typeRe.MatchString(text) {
			return true, nil
		}
	}
	return false, nil
}

// Run 是 sumika-dev check ui-skill 的入口，root 是仓库根（程序所在的那棵树）；不收参数。
// 退出码 1：入口页和代码对不上，或读不到要查的文件。
func Run(_ []string, root string, stdout, _ io.Writer) int {
	skillRaw, err := os.ReadFile(filepath.Join(root, ".claude/skills/mygo-ui/SKILL.md"))
	var modRaw []byte
	if err == nil {
		modRaw, err = os.ReadFile(filepath.Join(root, "native/go.mod"))
	}
	if err != nil {
		fmt.Fprintf(stdout, "读不到要查的文件：%v\n", err)
		return 1
	}
	var readErr error
	r := Check(jstext.DecodeLossy(skillRaw), jstext.DecodeLossy(modRaw), Deps{
		PathExists: func(p string) bool {
			st, err := os.Stat(filepath.Join(root, p))
			return err == nil && st.IsDir()
		},
		HasName: func(pkg, name string) bool {
			if readErr != nil {
				return true // 已经要失败了，后面的名字不再读
			}
			ok, err := hasDecl(filepath.Join(root, "native/internal", pkg), name)
			if err != nil {
				readErr = err
				return true
			}
			return ok
		},
	})
	if readErr != nil {
		fmt.Fprintf(stdout, "读不到要查的文件：%v\n", readErr)
		return 1
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(stdout, "入口页和代码对不上：\n")
		for _, e := range r.Errors {
			fmt.Fprintf(stdout, "- %s\n", e)
		}
		if len(r.Skipped) > 0 {
			fmt.Fprintf(stdout, "没核对：%s\n", strings.Join(r.Skipped, "、"))
		}
		return 1
	}
	fmt.Fprintf(stdout, "核对了 %d 个包、%d 个名字，跳过 %d 个\n", r.Packages, r.Names, len(r.Skipped))
	if len(r.Skipped) > 0 {
		fmt.Fprintf(stdout, "没核对：%s\n", strings.Join(r.Skipped, "、"))
	}
	return 0
}
