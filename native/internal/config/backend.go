package config

import "fmt"

// 本地私聊由哪个后台管（规格-本地私聊切到Bun 第二节）。Go 和 Bun 都读这一项，它是唯一的事实来源；锁只防同时写。
const (
	LocalByGo  = "go"  // 默认：本地私聊、本地群聊、悄悄话、提醒等由 Go 后台管
	LocalByBun = "bun" // 本地私聊交给 Bun 子进程；QQ 仍由 Go 管
)

// Backend 是 backend 段：只在启动时读，中途改了要重启 sumika.exe
type Backend struct {
	// Local 是本地私聊交给谁：go（默认）或 bun
	Local string `yaml:"local"`
	// Bun 是 Bun 可执行文件；空 = 自动找（PATH 里的 bun，再找 %USERPROFILE%\.bun\bin\bun.exe）。只在 local 为 bun 时用
	Bun string `yaml:"bun"`
	// Server 是 server/ 目录；空 = 资源根下的 server/。只在 local 为 bun 时用
	Server string `yaml:"server"`
}

// DefaultBackend 是默认值：本地私聊留在 Go
func DefaultBackend() Backend { return Backend{Local: LocalByGo} }

// LocalOwnedByBun 说本地私聊是不是交给了 Bun
func (b Backend) LocalOwnedByBun() bool { return b.Local == LocalByBun }

func (b Backend) check() []string {
	if b.Local != LocalByGo && b.Local != LocalByBun {
		return []string{fmt.Sprintf("backend.local：%q 不是 go / bun", b.Local)}
	}
	return nil
}
