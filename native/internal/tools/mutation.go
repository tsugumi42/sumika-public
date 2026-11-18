package tools

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"sumika-native/internal/approval"
)

// 改文件排队（pi 的 file-mutation-queue.js）：同一个文件的 write / edit 一个一个来，不管是不是同一个会话、同一个角色——
// 两个角色同时改一个文件，不会各自读到旧内容再先后覆盖、丢掉一次修改。不同的文件照样同时改。
// 键是真实路径（跟着联接、符号链接走；文件还不在就用最近一级在的上级拼回去，Node 是直接用解析后的路径），Windows 上不分大小写
var (
	mutMu     sync.Mutex
	mutQueues = map[string]*fileQueue{}
)

type fileQueue struct {
	mu    sync.Mutex
	users int
}

func mutationKey(abs string) string {
	return strings.ToLower(filepath.Clean(approval.RealOf(abs)))
}

func withFileMutation(abs string, fn func() (string, error)) (string, error) {
	key := mutationKey(abs)
	mutMu.Lock()
	q := mutQueues[key]
	if q == nil {
		q = &fileQueue{}
		mutQueues[key] = q
	}
	q.users++
	mutMu.Unlock()
	q.mu.Lock()
	defer func() {
		q.mu.Unlock()
		mutMu.Lock()
		if q.users--; q.users == 0 {
			delete(mutQueues, key)
		}
		mutMu.Unlock()
	}()
	return fn()
}

// errAborted 是 write / edit 中途发现被停下（write.js、edit.js 的 throwIfAborted）：每做完一步看一次，
// 写完了才发现被停下也报这句（文件已经写了）
var errAborted = errors.New("Operation aborted")

func aborted(ctx context.Context) error {
	if ctx.Err() != nil {
		return errAborted
	}
	return nil
}
