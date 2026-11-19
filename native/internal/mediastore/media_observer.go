package mediastore

import "sync"

// 新增字节回调（规格-运行页 第二节 D 项）：Put 真正把一个新文件落进媒体库时通知登记的观察者，
// 存储统计靠它累加"快照之后新增了多少"，不用重新遍历 13 GB。
// 同哈希已经在、什么都没写的那条路不通知；写失败也不通知。观察者在 Put 的协程里同步调用，要快、不能再调 Put。

// NewFile 是一次新落下的文件
type NewFile struct {
	Data     string // Put 收到的数据目录（原样）
	Platform string
	Bytes    int64
}

var (
	observersMu sync.RWMutex
	observers   = map[int]func(NewFile){}
	observerSeq int
)

// OnNewFile 登记一个观察者，返回撤销函数
func OnNewFile(f func(NewFile)) (cancel func()) {
	observersMu.Lock()
	observerSeq++
	id := observerSeq
	observers[id] = f
	observersMu.Unlock()
	return func() {
		observersMu.Lock()
		delete(observers, id)
		observersMu.Unlock()
	}
}

func notifyNewFile(n NewFile) {
	observersMu.RLock()
	fs := make([]func(NewFile), 0, len(observers))
	for _, f := range observers {
		fs = append(fs, f)
	}
	observersMu.RUnlock()
	for _, f := range fs {
		f(n)
	}
}
