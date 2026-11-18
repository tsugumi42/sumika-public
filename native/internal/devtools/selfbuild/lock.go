package selfbuild

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 重编锁用操作系统的文件锁：Windows 是 LockFileEx，别的系统是 flock。
// 锁文件（native/bin/sumika-dev.exe.lock）常驻、从不删除；锁的生命期就是打开它的那个文件句柄，
// 持锁进程被杀、崩溃，系统自动放锁，不用判断“老锁”“死锁”，也就没有认领、放回、pid 复用这些空窗。
// 以前用“创建锁文件 + 写 pid + 按年龄 / pid 清老锁”的做法，认领到活锁再放回之间有空窗，实测两个进程同时持锁（复核 2026-11-18）。
//
// lefthook 的 pre-commit 是并行的，好几个进程会同时发现源码变了；不加锁，它们一起改名、一起编，
// Windows 上有的会因为目标被占用报 Access is denied。
const (
	defaultLockWait = buildTimeout + buildWaitDelay + 30*time.Second // 等锁的上限不能小于一次编译的最长时间，否则别人编得好好的，后面的先报错
	lockPoll        = 50 * time.Millisecond                          // 拿不到就隔这么久再试（非阻塞地试，到期限为止）
)

// acquireLock 取锁：拿不到就每 50 毫秒试一次，到 wait 为止；返回的 release 放锁并关闭文件，不删锁文件。
// 锁里写进持锁进程的 pid，只给超时的提示用（谁占着），不参与任何判断。
func acquireLock(path string, wait time.Duration) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打不开重编锁文件 %s：%w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		ok, lerr := tryLock(f)
		if lerr != nil {
			f.Close()
			return nil, fmt.Errorf("给重编锁文件 %s 加锁失败：%w", path, lerr)
		}
		if ok {
			_ = f.Truncate(0)
			_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
			return func() {
				_ = unlock(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			who := ""
			if b, rerr := os.ReadFile(path); rerr == nil {
				if pid := strings.TrimSpace(string(b)); pid != "" {
					who = "，占着的是 pid " + pid
				}
			}
			f.Close()
			return nil, fmt.Errorf("等别的 sumika-dev 重编等了 %v 还没等到（锁文件 %s%s；它自己退出锁就会放开，确认没有别的重编在跑可以结束那个进程）", wait, path, who)
		}
		time.Sleep(lockPoll)
	}
}
