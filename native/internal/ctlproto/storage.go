package ctlproto

// 运行页存储标签的数据（规格-运行页 第五节）。界面和 sumika-ctl stats storage 共用这份结构，不各写一份。
// 报告里不放任何路径、文件名：只有类别、字节数、文件数；聊天库按平台名、记忆库按角色 id 分行。

// StatsStorage 是 stats 的存储子命令
const StatsStorage = "storage"

// 存储类别的稳定键（JSON 里用这些 ASCII 键，中文只是 Label）
const (
	StoreMedia            = "media"             // 媒体（go/media）
	StoreLegacyMedia      = "legacy-media"      // 旧版媒体（shared/media）
	StoreThumbs           = "thumbs"            // 缩略图
	StoreChat             = "chat"              // 聊天库
	StoreTrace            = "trace"             // 运行记录
	StoreUpgradeSnapshots = "upgrade-snapshots" // 升级前自动快照（go/upgrade-snapshots）
	StoreMemory           = "memory"            // 角色记忆库
	StoreOwnerMemory      = "owner-memory"      // 共用记忆库（shared/owner-memory.db，主人的事所有角色共用）
	StoreImport           = "import"            // 导入数据
	StoreWork             = "work"              // 工作结果、检查点
	StoreBackups          = "backups"           // 备份
	StoreLogs             = "logs"              // 日志
	StoreConfig           = "config"            // 配置和设置历史
	StoreCards            = "cards"             // 角色卡
	StoreDev              = "dev"               // 开发数据
	StoreReports          = "reports"           // 报告
	StoreLegacyOther      = "legacy-other"      // 旧版数据（shared/ 里除媒体、共用记忆库以外的 Bun 库）
	StoreModels           = "models"            // 本地模型（数据目录之外）
	StoreOther            = "other"             // 数据目录里没有归进上面任何一类的：单列，不让文件悄悄消失
)

// 每个类别数字的来源
const (
	SourceLive     = "live"           // 这次现量的（os.Stat、小目录遍历）
	SourceSnapshot = "snapshot"       // 上次统计的快照
	SourceDelta    = "snapshot+delta" // 快照加上之后的新增（只有媒体）
	SourceNone     = "none"           // 没有统计过
)

// StorageItem 是类别下的一行（聊天库按平台、记忆库按角色）
type StorageItem struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int64  `json:"files"`
}

// StorageCategory 是一个类别的占用
type StorageCategory struct {
	Key    string        `json:"key"`
	Label  string        `json:"label"`
	Bytes  int64         `json:"bytes"`
	Files  int64         `json:"files"`
	Approx bool          `json:"approx"` // 快照加增量、或者快照已经不是现量，页面写"约"
	Source string        `json:"source"` // Source* 之一
	AsOfMS int64         `json:"asOfMs"` // 这个数字是什么时候量的（现量的是现在；0 表示没有）
	Rule   string        `json:"rule"`   // 清理规则一句话；没有自动清理就写"没有自动清理"
	Auto   bool          `json:"auto"`   // 有没有任何自动清理
	Absent bool          `json:"absent"` // 位置不存在（比如备份目录还没建），大小是 0 但不是出错
	Links  int           `json:"links"`  // 遇到几个联接或符号链接，没有跟进去
	Errors int           `json:"errors"` // 遍历中几处读不了（只计数，不带路径）
	Items  []StorageItem `json:"items,omitempty"`
}

// StorageScan 是后台扫描的状态
type StorageScan struct {
	Running bool  `json:"running"`
	Entries int64 `json:"entries"` // 已经处理的项数（进度）
	StartMS int64 `json:"startMs"`
}

// StorageReport 是 stats storage 的 data
type StorageReport struct {
	Total        int64             `json:"total"`        // 各类别之和（数据目录里的加上目录外的）
	NeverScanned bool              `json:"neverScanned"` // 还没统计过：大目录的数字是空的，页面写"还没有统计过"
	ScannedAtMS  int64             `json:"scannedAtMs"`  // 上次完整统计开始的时间
	ScanMs       int64             `json:"scanMs"`       // 上次完整统计用了多久
	DeltaBytes   int64             `json:"deltaBytes"`   // 快照之后媒体新增的字节
	Live         bool              `json:"live"`         // 增量来自正在运行的后台；为假说明只有快照，没有增量
	Scan         StorageScan       `json:"scan"`
	Categories   []StorageCategory `json:"categories"`
}
