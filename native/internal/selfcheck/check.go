package selfcheck

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// Querier 是自检能拿到的全部"读库"能力：只有查询。*sql.DB 满足它；app 用 store.OpenReadOnlyFile 打开的只读连接传进来
// （mode=ro 加 query_only 两道保险），包里没有 Exec、没有事务，写不了库。
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// State 是一条不变量这一次的判定
type State string

const (
	Ok      State = "ok"
	Bad     State = "bad"
	Unknown State = "unknown" // 查询失败、超时、拿不到数据源：不许记成 ok
	Skip    State = "skip"    // 这一次没法判（比如不在静默点）：保持上一次的状态，不算 ok
)

// Result 是一次判定。Example 只放 id、时间、通道号这类定位用的字段，不放消息文字；写进文件前由 Runner 过一遍打码
type Result struct {
	State     State
	Value     int64
	Threshold int64
	Example   map[string]string
	Note      string
	Next      string // 下一步：能给命令就给命令；和 Note 分开，命令行和界面才看得到
}

// Check 是一条不变量。实现可以带自己的状态（比如"连续两次评估都不为零才报"），Runner 串行调它，不会并发
type Check interface {
	ID() string   // 稳定的英文键，比如 intake.unaccounted
	Name() string // 中文名，写进日志和文件
	Eval(ctx context.Context, e *Env) Result
}

// Env 是一次评估时检查能看到的东西
type Env struct {
	Now    time.Time
	Live   *Counters                 // 进程内的累计量（守恒式用：两边同一个进程，重启时一起归零）
	Day    map[string]int64          // 当天的累计量（含重启前文件里续上的，阈值按它判）
	Trace  Querier                   // 共用的运行记录库（只读）；nil = 没接
	Events func() map[string]Querier // 各平台聊天库（只读），平台名 → 连接；nil = 没接
	// Running 是进程内正在跑的回合 id（含内部运行）；nil = 没接
	Running func() []string
	// Pending 是入库队列里排着、还没做完的活的个数；nil = 没接。S1 用它判静默点：只看“最近交来的时间”，
	// 拦截链、闸门卡住队列时后面排着的消息会被当成丢了
	Pending func() int
	// Backup 是备份调度器缓存的状态（S9）；nil = 没接
	Backup func() BackupState
	// Drill 是备份抽验的状态（S12）；nil = 没接
	Drill func() DrillState
	// Faults 是回合内核现在没解除的故障（停止受理的会话或整个内核）；nil = 没接
	Faults func() []KernelFault
	// StrayEnds 是没人认领的活动退出记录条数（不属于任何会话的故障，不挡受理）；nil = 没接，按没有算
	StrayEnds func() int
	// MCP 是现在被隔离、或断了没重连上的 MCP 服务（S11）；nil = 没接；没有 MCP 或一切正常返回空
	MCP func(ctx context.Context) ([]MCPIsolation, error)
	// Embed 是本机嵌入现在的状态（S13）；nil = 没接
	Embed func() EmbedState
	// StoreUnavailable 是打不开、已经停掉的平台库（S14）；nil = 没接，没有就是空
	StoreUnavailable func() []StoreUnavailable
	// Classify 给"有错误原文、没有类别"的旧行现算类别（app 接 llm.ClassifyText；store.Run 允许 error 不空而类别空，读的时候现算）；nil 就不算
	Classify func(errText string) string
	KV       *KV
}

// DayCount 是某个计数项当天的累计值
func (e *Env) DayCount(k Counter) int64 { return e.Day[k.Name()] }

// KV 是自检自己的小状态（比如聊天库的序号水位），存在 <目录>/state.json。读坏了当没有，Fresh 为真
type KV struct {
	mu    sync.Mutex
	m     map[string]int64
	Fresh bool // 启动时没读到可用的状态文件（没有或读坏了）
	dirty bool
}

// Get 读一个键
func (k *KV) Get(key string) (int64, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok
}

// Set 写一个键（落盘由 Runner 在评估后做）
func (k *KV) Set(key string, v int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if old, ok := k.m[key]; ok && old == v {
		return
	}
	if k.m == nil {
		k.m = map[string]int64{}
	}
	k.m[key] = v
	k.dirty = true
}

func (k *KV) copyOut() (map[string]int64, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]int64, len(k.m))
	for a, b := range k.m {
		out[a] = b
	}
	d := k.dirty
	k.dirty = false
	return out, d
}
