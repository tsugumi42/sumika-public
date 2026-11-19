package app

import "errors"

// closeSet 先收备份、自检、环境快照这些也开着库的东西，再关库
func (a *App) closeSet() error {
	a.closeBackup()    // 先停备份：取消正在做的那份，之后才关库
	a.closeSelfcheck() // 再收自检：它的只读连接也在这个库目录里，要在关库前关掉
	a.closeEnvInputs() // 环境快照的运行部分也有只读连接，同样要在关库前关掉
	return a.set.Close()
}

// closeLocks 放两把锁（archive 没拿 local.lock，Close 对 nil 是空操作）
func (a *App) closeLocks() error {
	return errors.Join(a.dataLock.Close(), a.localLock.Close())
}
