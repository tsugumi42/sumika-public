package archcheck

import "testing"

// 架构测试（规格-记忆页 3.1）：记忆页的读接口只给桌面窗口。对话循环（turn）、渠道中间件（channel）、记忆插件（memplugin）、
// 工具（tools）、本机控制接口的服务端（ctlserver）都不许依赖读服务 internal/memview，也不许依赖放着记忆读接口类型的 internal/api：
// 页面读的是"库里全部的记忆，含撤销的、各场合的"，不套"她在某个场合能想起什么"，这些地方一拿到就越过了谁能看什么。
// Go 的依赖图只到包，分不出 api 里的哪个类型；这五个包今天都不依赖 api，所以整个 api 一起禁，哪天某个包真要用 api，
// 先把记忆的类型拆到自己的包里，再改这个测试。
// 先确认这五个包都在、memview 和 api 也在：任何一个改名或搬家了，测试不能什么也没查就过。
const (
	memviewPkg = "sumika-native/internal/memview"
	apiPkg     = "sumika-native/internal/api"
)

var memviewForbiddenIn = []string{"turn", "channel", "memplugin", "tools", "ctlserver"}

func TestMemoryReadersOnlyForTheDesktop(t *testing.T) {
	all := allPackages(t)
	for _, p := range []string{memviewPkg, apiPkg} {
		if !all[p] {
			t.Fatalf("模块里没有 %s：改名或搬家了就把 archcheck/memview_test.go 里的包名一起改", p)
		}
	}
	for _, name := range memviewForbiddenIn {
		pkg := "sumika-native/internal/" + name
		if !all[pkg] {
			t.Fatalf("模块里没有 %s，这个测试什么也没查：改名或搬家了就改 memviewForbiddenIn", pkg)
		}
		sawSelf := false
		for _, d := range depsOf(t, "./internal/"+name) {
			switch {
			case d == pkg:
				sawSelf = true
			case d == memviewPkg:
				t.Errorf("%s 依赖了记忆页的读服务 %s：它只属于桌面窗口（规格-记忆页 3.1）", pkg, memviewPkg)
			case d == apiPkg:
				t.Errorf("%s 依赖了 %s：里面有记忆页的读接口类型，只给桌面窗口", pkg, apiPkg)
			}
		}
		if !sawSelf {
			t.Fatalf("go list 的结果里没有 %s 自己，这一项什么也没查", pkg)
		}
	}
}

// 桌面程序（模块根）链接记忆页的读服务；命令行客户端 sumika-ctl 不链接（它只依赖协议包）。
// sumika-go（命令行后台）经 internal/app 带着读服务，但不把它交给任何人：Desktop 接口只有桌面入口去取，
// 谁能碰它由上一个测试里那五个包的依赖约束
func TestDesktopLinksMemviewAndCtlClientDoesNot(t *testing.T) {
	has := func(pkg string) bool {
		for _, d := range depsOf(t, pkg) {
			if d == memviewPkg {
				return true
			}
		}
		return false
	}
	if !has(".") {
		t.Errorf("桌面程序没有链接 %s：装配没接上，或者读服务改名了（改名时把 memviewPkg 一起改）", memviewPkg)
	}
	for _, p := range []string{"./cmd/sumika-ctl"} {
		if has(p) {
			t.Errorf("%s 链接了记忆页的读服务 %s：命令行客户端只依赖协议包", p, memviewPkg)
		}
	}
}
