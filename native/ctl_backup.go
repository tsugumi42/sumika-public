package main

// sumika-ctl backup now|list（规格-数据目录与备份 10.7）接进桌面程序：备份服务在后台那个 App 里（archive 和接收两种模式都有），
// 调度和命令行共用同一个 Service、同一把锁。backup now 不碰窗口，只读写备份目录。

import (
	"context"
	"errors"
	"time"

	backend "sumika-native/internal/app"
	"sumika-native/internal/backup"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.BackupBackend = (*ctlBackend)(nil)

// backupApp 取当前开着的后台（archive 或接收）；还没开好回失败。只在取引用时持 op，不在备份期间持着，不卡住宿主开关
func (b *ctlBackend) backupApp() (*backend.App, error) {
	return b.currentApp("后台还没开好，没法备份")
}

// currentApp 取当前开着的后台（archive 或接收）；还没开好时用 notReady 说明这次要做什么。
// 只在取引用时持 op，用后台的时候不持着，不卡住宿主开关。
func (b *ctlBackend) currentApp(notReady string) (*backend.App, error) {
	h, err := b.host()
	if err != nil {
		return nil, err
	}
	h.op.Lock()
	app := h.cur
	h.op.Unlock()
	if app == nil {
		return nil, ctlproto.Errorf(ctlproto.CodeFailed, notReady)
	}
	return app, nil
}

func backupErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrBusy):
		return ctlproto.Errorf(ctlproto.CodeFailed, "已经有备份、恢复或抽验在做（每天自动的、别的手动的、每周抽验，同一时刻只做一件），等它做完再试")
	case errors.Is(err, backup.ErrClosed):
		return ctlproto.Errorf(ctlproto.CodeFailed, "桌面程序正在退出")
	case errors.Is(err, backup.ErrDirInsideData):
		return ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
	case errors.Is(err, backup.ErrBadBackupName), errors.Is(err, backup.ErrBackupNotFound), errors.Is(err, backup.ErrTargetUnsafe):
		return ctlproto.Errorf(ctlproto.CodeInvalid, err.Error()) // 参数不允许：重试没用，退出码 3
	case errors.Is(err, backup.ErrNoDrillBackup):
		return ctlproto.Errorf(ctlproto.CodeFailed, "备份目录里还没有一份校验通过的备份可以抽验；先 sumika-ctl backup now")
	}
	return ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
}

func (b *ctlBackend) BackupNow(ctx context.Context, media string, wait time.Duration) (ctlproto.BackupNow, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.BackupNow{}, err
	}
	out, err := app.BackupNow(ctx, media, wait)
	return out, backupErr(err)
}

func (b *ctlBackend) BackupRestore(ctx context.Context, name, to string, wait time.Duration) (ctlproto.BackupRestored, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.BackupRestored{}, err
	}
	out, err := app.BackupRestore(ctx, name, to, wait)
	return out, backupErr(err)
}

func (b *ctlBackend) BackupVerify(ctx context.Context, name string, wait time.Duration) (ctlproto.BackupVerified, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.BackupVerified{}, err
	}
	out, err := app.BackupVerify(ctx, name, wait)
	return out, backupErr(err)
}

func (b *ctlBackend) BackupList(ctx context.Context) (ctlproto.BackupList, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.BackupList{}, err
	}
	out, err := app.BackupList(ctx)
	return out, backupErr(err)
}
