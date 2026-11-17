package archcheck

import (
	"strings"
	"testing"
)

// 架构测试（规格-渠道中间件 第十节 10.5"挡法"，第十六节 B"架构"）：插件只能拿出口，不能拿适配器。
// 原来放在 internal/channel，2026-11-01 搬进 archcheck（ADR-225：合并前只测改了的包，架构测试单独成包、挑上任何包都带上）。
// Go 的 internal 规则只挡 native/ 树外面的代码，挡不住同一棵树下的包直接 import 适配器（internal/channel/snowluma），所以靠这个测试：
// 谁绕过出口去引了适配器，这里就红。
//
// 两条：
//  1. 只有装配（internal/app）、桌面程序的入口（模块根）和 cmd/ 下的命令能依赖适配器，其余所有包都不能（原来写的是
//     "channel 下面没有包依赖 internal/onebot"，适配器改名搬进 channel/snowluma 以后那条一直在查一个不存在的包）；
//  2. 插件包（repeat、qq）在这棵树里只依赖 types；复读另可读中立的 audience 听众类型（规格-QQ发送上线 6.2）。
//     引了中间件本身（channel）就能摸到出口的实现，引了 store 就能绕过出口写库，都不行。
//     audience 不引项目里的其他包，只表达听众范围，不提供执行能力。

// adapterPkg 是 QQ 适配器。改名、搬家时这里一起改：测试先确认它在，不在就红，不会悄悄变成什么都没查。
const adapterPkg = "sumika-native/internal/channel/snowluma"

// mayUseAdapter 是能依赖适配器的包：装配、桌面程序的入口、命令行工具，以及适配器自己。新加的包默认不能。
func mayUseAdapter(pkg string) bool {
	return pkg == "sumika-native" || pkg == "sumika-native/internal/app" || strings.HasPrefix(pkg, "sumika-native/cmd/") ||
		pkg == adapterPkg || strings.HasPrefix(pkg, adapterPkg+"/")
}

func TestOnlyAssemblyReachesTheAdapter(t *testing.T) {
	sawAdapter, sawPlugin := false, false
	for pkg, info := range moduleInfo(t) {
		f := append([]string{pkg}, info.Deps...) // 包和它的全部传递依赖（不含测试文件的 import）
		if len(f) == 0 {
			continue
		}
		pkg := f[0]
		switch pkg {
		case adapterPkg:
			sawAdapter = true
		case "sumika-native/internal/channel/repeat":
			sawPlugin = true
		}
		if mayUseAdapter(pkg) {
			continue
		}
		for _, d := range f[1:] {
			if d == adapterPkg || strings.HasPrefix(d, adapterPkg+"/") {
				t.Errorf("%s 依赖了适配器 %s：只有装配（internal/app）和入口能接适配器，插件、中间件、内核只能拿出口", pkg, d)
			}
		}
	}
	if !sawAdapter {
		t.Fatalf("go list 的结果里没有适配器 %s：改名或搬家了就把 adapterPkg 一起改，不然这个测试什么也没查", adapterPkg)
	}
	if !sawPlugin {
		t.Fatalf("go list 的结果里连复读插件都没有，这个测试什么也没查")
	}
}

func TestPluginPackagesOnlyDependOnTypes(t *testing.T) {
	for _, pkg := range []string{"repeat", "qq"} {
		self := "sumika-native/internal/channel/" + pkg
		sawSelf := false
		for _, d := range depsOf(t, "./internal/channel/"+pkg) {
			if d == self {
				sawSelf = true
				continue
			}
			if pkg == "repeat" && d == "sumika-native/internal/audience" {
				continue
			}
			if strings.HasPrefix(d, "sumika-native/") && d != "sumika-native/internal/channel/types" {
				t.Errorf("插件包 %s 依赖了 %s：它只许依赖 types", pkg, d)
			}
		}
		if !sawSelf {
			t.Fatalf("go list 的结果里没有 %s 自己，这个测试什么也没查", self)
		}
	}
}
