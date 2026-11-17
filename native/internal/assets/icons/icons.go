// Package icons 是软件自带的 MingCute 图标全套（Apache-2.0，npm 包 mingcute_icon，许可证原文见同目录 LICENSE-MingCute）。
// 表是 scripts/gen-mingcute.ts 生成的（icons_gen.go，别手改），按名字取：名字是 MingCute 官网上的名字去掉 _line、_fill，
// 比如 add、arrow_down、chat_3；line 是线性、fill 是实心，24×24，颜色 currentColor，跟着元素的 TextColor 走。
// 这是底层包，只依赖 mygo 的 SVG，chatui、linkcard、将来的插件接口都从这里取，不各抄一份。
package icons

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

type entry struct{ name, line, fill string }

// ErrUnknown 是名字不在表里（或这个名字没有要的那种样式）。
var ErrUnknown = errors.New("assets/icons: 没有这个图标")

// Version 是图标来自的 mingcute_icon 版本。
func Version() string { return sourceVersion }

// Count 是图标个数（line、fill 算同一个）。
func Count() int { return len(table) }

// Names 返回全部图标名，已排序。
func Names() []string {
	out := make([]string, len(table))
	for i, e := range table {
		out[i] = e.name
	}
	return out
}

func find(name string) (entry, bool) {
	i := sort.Search(len(table), func(i int) bool { return table[i].name >= name })
	if i < len(table) && table[i].name == name {
		return table[i], true
	}
	return entry{}, false
}

// Body 返回图标放进 <svg> 里面的路径文本。名字也可以带 MingCute 文件名里的 _line、_fill 后缀，后缀说了算（fill 参数被覆盖）。
// 名字不在表里、或没有要的那种样式，返回包着 ErrUnknown 的错误。
func Body(name string, fill bool) (string, error) {
	e, ok := find(name)
	if !ok {
		for _, suf := range []struct {
			s    string
			fill bool
		}{{"_line", false}, {"_fill", true}} {
			if base, cut := strings.CutSuffix(name, suf.s); cut {
				if e, ok = find(base); ok {
					fill = suf.fill
					break
				}
			}
		}
	}
	if !ok {
		return "", fmt.Errorf("%w：%q", ErrUnknown, name)
	}
	body := e.line
	if fill {
		body = e.fill
	}
	if body == "" {
		return "", fmt.Errorf("%w：%q 没有%s版", ErrUnknown, name, map[bool]string{false: "线性", true: "实心"}[fill])
	}
	return body, nil
}

// Markup 是完整的 <svg> 文本（24×24），给要自己喂 SVG 的调用方（导出、给脚本插件）。
func Markup(name string, fill bool) (string, error) {
	body, err := Body(name, fill)
	if err != nil {
		return "", err
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">` + body + `</svg>`, nil
}

var (
	mu    sync.Mutex
	cache = map[string]*ui.SVG{}
)

// SVG 返回能直接给 mygo 画的图标（解析结果按名字和样式缓存，同一个指针可以到处用）。
func SVG(name string, fill bool) (*ui.SVG, error) {
	key := name
	if fill {
		key += "#fill"
	}
	mu.Lock()
	defer mu.Unlock()
	if s, ok := cache[key]; ok {
		return s, nil
	}
	m, err := Markup(name, fill)
	if err != nil {
		return nil, err
	}
	s, err := ui.ParseSVG([]byte(m))
	if err != nil {
		return nil, fmt.Errorf("assets/icons: %q 解析失败：%w", name, err)
	}
	cache[key] = s
	return s, nil
}

// Must 同 SVG，取不到就 panic。图标名是代码里写的常量，写错应当在测试里就红。
func Must(name string, fill bool) *ui.SVG {
	s, err := SVG(name, fill)
	if err != nil {
		panic(err)
	}
	return s
}
