package archcheck

import (
	"slices"
	"strings"
	"testing"
)

// 素材（图标、表情、字体）给插件用的唯一路径是插件服务 "assets"（internal/assetsvc 登记，turn.AssetsOf 取）：
// 凡接了插件契约（import internal/turn）的包，不许直接 import internal/assets 下的任何包，免得每个插件各找各的素材目录、各算一遍字体顺序。
// 例外：素材服务自己（assetsvc），以及入口（模块根、cmd/ 下的命令）——它们不是插件。界面库（chatui、linkcard）不接插件契约，直接用素材包。
func TestPluginsDoNotImportAssetsDirectly(t *testing.T) {
	const (
		assetsRoot = "sumika-native/internal/assets"
		turnPkg    = "sumika-native/internal/turn"
		svcPkg     = "sumika-native/internal/assetsvc"
	)
	info := moduleInfo(t)
	svc, ok := info[svcPkg]
	if !ok || !slices.Contains(svc.Imports, turnPkg) || !slices.Contains(svc.Imports, assetsRoot+"/icons") {
		t.Fatalf("go list 里的素材服务 %s 不对（没有、或没同时 import turn 和 assets/icons）：改名或搬家了就把这个测试一起改，不然它什么也没查", svcPkg)
	}
	sawPlugin := false
	for pkg, pi := range info {
		if !strings.HasPrefix(pkg, "sumika-native/internal/") || pkg == svcPkg || pkg == assetsRoot || strings.HasPrefix(pkg, assetsRoot+"/") {
			continue
		}
		if !slices.Contains(pi.Imports, turnPkg) {
			continue
		}
		sawPlugin = true
		for _, imp := range pi.Imports {
			if imp == assetsRoot || strings.HasPrefix(imp, assetsRoot+"/") {
				t.Errorf("%s 接了插件契约（import turn），又直接 import 了 %s：插件要用素材请在清单里 Requires turn.ServiceAssets，用 turn.AssetsOf 取", pkg, imp)
			}
		}
	}
	if !sawPlugin {
		t.Fatal("没有一个包 import turn，这个测试什么也没查")
	}
}
