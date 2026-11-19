// Package fonts 是软件自带的字体服务：按用途给出一串字体文件，依次是文渊圆体 → Maple Mono NF CN → Windows 自带字体固定名单。
// 它只管“文件在哪、按什么顺序”，不读文件、不注册（注册给 mygo 是调用方的事，linkcard、主程序各按自己的字族名来），所以不依赖 mygo。
//
// 找法只有一套（目录在上一层 assets 包：明说的目录、exe 旁的 fonts/、native/fonts/ 和主工作区的）：
//   - 文渊圆体：WenYuanRoundedSCVF.ttf（完整的，46 MB）和 WenYuanRoundedSCVF-subset.ttf（裁过的常用字子集，15 MB）；环境变量 SUMIKA_FONT 指定文件时优先。
//   - Maple Mono NF CN：fonts/ 里的 MapleMono-NF-CN-<字重>.ttf，字重名 Thin、ExtraLight、Light、Regular、Medium、SemiBold、Bold、ExtraBold，按字重取；
//     不去扫系统字体目录和用户另装的字体目录（用户装了什么不归我们管，行为要在每台机器上一样）。
//   - 系统兜底：只到 %WINDIR%\Fonts 里按固定文件名找，名单见 systemList，不扫目录。
//
// 许可证：文渊圆体见 native/fonts/WenYuan-LICENSE.md，Maple 是 OFL-1.1（native/fonts/MapleMono-OFL.txt），来源汇总在 assets/THIRD_PARTY.md。
package fonts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sumika-native/internal/assets"
)

// 文渊圆体的文件名。
const (
	WenYuanFull   = "WenYuanRoundedSCVF.ttf"
	WenYuanSubset = "WenYuanRoundedSCVF-subset.ttf"
)

// Source 是字体从哪来。
type Source int

const (
	SourceWenYuan Source = iota + 1 // 文渊圆体
	SourceMaple                     // Maple Mono NF CN
	SourceSystem                    // Windows 自带
)

func (s Source) String() string {
	switch s {
	case SourceWenYuan:
		return "文渊圆体"
	case SourceMaple:
		return "Maple Mono NF CN"
	case SourceSystem:
		return "系统字体"
	}
	return "未知"
}

// Font 是一个找到了的字体文件。
type Font struct {
	Source Source
	Name   string // 给人看的名字
	Path   string // 文件完整路径
	// Weight 是字重（400 常规、700 粗体）；文渊圆体是可变字体，各档都有，记 0。
	Weight int
	// Script 是系统字体管的文字：zh、ja、ko、symbol、latin；文渊圆体和 Maple 为空（通用）。
	Script string
}

// Options 是找字体的设置，零值就是正式程序和开发时的默认找法。
type Options struct {
	// Dirs 是素材字体目录（放文渊圆体和 Maple 的地方），按先后找；nil 用 assets.FontDirs()。测试和明说目录时给。
	Dirs []string
	// SystemDir 是 Windows 字体目录；空取 %WINDIR%\Fonts。
	SystemDir string
	// Subset 为真时文渊圆体先找裁过的子集（主程序启动时少读 30 MB），默认完整的优先（子集可能缺字，推特卡片要全）。
	Subset bool
}

func (o Options) dirs() []string {
	if o.Dirs != nil {
		return o.Dirs
	}
	return assets.FontDirs()
}

// WenYuanCandidates 是文渊圆体按顺序试的文件路径（不管在不在）：SUMIKA_FONT 指定的（只在没给 Dirs 时认），再是每个目录里的完整版、子集（Subset 为真时反过来）。
func WenYuanCandidates(o Options) []string {
	var out []string
	if p := os.Getenv("SUMIKA_FONT"); p != "" && o.Dirs == nil {
		out = append(out, p)
	}
	names := []string{WenYuanFull, WenYuanSubset}
	if o.Subset {
		names = []string{WenYuanSubset, WenYuanFull}
	}
	for _, d := range o.dirs() {
		for _, n := range names {
			out = append(out, filepath.Join(d, n))
		}
	}
	return out
}

// mapleWeights 是 Maple 文件名里的字重名。
var mapleWeights = []struct {
	weight int
	name   string
}{{100, "Thin"}, {200, "ExtraLight"}, {300, "Light"}, {400, "Regular"}, {500, "Medium"}, {600, "SemiBold"}, {700, "Bold"}, {800, "ExtraBold"}}

// MapleFile 是某个字重的 Maple 文件名；字重不是 100 到 800 的整百返回空串。
func MapleFile(weight int) string {
	for _, w := range mapleWeights {
		if w.weight == weight {
			return "MapleMono-NF-CN-" + w.name + ".ttf"
		}
	}
	return ""
}

// MapleCandidates 是这个字重的 Maple 文件在每个素材目录里的路径（不管在不在）。
func MapleCandidates(o Options, weight int) []string {
	f := MapleFile(weight)
	if f == "" {
		return nil
	}
	var out []string
	for _, d := range o.dirs() {
		out = append(out, filepath.Join(d, f))
	}
	return out
}

// First 返回路径里第一个存在的普通文件，没有返回空串。
func First(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// WenYuan 找文渊圆体。
func WenYuan(o Options) (Font, bool) {
	p := First(WenYuanCandidates(o))
	return Font{Source: SourceWenYuan, Name: "文渊圆体", Path: p}, p != ""
}

// WenYuanPath 是默认找法找到的文渊圆体文件路径，找不到是空串。样张工具、原型程序和测试用（它们不关心试过哪些路径）。
func WenYuanPath() string {
	f, _ := WenYuan(Options{})
	return f.Path
}

// Maple 找 Maple Mono NF CN：要的字重没有就取最近的（一样近取细的）；返回的 Weight 是实际取到的字重，调用方可以比较。一个字重都没有就是 false。
func Maple(o Options, weight int) (Font, bool) {
	var best Font
	bestDist := -1
	for _, w := range mapleWeights {
		p := First(MapleCandidates(o, w.weight))
		if p == "" {
			continue
		}
		d := w.weight - weight
		if d < 0 {
			d = -d
		}
		if bestDist < 0 || d < bestDist {
			best, bestDist = Font{Source: SourceMaple, Name: "Maple Mono NF CN", Path: p, Weight: w.weight}, d
		}
	}
	return best, bestDist >= 0
}

// systemList 是 Windows 自带字体的固定名单：只到 %WINDIR%\Fonts 里按文件名找。
var systemList = []struct{ name, file, script string }{
	{"微软雅黑", "msyh.ttc", "zh"},
	{"Yu Gothic", "YuGothR.ttc", "ja"},
	{"Malgun Gothic", "malgun.ttf", "ko"},
	{"Segoe UI Symbol", "seguisym.ttf", "symbol"},
	{"Segoe UI", "segoeui.ttf", "latin"},
}

func (o Options) systemDir() string {
	if o.SystemDir != "" {
		return o.SystemDir
	}
	if w := os.Getenv("WINDIR"); w != "" {
		return filepath.Join(w, "Fonts")
	}
	return ""
}

// System 返回名单里这台电脑上真有的 Windows 自带字体，顺序是中文、日文、韩文、符号、西文。
func System(o Options) []Font {
	dir := o.systemDir()
	if dir == "" {
		return nil
	}
	var out []Font
	for _, s := range systemList {
		if p := First([]string{filepath.Join(dir, s.file)}); p != "" {
			out = append(out, Font{Source: SourceSystem, Name: s.name, Path: p, Weight: 400, Script: s.script})
		}
	}
	return out
}

// Purpose 是字体的用途。
type Purpose int

const (
	// Body 是正文（界面、卡片上的字）：文渊圆体，缺的字 Maple 兜，再缺交 Windows 自带的。
	Body Purpose = iota + 1
	// Mono 是等宽（代码、控制台）：Maple 排第一，文渊圆体兜中文，再是 Windows 自带的。
	Mono
)

// Chain 按用途给出能找到的字体，先后就是回退的先后；没找到的不在里面（不是错误）。Maple 给常规和粗体两个字重。
func Chain(p Purpose, o Options) []Font {
	var wy, maple []Font
	if f, ok := WenYuan(o); ok {
		wy = append(wy, f)
	}
	for _, w := range []int{400, 700} {
		if f, ok := Maple(o, w); ok && f.Weight == w {
			maple = append(maple, f)
		}
	}
	var out []Font
	if p == Mono {
		out = append(append(out, maple...), wy...)
	} else {
		out = append(append(out, wy...), maple...)
	}
	return append(out, System(o)...)
}

// Missing 说明 Chain 里少了哪些来源，给日志用；都有就是空串。
func Missing(chain []Font) string {
	have := map[Source]bool{}
	for _, f := range chain {
		have[f.Source] = true
	}
	var miss []string
	for _, s := range []Source{SourceWenYuan, SourceMaple, SourceSystem} {
		if !have[s] {
			miss = append(miss, s.String())
		}
	}
	sort.Strings(miss)
	if len(miss) == 0 {
		return ""
	}
	return fmt.Sprintf("没找到：%s", strings.Join(miss, "、"))
}
