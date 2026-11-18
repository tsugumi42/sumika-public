package turn

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// race 工作流（.github/workflows/race.yml）对 turn 用手写的 -run 白名单（issue #36，2026-11-13）：
// 专门测并发的测试（名字带 Race、Concurrent，或写在 *_race_test.go 里）必须被选中，白名单里每个分支至少命中一个测试。
// 不然新加的竞态测试没进 -race，或者删掉的测试名留在白名单里也不会报红。
func TestWorkflowWhitelistCoversConcurrencyTests(t *testing.T) { // 名字里不带 Race：不然它自己会替白名单的 TestRace 分支凑一个命中（复核 #36）
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "race.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\./internal/turn/ -run '([^']+)'`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("race.yml 里找不到 ./internal/turn/ 的 -run 白名单")
	}
	all := regexp.MustCompile(m[1])
	names, inRaceFile := turnTests(t)
	for _, branch := range strings.Split(m[1], "|") {
		if branch == "" { // 空分支（结尾多一个 |、或者 ||）匹配所有测试，零命中和覆盖检查都会悄悄通过
			t.Error("白名单里有空分支")
			continue
		}
		re := regexp.MustCompile(branch)
		hit := false
		for _, n := range names {
			if re.MatchString(n) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("白名单分支 %q 一个测试都没命中（测试删了或改名了？）", branch)
		}
	}
	dedicated := regexp.MustCompile(`Race|Concurrent`)
	for _, n := range names {
		if (dedicated.MatchString(n) || inRaceFile[n]) && !all.MatchString(n) {
			t.Errorf("%s 是专门测并发的，没进 race.yml 的白名单", n)
		}
	}
}

// turnTests 列出这个包里所有 TestXxx(t *testing.T)，并标出写在 *_race_test.go 里的
func turnTests(t *testing.T) ([]string, map[string]bool) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	inRace := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Type.Params.NumFields() != 1 {
				continue
			}
			names = append(names, fn.Name.Name)
			if strings.HasSuffix(f, "_race_test.go") {
				inRace[fn.Name.Name] = true
			}
		}
	}
	if len(names) < 100 {
		t.Fatalf("只找到 %d 个测试，解析不对", len(names))
	}
	return names, inRace
}
