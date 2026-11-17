package archcheck

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 架构测试（ADR-224）：发出去的两个程序——桌面程序（模块根）和 Go 后台（cmd/sumika-go）——不链接一次性的、
// 只给测试和命令行工具用的包。一次性的东西改了不该牵动常驻程序；常驻代码要用它们里的某一块，先把那一块搬进常驻的包
// 放在 internal/archcheck：合并前只测改了的包（ADR-225），挑包脚本只要挑上了 Go 包就带上这个包（只调 go list，几秒）。
//
// toolOnly 是不许进这两个程序的包，新加的这类包登记在这里。改名、搬家时一起改：测试先确认它们都在，不然什么也没查。
var toolOnly = []string{
	"sumika-native/internal/bench",              // 表二执行器的评测，只给 cmd/bench-t2、cmd/bench-shell
	"sumika-native/internal/channel/fake",       // 假适配器，给测试用
	"sumika-native/internal/channel/contract",   // 适配器的契约测试框架
	"sumika-native/internal/llm/llmtest",        // 测试用的假网关和录像回放
	"sumika-native/internal/embed/embedtest",    // 测试程序扮演的假 sumika-models.exe，给 embed 和 app 的测试
	"sumika-native/internal/turn/turntest",      // 内核测试的夹具
	"sumika-native/internal/testutil/junction",  // 测试在进程内建目录联接，不给常驻代码用
	"sumika-native/internal/testutil/volpath",   // 测试把路径写成 \\?\Volume{GUID}\… 的卷标识写法，不给常驻代码用
	"sumika-native/internal/localmodels",        // 本机嵌入、重排的纯 Go 实现（分词器、ORT 封装、引擎……），只给 cmd/sumika-models；ORT 是原生代码，不链进常驻程序
	"sumika-native/third_party/goSentencePiece", // 拷进来自己改的 tggo 分词器 fork，只给 internal/localmodels/tokenizer
}

var shipped = []string{".", "./cmd/sumika-go", "./cmd/sumika-ctl"}

// moduleRoot 是模块根目录（go.mod 所在）：go list ./... 和两个程序的相对路径都从这里算
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := goEnvGoMod()
	if err != nil {
		t.Fatalf("找不到模块根目录：%v", err)
	}
	return root
}

var goEnvGoMod = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	return filepath.Dir(strings.TrimSpace(string(out))), err
})

// 这个包的测试都在问“谁依赖谁”，答案在一次运行里不会变：整个模块只问 go 一次。
// 每条单问一次 go list 要几秒，加起来这个包要跑半分钟以上（2026-11-09 量的）。
type packageInfo struct {
	Deps                               []string // 全部传递依赖，不含测试文件的 import，也不含包自己
	Imports, TestImports, XTestImports []string // 直接 import：正式代码、包内测试、外部测试包
}

var moduleInfoOnce struct {
	sync.Once
	pkgs map[string]packageInfo
	err  error
	out  []byte
}

// moduleInfo 是模块里每个包的依赖。-e：某个包编不过也列出来，由别的测试去报它编不过。
func moduleInfo(t *testing.T) map[string]packageInfo {
	t.Helper()
	root := moduleRoot(t)
	o := &moduleInfoOnce
	o.Do(func() {
		cmd := exec.Command("go", "list", "-e", "-f", `{{.ImportPath}}|{{join .Deps ","}}|{{join .Imports ","}}|{{join .TestImports ","}}|{{join .XTestImports ","}}`, "./...")
		cmd.Dir = root
		var stderr strings.Builder
		cmd.Stderr = &stderr
		o.out, o.err = cmd.Output()
		if o.err != nil {
			o.out = []byte(stderr.String())
			return
		}
		list := func(s string) []string {
			if s == "" {
				return nil
			}
			return strings.Split(s, ",")
		}
		o.pkgs = map[string]packageInfo{}
		for _, line := range strings.Split(strings.TrimSpace(string(o.out)), "\n") {
			f := strings.Split(strings.TrimSpace(line), "|")
			if len(f) != 5 {
				o.err = fmt.Errorf("输出格式不对：%q", line)
				return
			}
			o.pkgs[f[0]] = packageInfo{Deps: list(f[1]), Imports: list(f[2]), TestImports: list(f[3]), XTestImports: list(f[4])}
		}
	})
	if o.err != nil {
		t.Fatalf("go list ./... 失败：%v\n%s", o.err, o.out)
	}
	return o.pkgs
}

const modulePath = "sumika-native"

// importPathOf 把 ./x 这种相对写法换成导入路径。
func importPathOf(pkg string) string {
	switch {
	case pkg == ".":
		return modulePath
	case strings.HasPrefix(pkg, "./"):
		return modulePath + "/" + strings.TrimPrefix(pkg, "./")
	}
	return pkg
}

// allPackages 是模块里所有的包。
func allPackages(t *testing.T) map[string]bool {
	t.Helper()
	all := map[string]bool{}
	for p := range moduleInfo(t) {
		all[p] = true
	}
	return all
}

// depsOf 是这个包自己加上它的全部传递依赖（和 go list -deps 列的一样，顺序不同）。
func depsOf(t *testing.T, pkg string) []string {
	t.Helper()
	path := importPathOf(pkg)
	info, ok := moduleInfo(t)[path]
	if !ok {
		t.Fatalf("go list ./... 里没有 %s", path)
	}
	return append([]string{path}, info.Deps...)
}

func TestShippedProgramsSkipToolOnlyPackages(t *testing.T) {
	have := allPackages(t)
	for _, p := range toolOnly {
		if !have[p] {
			t.Fatalf("go list ./... 里没有 %s：改名或搬家了就把 toolOnly 一起改，不然这个测试什么也没查", p)
		}
	}
	for _, prog := range shipped {
		// -deps 只列正式代码的依赖，测试文件的 import 不算
		for _, d := range depsOf(t, prog) {
			for _, p := range toolOnly {
				if d == p || strings.HasPrefix(d, p+"/") {
					t.Errorf("%s 链接了只给测试和工具用的 %s：常驻代码要用里面的东西，先把那一块搬进常驻的包", prog, d)
				}
			}
		}
	}
}
