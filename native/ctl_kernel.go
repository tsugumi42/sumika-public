package main

// sumika-ctl kernel faults|retry（质检 X-07）接进桌面程序：回合内核在后台那个 App 里，命令行和界面里的“重试”走同一个 Hub 方法。
// 这条命令只经本机管道，不注册成模型能调的工具。

import (
	"context"
	"errors"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.KernelBackend = (*ctlBackend)(nil)

// kernelApp 取当前开着的后台；还没开好回失败
func (b *ctlBackend) kernelApp() (*backend.App, error) {
	h, err := b.host()
	if err != nil {
		return nil, err
	}
	h.op.Lock()
	app := h.cur
	h.op.Unlock()
	if app == nil {
		return nil, ctlproto.Errorf(ctlproto.CodeFailed, "后台还没开好，内核没有在运行")
	}
	return app, nil
}

func kernelErr(err error) error {
	var ce *ctlproto.Error
	switch {
	case err == nil, errors.As(err, &ce):
		return err
	case errors.Is(err, backend.ErrNoKernel):
		return ctlproto.Errorf(ctlproto.CodeFailed, "后台的回合内核还没准备好")
	}
	return ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
}

func (b *ctlBackend) KernelFaults(ctx context.Context) (ctlproto.KernelFaults, error) {
	app, err := b.kernelApp()
	if err != nil {
		return ctlproto.KernelFaults{}, err
	}
	out, err := app.KernelFaults(ctx)
	return out, kernelErr(err)
}

func (b *ctlBackend) KernelRetry(ctx context.Context, character, conversation string) (ctlproto.KernelRetry, error) {
	app, err := b.kernelApp()
	if err != nil {
		return ctlproto.KernelRetry{}, err
	}
	out, err := app.KernelRetry(ctx, character, conversation)
	return out, kernelErr(err)
}
