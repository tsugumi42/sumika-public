// Package audience 是"这一轮谁在听、在哪听"（规格-Go后台 8.1）：渠道中间件填，内核原样交给插件和工具的关，
// 记忆按它算能看哪些范围（规格-记忆 3.3）。中立的小包：不引项目里别的包，内核、记忆、中间件都引它，谁也不用认识谁
package audience

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Audience 由路由算好给，模型改不了
type Audience struct {
	Channel      string   // console / qq …
	Conversation string   // 在哪个会话里（内核按 Input 的会话填，两边不会对不上）
	Scene        string   // "private" / "group"；空的按会话 id 认
	PersonIDs    []string // 这一批是谁在说："这个人"的记忆只在和他私聊时给
	PrivateOK    bool     // 主人在场（本地，或主人的平台私聊）
	OwnerAsked   bool     // 触发这一轮的消息是主人本人发的（ADR-174）
}

// Outsiders 是这一轮有没有外人在：只有平台的群算（规格-语言 3.3、ADR-123、ADR-140），本地群聊、私聊都不算。
// 角色卡的"私下"那段有外人时不拼。Scene 空的按会话 id 带不带 :group: 认（和 memory.PlaceOf 一样）
func (a Audience) Outsiders() bool {
	group := strings.Contains(a.Conversation, ":group:")
	if a.Scene != "" {
		group = a.Scene == "group"
	}
	return a.Channel != "console" && group
}

// Console 是主人在本地主窗口：一个人在、私聊、就是主人在问
func Console(conversation string) Audience {
	return Audience{Channel: "console", Conversation: conversation, Scene: "private", PrivateOK: true, OwnerAsked: true}
}

// 自检会话（sumika-ctl chat，规格-本机控制接口第八节）：本地私聊里 id 以 selftest 开头的话题。
// 这是一处共用的认法：内核给这个会话里存下的每一条事件打 selftest 标记，记忆不收、不开 remember，也不去取链接；
// 控制接口拼会话串、内核和记忆认串，都用这里的常量和函数，不各写一份。
const (
	// SelftestTag 是存进事件 segments.tags 里的标记，和渠道自检的标记同一个词（记忆整理丢掉带它的行）
	SelftestTag = "selftest"
	// SelftestTopicPrefix 是自检话题 id 的前缀：固定的一个叫 selftest-<角色>，--new 开的叫 selftest-<角色>-<时间>（SelftestTopicID）
	SelftestTopicPrefix = "selftest"
)

// IsSelftest 认自检会话：只认 console:<角色>:topic:selftest 和 console:<角色>:topic:selftest-<后缀>，
// 角色、话题段都不许为空、不许带空白，不是这个形状的一律不算
func IsSelftest(conversation string) bool {
	parts := strings.Split(conversation, ":")
	if len(parts) != 4 || parts[0] != "console" || parts[1] == "" || parts[2] != "topic" {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return false
		}
	}
	id := parts[3]
	return id == SelftestTopicPrefix || strings.HasPrefix(id, SelftestTopicPrefix+"-")
}

// SelftestConversation 拼某个角色的自检会话串；topicID 必须是自检话题 id
func SelftestConversation(character, topicID string) string {
	return "console:" + character + ":topic:" + topicID
}

// SelftestTopicID 拼自检话题 id：话题 id 在本地库里全局唯一，所以带上角色。stamp 空是这个角色固定的那一个
// （selftest-<角色>），不空是 --new 开的（selftest-<角色>-<stamp>）
func SelftestTopicID(character, stamp string) string {
	id := SelftestTopicPrefix + "-" + character
	if stamp != "" {
		id += "-" + stamp
	}
	return id
}

// 主动联系的话题线（规格-主动联系 第四节）：属于一个角色的有库会话，串为 console:<角色>:topic:thread-<线号>，
// 不登记进 local_topics（会话列表看不到）。和自检话题一样是一处共用的认法：别处不许自己拆串。
const (
	// ThreadTag 是存进线的事件 segments.tags 里的标记；记忆整理读线时据此认出它（第六节）
	ThreadTag = "proactive-thread"
	// ThreadTopicPrefix 是线号的前缀：话题段是 thread-<线号>
	ThreadTopicPrefix = "thread-"
	// ThreadScope 是线用的权限档（Input.Scope）；liveKey 带它，线天然是另一个实例、另一份工具清单
	ThreadScope = "thread"
	// ThreadActor 是线里"系统"发言人的 id（导演醒来行、主人回复转述）：身份由宿主预先绑定，不从正文推导
	ThreadActor = "proactive"
)

// IsThread 认话题线：只认 console:<角色>:topic:thread-<线号>，各段不许为空、不许带空白，线号不许再带冒号
func IsThread(conversation string) bool {
	parts := strings.Split(conversation, ":")
	if len(parts) != 4 || parts[0] != "console" || parts[1] == "" || parts[2] != "topic" {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return false
		}
	}
	id, ok := strings.CutPrefix(parts[3], ThreadTopicPrefix)
	return ok && id != ""
}

// ThreadConversation 拼线的会话串；id 是线号（不含 thread- 前缀）
func ThreadConversation(character, id string) string {
	return "console:" + character + ":topic:" + ThreadTopicPrefix + id
}

// ThreadID 取出会话串里的线号；不是线返回空
func ThreadID(conversation string) string {
	if !IsThread(conversation) {
		return ""
	}
	return strings.TrimPrefix(strings.Split(conversation, ":")[3], ThreadTopicPrefix)
}

// 提醒到点那一轮（规格-提醒 第二节）用的专用会话：每个角色一条，形状就是一条固定线号的线，所以内核的身份规矩（system 发言人、
// PrivateOK 为假、不签本地授权）一条不用改。它不是主动联系的线：不看 proactive.enabled，线的工具和提醒工具对它都不可见，
// 所以到点那一轮建不了新提醒。认法也集中在这里，别处不许自己拆串。
const reminderThreadID = "reminder"

// ReminderConversation 拼某个角色的提醒专用会话
func ReminderConversation(character string) string {
	return ThreadConversation(character, reminderThreadID)
}

// IsReminderRun 认提醒专用会话
func IsReminderRun(conversation string) bool { return ThreadID(conversation) == reminderThreadID }

// 悄悄话会话（规格-群聊引用和路由 7.1）：主人在窗口里引用一段话、指给某位角色，是对她说的悄悄话。
// 它存在她自己的独立本地会话里，串为 console:<角色>:whisper:<来源摘要>：每个角色每个来源会话一个。
// 来源会话键带冒号（qq:group:1），不能直接放进串里，所以第 4 段是来源键的短摘要，真实来源存在事件的 segments.whisper.source，
// 不靠串反解。不登记进 local_topics，会话列表和聊天区读取（store 的 display 校验只认 topic）天然读不到它；
// 只有这里的构造函数能生成这个形状，别处不许自己拼、自己拆。
const (
	// WhisperSegment 是串里第 3 段
	WhisperSegment = "whisper"
	// WhisperScope 是悄悄话处理用的权限档：和本地私聊同一份工具（主人 2026-11-08 定），但单独一个实例，
	// 不与主会话共用参考信息和工具结果
	WhisperScope = "whisper"
)

// WhisperNoSay 是主人只引用、没写字的悄悄话事件的占位文字。它不是主人说的话：记忆的 remember、memory_forget 不能拿它当“主人原话”
// （否则被引的别人的话经模型转述，就能顶着“主人说的”写进记忆）。
const WhisperNoSay = "（没有写字）"

// WhisperConversation 拼某个角色针对某个来源会话的悄悄话会话串；source 是来源会话键（任意，不要求没有冒号）
func WhisperConversation(character, source string) string {
	sum := sha256.Sum256([]byte(source))
	return "console:" + character + ":" + WhisperSegment + ":" + hex.EncodeToString(sum[:8])
}

// IsWhisper 认悄悄话会话：只认 console:<角色>:whisper:<16 位小写十六进制>，各段不许为空、不许带空白
func IsWhisper(conversation string) bool {
	parts := strings.Split(conversation, ":")
	if len(parts) != 4 || parts[0] != "console" || parts[1] == "" || parts[2] != WhisperSegment || len(parts[3]) != 16 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return false
		}
	}
	for _, c := range parts[3] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// WhisperCharacter 取悄悄话会话串里的角色；不是悄悄话会话返回空
func WhisperCharacter(conversation string) string {
	if !IsWhisper(conversation) {
		return ""
	}
	return strings.Split(conversation, ":")[1]
}

// NoMemory 说这个会话里的事件记忆不收：自检话题、提醒专用会话（主人看到的那句在主会话里，只记一次），
// 和悄悄话会话（里面是被引用的别人的话加主人的交代，不是聊天；处理的结论以处理记录的形式进来源会话的背景）
func NoMemory(conversation string) bool {
	return IsSelftest(conversation) || IsReminderRun(conversation) || IsWhisper(conversation)
}

// Thread 是线的听众：本地、私聊，但 PrivateOK 和 OwnerAsked 都为假——线里没有执行工具，
// ownerConsoleAudience 不成立，也不会被当成"主人在场"（第四节"听众和身份"，改坏检查靠它）
func Thread(conversation string) Audience {
	return Audience{Channel: "console", Conversation: conversation, Scene: "private", PrivateOK: false, OwnerAsked: false}
}
