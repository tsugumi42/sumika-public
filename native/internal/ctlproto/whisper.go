package ctlproto

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// CmdWhisper 是悄悄话在命令行里的写法（规格-群聊引用和路由 第十一节），让开发助手测后台而不碰真实会话：
//
//	whisper create --selftest --character a[,b…] --source-text "<引用的文字>" [--quote-name 名字] --say "<文字>" [--timeout 120s]
//	whisper status <批号>
//	whisper cancel <批号>
//
// create 的来源是命令行给的文字，不读任何真实会话的事件：请求里没有任何字段能指向真实的 QQ 会话或本地群，
// 来源会话由服务端按第一个角色拼成自检话题；没有 --selftest 一律 invalid。
const CmdWhisper = "whisper"

// WhisperArgs 是 whisper create 的参数（status、cancel 不带）。
type WhisperArgs struct {
	Selftest   bool   `json:"selftest,omitempty"`
	SourceText string `json:"sourceText,omitempty"`
	QuoteName  string `json:"quoteName,omitempty"`
	Say        string `json:"say,omitempty"`
}

// 悄悄话命令的限制
const (
	MaxWhisperCharacters = 10
	MaxQuoteName         = 40
	// MaxWhisperNote 是回复里一条处理记录最多回多少字（超出的截断）
	MaxWhisperNote = 4000
)

var batchShape = regexp.MustCompile("^[0-9A-Za-z]{1,64}$")

// ValidBatch 批号的写法合不合。
func ValidBatch(s string) bool { return batchShape.MatchString(s) }

// ValidWhisperName 引用者名字合不合：可以空（用默认），不超过 MaxQuoteName 字，不带控制字符和换行。
func ValidWhisperName(s string) bool {
	if utf8.RuneCountInString(s) > MaxQuoteName || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return isControl(r) || r == '\n' || r == '\r' || r == '\t' })
}

// 悄悄话各角色的状态（WhisperEntry.State）和整批的结局（WhisperResult.Status）
const (
	WhisperQueued      = "queued"
	WhisperProcessing  = "processing"
	WhisperDone        = "done"
	WhisperFailed      = "failed"
	WhisperCancelled   = "cancelled"
	WhisperTimedOut    = "timed_out"
	WhisperInterrupted = "interrupted"
	// WhisperPending 是整批到时限还有没落定的
	WhisperPending = "pending"
)

// WhisperEntry 是一位角色那一份的现状。Note 是她的处理记录（模型写的，只当数据看）。
type WhisperEntry struct {
	Character         string `json:"character"`
	State             string `json:"state"`
	Position          int    `json:"position,omitempty"` // 排队中时前面还有几个
	SourceTurnRunning bool   `json:"sourceTurnRunning,omitempty"`
	Detail            string `json:"detail,omitempty"`
	Note              string `json:"note,omitempty"`
}

// WhisperResult 是 whisper 三个子命令的 data。
type WhisperResult struct {
	Batch     string         `json:"batch"`
	Status    string         `json:"status"`
	Entries   []WhisperEntry `json:"entries"`
	ElapsedMS int64          `json:"elapsedMs,omitempty"`
}
