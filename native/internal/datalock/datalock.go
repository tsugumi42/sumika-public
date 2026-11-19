// Package datalock 是数据目录的排他锁：Go 后台和只读诊断开库前都先拿 <数据目录>/go/backend.lock；
// 本地私聊的写权另有一把 go/local.lock：Go 后台的 receive 阶段拿（archive 只读，不拿）。
// 老工具（memory-migrate、reply-backfill 等）用 LockExisting / LockExistingForWrite：除了 backend.lock，也要认 local.lock——
// 只认 backend.lock 的工具会和正在写本地的后台同时写 local.db、memory.db。
package datalock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DataLock 从开库前持有到关库后；进程被杀由系统释放，不用 PID 猜进程是否活着。
// 一个 DataLock 可能拿着一把或两把锁（LockExisting 系列），Close 一起放
type DataLock struct{ files []*os.File }

var ErrDataInUse = errors.New("数据目录正在被另一个 Sumika 进程使用，请先关掉它")

// ErrLocalInUse：本地私聊的写锁（local.lock）被另一个进程拿着——多半是另一个在接收的 sumika.exe，或正在写本地的诊断、迁移工具
var ErrLocalInUse = errors.New("本地私聊的写锁正被另一个进程占用（可能是另一个在接收的 Sumika，或正在写本地的工具），请先关掉它")

// BackendLockName 和 LocalLockName 是 <数据目录>/go/ 下两把锁的文件名
const (
	BackendLockName = "backend.lock"
	LocalLockName   = "local.lock"
)

func LockData(data string) (*DataLock, error) {
	f, held, err := lockNamed(data, BackendLockName)
	if held {
		return nil, fmt.Errorf("%w：%v", ErrDataInUse, err)
	}
	if err != nil {
		return nil, err
	}
	return &DataLock{files: []*os.File{f}}, nil
}

// LockLocal 拿本地私聊的写锁（go/local.lock）。别人拿着是 ErrLocalInUse
func LockLocal(data string) (*DataLock, error) {
	f, held, err := lockNamed(data, LocalLockName)
	if held {
		return nil, fmt.Errorf("%w：%v", ErrLocalInUse, err)
	}
	if err != nil {
		return nil, err
	}
	return &DataLock{files: []*os.File{f}}, nil
}

// lockNamed 拿 <数据目录>/go/<name>；held 为真表示文件打开了、锁被别人拿着（err 是系统给的原因）
func lockNamed(data, name string) (f *os.File, held bool, err error) {
	path := filepath.Join(data, "go", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err = lockDataFile(f); err != nil {
		f.Close()
		return nil, true, err
	}
	return f, false, nil
}

// LockExisting 和 LockData 拿同一把锁，但只读打开已有的锁文件：只读诊断不能为了拿锁去建文件。
// 锁文件不在就把打开的错误原样交回；被别人拿着就是 ErrDataInUse（不带系统的原因）。
// go/local.lock 已经存在的话也探一下并拿住：有人（多半是 Bun）正在写本地，诊断和迁移工具不该和它同时碰库，被占是 ErrLocalInUse。
// local.lock 不存在就算没人在写本地，不建它
func LockExisting(data string) (*DataLock, error) {
	l, err := lockExistingBackend(data)
	if err != nil {
		return nil, err
	}
	lf, err := os.Open(filepath.Join(data, "go", LocalLockName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return l, nil
	case err != nil:
		l.Close()
		return nil, err
	}
	if err := lockDataFile(lf); err != nil {
		lf.Close()
		l.Close()
		return nil, ErrLocalInUse
	}
	l.files = append(l.files, lf)
	return l, nil
}

// LockExistingForWrite 给要写 local.db 或 memory.db 的老工具：backend.lock 照 LockExisting（必须已存在，被占是 ErrDataInUse），
// 再把 go/local.lock 也拿住——不存在就建（它要写本地库，而不是只看；建出来也防着之后有人起 Bun 去写），被占是 ErrLocalInUse，拿不到就拒绝
func LockExistingForWrite(data string) (*DataLock, error) {
	l, err := lockExistingBackend(data)
	if err != nil {
		return nil, err
	}
	lf, held, err := lockNamed(data, LocalLockName)
	if held {
		l.Close()
		return nil, fmt.Errorf("%w：%v", ErrLocalInUse, err)
	}
	if err != nil {
		l.Close()
		return nil, err
	}
	l.files = append(l.files, lf)
	return l, nil
}

// lockExistingBackend：只读打开已有的 backend.lock 并拿住
func lockExistingBackend(data string) (*DataLock, error) {
	f, err := os.Open(filepath.Join(data, "go", BackendLockName))
	if err != nil {
		return nil, err
	}
	if err := lockDataFile(f); err != nil {
		f.Close()
		return nil, ErrDataInUse
	}
	return &DataLock{files: []*os.File{f}}, nil
}

func (l *DataLock) Close() error {
	if l == nil {
		return nil
	}
	files := l.files
	l.files = nil
	var errs []error
	for _, f := range files {
		errs = append(errs, unlockDataFile(f), f.Close())
	}
	return errors.Join(errs...)
}
