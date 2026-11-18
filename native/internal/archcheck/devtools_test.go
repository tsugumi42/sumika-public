package archcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sumika-native/internal/devtools/selfbuild"
)

// 规格-脚本迁Go「落点」、ADR-389：开发工具程序 sumika-dev（cmd/sumika-dev 和 internal/devtools/...）不许依赖产品包
// （app、turn、channel、界面、模型……）。开发工具要在新机器上只装 Go 就能编，也要在产品代码写坏、编不过的时候照样能跑去检查它，
// 所以只许用标准库、golang.org/x 和 internal/ 里的纯工具包（selfbuild.AllowedModulePackages）。
// 写成白名单：新加的包默认不许，要用就来这里登记并写理由；第三方库只列真用到的（见 devtoolsAllowedThirdParty）。
// 查的是传递依赖（go list -deps -test）：jsjson、netsafe 自己再 import 产品包，也算开发工具依赖了它；测试文件的依赖也算。

const devtoolsPrefix = modulePath + "/internal/devtools"

// isDevtoolsPackage 是受这条规则管的包：internal/devtools 及其下所有包，加上 cmd/sumika-dev。
func isDevtoolsPackage(pkg string) bool {
	return pkg == modulePath+"/cmd/sumika-dev" || pkg == devtoolsPrefix || strings.HasPrefix(pkg, devtoolsPrefix+"/")
}

// devtoolsAllowedThirdParty 是许可的第三方库（前缀匹配，含子路径），golang.org/x/... 另算。
// 理由都是“同一份库产品里已经在用，开发工具不另选”：
//   - modernc.org/sqlite：modernc.org/libc、mathutil、memory，github.com/dustin/go-humanize、mattn/go-isatty、ncruces/go-strftime、remyoudompheng/bigfft 都是它的传递依赖（go list -deps 会列出来，所以也要登记），不是开发工具自己选的：
//     隐私扫描读本机聊天库名单（只读），不用 cgo、不装 sqlite 命令行；
//     github.com/google/uuid 是 modernc.org/libc 在 Linux 上的传递依赖（Windows 上不引），第五轮 CI 的 Linux 一格才列出来，同样登记；
//   - github.com/goccy/go-yaml：隐私扫描读 data/config/sumika.yml，旧版用 Bun.YAML，Go 里用产品配置同一个库（保持键顺序要 UseOrderedMap）。
//
// 第三方库变化由 go.sum 覆盖（selfbuild 的摘要含 go.mod、go.sum），不需要进 SourceDirs。
var devtoolsAllowedThirdParty = []string{
	"modernc.org/sqlite", "modernc.org/libc", "modernc.org/mathutil", "modernc.org/memory",
	"github.com/dustin/go-humanize", "github.com/google/uuid", "github.com/mattn/go-isatty", "github.com/ncruces/go-strftime", "github.com/remyoudompheng/bigfft",
	"github.com/goccy/go-yaml",
}

// devtoolsAllowedInternal 是许可的本模块纯工具包（前缀匹配，含子路径），internal/devtools/... 自己另算。
// 直接取 selfbuild.AllowedModulePackages：它和参与源码摘要的目录（selfbuild.SourceDirs）是同一份常量，
// 开发工具依赖的本模块代码一定在摘要里，改了会触发重编；两边不会一边加了另一边漏掉。
var devtoolsAllowedInternal = func() []string {
	var out []string
	for _, d := range selfbuild.AllowedModulePackages {
		out = append(out, modulePath+"/"+d)
	}
	return out
}()

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// devtoolsDepAllowed 判断开发工具的一个（传递）依赖是否在白名单里；标准库由调用者用 go list 的 Standard 字段先排除。
func devtoolsDepAllowed(dep string) bool {
	if hasPathPrefix(dep, "golang.org/x") || hasPathPrefix(dep, devtoolsPrefix) || dep == modulePath+"/cmd/sumika-dev" {
		return true
	}
	for _, p := range devtoolsAllowedInternal {
		if hasPathPrefix(dep, p) {
			return true
		}
	}
	for _, p := range devtoolsAllowedThirdParty {
		if hasPathPrefix(dep, p) {
			return true
		}
	}
	return false
}

// devtoolsDeps 返回 cmd/sumika-dev 和 internal/devtools/... 的全部传递依赖（含测试文件的），去掉标准库。
// 标准库用 go list 的 Standard 字段认，不用“第一段不含点”猜。第二个返回值是找到的开发工具包个数。
func devtoolsDeps(t *testing.T) (deps []string, seen int) {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-f", "{{.ImportPath}}|{{.Standard}}", "./cmd/sumika-dev", "./internal/devtools/...")
	cmd.Dir = moduleRoot(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list 失败：%v\n%s", err, stderr.String())
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, std, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			t.Fatalf("go list 输出格式不对：%q", line)
		}
		if std == "true" {
			continue
		}
		if i := strings.Index(path, " ["); i >= 0 { // 带测试的变体：pkg [pkg.test]
			path = path[:i]
		}
		path = strings.TrimSuffix(path, ".test")
		path = strings.TrimSuffix(path, "_test") // 外部测试包 pkg_test
		set[path] = true
	}
	for p := range set {
		deps = append(deps, p)
		if isDevtoolsPackage(p) {
			seen++
		}
	}
	sort.Strings(deps)
	return deps, seen
}

func TestDevtoolsImportAllowlist(t *testing.T) {
	deps, seen := devtoolsDeps(t)
	if seen < 3 {
		t.Fatalf("只找到 %d 个开发工具包：包改名、搬家了就把这里的路径一起改，不然这个测试什么也没查", seen)
	}
	for _, d := range deps {
		if !devtoolsDepAllowed(d) {
			t.Errorf("开发工具（含传递依赖和测试）依赖了 %s：只许标准库、golang.org/x、internal/devtools、%s；要用别的先在 archcheck/devtools_test.go 登记并写理由",
				d, strings.Join(selfbuild.AllowedModulePackages, "、"))
		}
	}
}

// 源码摘要只覆盖 selfbuild.SourceDirs 里的 .go 文件加 go.mod、go.sum：目录要都在；
// 这些目录里不许用 //go:embed（embed 的非 .go 文件不在摘要里，改了不会触发重编。真要用，先让 selfbuild.Digest 把那些文件也算进去，再删掉这条断言）。
func TestDevtoolsDigestDirsExistAndHaveNoEmbed(t *testing.T) {
	root := moduleRoot(t)
	for _, d := range selfbuild.SourceDirs {
		dir := filepath.Join(root, filepath.FromSlash(d))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Errorf("摘要目录 %s 不存在：改名、搬家了就把 selfbuild.SourceDirs 一起改", d)
			continue
		}
		err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(b), "//go:embed") {
				t.Errorf("%s 用了 //go:embed：embed 的文件不在 sumika-dev 的源码摘要里，改了不会触发重编", p)
			}
			return nil
		})
		if err != nil {
			t.Errorf("读 %s 失败：%v", d, err)
		}
	}
}
