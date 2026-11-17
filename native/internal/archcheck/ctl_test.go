package archcheck

import (
	"strings"
	"testing"
)

// 架构测试（规格-本机控制接口 第五节第 6 条）：命令行客户端 sumika-ctl 只依赖协议包 internal/ctlproto，不链接后台。
// 客户端要小、要快，每次 Agent 调它都是起一个新进程；它要是依赖了 internal/app、store、config 这些，就把整个后台链接进来了，
// 而且客户端和后台各管各的事这条线也就断了（后台那边的改动会牵动客户端）。
// 查的是依赖里 sumika-native/ 开头的包（含传递依赖，不含测试文件的 import：e2e 测试会引进服务端）；协议包自己也不许引项目里的别的包。
// 改名、搬家时 ctlSelf、ctlProtoPkg 一起改：测试先确认它们都在，不然什么也没查。
const (
	ctlSelf     = "sumika-native/cmd/sumika-ctl"
	ctlProtoPkg = "sumika-native/internal/ctlproto"
)

func TestCtlClientOnlyDependsOnProtocol(t *testing.T) {
	sawSelf, sawProto := false, false
	for _, d := range depsOf(t, "./cmd/sumika-ctl") {
		switch {
		case d == ctlSelf:
			sawSelf = true
		case d == ctlProtoPkg:
			sawProto = true
		case strings.HasPrefix(d, "sumika-native/"):
			t.Errorf("命令行客户端 %s 依赖了 %s：它只许依赖协议包 %s（不链接后台，要小要快）", ctlSelf, d, ctlProtoPkg)
		}
	}
	if !sawSelf {
		t.Fatalf("go list 的结果里没有 %s 自己，这个测试什么也没查", ctlSelf)
	}
	if !sawProto {
		t.Fatalf("go list 的结果里没有协议包 %s：客户端不依赖协议包就没法说话，改名或搬家了就把 ctlProtoPkg 一起改", ctlProtoPkg)
	}
}

// 服务端不许反过来被客户端引到（服务端依赖 go-winio 的监听、校验逻辑），上一个测试已经覆盖；这里再单独确认两个发出去的程序的分工：
// 桌面程序（模块根）链接服务端，客户端不链接
func TestOnlyDesktopLinksTheCtlServer(t *testing.T) {
	const server = "sumika-native/internal/ctlserver"
	has := func(pkg string) bool {
		for _, d := range depsOf(t, pkg) {
			if d == server {
				return true
			}
		}
		return false
	}
	if !has(".") {
		t.Fatalf("桌面程序没有链接 %s：控制接口没接进去，或者服务端改名了（改名时把 server 常量一起改）", server)
	}
	for _, p := range []string{"./cmd/sumika-ctl", "./cmd/sumika-go"} {
		if has(p) {
			t.Errorf("%s 链接了控制接口的服务端 %s：服务端只属于桌面程序", p, server)
		}
	}
}
