package policy

import "runtime"

// 下面三个让 ask 规则、会话范围、deny 这批用例在两个系统上都真跑：规则按路径比，
// 工作目录和受保护目录要写成本机解析得了的绝对路径。
// Windows 上还是原来的 D:\Work、D:\Data、Z:\别处，一字不变；别的系统用小写的 /work、/data、/别处
// （策略层的路径比较一律转小写，见 IsInside 的注释；这条在 docs/待办.md 第八节挂着）。

func localAskBase(rules ...UserRule) Env {
	e := askBase(rules...)
	if runtime.GOOS != "windows" {
		e.Workdir, e.ProtectedDirs = "/work", []string{"/data"}
	}
	return e
}

func localProtectedFile() string {
	if runtime.GOOS == "windows" {
		return `D:\Data\x.yml`
	}
	return "/data/x.yml"
}

func localElsewhere() string {
	if runtime.GOOS == "windows" {
		return `Z:\别处`
	}
	return "/别处"
}

// localOtherWorkdir 是和 askBase 的工作目录不同的另一个项目目录（Windows 上还是原来的 D:\Other）
func localOtherWorkdir() string {
	if runtime.GOOS == "windows" {
		return `D:\Other`
	}
	return "/other"
}
