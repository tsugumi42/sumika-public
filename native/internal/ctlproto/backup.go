package ctlproto

// CmdBackup 是备份在命令行里的写法（规格-数据目录与备份 10.7）：
//
//	backup now [--media none|stickers|all] [--wait[=60s]]   做一份手动备份（原因记“手动”）
//	backup list                                              列出备份，并给总览
//	backup restore <备份名> --to <新目录> [--wait[=60s]]      把一份备份恢复成新目录并核对（规格 10.10；不切换、不碰现在的数据目录）
//	backup verify [<备份名>] [--wait[=60s]]                  抽验：恢复到临时目录、核对、删掉、记下结果（不写名字抽最新一份好的每天备份）
//
// 备份目录里的名字、清单里的文字都是读出来的，只当数据看。
const CmdBackup = "backup"

// 子命令
const (
	BackupNowCmd  = "now"
	BackupListCmd = "list"
	// BackupRestoreCmd、BackupVerifyCmd 是 10.10 加的：都不是只读（restore 往主人指定的位置写一整份数据目录的副本，verify 写几个 GB 的临时文件、占着备份那把锁、最后删一个目录）
	BackupRestoreCmd = "restore"
	BackupVerifyCmd  = "verify"
)

// BackupDefaultWait 是 backup now 不写 --wait 时等多久：到点还没做完就回“已开始，没做完”，用 backup list 看结果
const BackupDefaultWait = DefaultWait

// BackupArgs 是 backup now 的参数
type BackupArgs struct {
	Media string `json:"media,omitempty"` // none | stickers | all；空用配置里的 backup.dailyMedia（只用于 backup now）
	To    string `json:"to,omitempty"`    // 恢复成的新目录，要写绝对路径（只用于 backup restore）
}

// BackupDBCheck 是恢复核对里一个库的结果
type BackupDBCheck struct {
	Path      string   `json:"path"`
	Integrity string   `json:"integrity"`
	Rows      string   `json:"rows"` // match | mismatch | unrecorded（这份备份没记行数）
	Tables    int      `json:"tables"`
	Mismatch  []string `json:"mismatch,omitempty"`
}

// BackupRestored 是 backup restore 的 data。Settled 为假是等到时限还没做完（它还在桌面程序里做，看目标目录里的 .restore/report.json）。
// 核对没通过时 OK 为假，随失败的回复一起给出
type BackupRestored struct {
	Settled      bool            `json:"settled"`
	Name         string          `json:"name,omitempty"`
	Target       string          `json:"target,omitempty"`
	OK           bool            `json:"ok"`
	OtherSource  bool            `json:"otherSource,omitempty"`
	Files        int             `json:"files"`
	Bytes        int64           `json:"bytes"`
	DurationMs   int64           `json:"durationMs"`
	RowsRecorded bool            `json:"rowsRecorded"`
	DBs          []BackupDBCheck `json:"dbs,omitempty"`
	Problems     []string        `json:"problems,omitempty"`
	Notes        []string        `json:"notes,omitempty"`
}

// BackupVerified 是 backup verify 的 data（字段和 BackupRestored 一样的含义；临时目录已经删了，没有 Target）
type BackupVerified struct {
	Settled      bool            `json:"settled"`
	Name         string          `json:"name,omitempty"`
	Kind         string          `json:"kind,omitempty"` // weekly | manual
	OK           bool            `json:"ok"`
	Files        int             `json:"files"`
	Bytes        int64           `json:"bytes"`
	DurationMs   int64           `json:"durationMs"`
	RowsRecorded bool            `json:"rowsRecorded"`
	DBs          []BackupDBCheck `json:"dbs,omitempty"`
	Problems     []string        `json:"problems,omitempty"`
	// Counts 为假：这次抽验照样核对了，但不计入“上次通过”（不推迟每周抽验、不让自检转绿），原因在 NotCountedWhy
	Counts        bool   `json:"counts"`
	NotCountedWhy string `json:"notCountedWhy,omitempty"`
}

// BackupNow 是 backup now 的 data。Settled 为假是等到时限还没做完（它还在桌面程序里做，用 backup list 看）
type BackupNow struct {
	Settled  bool     `json:"settled"`
	Name     string   `json:"name,omitempty"`
	Status   string   `json:"status,omitempty"` // ok | bad
	Files    int      `json:"files"`
	Linked   int      `json:"linked"`
	Bytes    int64    `json:"bytes"`
	Problems []string `json:"problems,omitempty"`
}

// BackupRow 是 backup list 的一行。时间是毫秒时间戳
type BackupRow struct {
	Name     string   `json:"name"`
	At       int64    `json:"at"`
	Reason   string   `json:"reason"` // daily | manual | pre-change
	Status   string   `json:"status"` // ok | bad | partial
	Bytes    int64    `json:"bytes"`  // 清单里的文件大小合计（含和别的备份共用的）
	Linked   int      `json:"linked"`
	Classes  []string `json:"classes"` // self | material | media
	Media    string   `json:"media"`
	DBs      int      `json:"dbs"`
	Problems []string `json:"problems,omitempty"`
	// SkippedMedia 是这份没带上的媒体个数（链接、读不了）
	SkippedMedia int `json:"skippedMedia,omitempty"`
	// 备份跑着时对聊天库写锁的采样（规格 10.10.6）：探了几次、几次没探到、最长等了多少毫秒；没有探针的备份都是 0
	WriteWaitSamples int   `json:"writeWaitSamples,omitempty"`
	WriteWaitFailed  int   `json:"writeWaitFailed,omitempty"`
	WriteWaitMaxMs   int64 `json:"writeWaitMaxMs,omitempty"`
}

// BackupDrill 是总览里的“上次抽验”（毫秒时间戳；没抽验过 Have 为假）
type BackupDrill struct {
	On         bool     `json:"on"`   // 每周自动抽验开着（跟着每天自动备份）
	Have       bool     `json:"have"` // 有过抽验记录
	At         int64    `json:"at,omitempty"`
	Kind       string   `json:"kind,omitempty"` // weekly | manual
	Backup     string   `json:"backup,omitempty"`
	OK         bool     `json:"ok"`
	Problems   []string `json:"problems,omitempty"`
	DurationMs int64    `json:"durationMs,omitempty"`
	HavePass   bool     `json:"havePass"`
	PassAt     int64    `json:"passAt,omitempty"` // 最近一次通过的时间
	Running    bool     `json:"running,omitempty"`
	// 最近一次不计入“上次通过”的手动抽验（别的数据目录的、不是最新一份好备份的）：照样核对、记录、显示，只是不顶替上面的
	HaveOther    bool   `json:"haveOther,omitempty"`
	OtherAt      int64  `json:"otherAt,omitempty"`
	OtherBackup  string `json:"otherBackup,omitempty"`
	OtherOK      bool   `json:"otherOk,omitempty"`
	OtherWhy     string `json:"otherWhy,omitempty"`
	OtherProblem string `json:"otherProblem,omitempty"`
}

// BackupOverview 是总览：共几份、按原因各几份、实际占用（硬链接只算一次）、规则、提醒线、位置和所在盘剩余空间
type BackupOverview struct {
	Dir           string      `json:"dir"`
	DirError      string      `json:"dirError,omitempty"`
	Total         int         `json:"total"`
	Daily         int         `json:"daily"`
	Manual        int         `json:"manual"`
	PreChange     int         `json:"preChange"`
	Bad           int         `json:"bad"`
	Partial       int         `json:"partial"`
	Foreign       int         `json:"foreign"` // 别的数据目录写进这个备份目录的份数（不参与轮换，不算在上面）
	UsedBytes     int64       `json:"usedBytes"`
	UsedAt        int64       `json:"usedAt,omitempty"`        // 占用是哪次备份结束时算的（毫秒时间戳）；估算时没有
	UsedEstimated bool        `json:"usedEstimated,omitempty"` // 没有算好的占用，按清单估算
	FreeBytes     int64       `json:"freeBytes"`
	FreeKnown     bool        `json:"freeKnown"`
	AlertGB       float64     `json:"alertGB"`
	DailyOn       bool        `json:"dailyOn"`
	Media         string      `json:"media"`
	Rules         string      `json:"rules"`
	Drill         BackupDrill `json:"drill"`
}

// BackupList 是 backup list 的 data
type BackupList struct {
	Overview BackupOverview `json:"overview"`
	Rows     []BackupRow    `json:"rows"`
}
