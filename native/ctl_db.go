package main

// sumika-ctl db status 接进桌面程序：库是后台打开的，版本、状态和快照在它打开时记下了。
// 这个命令只读，不重新开库；不可用平台的解除办法是换上能读的版本或修好问题后重启。

import (
	"context"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.DBBackend = (*ctlBackend)(nil)

func (b *ctlBackend) DBStatus(ctx context.Context) (ctlproto.DBStatus, error) {
	app, err := b.backupApp()
	if err != nil {
		return ctlproto.DBStatus{}, err
	}
	return app.DBStatus(ctx)
}
