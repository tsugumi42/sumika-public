package main

// 崩溃记录：-H windowsgui 编出来的程序没有控制台，Go 运行时崩溃（没接住的 panic、fatal error）时打到 stderr 的出错信息和调用栈
// 一行都留不下。2026-11-12 19:37 前后桌面程序就这样无声地没了，事后查不出死在哪。
// 这里用 runtime/debug.SetCrashOutput 把它另外写一份到 <数据目录>/logs/crash.log（追加写，没崩过就是空文件）。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// crashLogEnabled 只在真正的程序入口 runMain 里打开：崩溃输出管的是整个进程，文件会一直开着；
// 测试直接调 openDeskLog 时若也打开，Windows 上 t.TempDir 收尾删不掉 crash.log（11-13 队列里三条桌面日志测试因此红）。
var crashLogEnabled bool

// crashLogName 是崩溃记录的文件名，放在 logs/ 下，和 desk-<日期>.log 挨着
const crashLogName = "crash.log"

// openCrashLog 在 dataDir/logs 已经存在时打开 crash.log 并交给运行时；logs/ 还没建（数据目录没初始化）就不建、返回 nil。
// 每次启动先写一行启动标记，崩溃时运行时写的调用栈就落在它后面，能看出是哪一次启动崩的。
func openCrashLog(dataDir string, now time.Time) (*os.File, error) {
	logs := filepath.Join(dataDir, "logs")
	if st, err := os.Stat(logs); err != nil || !st.IsDir() {
		return nil, nil
	}
	f, err := os.OpenFile(filepath.Join(logs, crashLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return nil, err
	}
	fmt.Fprintf(f, "—— 桌面程序启动 %s，进程 %d ——\n", now.Format("2006-01-02 15:04:05"), os.Getpid())
	return f, nil
}
