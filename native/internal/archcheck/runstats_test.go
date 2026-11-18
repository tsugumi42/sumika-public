package archcheck

import "testing"

// 架构测试（规格-运行页 第十四节）：运行页的统计服务 internal/runstats 只读运行记录库，直接导入里没有 各插件和模块的实现，
// 以后资源、队列这些读数一律走注册表（runmetrics），页面只认注册表。先确认这些包都在、runstats 自己也在：
// 改名或搬家了，测试不能什么也没查就过。
var runstatsForbidden = []string{
	"app", "turn", "channel", "memplugin", "linkplugin", "mcpplugin", "linkexpand", "mediafetch", "tools",
	"approval", "advisor", "embed", "localmodels", "policy", "mcpclient", "modelcall", "ctlserver",
}

func TestRunstatsDoesNotImportPluginImplementations(t *testing.T) {
	all := allPackages(t)
	const self = "sumika-native/internal/runstats"
	if !all[self] {
		t.Fatalf("模块里没有 %s：改名或搬家了就把 archcheck/runstats_test.go 一起改", self)
	}
	forbidden := map[string]bool{}
	for _, name := range runstatsForbidden {
		pkg := "sumika-native/internal/" + name
		if !all[pkg] {
			t.Fatalf("模块里没有 %s，这个测试查不到它：改名或搬家了就改 runstatsForbidden", pkg)
		}
		forbidden[pkg] = true
	}
	// 只查直接导入：store 等基础包自己往下依赖什么不归这条管；runstats 自己不许去引实现
	direct := moduleInfo(t)[self].Imports
	sawStore := false
	for _, d := range direct {
		if d == "sumika-native/internal/store" {
			sawStore = true
		}
		if forbidden[d] {
			t.Errorf("runstats 直接导入了 %s：统计服务只读运行记录库，不 import 插件和模块的实现", d)
		}
	}
	if !sawStore {
		t.Fatalf("runstats 的直接导入里应有 internal/store（读库用），没看到说明这个测试没查到东西：%v", direct)
	}
}

// 资源标签用到的三个包（登记簿 procreg、队列注册表 runmetrics、采样器 resstat）也是叶子：
// 起进程的地方和各队列在装配处往它们里面登记，它们自己不 import 任何插件和模块的实现，否则登记反过来依赖实现，成了环。
func TestResourcePackagesStayLeaves(t *testing.T) {
	all := allPackages(t)
	info := moduleInfo(t)
	forbidden := map[string]bool{}
	for _, name := range runstatsForbidden {
		forbidden["sumika-native/internal/"+name] = true
	}
	for _, name := range []string{"procreg", "runmetrics", "resstat"} {
		pkg := "sumika-native/internal/" + name
		if !all[pkg] {
			t.Fatalf("模块里没有 %s：改名或搬家了就改这个测试", pkg)
		}
		for _, d := range info[pkg].Imports {
			if forbidden[d] {
				t.Errorf("%s 直接导入了 %s：登记簿、注册表、采样器不 import 插件和模块的实现", pkg, d)
			}
		}
	}
	// 反过来：起进程的地方确实登记了（改名或删掉登记，这条先红，不是进程表悄悄少一类）
	for name, want := range map[string]string{"embed": "procreg", "mcpclient": "procreg", "policy": "procreg", "app": "runmetrics"} {
		pkg := "sumika-native/internal/" + name
		found := false
		for _, d := range info[pkg].Imports {
			if d == "sumika-native/internal/"+want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 该 import %s（运行页资源标签的登记）", pkg, want)
		}
	}
}
