package tools

// write 的外部改动检查（规格-读文件和文档第五节）：记下“这个会话读过或写过的文件是什么样”，
// 写之前对一下，变了就不写，让模型先重读。没读过的不拦；记录丢了只是回到“没读过”。

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"sumika-native/internal/kernel"
)

const fileStateCap = 4096 // 记录条数上限，满了丢最早的

type fileVersion struct {
	size int64
	mod  time.Time
}

var fileState = struct {
	sync.Mutex
	m     map[string]fileVersion
	order []string
}{m: map[string]fileVersion{}}

// fileStateKey 是“角色 × 会话 × 真实路径”。不同会话互相算外部：另一个角色或子代理写了，这个会话下次写前要重读。
// 没有 Invocation（评测、命令行程序）时 Scope 是空的，空的也照记。
func fileStateKey(ctx context.Context, abs string) string {
	s := kernel.InvocationFromContext(ctx).Scope
	return s.CharacterID + "\x00" + s.Conversation + "\x00" + s.ScopeID + "\x00" + mutationKey(abs)
}

func rememberFile(ctx context.Context, abs string, fi os.FileInfo) {
	if fi == nil || fi.IsDir() {
		return
	}
	k := fileStateKey(ctx, abs)
	fileState.Lock()
	defer fileState.Unlock()
	if _, ok := fileState.m[k]; !ok {
		if len(fileState.order) >= fileStateCap {
			delete(fileState.m, fileState.order[0])
			fileState.order = fileState.order[1:]
		}
		fileState.order = append(fileState.order, k)
	}
	fileState.m[k] = fileVersion{fi.Size(), fi.ModTime()}
}

// rememberFileAt 在写完以后按磁盘上的样子记。
func rememberFileAt(ctx context.Context, abs string) {
	if fi, err := os.Stat(abs); err == nil {
		rememberFile(ctx, abs, fi)
	}
}

// checkNotChanged 在 write 的队列锁里、写入之前调用：读过或写过之后文件被别人改了就报错。
// 文件被删了、从没读过，都放行。只比大小和修改时间，不算哈希：大文件不该为这个再读一遍；
// 只 touch 了一下的假阳性，代价是多读一次。
func checkNotChanged(ctx context.Context, abs, shown string) error {
	k := fileStateKey(ctx, abs)
	fileState.Lock()
	old, ok := fileState.m[k]
	fileState.Unlock()
	if !ok {
		return nil
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return nil
	}
	if fi.Size() == old.size && fi.ModTime().Equal(old.mod) {
		return nil
	}
	const layout = "2006-01-02 15:04:05"
	return fmt.Errorf("%s 在你上次读或写之后被改过（当时 %s、%s，现在 %s、%s），没有写入。先用 read 重读一遍，再基于最新内容写。",
		shown, formatSize(int(old.size)), old.mod.Local().Format(layout), formatSize(int(fi.Size())), fi.ModTime().Local().Format(layout))
}
