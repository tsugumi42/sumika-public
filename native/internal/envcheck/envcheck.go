// Package envcheck 是“我现在缺什么、坏在哪、下一步做什么”的环境清单：每一项由纯函数从调用方注入的普通值算出来，
// 不读文件、不查 PATH、不起外部程序。宿主生成一份快照，命令行和界面读同一份（规格-环境自检第 1 条）。
package envcheck

import "time"

// State 是一项环境检查这一次的判定
type State string

const (
	OK      State = "ok"      // 正常
	Warn    State = "warn"    // 能用但打了折扣
	Bad     State = "bad"     // 有功能用不了
	NA      State = "na"      // 不适用，Detail 写原因
	Unknown State = "unknown" // 不知道，Detail 写原因；不能当正常显示
)

// Mode 是生成快照时后台所处的模式
type Mode string

const (
	ModeReceive Mode = "receive" // 接收
	ModeArchive Mode = "archive" // 只看历史
	ModeStopped Mode = "stopped" // 没开
)

// Item 是环境清单的一项。Detail 是现在是什么样，Next 是下一步（能给命令就给命令），Affects 是影响什么。
// Sub 是这一项下面按程序或部件分的小项（第一版只有 env.tools 用），每项自己带状态、详情和下一步。
type Item struct {
	ID      string    `json:"id"`
	Group   string    `json:"group"`
	Name    string    `json:"name"`
	State   State     `json:"state"`
	Detail  string    `json:"detail"`
	Next    string    `json:"next,omitempty"`
	Affects string    `json:"affects"`
	Sub     []SubItem `json:"sub,omitempty"`
}

// SubItem 是清单项下面的一个小项：Name 是程序名或部件名，Detail 是找到在哪或影响什么，Next 是没找到时的下一步。
type SubItem struct {
	Name   string `json:"name"`
	State  State  `json:"state"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}

// Snapshot 是一次环境清单；At 是生成时间，Mode 是后台当前所处的模式。
type Snapshot struct {
	At    time.Time `json:"at"`
	Mode  Mode      `json:"mode"`
	Items []Item    `json:"items"`
}

// Inputs 是清单全部项的输入。运行部分没给（后台没开）时对应输入的 Available 为假，那一项记 unknown 并写原因。
type Inputs struct {
	At   time.Time
	Mode Mode

	Secrets  SecretsInput
	Platform PlatformInput
	Gateway  GatewayInput
	Catalog  CatalogInput
	Embed    CheckInput
	Kernel   CheckInput
	Plugins  PluginsInput
	MCP      MCPInput
	Analyzer AnalyzerInput
	Backup   BackupInput
	Tools    ToolsInput
	Paths    PathsInput
}

// Build 按规格固定的 ID 顺序拼出全部项。
func Build(in Inputs) Snapshot {
	return Snapshot{At: in.At, Mode: in.Mode, Items: []Item{
		Secrets(in.Secrets),
		Platform(in.Platform),
		ModelsGateway(in.Gateway),
		ModelsCatalog(in.Catalog),
		Embed(in.Embed),
		Kernel(in.Kernel),
		Plugins(in.Plugins),
		MCP(in.MCP),
		Analyzer(in.Analyzer),
		Backup(in.Backup),
		Nightly(),
		Tools(in.Tools),
		Paths(in.Paths),
	}}
}

// CheckResult 是自检一条检查这一次算出来的结果（状态、备注、下一步、例子的普通值拷贝）。
// envcheck 只把它翻成清单项，不再自己判阈值（规格-环境自检 设计第 6 条）。
type CheckResult struct {
	State   string            // ok / bad / unknown / skip（selfcheck.Result.State）
	Note    string            // 现在是什么样
	Next    string            // 下一步；空时清单项给按项的兜底
	Example map[string]string // 定位用的字段（服务名、会话、故障种类等）
}

// CheckInput 是一条依赖后台的自检项在清单里的输入。
// Available 为假时 Reason 写为什么查不了（后台没开、打开失败的原因摘要），那一项记 unknown。
type CheckInput struct {
	Available bool
	Reason    string
	CheckResult
}

// SecretsInput 是密钥库的状态（app 的 SecretsStatus 去掉值以后传进来）
type SecretsInput struct {
	NeedToken    bool   // 配置里的 QQ 号要访问令牌
	Account      string // 要令牌的号；只用来打码显示
	Exists       bool   // 密钥文件在
	Unlocked     bool   // 这个库解开了
	HasLocalCopy bool   // 本机副本可用
	Note         string // SecretsStatus.Note：锁着或本机副本不能用的原因
	ReadErr      string // 读取状态失败的原因；非空时这一项记 unknown
}

// PlatformAccount 是一个 QQ 账号的配置事实（令牌只看名字在不在、配置里有没有写，不读值）
type PlatformAccount struct {
	ID       string
	URL      string
	HasToken bool
}

// PlatformInput 是 QQ 账号配置
type PlatformInput struct {
	Enabled  bool // 这次要开 QQ
	Accounts []PlatformAccount
	Locked   bool // 密钥库锁着：有账号要令牌也取不出来
}

// GatewayInput 是模型来源（Magpie 或直连）的目录状态
type GatewayInput struct {
	Available bool
	Reason    string // 不可用时的原因（后台没开等）
	Source    string
	ReadAt    time.Time
	Err       string
}

// CatalogInput 是 models.dev 模型资料库的状态
type CatalogInput struct {
	Available bool
	Reason    string // 不可用时的原因（后台没开等）
	Enabled   bool
	Entries   int
	FetchedAt time.Time
	FromCache bool   // 这次没联网成功，用的是缓存
	Err       string // 取不到的原因
}

// PluginFailure 是一个启用失败的插件
type PluginFailure struct {
	ID  string
	Err string
}

// PluginsInput 是内置插件的状态（Hub.PluginStatuses 里 failed 的那些）
type PluginsInput struct {
	Available bool
	Reason    string // 不可用时的原因（后台没开等）
	Failed    []PluginFailure
}

// NeverConnectedService 是一个现在没连上、也不在 S11 里的 MCP 服务（启动时没连上、正在重连）。
type NeverConnectedService struct {
	Name  string
	Cause string
}

// MCPInput 是 MCP 的状态（S11 的结果，另补从没连上的服务；坑 11）
type MCPInput struct {
	CheckInput
	NeverConnected []NeverConnectedService
}

// AnalyzerInput 是命令分析器的状态（policy.Analyzer.Status）
type AnalyzerInput struct {
	Available bool
	Reason    string // 不可用时的原因（后台没开等）
	OK        bool   // 解析器能用
	Path      string
	LastErr   string
}

// BackupInput 是每天备份和每周抽验（S9、S12）的结果；清单只合并呈递，不再自己判阈值。
type BackupInput struct {
	Available bool
	Reason    string // 不可用时的原因（后台没开等）
	Daily     CheckResult
	Drill     CheckResult
}

// ToolsInput 是外部程序的查找结果。Checked 为假表示还没查，记 unknown，不当成“都缺”。
type ToolsInput struct {
	Checked bool
	Results []ToolResult
}

// ToolResult 是 internal/toolchain 的一行查找结果（这里复制一份普通值，envcheck 不依赖 toolchain）
type ToolResult struct {
	Name     string
	Required bool
	Found    bool
	Path     string
	Source   string // bundled / path / config / system
	Version  string // 版本命令的第一行；没取到是空
	Purpose  string
}

// PathValue 是配置里一个指向数据目录外的绝对路径
type PathValue struct {
	Key   string
	Value string
}

// PathsInput 是数据目录位置和配置里绑机器的绝对路径
type PathsInput struct {
	Root    string
	Source  string
	Outside []PathValue // 指向数据目录外的绝对路径；空 = 都相对数据目录
}
