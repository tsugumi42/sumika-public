package policy

import (
	"runtime"
	"testing"
)

// 规则里写的路径怎么解、怎么比：resolveSpec、pathHits、specHits、absDir 在两个系统上都跑，期望值按 runtime.GOOS 分。
// Windows 一列是原来的行为（一字不变）；非 Windows 一列是按本机路径语义的行为：/ 是分隔符、\ 只是个字符，
// 解完统一成反斜杠再比（resolveSpec 的注释）。非 Windows 用 /work 当工作目录、/x 形式的绝对路径。
// 非 Windows 那一列在 Windows 上跑不到，靠 CI 的 Linux 一格；Windows 那一列本机直接跑。

const (
	specWinWork   = `D:\Work`
	specOtherWork = "/work"
)

func specWork() string {
	if runtime.GOOS == "windows" {
		return specWinWork
	}
	return specOtherWork
}

type dual struct{ win, other string }

func (d dual) get() string {
	if runtime.GOOS == "windows" {
		return d.win
	}
	return d.other
}

type dualBool struct{ win, other bool }

func (d dualBool) get() bool {
	if runtime.GOOS == "windows" {
		return d.win
	}
	return d.other
}

func TestResolveSpecTable(t *testing.T) {
	for _, c := range []struct {
		name, spec string
		want       dual
	}{
		{"通配符", "src/**", dual{`D:\Work\src\**`, `\work\src\**`}},
		{"尾部斜杠", "src/", dual{`D:\Work\src`, `\work\src`}},
		{"上一级", "../x", dual{`D:\x`, `\x`}},
		{"中间有 ..", "src/../x/**", dual{`D:\Work\x\**`, `\work\x\**`}},
		{"根", "/", dual{`D:\`, `\`}},
		{"绝对路径", "/other/x", dual{`D:\other\x`, `\other\x`}},
		{"单星", "src/*", dual{`D:\Work\src\*`, `\work\src\*`}},
		// 规则里写了反斜杠（从 Windows 抄来的）：Windows 上就是分隔符；非 Windows 上 \ 只是文件名里的字符，不当分隔符
		{"点开头的反斜杠", `.\secrets\**`, dual{`D:\Work\secrets\**`, `\work\.\secrets\**`}},
		{"反斜杠里有 ..", `src\..\secrets\**`, dual{`D:\Work\secrets\**`, `\work\src\..\secrets\**`}},
	} {
		if got := resolveSpec(specWork(), c.spec); got != c.want.get() {
			t.Errorf("%s：resolveSpec(%q, %q) = %q，要 %q", c.name, specWork(), c.spec, got, c.want.get())
		}
	}
}

func TestPathHitsTable(t *testing.T) {
	// 家目录：两个系统都用环境变量指到一个编的目录
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", `D:\Home`)
	} else {
		t.Setenv("HOME", "/home/u")
	}
	for _, c := range []struct {
		name, spec string
		path       dual
		want       bool
	}{
		{"双星命中一级", "src/**", dual{`D:\Work\src\a.go`, "/work/src/a.go"}, true},
		{"双星跨目录", "src/**", dual{`D:\Work\src\x\y.go`, "/work/src/x/y.go"}, true},
		{"前缀相同的另一个目录不命中", "src/**", dual{`D:\Work\src2\a.go`, "/work/src2/a.go"}, false},
		{"别的目录不命中", "src/**", dual{`D:\Work\other\a.go`, "/work/other/a.go"}, false},
		{"单星命中一级", "src/*", dual{`D:\Work\src\a.go`, "/work/src/a.go"}, true},
		{"单星不跨目录", "src/*", dual{`D:\Work\src\x\y.go`, "/work/src/x/y.go"}, false},
		{"不带通配符的目录盖住里面", "src", dual{`D:\Work\src\a.go`, "/work/src/a.go"}, true},
		{"不带通配符不吃前缀相同的", "src", dual{`D:\Work\srcx\a.go`, "/work/srcx/a.go"}, false},
		{"尾部斜杠", "src/", dual{`D:\Work\src\a.go`, "/work/src/a.go"}, true},
		{"上一级", "../x/**", dual{`D:\x\a.go`, "/x/a.go"}, true},
		{"根盖住所有路径", "/", dual{`D:\Work\a.go`, "/work/a.go"}, true},
		{"~ 开头", "~/notes/**", dual{`D:\Home\notes\a.md`, "/home/u/notes/a.md"}, true},
		{"~ 开头不命中别处", "~/notes/**", dual{`D:\Work\notes\a.md`, "/work/notes/a.md"}, false},
		// 规则里写反斜杠：Windows 上命中；非 Windows 上 pathHits 本身不命中（宽松那一遍在 specHits 里，见下一个测试）
		{"反斜杠写法", `.\secrets\**`, dual{`D:\Work\secrets\a.txt`, "/work/secrets/a.txt"}, runtime.GOOS == "windows"},
	} {
		if got := pathHits(c.spec, c.path.get(), specWork()); got != c.want {
			t.Errorf("%s：pathHits(%q, %q) = %v，要 %v", c.name, c.spec, c.path.get(), got, c.want)
		}
	}
}

// 非 Windows 上的“宽松再比一遍”：deny、ask 规则写了反斜杠，多拦不吃亏；allow 不加（多放行是放宽）。
func TestSpecHitsLooseBackslash(t *testing.T) {
	path := dual{`D:\Work\secrets\a.txt`, "/work/secrets/a.txt"}.get()
	for _, c := range []struct {
		effect string
		want   dualBool
	}{
		{EffectDeny, dualBool{true, true}},
		{EffectAsk, dualBool{true, true}},
		{EffectAllow, dualBool{true, false}}, // Windows 上写法本来就一样；非 Windows 上 allow 不吃反斜杠
	} {
		for _, spec := range []string{`.\secrets\**`, `src\..\secrets\**`} {
			r := UserRule{Effect: c.effect, Tool: "read", Spec: spec}
			if got := specHits(r, path, specWork()); got != c.want.get() {
				t.Errorf("%s 规则 %q 对 %q：%v，要 %v", c.effect, spec, path, got, c.want.get())
			}
		}
	}
	// 正斜杠写法的 allow 照常命中：不被宽松那一遍的开关影响
	if !specHits(UserRule{Effect: EffectAllow, Tool: "read", Spec: "secrets/**"}, path, specWork()) {
		t.Error("正斜杠写法的 allow 该命中")
	}
}

func TestAbsDirTable(t *testing.T) {
	for _, c := range []struct {
		name, p string
		want    dualBool
	}{
		{"盘符反斜杠", `D:\x`, dualBool{true, true}},
		{"盘符正斜杠", "D:/x", dualBool{true, true}},
		{"UNC", `\\server\share`, dualBool{true, true}},
		{"UNC 正斜杠", "//server/share", dualBool{true, true}},
		{"设备路径", `\\?\C:\x`, dualBool{false, false}},
		{"POSIX 绝对路径", "/x/y", dualBool{false, true}},
		{"只有根", "/", dualBool{false, false}},
		{"相对路径", "x/y", dualBool{false, false}},
		{"空", "", dualBool{false, false}},
		{"只有盘符", "D:", dualBool{false, false}},
	} {
		if got := absDir(c.p); got != c.want.get() {
			t.Errorf("%s：absDir(%q) = %v，要 %v", c.name, c.p, got, c.want.get())
		}
	}
}
