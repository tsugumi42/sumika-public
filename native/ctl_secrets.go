package main

// sumika-ctl secrets ... 接进桌面程序：密钥库只在后台那个 App 里（规格-数据目录与备份 5.3.3），
// 命令行看到、写的就是正在用的那一份，ctl 自己从不打开密钥文件。演示模式和别的命令一样由
// host() 回 demo；后台还没开好（切换中、退出中）回失败，不碰文件。

import (
	"context"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.SecretsBackend = (*ctlBackend)(nil)

// secretsApp 取当前开着的后台（archive 或接收）。只在取引用时持 op，不在读写密钥期间持着，不卡住宿主开关
func (b *ctlBackend) secretsApp() (*backend.App, error) {
	return b.currentApp("后台还没开好，读不了密钥库")
}

func (b *ctlBackend) SecretsStatus(ctx context.Context) (ctlproto.SecretsStatus, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsStatus{}, err
	}
	return app.SecretsStatus(ctx)
}

func (b *ctlBackend) SecretsList(ctx context.Context) (ctlproto.SecretsList, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsList{}, err
	}
	return app.SecretsList(ctx)
}

func (b *ctlBackend) SecretsSet(ctx context.Context, name, value string) (ctlproto.SecretsWritten, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsWritten{Name: name}, err
	}
	return app.SecretsSet(ctx, name, value)
}

func (b *ctlBackend) SecretsDelete(ctx context.Context, name string) (ctlproto.SecretsWritten, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsWritten{Name: name}, err
	}
	return app.SecretsDelete(ctx, name)
}

func (b *ctlBackend) SecretsSetPassphrase(ctx context.Context, passphrase string) (ctlproto.SecretsPassphrase, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsPassphrase{}, err
	}
	return app.SecretsSetPassphrase(ctx, passphrase)
}

func (b *ctlBackend) SecretsUnlock(ctx context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error) {
	app, err := b.secretsApp()
	if err != nil {
		return ctlproto.SecretsUnlocked{}, err
	}
	return app.SecretsUnlock(ctx, secret, recovery)
}
