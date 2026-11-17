// Package ctlproto 是桌面程序的本机控制接口（规格-本机控制接口）的协议：请求、回复、错误码、退出码，
// 以及客户端（cmd/sumika-ctl）和服务端（internal/ctlserver）共用的管道名与读写一行的做法。
//
// 客户端只许依赖这个包（internal/archcheck 守着）：客户端要小、要快，不能把整个后台链接进去。所以这里不引项目里的别的包，
// 数据结构（Status、GateRow 等）也都写在这里。
//
// 线上格式：一次连接一个请求、一个回复，各是一行 JSON（以换行结束）。
//
//	请求 {"v":1,"cmd":"receive","args":["start"],"wait":"60s"}
//	回复 {"ok":true,"data":{…}} 或 {"ok":false,"error":"…","code":"…"}
package ctlproto

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

// Version 是协议版本。请求的 v 不是它，服务端回 code "version"，不去猜
const Version = 1

// 限制：请求最长 64 KB；读请求、写回复各 5 秒（receive start --wait 在宿主里等，那段不受这个限制）
const (
	MaxRequestBytes = 64 << 10
	IOTimeout       = 5 * time.Second
	// MaxWait 是 --wait 能要求的最长时间；比宿主关一次后台的上限（30 秒）留足余量
	MaxWait = 5 * time.Minute
	// DefaultWait 是只写 --wait 没给时间时的默认
	DefaultWait = 60 * time.Second
	// MaxLogLines、DefaultLogLines 是 logs 的行数
	MaxLogLines     = 500
	DefaultLogLines = 50
)

// 命令名
const (
	CmdStatus      = "status"
	CmdDoctor      = "doctor"
	CmdReceive     = "receive"
	CmdWindow      = "window"
	CmdLogs        = "logs"
	CmdGate        = "gate"
	CmdAutoReceive = "auto-receive"
	CmdAutostart   = "autostart"
	CmdQuit        = "quit"
)

// Request 是一个请求。除 v 以外的字段都由服务端校验，客户端不替它把关
type Request struct {
	V      int      `json:"v"`
	Cmd    string   `json:"cmd"`
	Args   []string `json:"args,omitempty"`
	Wait   string   `json:"wait,omitempty"`   // receive start：等阶段落定的上限；chat：这一轮的时限。Go 的时长写法，如 "60s"
	N      int      `json:"n,omitempty"`      // logs / backfill gaps：要几行。
	Level  string   `json:"level,omitempty"`  // logs：最低级别 debug|info|warn|error，空 = 不限
	Module string   `json:"module,omitempty"` // logs：只要这个模块的行，空 = 不限
	Cursor *string  `json:"cursor,omitempty"` // 仅 backfill gaps 使用；指针区分未传与空值。
	// 以下三个只用于 chat（见 chat.go）：一句话、指定角色（空 = 主窗口当前角色）、是否新开一个自检话题；时限用 wait
	Text      string `json:"text,omitempty"`
	Character string `json:"character,omitempty"`
	New       bool   `json:"new,omitempty"`
	// 只用于 stats（见 stats.go）：范围、分组、是否列到模型；子命令放在 Args[0]
	Stats *StatsArgs `json:"stats,omitempty"`
	// 只用于 link（见 link.go）：模板、站点键、是否用示例数据；子命令放在 Args[0]
	Link *LinkArgs `json:"link,omitempty"`
	// 只用于 artist（见 artist.go）：平台、状态筛选、撤回的批次号；子命令放在 Args[0]
	Artist *ArtistArgs `json:"artist,omitempty"`
	// 只用于 backup now（见 backup.go）：媒体档；子命令放在 Args[0]，等多久用 wait
	Backup *BackupArgs `json:"backup,omitempty"`
	// 只用于 mcp release（见 mcp.go）：解除原因、是否明确接受“旧调用结果未知”的风险；子命令放在 Args[0]，服务 id 放在 Args[1]
	MCP *MCPArgs `json:"mcp,omitempty"`
	// 只用于 loop add|edit（见 slash.go）：日程、聊天方式、通知档
	Loop *LoopArgs `json:"loop,omitempty"`
	// 只用于 whisper create（见 whisper.go）：自检悄悄话的来源文字、说的话；角色用 character（逗号隔开），时限用 wait
	Whisper *WhisperArgs `json:"whisper,omitempty"`
	// 只用于 whisper log（见 whisperlog.go）：自检来源会话、批号、条目、游标、页大小
	WhisperLog *WhisperLogArgs `json:"whisperLog,omitempty"`
	// 只用于 secrets（见 secrets.go）：值、主密码、恢复码只放在这里，不进命令行参数
	Secrets *SecretsArgs `json:"secrets,omitempty"`
}

// Reply 是一个回复。失败时 Data 可以带着当时的状态（比如 receive start --wait 超时，把当时的阶段给出来）
type Reply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Code  string          `json:"code,omitempty"`
}

// 错误码。服务端回的是前七个；NotRunning 只有客户端自己会生成（连不上管道时没有服务端可回）
const (
	CodeVersion    = "version"  // 协议版本不对
	CodeInvalid    = "invalid"  // 命令、参数不认识或不允许（含 speak、本地会话、私聊、不存在的群）
	CodeDenied     = "denied"   // 不是这条管道该做的事
	CodeReadOnly   = "readonly" // 只看历史（archive）模式，库是只读的，先开始接收
	CodeDemo       = "demo"     // 演示数据模式
	CodeFailed     = "failed"   // 命令执行失败，error 里写原因
	CodeTimeout    = "timeout"  // --wait 等到时限也没落定
	CodeNotRunning = "not-running"
)

// 退出码（规格第二节第 4 条）
const (
	ExitOK         = 0
	ExitFailed     = 1 // 命令执行失败
	ExitNotRunning = 2 // 没在运行或连不上
	ExitDenied     = 3 // 被拒：参数不允许、不认识的命令
	ExitVersion    = 4 // 协议版本不对
)

// ExitCode 是一个回复对应的退出码。readonly 是 1：状态问题，先开始接收就能成；demo 是 3：这个模式里永远做不了
func ExitCode(r Reply) int {
	if r.OK {
		return ExitOK
	}
	switch r.Code {
	case CodeVersion:
		return ExitVersion
	case CodeInvalid, CodeDenied, CodeDemo:
		return ExitDenied
	case CodeNotRunning:
		return ExitNotRunning
	}
	return ExitFailed
}

// Error 是后端交给服务端的带码错误；服务端把它原样变成回复里的 code 和 error。没带码的错误一律按 failed
type Error struct {
	Code string
	Msg  string
	// Data 可选：失败时随回复一起给出的当时状态
	Data any
}

func (e *Error) Error() string { return e.Msg }

// DeniedError 是客户端连管道时被拒：没有权限打开，或者管道另一头的进程不是当前用户的（有人抢先占了这个管道名）。
// 客户端按 denied（退出码 3）报，不当"没在运行"——Agent 会以为程序没开，反而去重试
type DeniedError struct{ Msg string }

func (e *DeniedError) Error() string { return e.Msg }

// Errorf 造一个带码的错误
func Errorf(code, msg string) *Error { return &Error{Code: code, Msg: msg} }

// ───────────── 各命令的 data ─────────────

// Alert 是一条没恢复的提醒（和"后台状态"页的"没恢复的提醒"同一份）
type Alert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Status 是 status 的 data。号码一律照界面上的打码规则；Autostart 取不到时是 null
type Status struct {
	Mode    string `json:"mode"`   // archive / receive；后台还没开好是空
	Phase   string `json:"phase"`  // starting archive local receiving switching blocked stopping stopped
	Reason  string `json:"reason"` // 给人看的原因
	Account string `json:"account"`
	NoRecv  string `json:"noRecv"` // 不能开始接收的原因；能接收是空
	// Accounts 是配置里的 QQ 小号数（-1 = 还没读到）。0 表示只能本地可用，多出来又没指定也在这里看得出
	Accounts    int     `json:"accounts"`
	Connection  string  `json:"connection"`
	Alerts      []Alert `json:"alerts"`
	AutoReceive bool    `json:"autoReceive"`
	Autostart   *bool   `json:"autostart"`
	DataDir     string  `json:"dataDir"`
	Version     string  `json:"version"`
	Pid         int     `json:"pid"`
	Demo        bool    `json:"demo"`
	// Watchdog 是守护进程那一栏（规格-看门狗）；演示数据是空
	Watchdog *WatchdogInfo `json:"watchdog,omitempty"`
	// RunningTurns 是正在跑的回合数（和角色对话的一轮）；换 exe 前看它，不为零退出会切断回合。只看历史或后台没开好是 0
	RunningTurns int `json:"runningTurns"`
	// Selfcheck 是运行自检的一句话（正常 / N 项异常（…）/ 未更新 / 已关闭，规格-运行时自检 第七节）；后台还没建好是空，状态行不显示
	Selfcheck string `json:"selfcheck,omitempty"`
	// Env 是环境清单的一句话（正常 / N 项有问题（sumika-ctl doctor 看详情）……）；和 doctor 读同一份快照
	Env string `json:"env,omitempty"`
	// WebBalances 是联网搜索各家的剩余额度，每家一行（"tavily 剩余 123"）；没开联网或还没查到是空
	WebBalances []string `json:"webBalances,omitempty"`
}

// WatchdogInfo 是 status 里守护进程那一栏。Running 为假且 Supervised 也为假，就是桌面程序被直接启动、没人看着
type WatchdogInfo struct {
	Running    bool   `json:"running"`         // 守护进程现在在不在（状态文件够新、进程还活着）
	Supervised bool   `json:"supervised"`      // 这个桌面程序是不是它拉起的
	Pid        int    `json:"pid,omitempty"`   // 守护进程的进程号，在的时候才有
	State      string `json:"state,omitempty"` // running / gave-up（连续崩溃停止拉起）/ stopped
	Restarts   int    `json:"restarts"`        // 这一轮守护进程已经重新拉起过几次
	// 最近一次异常退出：时间（RFC3339）、原因、事件记录文件名（在 <数据目录>/logs/incidents/）；没有就都是空
	LastIncidentAt string `json:"lastIncidentAt,omitempty"`
	LastReason     string `json:"lastReason,omitempty"`
	LastFile       string `json:"lastFile,omitempty"`
}

// Receive 是 receive start / stop 的 data
type Receive struct {
	Phase     string `json:"phase"`
	Mode      string `json:"mode"`
	Reason    string `json:"reason"`
	Receiving bool   `json:"receiving"`
	// Settled 为真是阶段已经落定（--wait 等到了，或者很快就做完了）；为假是已经开始、还在做
	Settled bool `json:"settled"`
}

// Logs 是 logs 的 data。过滤时才有 Scanned、Matched；Old 是旧格式（没有级别）的行数，它们没有参与过滤
type Logs struct {
	Lines   []string `json:"lines"`
	Scanned int      `json:"scanned,omitempty"`
	Matched int      `json:"matched,omitempty"`
	Old     int      `json:"old,omitempty"`
}

// LogFilter 是 logs 的过滤条件（空 = 不过滤）
type LogFilter struct {
	Level  string
	Module string
}

// GateRow 是 gate list 的一行。Conversation 是打码后的群会话（qq:group:***01，和日志同一口径），完整群号不出这个接口；
// 要指这个群用 Handle（gate set 认它）。HandleClash 为真是有别的群的句柄和它撞了，这时 gate set 不认这个句柄，要写完整群号。
// Mode 是这个群现在的档位设置（off 只记录 / dry 试运行 / speak 说话），Source 说它从哪来：
// own 这个群自己的、all 所有群的、default 没设过用配置里的默认。Actual 只在接收中有：真正在用的档位（发送条件不齐时 speak 会被压成试运行）
type GateRow struct {
	Conversation string `json:"conversation"`
	Handle       string `json:"handle"`
	HandleClash  bool   `json:"handleClash,omitempty"`
	Name         string `json:"name,omitempty"`
	Mode         string `json:"mode"`
	Source       string `json:"source"`
	Actual       string `json:"actual,omitempty"`
}

// GateList 是 gate list 的 data
type GateList struct {
	Groups []GateRow `json:"groups"`
}

// GateChange 是 gate set 的 data。Conversation 同样是打码后的
type GateChange struct {
	Conversation string `json:"conversation"`
	Handle       string `json:"handle"`
	Before       string `json:"before"` // 改之前这个群自己的设置；没设过是空
	After        string `json:"after"`
}

// HandleLen 是群句柄的长度：HMAC-SHA256(本机密钥, 会话号) 的前 8 个十六进制字符（小写）。
// gate set 的会话参数要么是完整的 qq:group:<群号>，要么正好是这样一个句柄；完整写法必须带 qq:group: 前缀，所以两种写法不会混
const HandleLen = 8

var handleShape = regexp.MustCompile(`^[0-9a-f]{8}$`)

// IsHandle 是不是句柄的写法（只看形状：小写十六进制、恰好 8 个字符）
func IsHandle(s string) bool { return handleShape.MatchString(s) }

// Toggle 是 auto-receive、autostart 的 data
type Toggle struct {
	On bool `json:"on"`
}

// Quitting 是 quit 的 data
type Quitting struct {
	Quitting bool `json:"quitting"`
}

// ───────────── 读写一行 ─────────────

// ErrTooLong 是请求或回复超过了长度上限
var ErrTooLong = errors.New("一行太长")

// ReadLine 读一行（不含换行），最多 max 个字节；超了返回 ErrTooLong，不继续读。连接断了没读到换行也算读到了最后一行
func ReadLine(r *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > max+1 { // +1：换行本身不算
			return nil, ErrTooLong
		}
		switch {
		case err == nil:
			out = out[:len(out)-1]
			if len(out) > max {
				return nil, ErrTooLong
			}
			return out, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(out) > 0:
			if len(out) > max {
				return nil, ErrTooLong
			}
			return out, nil
		default:
			return nil, err
		}
	}
}

// WriteJSON 把 v 写成一行 JSON
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
