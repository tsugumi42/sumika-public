package archcheck

import (
	"strings"
	"testing"
)

// 规格-Go本地模型 2.2、GM2"禁止的偷懒写法"：第三方的 ONNX Runtime 绑定只在 internal/localmodels/ort 一个包里出现，
// 包外一律走它自己的小接口（Runtime、Session），不许直接 import github.com/younland/onnxruntime。
// 这样绑定换了（比如换成 cgo 版）只改一个包；也不会有人绕过那一包的几道关（绝对路径、哈希先于 LoadLibrary、版本钉死）自己去加载动态库。
// 测试文件里的 import 也算：测试里直接调绑定，同样绕过了那几道关。

const (
	ortBinding = "github.com/younland/onnxruntime"
	ortWrapper = "sumika-native/internal/localmodels/ort"
)

func TestOnlyOrtPackageImportsTheBinding(t *testing.T) {
	sawWrapper := false
	for pkg, info := range moduleInfo(t) { // 带 -e：某个包编不过也列出来，不让它悄悄消失
		uses := false
		for _, group := range [][]string{info.Imports, info.TestImports, info.XTestImports} {
			for _, imp := range group {
				if imp == ortBinding || strings.HasPrefix(imp, ortBinding+"/") {
					uses = true
				}
			}
		}
		if pkg == ortWrapper {
			sawWrapper = uses
			continue
		}
		if uses {
			t.Errorf("%s 直接 import 了 %s：只有 %s 能 import 它，别的包用那一包的 Runtime、Session", pkg, ortBinding, ortWrapper)
		}
	}
	if !sawWrapper {
		t.Fatalf("%s 里没找到对 %s 的 import：包改名、搬家了就把这里一起改，不然这个测试什么也没查", ortWrapper, ortBinding)
	}
}
