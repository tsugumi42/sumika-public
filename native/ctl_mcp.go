package main

// sumika-ctl mcp status|release（质检 X-08，后续路线 P0-3）接进桌面程序：MCP 客户端在后台那个 App 里，
// 命令行看到、解除的就是正在用的那一份。解除只在这里：模型能调的工具清单里没有它（app 包的测试遍历工具清单守着）。

import (
	"context"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.MCPBackend = (*ctlBackend)(nil)

func (b *ctlBackend) MCPStatus(ctx context.Context) (ctlproto.MCPStatus, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.MCPStatus{}, err
	}
	return app.MCPStatus(ctx)
}

func (b *ctlBackend) MCPReconnect(ctx context.Context, server string) (ctlproto.MCPReconnected, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.MCPReconnected{Server: server}, err
	}
	return app.MCPReconnect(ctx, server)
}

func (b *ctlBackend) MCPRelease(ctx context.Context, server, reason string) (ctlproto.MCPReleased, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.MCPReleased{Server: server}, err
	}
	return app.MCPRelease(ctx, server, reason)
}
