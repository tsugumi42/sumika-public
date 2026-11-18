package ctlproto

// 运行页资源标签的数据（规格-运行页 第四节）。界面和 sumika-ctl stats resources 共用这份结构。
// 原则：拿不到的数不能写成 0。可能缺的数值一律用指针（JSON 里是 null），整列取不到时用 Available 加 Reason 说原因，
// 页面写"—"或"不可用"，不画 0。报告里不放命令行、路径：进程只有 exe 名、类别和登记的标签（MCP 只写服务 id）。

// StatsResources 是 stats 的资源子命令
const StatsResources = "resources"

// 进程类别的稳定键（JSON 里用这些 ASCII 键，中文只是 Label）
const (
	ProcMain         = "main"         // 主程序
	ProcModels       = "models"       // 本地模型（sumika-models，或旧的 Python 嵌入子进程）
	ProcAnalyzer     = "analyzer"     // 命令分析用的常驻 pwsh
	ProcMCP          = "mcp"          // MCP 服务，标服务 id
	ProcShell        = "shell"        // 运行中的命令
	ProcMedia        = "media"        // 媒体工具（ffmpeg 一类）
	ProcUnregistered = "unregistered" // 主程序的后代但没人登记：让漏登记的地方自己暴露
)

// 内存列的口径：默认是专用工作集（任务管理器默认列的口径）；系统不给时退回工作集，列名跟着换，不静默换口径
const (
	MemPrivate    = "private" // 专用工作集
	MemWorkingSet = "working-set"
)

// 队列行的状态
const (
	QueueIdle    = "idle"    // 空闲
	QueueBusy    = "busy"    // 忙
	QueueJammed  = "jammed"  // 堵：积压大于 0 且最久等待超过该队列自己的正常时限
	QueueUnknown = "unknown" // 数据不够判断（没接入或读失败）
)

// ProcessRow 是进程表的一行。数值为 nil 表示这一刻没取到（第一次采样的 CPU、没权限的进程、没有 PDH），不是 0
type ProcessRow struct {
	PID      int    `json:"pid"`
	Parent   int    `json:"parent"` // 父进程号；根（主程序）的父进程不在表里
	Name     string `json:"name"`   // exe 名，不含路径
	Category string `json:"category"`
	Label    string `json:"label,omitempty"` // 登记时写的标签，例如 MCP 的服务 id
	Depth    int    `json:"depth"`           // 树里的缩进层数，主程序是 0
	// CPU 是占整机的百分比（两次采样之差除以时间差再除以逻辑核数，任务管理器同口径）
	CPU       *float64 `json:"cpu"`
	Memory    *int64   `json:"memory"` // 口径看 ResourcesReport.MemoryKind
	Commit    *int64   `json:"commit"` // 提交（PrivateUsage）；系统不给时是 nil
	GPU       *float64 `json:"gpu"`    // GPU 引擎利用率百分比；整列不可用时全是 nil
	GPUMemory *int64   `json:"gpuMemory"`
}

// GPUStatus 说显存、GPU% 这两列取不取得到。Available 为假时页面整列写"不可用"和 Reason，不写 0。
// Pending 为真是"还在准备"：打开性能计数器要 400 多毫秒，页面第一次采样先不开，下一次再开；这时两列写"—"，不写"不可用"
type GPUStatus struct {
	Available bool   `json:"available"`
	Pending   bool   `json:"pending,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// RuntimeStats 是主程序内部（Go 运行时），来自 runtime/metrics。各模块共用一个堆，所以不按模块拆
type RuntimeStats struct {
	Total      int64    `json:"total"` // Go 运行时向系统要的内存总量
	HeapObj    int64    `json:"heapObjects"`
	Stacks     int64    `json:"stacks"`
	HeapLive   int64    `json:"heapLive"` // 上一轮 GC 之后存活的堆
	MemLimit   *int64   `json:"memLimit"` // GOMEMLIMIT；没设是 nil
	Goroutines int64    `json:"goroutines"`
	GCCycles   int64    `json:"gcCycles"`
	GCCPU      *float64 `json:"gcCpu"`      // GC 占 Go 运行时 CPU 的比例（0 到 1）；还没有数据是 nil
	PauseP99Ns *int64   `json:"pauseP99Ns"` // GC 暂停的 p99（自进程启动起）；还没发生过 GC 是 nil
	// Other 是主程序的内存减去 Total：SQLite、图片解码、界面缓冲、系统库。取不到主程序内存或差为负时是 nil
	Other *int64 `json:"other"`
}

// QueueRow 是后台队列表的一行。Wired 为假表示这一队列还没接入注册表：页面写"未接入"，不写 0。
// 数值为 nil 是"这个队列没有这项计数"，不是 0；Err 非空是这一行读失败（只影响这一行）
type QueueRow struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Wired        bool   `json:"wired"`
	Backlog      *int64 `json:"backlog"`
	Running      *int64 `json:"running"`
	Capacity     *int64 `json:"capacity"`
	OldestWaitMs *int64 `json:"oldestWaitMs"`
	State        string `json:"state"`
	Note         string `json:"note,omitempty"` // 一句说明，例如"估计值"
	Err          string `json:"err,omitempty"`
}

// ResourcesReport 是 stats resources 的 data
type ResourcesReport struct {
	SampledAtMS int64 `json:"sampledAtMs"`
	// IntervalMS 是这次 CPU 用的两次采样的间隔；0 表示没有前一次采样，CPU 列写"—"
	IntervalMS int64 `json:"intervalMs"`
	Cores      int   `json:"cores"`
	// MemoryKind 是内存列的口径，MemPrivate 或 MemWorkingSet
	MemoryKind string       `json:"memoryKind"`
	GPU        GPUStatus    `json:"gpu"`
	Processes  []ProcessRow `json:"processes"`
	Runtime    RuntimeStats `json:"runtime"`
	Queues     []QueueRow   `json:"queues"`
}
