// Package procreg 是"Sumika 自己起了哪些子进程"的登记簿（规格-运行页 第四节第 2 条）。
// 起进程的地方在 Start 成功后登记（类别、标签、pid），进程退出时撤销；运行页的进程表读它。
// 这是叶子包：不依赖任何插件和模块，也没有协程和定时器，登记只是往一个带锁的表里放一行。
// 标签不放命令行参数和路径；MCP 只写配置里的服务 id。
package procreg

import "sync"

// 类别键和 ctlproto 里的进程类别键一致（resstat 的测试钉住），这里不 import ctlproto 保持叶子
const (
	CatModels   = "models"   // 本地模型（sumika-models，或旧的 Python 嵌入子进程）
	CatAnalyzer = "analyzer" // 命令分析用的常驻 pwsh
	CatMCP      = "mcp"      // MCP 服务
	CatShell    = "shell"    // 运行中的命令
	CatMedia    = "media"    // 媒体工具
)

// Entry 是登记的一行。Created 是进程的创建时间（纳秒数，只拿来核对）；
// 0 表示登记时没取到。采样前用它核对：pid 被系统复用后，创建时间对不上，这一行作废
type Entry struct {
	PID      int
	Category string
	Label    string
	Created  int64
}

// Registry 是一份登记簿；零值可用
type Registry struct {
	// CreatedOf 取进程创建时间；nil 用 CreationTime。只有测试需要换（模拟 pid 被复用）
	CreatedOf func(pid int) int64

	mu sync.Mutex
	m  map[int]Entry
}

// Default 是整个进程共用的登记簿：起进程的地方不方便层层传参，直接登记到它
var Default = &Registry{}

// Register 登记一个刚起来的子进程，返回撤销函数（进程退出时调；可以重复调）。
// 撤销只撤自己登记的那一行：pid 已经被别的进程登记了就不动它
func (r *Registry) Register(category, label string, pid int) (unregister func()) {
	if pid <= 0 {
		return func() {}
	}
	created := CreationTime
	if r.CreatedOf != nil {
		created = r.CreatedOf
	}
	e := Entry{PID: pid, Category: category, Label: label, Created: created(pid)}
	r.mu.Lock()
	if r.m == nil {
		r.m = map[int]Entry{}
	}
	r.m[pid] = e
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if cur, ok := r.m[pid]; ok && cur == e {
			delete(r.m, pid)
		}
	}
}

// Snapshot 取一份当前登记的拷贝，按 pid 无序
func (r *Registry) Snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.m))
	for _, e := range r.m {
		out = append(out, e)
	}
	return out
}

// Register 登记到进程共用的登记簿
func Register(category, label string, pid int) (unregister func()) {
	return Default.Register(category, label, pid)
}
