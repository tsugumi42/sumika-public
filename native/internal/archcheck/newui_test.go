package archcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 界面组件库只能经受控接口面用（ADR-374 决定第 4 条，修正 ADR-364 第 2 条、ADR-322）。
//
// 这条检查对应的原话：
//
//	“页面包不许直接 import 任何组件库，只许 import 接口面；接口面所在的包是唯一放开 CrychicUI 的地方，
//	 MewTypeUI、mygo-m3e 仍按原规则不许进。”
//
// 翻成检查，一句话：整个模块里，除了接口面所在的包 internal/winshell（那里只放开 CrychicUI），
// 任何 .go 文件（含测试、cmd/ 下的工具、带构建标签的文件、_ 开头的试验目录）都不许直接 import
// MewTypeUI、CrychicUI、mygo-m3e 三个组件库；已经在这么做的存量登记在 uiKitBaseline（newui_baseline_test.go），
// 只许变少、不许新增，登记项不成立了也红（不留死条目）。
// “接口面”就是现有 Kit 的那一层（ADR-374 决定第 2 条：internal/winshell 的 winshell.Kit），不另造抽象。
//
// 三层：
//  1. 直接 import（TestUIKitImportsOnlyThroughFacade）：逐个文件的语法树，不靠 go list（go list 只看当前平台、当前标签的文件，
//     带别的构建标签的文件会漏）。别名、点导入、空白导入都算：它们在语法树里都是同一个 import 路径。
//  2. 传递依赖（TestUIPackagesDepsAvoidKits）：接口面 winshell 的整棵依赖树里不许出现 MewTypeUI、mygo-m3e；
//     聊天区 chatui 不是接口面，整棵树里不许出现任何一个。两种算法都跑：go list（当前平台、默认标签，但算得到第三方包）
//     和语法树闭包（kitReach：含测试文件、含任何构建标签的文件，不看平台）。后者是为了防这条路：winshell 的测试文件或
//     带 e2e 标签的文件引一个基线里的中间包（如 lookpreset，它自己合法地引着 MewTypeUI），第 1 层只看文件自己的直接 import，
//     go list 又看不见测试文件和别的标签，两边都放过。根包页面经中间包间接引库不拦：ADR-374 写的是“直接”。
//  3. 旧桥（TestNewUIPathsDoNotCallOldBridge）：新界面路径上不许调用把旧主题翻译成新令牌的旧桥函数。
//
// 看不见的：同包里的新文件调用基线内文件的帮助函数（legacy_choice.go、group、row 一类），import 层面没有变化，这里查不到；
// 复制组件库源码改个路径再放进来，import 路径里不再出现库名，也查不到。这两种靠评审。
// 新增第四个组件库时，必须手动把它加进 uiKits，不然规则对它是空的（没有机器提醒：新库要先在 go.mod 里 require，评审时看到就加）。

// uiKit 是一个界面组件库：name 是在登记表里的叫法，module 是它的模块路径（import 路径以它开头，后面是“/”或结束）。
type uiKit struct{ name, module string }

var uiKits = []uiKit{
	{"MewTypeUI", "github.com/tsugumi42/MewTypeUI"},
	{"CrychicUI", "github.com/ELY-Open-Source/CrychicUI"},
	{"mygo-m3e", "github.com/tsugumi42/mygo-m3e"},
}

// 接口面所在的包（相对 native/，正斜杠），以及它被放开的库。只放开这一个包、这一个库；子目录不算。
const facadeDir = "internal/winshell"

var facadeKits = map[string]bool{"CrychicUI": true}

// kitOf 返回 import 路径属于哪个组件库，不属于任何一个返回空串。按路径段匹配：.../MewTypeUIextra 不算 MewTypeUI。
func kitOf(importPath string) string {
	for _, k := range uiKits {
		if importPath == k.module || strings.HasPrefix(importPath, k.module+"/") {
			return k.name
		}
	}
	return ""
}

// kitScan 是一次扫描的结果。
type kitScan struct {
	kits       map[string]map[string]bool // 文件（相对 root、正斜杠）→ 它直接 import 的组件库
	dirImports map[string]map[string]bool // 目录（相对 root，根目录是 "."）→ 这个目录里所有 .go 文件（含测试、含任何构建标签）import 的路径
	goFiles    int                        // 扫到的 .go 文件数：测试用它确认扫描器没有悄悄什么也没扫
}

// moduleNameOf 读目录里 go.mod 的 module 行；没有 go.mod 返回空串、false。
func moduleNameOf(dir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), true
		}
	}
	return "", true
}

// scanTree 走 root 下所有 .go 文件（只解析 import）。
// 不走的目录：. 开头的、testdata（go 本来就不编）、node_modules，以及“模块路径本身就是组件库”的子目录
// （例如 third_party/MewTypeUI 它自己的源码）。其他自带 go.mod 的子目录照扫：不然把组件库包进
// third_party 下的一个自制模块、再用 replace 引进来，就绕过了这条检查。_ 开头的试验目录也扫。
// 解析失败的文件记进 errs，调用方要当失败处理，不能当没有。
func scanTree(root string) (scan kitScan, errs []string) {
	scan = kitScan{kits: map[string]map[string]bool{}, dirImports: map[string]map[string]bool{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			if mod, has := moduleNameOf(p); has && kitOf(mod) != "" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		scan.goFiles++
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			errs = append(errs, "解析 "+rel+" 失败："+err.Error())
			return nil
		}
		dir := path.Dir(rel)
		for _, imp := range f.Imports {
			ip, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				errs = append(errs, "读不懂 "+rel+" 的 import "+imp.Path.Value)
				continue
			}
			if scan.dirImports[dir] == nil {
				scan.dirImports[dir] = map[string]bool{}
			}
			scan.dirImports[dir][ip] = true
			if k := kitOf(ip); k != "" {
				if scan.kits[rel] == nil {
					scan.kits[rel] = map[string]bool{}
				}
				scan.kits[rel][k] = true
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, "遍历 "+root+" 失败："+err.Error())
	}
	return scan, errs
}

// scanKitImports 只要“文件 → 直接 import 的库”这一半。
func scanKitImports(root string) (map[string]map[string]bool, []string) {
	scan, errs := scanTree(root)
	return scan.kits, errs
}

// kitReach 从 start 目录出发，沿本模块内的 import 一路走下去（语法树算的闭包：含测试文件、含任何构建标签的文件，
// 不看当前平台，所以 go list 看不到的路径也在），返回能到达哪些组件库，值是其中一条路径。
// 第三方包不往里走：它们不会引本仓库的库。
func kitReach(dirImports map[string]map[string]bool, start string) map[string]string {
	reach := map[string]string{}
	type item struct{ dir, via string }
	seen := map[string]bool{start: true}
	queue := []item{{start, start}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		imps := make([]string, 0, len(dirImports[it.dir]))
		for ip := range dirImports[it.dir] {
			imps = append(imps, ip)
		}
		sort.Strings(imps)
		for _, ip := range imps {
			if k := kitOf(ip); k != "" {
				if _, ok := reach[k]; !ok {
					reach[k] = it.via + " → " + ip
				}
				continue
			}
			dir, ok := strings.CutPrefix(ip, modulePath+"/")
			if !ok || seen[dir] {
				continue
			}
			seen[dir] = true
			queue = append(queue, item{dir, it.via + " → " + dir})
		}
	}
	return reach
}

// facadeMay 说这个文件能不能直接用这个库：只有接口面所在的那个包（同一个目录）、只有被放开的库。
func facadeMay(file, kit string) bool {
	return path.Dir(file) == facadeDir && facadeKits[kit]
}

// baselineRefused 是基线里不许出现的位置：这些地方一条例外都不给。
// 接口面所在的包（放开的库不用登记，没放开的库不能登记）、聊天区 chatui、根包的 workbench_*.go（新界面的挂壳文件）。
func baselineRefused(file string) bool {
	return path.Dir(file) == facadeDir || path.Dir(file) == "internal/chatui" || strings.HasPrefix(path.Base(file), "workbench_")
}

// kitProblems 把扫描结果和基线对一遍，返回所有问题。纯函数：真实的树和测试里搭的桩树用同一份判断。
func kitProblems(found map[string]map[string]bool, baseline map[string][]string) []string {
	var out []string
	inBase := func(file, kit string) bool {
		for _, k := range baseline[file] {
			if k == kit {
				return true
			}
		}
		return false
	}
	files := make([]string, 0, len(found))
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		kits := make([]string, 0, len(found[f]))
		for k := range found[f] {
			kits = append(kits, k)
		}
		sort.Strings(kits)
		for _, k := range kits {
			switch {
			case facadeMay(f, k):
				if inBase(f, k) {
					out = append(out, f+" 的 "+k+" 在接口面里本来就能用，不用登记：从 uiKitBaseline 删掉")
				}
			case inBase(f, k):
				// 存量，放行
			case path.Dir(f) == facadeDir:
				out = append(out, f+" 直接 import 了 "+k+"：接口面所在的包只放开 CrychicUI，MewTypeUI、mygo-m3e 不许进")
			default:
				out = append(out, f+" 直接 import 了 "+k+"：页面只许经 "+facadeDir+" 用组件，不许直接 import 组件库实现（新代码没有例外；老代码搬了位置要在提交说明里写原因才能登记进 uiKitBaseline）")
			}
		}
	}
	names := map[string]bool{}
	for _, k := range uiKits {
		names[k.name] = true
	}
	baseFiles := make([]string, 0, len(baseline))
	for f := range baseline {
		baseFiles = append(baseFiles, f)
	}
	sort.Strings(baseFiles)
	for _, f := range baseFiles {
		if baselineRefused(f) {
			out = append(out, "uiKitBaseline 里有 "+f+"：接口面所在的包、chatui、workbench_*.go 不许有例外，删掉这一项并改文件")
		}
		for _, k := range baseline[f] {
			switch {
			case !names[k]:
				out = append(out, "uiKitBaseline 里 "+f+" 写了不认识的库 "+strconv.Quote(k)+"：只认 uiKits 里的名字")
			case !found[f][k]:
				out = append(out, "uiKitBaseline 里的 "+f+"（"+k+"）已经不成立（文件没了或不再 import 它）：从基线删掉这一项")
			}
		}
	}
	return out
}

// minGoFiles 是“扫描器确实扫到了整个模块”的下限：模块里有两千多个 .go 文件，低于它说明根目录不对或扫描器坏了。
// 比“扫到了几个库”稳：基线清空、winshell 又没引库时，合规状态下扫到的库数可以是 0。
const minGoFiles = 500

func TestUIKitImportsOnlyThroughFacade(t *testing.T) {
	root := moduleRoot(t)
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(facadeDir))); err != nil || !fi.IsDir() {
		t.Fatalf("模块里没有接口面所在的包 %s（%v）：改名或搬家了就改 facadeDir，测试不能什么也没查就过", facadeDir, err)
	}
	scan, errs := scanTree(root)
	for _, e := range errs {
		t.Error(e)
	}
	if scan.goFiles < minGoFiles {
		t.Fatalf("只扫到 %d 个 .go 文件（应有上千个）：扫描器坏了或根目录不对，这一项什么也没查", scan.goFiles)
	}
	for _, p := range kitProblems(scan.kits, uiKitBaseline) {
		t.Error(p)
	}
	pairs := 0
	for _, kits := range uiKitBaseline {
		pairs += len(kits)
		for _, k := range kits {
			if uiKitBaselineReason[k] == "" {
				t.Errorf("uiKitBaselineReason 缺 %s 的原因：基线里出现的库都要写清为什么还在、什么时候去掉", k)
			}
		}
	}
	// 棘轮的弱点：同一个提交里新增一处违规、再往基线加一行，上面查不出来。所以给“文件 × 库”的对数一个写死的上限，
	// 加条目就得同时改它，评审一眼就看得见。
	if pairs > uiKitBaselineMax {
		t.Errorf("uiKitBaseline 有 %d 项，超过上限 %d：基线只许变少。要加条目先问主人，不要自己把 uiKitBaselineMax 改大", pairs, uiKitBaselineMax)
	}
	if pairs < uiKitBaselineMax {
		t.Errorf("uiKitBaseline 只剩 %d 项，把 uiKitBaselineMax 同步改小到 %d（上限跟着缩，才能挡住以后悄悄加回来）", pairs, pairs)
	}
}

// 传递依赖：接口面 winshell 的整棵依赖树里不许出现没被放开的库；聊天区 chatui 不是接口面，树里一个库都不许有。
var uiPackageDeps = []struct {
	name string
	may  map[string]bool // 这个包的依赖树里允许出现的库
}{
	{"internal/winshell", facadeKits},
	{"internal/chatui", nil},
}

// reachProblems 用语法树算每个界面包的 import 闭包（含测试文件、含任何构建标签的文件，kitReach），报出不该到达的库。
// 和下面 go list 那层互补：go list 只看当前平台、默认标签、不含测试文件，但能把第三方包的依赖也算进来。
func reachProblems(dirImports map[string]map[string]bool) []string {
	var out []string
	for _, p := range uiPackageDeps {
		reach := kitReach(dirImports, p.name)
		kits := make([]string, 0, len(reach))
		for k := range reach {
			kits = append(kits, k)
		}
		sort.Strings(kits)
		for _, k := range kits {
			if !p.may[k] {
				out = append(out, p.name+" 能到达组件库 "+k+"（"+reach[k]+"）：包括测试文件和带构建标签的文件，这个包的依赖树里不许有它（ADR-374 决定第 4 条）")
			}
		}
	}
	return out
}

func TestUIPackagesDepsAvoidKits(t *testing.T) {
	all := allPackages(t)
	for _, p := range uiPackageDeps {
		pkg := "sumika-native/" + p.name
		if !all[pkg] {
			t.Fatalf("模块里没有 %s：改名或搬家了就改 uiPackageDeps，测试不能什么也没查就过", pkg)
		}
		sawSelf := false
		for _, d := range depsOf(t, "./"+p.name) {
			if d == pkg {
				sawSelf = true
				continue
			}
			if k := kitOf(d); k != "" && !p.may[k] {
				t.Errorf("%s 依赖了组件库 %s（%s）：这个包的依赖树里不许有它（ADR-374 决定第 4 条）", pkg, k, d)
			}
		}
		if !sawSelf {
			t.Fatalf("go list 的结果里没有 %s 自己，这一项什么也没查", pkg)
		}
	}
	scan, errs := scanTree(moduleRoot(t))
	for _, e := range errs {
		t.Error(e)
	}
	for _, p := range uiPackageDeps {
		if len(scan.dirImports[p.name]) == 0 {
			t.Fatalf("语法树里没有 %s 的 import：扫描器坏了或包搬了，这一项什么也没查", p.name)
		}
	}
	for _, msg := range reachProblems(scan.dirImports) {
		t.Error(msg)
	}
}

// 根包里挂新界面的文件：workbench_ 开头的非测试文件。
// 旧桥函数是根包里把旧主题翻成新令牌的东西，新界面不许调。
var oldBridgeCalls = map[string]bool{"chatTheme": true, "windowBackground": true, "chatFont": true}

// 这些根包文件整体还属于旧界面，不能整体卡，但新界面路径上用到的函数不许调旧桥。
var bridgeOnlyFiles = []string{"chatmedia.go", "workbench_composer.go"}

func TestNewUIPathsDoNotCallOldBridge(t *testing.T) {
	root := moduleRoot(t)
	wb, err := filepath.Glob(filepath.Join(root, "workbench_*.go"))
	if err != nil || len(wb) == 0 {
		t.Fatalf("根包里没有 workbench_*.go（%v）：改名了就改这个测试", err)
	}
	sort.Strings(wb)
	files := append([]string(nil), wb...)
	for _, name := range bridgeOnlyFiles {
		files = append(files, filepath.Join(root, name))
	}
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		for _, b := range oldBridgeUse(t, f) {
			t.Errorf("%s：%s", base, b)
		}
	}
}

// oldBridgeUse 列出一个文件里对旧桥函数的调用。
func oldBridgeUse(t *testing.T, file string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s：%v", file, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && oldBridgeCalls[id.Name] {
			out = append(out, "调用了旧桥 "+id.Name+"（"+fset.Position(call.Pos()).String()+"）")
		}
		return true
	})
	return out
}

// 测试自己的检查：旧桥扫描器要把每个调用都报出来。防止规则悄悄失效。
func TestOldBridgeScannerCatchesCalls(t *testing.T) {
	dir := t.TempDir()
	src := "package main\nfunc f() { chatTheme(nil); windowBackground(nil); _ = chatFont }\n"
	p := filepath.Join(dir, "workbench_stub.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := oldBridgeUse(t, p); len(got) != 2 {
		t.Fatalf("应当报 2 条（两个旧桥调用；chatFont 只是引用不是调用），实际 %d：%v", len(got), got)
	}
}

// writeTree 在 root 下按“相对路径 → 内容”建文件。
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 测试自己的检查：每一种引用写法、每一个入口都要被扫出来（或被明确放过）。判断逻辑改坏了，这里要红。
func TestUIKitScannerShapes(t *testing.T) {
	const (
		mew = `"github.com/tsugumi42/MewTypeUI/theme"`
		cry = `"github.com/ELY-Open-Source/CrychicUI/kit"`
		m3e = `"github.com/tsugumi42/mygo-m3e/m3e"`
		pk  = "package x\n"
	)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// 接口面：CrychicUI 放行，MewTypeUI、mygo-m3e 不行
		"internal/winshell/ok.go":      pk + "import _ " + cry + "\n",
		"internal/winshell/bad_mew.go": pk + "import " + mew + "\n",
		"internal/winshell/bad_m3e.go": pk + "import " + m3e + "\n",
		// 接口面的子目录不算接口面
		"internal/winshell/sub/x.go": pk + "import " + cry + "\n",
		// 写法：普通、别名、点导入、空白导入、分组里夹着别的
		"page_plain.go": pk + "import " + cry + "\n",
		"page_alias.go": pk + "import k " + cry + "\n",
		"page_dot.go":   pk + "import . " + cry + "\n",
		"page_blank.go": pk + "import _ " + mew + "\n",
		"page_group.go": pk + "import (\n\t\"fmt\"\n\tm " + m3e + "\n)\nvar _ = fmt.Sprint\n",
		// 测试文件、带别的平台或忽略标签的文件
		"page_test.go":  pk + "import " + mew + "\n",
		"page_tag.go":   "//go:build ignore\n\n" + pk + "import " + cry + "\n",
		"page_win.go":   "//go:build plan9\n\n" + pk + "import " + mew + "\n",
		"page_plan9.go": pk + "import " + mew + "\n",
		// 入口：别的 internal 包、cmd 工具、_ 开头的试验目录、别的 third_party 包
		"internal/other/x.go":  pk + "import " + cry + "\n",
		"cmd/tool/main.go":     "package main\nimport " + cry + "\nfunc main() {}\n",
		"_spike/x_test.go":     pk + "import " + cry + "\n",
		"third_party/lib/x.go": pk + "import " + cry + "\n",
		// 不算：名字相近的路径、不引库的文件、testdata、自己带 go.mod 的子目录（另一个模块）、非 .go 文件
		"near.go":             pk + "import \"github.com/tsugumi42/MewTypeUIextra/theme\"\nimport \"github.com/ELY-Open-Source/CrychicUI2\"\n",
		"clean.go":            pk + "import \"fmt\"\nvar _ = fmt.Sprint\n",
		"testdata/fixture.go": pk + "import " + cry + "\n",
		// 嵌套模块：不是组件库本身的照扫（有人把库包进 third_party 下自己的模块再 replace 进来）
		"third_party/uikit/go.mod":         "module example.com/uikit\n",
		"third_party/uikit/x.go":           pk + "import " + cry + "\n",
		"third_party/MewTypeUI/go.mod":     "module github.com/tsugumi42/MewTypeUI\n",
		"third_party/MewTypeUI/theme/t.go": pk + "import " + cry + "\n",
		"notes.txt":                        cry,
	})
	found, errs := scanKitImports(root)
	if len(errs) != 0 {
		t.Fatalf("桩树不该有解析错误：%v", errs)
	}
	var got []string
	for f, ks := range found {
		for k := range ks {
			got = append(got, f+" "+k)
		}
	}
	sort.Strings(got)
	want := []string{
		"_spike/x_test.go CrychicUI",
		"cmd/tool/main.go CrychicUI",
		"internal/other/x.go CrychicUI",
		"internal/winshell/bad_m3e.go mygo-m3e",
		"internal/winshell/bad_mew.go MewTypeUI",
		"internal/winshell/ok.go CrychicUI",
		"internal/winshell/sub/x.go CrychicUI",
		"page_alias.go CrychicUI",
		"page_blank.go MewTypeUI",
		"page_dot.go CrychicUI",
		"page_group.go mygo-m3e",
		"page_plain.go CrychicUI",
		"page_plan9.go MewTypeUI",
		"page_tag.go CrychicUI",
		"page_test.go MewTypeUI",
		"page_win.go MewTypeUI",
		"third_party/lib/x.go CrychicUI",
		"third_party/uikit/x.go CrychicUI",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("扫出来的和预期不一样\n实际：\n%s\n预期：\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// 空基线：只有 internal/winshell/ok.go 放行，其余全是违规。
	problems := kitProblems(found, nil)
	if len(problems) != len(want)-1 {
		t.Fatalf("空基线下应有 %d 条违规（只有接口面里的 CrychicUI 放行），实际 %d：\n%s", len(want)-1, len(problems), strings.Join(problems, "\n"))
	}
	for _, p := range problems {
		if strings.HasPrefix(p, "internal/winshell/ok.go ") {
			t.Errorf("接口面里的 CrychicUI 不该报：%s", p)
		}
	}

	// 棘轮：登记的存量放行；登记了不成立的、不认识的库、不许有例外的位置，都要报。
	base := map[string][]string{
		"page_plain.go":                {"CrychicUI"},              // 成立，放行
		"page_alias.go":                {"CrychicUI", "MewTypeUI"}, // MewTypeUI 那半不成立
		"gone.go":                      {"CrychicUI"},              // 文件不存在
		"clean.go":                     {"MewTypeUI"},              // 文件在但不 import
		"page_dot.go":                  {"Fyne"},                   // 不认识的库
		"internal/winshell/ok.go":      {"CrychicUI"},              // 接口面里本来就能用
		"internal/winshell/bad_mew.go": {"MewTypeUI"},              // 接口面不收这类例外
		"internal/chatui/x.go":         {"MewTypeUI"},              // chatui 不许有例外
		"workbench_x.go":               {"MewTypeUI"},              // workbench_ 不许有例外
	}
	joined := strings.Join(kitProblems(found, base), "\n")
	for _, must := range []string{
		"page_alias.go（MewTypeUI）已经不成立",
		"gone.go（CrychicUI）已经不成立",
		"clean.go（MewTypeUI）已经不成立",
		"page_dot.go 写了不认识的库",
		"internal/winshell/ok.go 的 CrychicUI 在接口面里本来就能用",
		"uiKitBaseline 里有 internal/winshell/bad_mew.go",
		"uiKitBaseline 里有 internal/chatui/x.go",
		"uiKitBaseline 里有 workbench_x.go",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("基线对账应报出 %q，实际：\n%s", must, joined)
		}
	}
	for _, registered := range []string{"page_plain.go 直接 import", "page_alias.go 直接 import"} {
		if strings.Contains(joined, registered) {
			t.Errorf("登记过的存量不该再报新增（%s）：\n%s", registered, joined)
		}
	}
	if !strings.Contains(joined, "page_blank.go 直接 import 了 MewTypeUI") {
		t.Errorf("没登记的 page_blank.go 要报新增：\n%s", joined)
	}

	// 解析失败不能当没有。
	writeTree(t, root, map[string]string{"broken.go": "package x\nimport (\n"})
	if _, errs := scanKitImports(root); len(errs) == 0 {
		t.Error("有语法错误的文件应当报解析失败，不能悄悄跳过")
	}
}

// 测试自己的检查：语法树闭包要把测试文件、带构建标签的文件、外部测试包经中间包引到的库都算出来，并认得放开的库。
func TestUIKitReachShapes(t *testing.T) {
	const (
		mew = `"github.com/tsugumi42/MewTypeUI/theme"`
		cry = `"github.com/ELY-Open-Source/CrychicUI/kit"`
		pk  = "package x\n"
	)
	mod := func(rel string) string { return `"` + modulePath + "/" + rel + `"` }
	cases := []struct {
		name  string
		files map[string]string
		want  []string // reachProblems 里必须出现的片段；空表示不许有问题
	}{
		{"干净", map[string]string{
			"internal/winshell/a.go": pk + "import _ " + cry + "\n", // winshell 自己引放开的库
			"internal/chatui/a.go":   pk,
		}, nil},
		{"经放开的库的中间包到 CrychicUI：winshell 可以，chatui 不行", map[string]string{
			"internal/winshell/a.go": pk + "import _ " + mod("internal/viacry") + "\n",
			"internal/chatui/a.go":   pk + "import _ " + mod("internal/viacry") + "\n",
			"internal/viacry/a.go":   pk + "import _ " + cry + "\n",
		}, []string{"internal/chatui 能到达组件库 CrychicUI"}},
		{"默认文件经基线里的中间包到 MewTypeUI", map[string]string{
			"internal/winshell/a.go": pk + "import _ " + mod("internal/legacy") + "\n",
			"internal/chatui/a.go":   pk,
			"internal/legacy/a.go":   pk + "import _ " + mew + "\n",
		}, []string{"internal/winshell 能到达组件库 MewTypeUI（internal/winshell → internal/legacy → github.com/tsugumi42/MewTypeUI/theme）"}},
		{"带 e2e 标签的文件经中间包", map[string]string{
			"internal/winshell/a.go":     pk,
			"internal/winshell/a_e2e.go": "//go:build e2e\n\n" + pk + "import _ " + mod("internal/legacy") + "\n",
			"internal/chatui/a.go":       pk,
			"internal/legacy/a.go":       pk + "import _ " + mew + "\n",
		}, []string{"internal/winshell 能到达组件库 MewTypeUI"}},
		{"测试文件经中间包", map[string]string{
			"internal/winshell/a.go":       pk,
			"internal/winshell/zz_test.go": pk + "import _ " + mod("internal/legacy") + "\n",
			"internal/chatui/a.go":         pk,
			"internal/legacy/a.go":         pk + "import _ " + mew + "\n",
		}, []string{"internal/winshell 能到达组件库 MewTypeUI"}},
		{"外部测试包经两层中间包，带环", map[string]string{
			"internal/winshell/a.go":      pk,
			"internal/winshell/x_test.go": "package x_test\nimport _ " + mod("internal/m1") + "\n",
			"internal/chatui/a.go":        pk,
			"internal/m1/a.go":            pk + "import _ " + mod("internal/m2") + "\n",
			"internal/m2/a.go":            pk + "import _ " + mod("internal/m1") + "\nimport _ " + mew + "\n",
		}, []string{"internal/winshell 能到达组件库 MewTypeUI（internal/winshell → internal/m1 → internal/m2 →"}},
	}
	for _, c := range cases {
		root := t.TempDir()
		writeTree(t, root, c.files)
		scan, errs := scanTree(root)
		if len(errs) != 0 {
			t.Fatalf("%s：桩树不该有解析错误：%v", c.name, errs)
		}
		got := strings.Join(reachProblems(scan.dirImports), "\n")
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s：不该有问题，实际：\n%s", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：应报出 %q，实际：\n%s", c.name, w, got)
			}
		}
	}
}
