package mcpclient

import "strings"

// bridgePrograms 是“进程在本机、真正干活的地方在别处”的程序名（不带路径和 .exe，小写）：
// 把 MCP 转给远端服务的桥（mcp-remote、supergateway、mcp-proxy）、跑在容器或别的系统里的（docker、podman、nerdctl、kubectl、wsl）、
// 登到别的机器上的（ssh、plink）。它们的进程树没了，对方可能仍在执行已经交出去的调用，
// 所以留痕把它们记成 "stdio-remote"：启动收尾不自动当作“已终止”，按远端对待（改 unknown，等主人解除）。
// 判据：服务配置的 command 或 args 里任何一项的程序名在这个名单里。认不出的照本机处理——这是往严的方向补，不求穷举；
// 在线路径（超时后关进程树再记已终止）有同样的前提，规格 2.4 已认可，这里不动它。
var bridgePrograms = map[string]bool{
	"mcp-remote": true, "supergateway": true, "mcp-proxy": true,
	"docker": true, "podman": true, "nerdctl": true, "kubectl": true, "wsl": true,
	"ssh": true, "plink": true,
}

// programName 取一项命令行参数的程序名：去掉路径、版本后缀（mcp-remote@latest）和 .exe/.cmd/.bat/.ps1，小写
func programName(arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if i := strings.LastIndexAny(arg, "\\/"); i >= 0 {
		arg = arg[i+1:]
	}
	if i := strings.Index(arg, "@"); i > 0 {
		arg = arg[:i]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		arg = strings.TrimSuffix(arg, ext)
	}
	return arg
}

// bridgesElsewhere 是这份本机服务配置是不是把调用转到别处（见 bridgePrograms）
func bridgesElsewhere(cfg ServerConfig) bool {
	if bridgePrograms[programName(cfg.Command)] {
		return true
	}
	for _, a := range cfg.Args {
		if bridgePrograms[programName(a)] {
			return true
		}
	}
	return false
}

// dispatchTransport 是写进调用留痕的传输种类：http、stdio（副作用就在本机进程树里）、stdio-remote（本机进程但转给别处）
func dispatchTransport(cfg ServerConfig) string {
	switch {
	case cfg.Type == "http":
		return "http"
	case bridgesElsewhere(cfg):
		return "stdio-remote"
	}
	return "stdio"
}
