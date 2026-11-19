package hook

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"sumika-native/internal/devtools/conflicts"
	"sumika-native/internal/devtools/docs"
	"sumika-native/internal/devtools/gofmt"
	"sumika-native/internal/devtools/private"
	"sumika-native/internal/devtools/uiskill"
)

// Item 是提交前的一项检查。Globs 为空表示每次提交都跑；否则暂存的文件里有一个匹配才跑，Run 收到的是匹配上的那些文件。
type Item struct {
	Name  string
	Globs []string
	Run   func(c *Ctx, files []string, w io.Writer) int
}

// Items 是提交前检查的挑选表，顺序就是输出顺序。glob 逐字照搬原 lefthook.yml 各项（规格-检查统一入口 设计第 1 节）。
//
// 已摘掉（2026-11-19）：Server类型检查、Server测试——server/ 是冻结的 Bun 后台（ADR-359），关卡碰到冻结代码就摘掉并登记（ADR-378，登记在规格-提交前检查）。
// 所以 server/、desktop/ 下的文件只触发三项每次都跑的检查，加上 server/src/protocol/gen.ts 触发的协议生成检查（它是生成文件，过期要红）。
var Items = []Item{
	{Name: "隐私扫描", Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 不在这里清 SUMIKA_SCAN_DATA、SUMIKA_SCAN_MAIBOT：钩子那一行 env -u 清掉；漏清时 private.Run 见到它们就拒绝扫描（红），不会读编造的名单
		return private.Run([]string{"--staged"}, c.Root, w, w)
	}},
	{Name: "文档检查", Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 读暂存区的版本：别的会话改了一半、没暂存的文件不拦这次提交（2026-10-26）
		return docs.Run([]string{"--staged"}, c.Root, w, w)
	}},
	{Name: "扫描自测", Globs: []string{"scripts/friction-report*.ts"}, Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 迁完删：friction-report 的 TS 测试（隐私扫描的测试早已是 Go 测试）；脚本迁到 Go 时同提交删这一项
		return c.spawn(w, c.Root, BunProgram, "test", "./scripts/friction-report.test.ts")
	}},
	{Name: "冲突标记", Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 合并时漏解决的冲突连标记一起提交过（2026-10-30，80062b2c），docs 检查不查这种标记；暂存区新增行由检查自己读
		return conflicts.Run(nil, c.Root, w, w)
	}},
	{Name: "类型检查", Globs: []string{"{scripts,test}/**/*.{ts,tsx}"}, Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 迁完删：根目录 tsconfig 只含 scripts、test，没有 TS 脚本了就没有这一项；只改 server/ 不触发（规格-Bun后台第1批 第八节）
		return c.spawn(w, c.Root, BunProgram, "run", "typecheck")
	}},
	{Name: "协议生成检查", Globs: []string{"protocol/**", "scripts/gen-rpc.ts", "native/internal/rpcwire/wire_gen.go", "server/src/protocol/gen.ts"}, Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 迁完删：gen-rpc 迁成 Go 子命令时（规格-脚本迁Go）换成直接调函数；定义、生成器或任一生成文件被暂存，生成文件过期就红
		return c.spawn(w, c.Root, BunProgram, "scripts/gen-rpc.ts", "--check")
	}},
	{Name: "界面技能核对", Globs: []string{".claude/skills/mygo-ui/SKILL.md", "native/go.mod", "native/internal/winshell/**/*.go", "native/internal/devtools/uiskill/**/*.go"}, Run: func(c *Ctx, _ []string, w io.Writer) int {
		// 读工作区的文件，不是暂存区：入口页很少和别的改动同时半改（2026-11-14，质检 X-13）
		return uiskill.Run(nil, c.Root, w, w)
	}},
	{Name: "Go格式", Globs: []string{"native/**/*.go"}, Run: func(c *Ctx, files []string, w io.Writer) int {
		// 读暂存区的内容（git show :<文件>），不看工作区，CRLF 的工作区不受影响
		return gofmt.Run(files, c.Root, w, w)
	}},
	{Name: "Go检查", Globs: []string{"native/**/*.go", "native/go.mod", "native/go.sum"}, Run: runGoVet},
}

// Selected 是挑上的一项和交给它的文件。
type Selected struct {
	Item
	Files []string
}

// Select 按暂存的文件挑检查，保持表里的顺序；没挑上的不在结果里。
func Select(items []Item, staged []string) []Selected {
	var out []Selected
	for _, it := range items {
		if len(it.Globs) == 0 {
			out = append(out, Selected{it, staged})
			continue
		}
		var hit []string
		for _, f := range staged {
			if matchAny(it.Globs, f) {
				hit = append(hit, f)
			}
		}
		if len(hit) > 0 {
			out = append(out, Selected{it, hit})
		}
	}
	return out
}

// goPackages 是暂存的 .go 所在的包目录，相对 native/，写成 ./dir 的形式（根包是 .）；testdata 下的 .go 不是包，跳过；
// go.mod、go.sum 变了就是整个模块（依赖变了，哪个包都可能受影响）。原 scripts/precommit-fast.ts 的 go-vet。
func goPackages(files []string) []string {
	for _, f := range files {
		if f == "native/go.mod" || f == "native/go.sum" {
			return []string{"./..."}
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f, "native/") || !strings.HasSuffix(f, ".go") || hasTestdata(f) {
			continue
		}
		d := path.Dir(strings.TrimPrefix(f, "native/"))
		if d != "." {
			d = "./" + d
		}
		seen[d] = true
	}
	var out []string
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func hasTestdata(f string) bool {
	return strings.HasPrefix(f, "testdata/") || strings.Contains(f, "/testdata/")
}

// runGoVet 只 vet 暂存的 .go 所在的包（整个模块 30 秒）；整个模块的 vet 和受影响包的测试在合并队列和推送钩子里跑。
// 改了导出接口、别的包跟着坏、纯删除文件这些，提交时看不到，推送和合并时拦（2026-11-12 复核记下的取舍）。
func runGoVet(c *Ctx, files []string, w io.Writer) int {
	native := filepath.Join(c.Root, "native")
	pkgs := goPackages(files)
	// 已删掉的文件所在目录可能整个没了：只留还在的
	var listed []string
	for _, p := range pkgs {
		if p == "./..." {
			listed = []string{p}
			break
		}
		if st, err := os.Stat(filepath.Join(native, filepath.FromSlash(p))); err == nil && st.IsDir() {
			listed = append(listed, p)
		}
	}
	// 目录里的 .go 全被构建约束排除（只在 Linux 编、//go:build ignore）时 go vet ./dir 会报错，./... 却会跳过：先问 go list 这目录在本机有没有可编的文件
	var vet []string
	for _, p := range listed {
		if p == "./..." {
			vet = listed
			break
		}
		var sb, eb strings.Builder
		code := c.spawnSplit(&sb, &eb, native, goProgram(), "list", "-e", "-f", "{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}y{{end}}", p)
		if code != 0 {
			// go 起不来、GOFLAGS 坏了这类：不能当成“这个包没有可编的文件”放行（旧脚本在这里会抛异常、红）
			fmt.Fprintf(w, "go list %s 失败（退出码 %d），没法判断要不要 vet，按红处理：\n%s\n", p, code, strings.TrimSpace(eb.String()))
			return 1
		}
		if strings.TrimSpace(sb.String()) == "y" {
			vet = append(vet, p)
		}
	}
	if len(vet) == 0 {
		fmt.Fprintln(w, "这次暂存的 .go 没有落在还存在的包里")
		return 0
	}
	return c.spawn(w, native, goProgram(), append([]string{"vet"}, vet...)...)
}
