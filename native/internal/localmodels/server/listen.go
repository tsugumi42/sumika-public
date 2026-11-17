package server

import (
	"errors"
	"syscall"
)

// isPortProblem 判断监听失败是不是端口的问题（被占，或者没有权限绑定）：这种退出码 3，Local 换一个口立即重试。
// 别的监听错误（地址写错之类）是配置错误，退出码 2。
//
// 用 errors.Is 看错误码的类型，不看文字（Windows 的中文系统上文字不一样）。Windows 上有个坑：
// syscall.EADDRINUSE 是 Go 自己编的占位值，和 Winsock 真正返回的 WSAEADDRINUSE（10048）不相等，
// errors.Is(err, syscall.EADDRINUSE) 对真实的端口占用错误不成立，所以两套码都认。
func isPortProblem(err error) bool {
	for _, target := range portErrnos {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// portErrnos 是各系统上"端口被占 / 没权限绑"的错误码。
var portErrnos = []error{
	syscall.EADDRINUSE,   // Linux / macOS 的真值；Windows 上是占位值
	syscall.EACCES,       // 同上
	syscall.Errno(10048), // WSAEADDRINUSE：Windows 上端口已被占用
	syscall.Errno(10013), // WSAEACCES：Windows 上端口被排除（Hyper-V 保留段）或被别的程序独占
}
