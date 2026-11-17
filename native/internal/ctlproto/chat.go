package ctlproto

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// CmdChat 是自检对话：往角色的专用自检话题发一句话，等这一轮结束再回（规格-本机控制接口第八节）。
// 请求用 text、character、new 三个可选字段和 wait（时限）；参数里没有会话 id：会话由服务端按角色拼，只会是本地私聊的自检话题
const CmdChat = "chat"

// 自检对话的限制：一句话最多 4000 字；时限默认 2 分钟、上限同 MaxWait
const (
	MaxChatText    = 4000
	DefaultChatTTL = 120 * time.Second
	// MaxChatReply 是回复里她那句话最多回多少字（超出的截断）
	MaxChatReply = 32000
)

// ChatStatus 是这一轮的结局（ChatResult.Status）
const (
	ChatDone            = "done"             // 她说完了
	ChatWaitingApproval = "waiting_approval" // 停在审批卡上；没替用户批，回合还在等，要用户在界面上批或拒
	ChatTimeout         = "timeout"          // 到时限还没说完，这一轮已取消
	ChatStopped         = "stopped"          // 被停下、被打断
	ChatError           = "error"            // 这一轮出错（模型报错等）
	ChatBusy            = "busy"             // 这个自检话题上一轮还在跑，这一句没发
)

// characterShape 角色 id 的写法：不许空白、冒号、斜杠、反斜杠（会话串靠冒号分段），和 checkpoint 认的一致
var characterShape = regexp.MustCompile(`^[^\x00-\x20:/\\]{1,128}$`)

// ValidCharacter 角色 id 的形状合不合（只看形状；有没有这个角色由桌面程序查）
func ValidCharacter(s string) bool { return characterShape.MatchString(s) }

// ValidChatText 自检对话的一句话合不合：非空白、合法 UTF-8、不超过 MaxChatText 字、
// 除换行和制表符外没有控制字符（终端序列、NUL 等不往模型里送）
func ValidChatText(s string) bool {
	if strings.TrimSpace(s) == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxChatText {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if isControl(r) {
			return false
		}
	}
	return true
}

// ChatTool 是这一轮调用的一个工具（取自运行记录）；参数和结果都是摘要
type ChatTool struct {
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Status string `json:"status,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

// ChatApproval 是停住的审批卡
type ChatApproval struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Tool    string   `json:"tool,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Why     string   `json:"why,omitempty"`
	Purpose string   `json:"purpose,omitempty"`
	Preview string   `json:"preview,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// ChatResult 是 chat 的 data。失败（status 不是 done）时也带在回复的 data 里：回复的 ok 为假、退出码 1，status 说明是哪种
type ChatResult struct {
	Status       string        `json:"status"`
	Character    string        `json:"character"`
	Topic        string        `json:"topic"`
	Conversation string        `json:"conversation"`
	TurnID       string        `json:"turnId,omitempty"`
	Reply        string        `json:"reply"`
	Draft        string        `json:"draft,omitempty"` // 超时、等审批时已经说出来的半截
	Tools        []ChatTool    `json:"tools"`
	Model        string        `json:"model,omitempty"`
	Route        string        `json:"route,omitempty"`
	ElapsedMS    int64         `json:"elapsedMs"`
	Approval     *ChatApproval `json:"approval,omitempty"`
	Error        string        `json:"error,omitempty"`
}
