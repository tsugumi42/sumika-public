package hook

import (
	"reflect"
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"native/**/*.go", "native/main.go", true}, // ** 可以是零层目录
		{"native/**/*.go", "native/internal/a/b.go", true},
		{"native/**/*.go", "server/a.go", false},
		{"native/**/*.go", "native/a.gox", false},
		{"native/**/*.go", "native/internal/a/b.go/c", false},
		{"{scripts,test}/**/*.{ts,tsx}", "scripts/a.ts", true},
		{"{scripts,test}/**/*.{ts,tsx}", "test/x/y.tsx", true},
		{"{scripts,test}/**/*.{ts,tsx}", "server/test/a.ts", false}, // 只改 server/ 不触发根目录的类型检查
		{"{scripts,test}/**/*.{ts,tsx}", "scripts/a.js", false},
		{"scripts/friction-report*.ts", "scripts/friction-report.test.ts", true},
		{"scripts/friction-report*.ts", "scripts/sub/friction-report.ts", false}, // * 不跨目录
		{"protocol/**", "protocol/a/b.json", true},
		{"protocol/**", "protocols/a.json", false},
		{"protocol/**", "protocol/a\nb", true}, // 路径里有换行（Linux 上能建）：.* 要能跨过去，对应检查不能被跳过
		{"native/**/*.go", "native/x\ny/a.go", true},
		{"native/**/*.go", "native/a\n.go.txt", false},
		{"native/go.mod", "native/go.mod", true},
		{"native/go.mod", "native/goxmod", false}, // . 不是通配
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v，应为 %v", c.pat, c.name, got, c.want)
		}
	}
}

// 挑选表：改了 X → 跑 Y、不跑 Z。每次都跑的三项（隐私、文档、冲突标记）写在 always 里。
var always = []string{"隐私扫描", "文档检查", "冲突标记"}

func names(sel []Selected) []string {
	out := make([]string, 0, len(sel))
	for _, s := range sel {
		out = append(out, s.Name)
	}
	return out
}

func TestSelectTable(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		extra []string // 除 always 以外还应挑上的，按表里的顺序
	}{
		{"没有暂存文件：只剩每次都跑的三项", nil, nil},
		{"文档", []string{"docs/a.md"}, nil},
		{"冻结的 server/ 源码", []string{"server/src/a.ts", "server/src/b.tsx"}, nil},
		{"冻结的 server/ 测试", []string{"server/test/a.test.ts", "server/test/helpers/c.ts"}, nil},
		{"冻结的 desktop/", []string{"desktop/src/a.ts"}, nil},
		{"server/ 下的 .go 不在 native/，不挑 Go 项", []string{"server/a.go"}, nil},
		{"native/ 下的非 Go 文件", []string{"native/README.md", "native/internal/winshell/a.txt"}, nil},
		{"根目录脚本", []string{"scripts/foo.ts"}, []string{"类型检查"}},
		{"test/ 下的 tsx", []string{"test/a/b.tsx"}, []string{"类型检查"}},
		{"friction-report 的测试", []string{"scripts/friction-report.test.ts"}, []string{"扫描自测", "类型检查"}},
		{"friction-report 的 js 不挑", []string{"scripts/friction-report.js"}, nil},
		{"协议定义", []string{"protocol/x/y.json"}, []string{"协议生成检查"}},
		{"gen-rpc 生成器", []string{"scripts/gen-rpc.ts"}, []string{"类型检查", "协议生成检查"}},
		{"Go 生成文件 wire_gen.go", []string{"native/internal/rpcwire/wire_gen.go"}, []string{"协议生成检查", "Go格式", "Go检查"}},
		{"TS 生成文件 server/src/protocol/gen.ts", []string{"server/src/protocol/gen.ts"}, []string{"协议生成检查"}},
		{"界面技能入口页", []string{".claude/skills/mygo-ui/SKILL.md"}, []string{"界面技能核对"}},
		{"go.mod：vet 整个模块并核对 mygo 版本，没有 gofmt", []string{"native/go.mod"}, []string{"界面技能核对", "Go检查"}},
		{"go.sum：只 vet", []string{"native/go.sum"}, []string{"Go检查"}},
		{"winshell 的 Go 文件", []string{"native/internal/winshell/a/b.go"}, []string{"界面技能核对", "Go格式", "Go检查"}},
		{"检查 uiskill 自己", []string{"native/internal/devtools/uiskill/x.go"}, []string{"界面技能核对", "Go格式", "Go检查"}},
		{"别的 Go 包", []string{"native/internal/turn/a.go"}, []string{"Go格式", "Go检查"}},
		{"根包的 Go 文件", []string{"native/main.go"}, []string{"Go格式", "Go检查"}},
		{"一次改好几类", []string{"docs/a.md", "scripts/a.ts", "native/internal/turn/a.go", "protocol/p.json"}, []string{"类型检查", "协议生成检查", "Go格式", "Go检查"}},
	}
	for _, c := range cases {
		want := append(append([]string{}, always...), c.extra...)
		// 表里的顺序：把 want 按 Items 的顺序排
		var ordered []string
		for _, it := range Items {
			for _, w := range want {
				if it.Name == w {
					ordered = append(ordered, w)
				}
			}
		}
		if got := names(Select(Items, c.files)); !reflect.DeepEqual(got, ordered) {
			t.Errorf("%s：挑上 %v，应为 %v", c.name, got, ordered)
		}
	}
}

func TestSelectHandsOverOnlyMatchingFiles(t *testing.T) {
	staged := []string{"docs/a.md", "native/a.go", "native/internal/x/b.go", "native/go.mod", "server/c.go"}
	byName := map[string][]string{}
	for _, s := range Select(Items, staged) {
		byName[s.Name] = s.Files
	}
	if got, want := byName["Go格式"], []string{"native/a.go", "native/internal/x/b.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Go格式 拿到 %v，应为 %v（go.mod 不是 gofmt 的对象）", got, want)
	}
	if got, want := byName["Go检查"], []string{"native/a.go", "native/internal/x/b.go", "native/go.mod"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Go检查 拿到 %v，应为 %v", got, want)
	}
	if !reflect.DeepEqual(byName["隐私扫描"], staged) {
		t.Errorf("每次都跑的项应拿到全部暂存文件：%v", byName["隐私扫描"])
	}
}

func TestTableHasNoFrozenServerItems(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Items {
		if strings.Contains(it.Name, "Server") {
			t.Errorf("冻结的 server/ 不进提交关卡（ADR-378）：%s", it.Name)
		}
		if seen[it.Name] {
			t.Errorf("项名重复：%s", it.Name)
		}
		seen[it.Name] = true
		if it.Run == nil {
			t.Errorf("%s 没有 Run", it.Name)
		}
	}
}

func TestGoPackages(t *testing.T) {
	cases := []struct {
		files []string
		want  []string
	}{
		{[]string{"native/main.go", "native/internal/app/a.go", "native/internal/app/a_test.go", "native/internal/x/testdata/t.go", "server/a.go"}, []string{".", "./internal/app"}},
		{[]string{"native/internal/app/a.go", "native/go.sum"}, []string{"./..."}},
		{[]string{"native/go.mod"}, []string{"./..."}},
		{[]string{"docs/a.md"}, nil},
	}
	for _, c := range cases {
		if got := goPackages(c.files); !reflect.DeepEqual(got, c.want) {
			t.Errorf("goPackages(%v) = %v，应为 %v", c.files, got, c.want)
		}
	}
}
