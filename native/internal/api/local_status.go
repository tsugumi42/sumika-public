package api

import "context"

// LocalStatus 是一个本地私聊会话里目标和定期任务的当前状态（界面输入框上方的状态行用）。
// 命令的回复、续轮、达成、暂停这些系统行不在这里，它们是会话记录里 kind=notice、actor_kind=system 的消息，
// 续轮发出的输入是 origin 为 goal 或 loop 的消息，由消息订阅照常交付。
type LocalStatus struct {
	Key ConversationKey
	// GoalLine 是目标的一行状态（"目标：…… · 第 3 轮 · 进行中"），没有目标为空；GoalState 是 active、paused 或空。
	GoalLine, GoalState string
	// AutoRunning 为真表示宿主正在这个会话里自动跑（目标的裁判或续轮、定期任务的一轮）：界面显示停止，点停止同时暂停目标。
	AutoRunning bool
	// Typing 为真表示对方正在输入（QQ 私聊的瞬时状态，几秒后自己消失）。
	Typing bool
	// Loops 是这个会话没停的定期任务，每个一行文字。
	Loops []string
	// Goal 是目标的详情（输入框上方那一行点开看）；没有目标是 nil。只读：改目标走 /goal 命令。
	Goal *GoalInfo
	// LoopTasks 是这个会话没停的定期任务的结构化内容（定期任务页用），和 Loops 一一对应、同序。
	LoopTasks []LoopInfo
	// Reminders 是这个角色的提醒（提醒页用，挂在 console:<角色> 上；结束的也在，30 天后清）。
	Reminders []ReminderInfo
}

// ReminderRepeat 是周期。Unit 空是一次性；minute、hour、day、week、month；Weekdays 只给 week，1 是周一、7 是周日
type ReminderRepeat struct {
	Unit     string
	Every    int
	Weekdays []int
}

// ReminderInfo 是一条提醒。时间都是 Unix 毫秒，0 表示没有。Status：active、paused、done、cancelled。
// Source：owner（主人在界面建的）、chat（她在和主人说话时建的）、thread（她在主动联系的线里建的）。
type ReminderInfo struct {
	ID               int
	Character        string
	Text             string
	At, NextAt       int64
	Status, Source   string
	Repeat           ReminderRepeat
	RepeatText       string
	Targets          []string // 自己写的目标，空是跟配置
	EffectiveTargets []string // 实际发到哪：local、qq
	Rings            int
	LastRing         int64
	LastNote         string
	EndNote          string
	EndedAt, Created int64
	History          []ReminderEvent // 最近的记录，旧的在前
}

// ReminderEvent 是提醒记录里的一笔。Kind：create、edit、pause、resume、ring、done、cancel、defer
type ReminderEvent struct {
	At       int64
	Kind, By string
	Note     string
}

// ReminderDraft 是界面上新建提醒的内容；At 写成 2026-11-10 09:00（本机时区）
type ReminderDraft struct {
	Text, At string
	Repeat   ReminderRepeat
	Targets  []string
}

// ReminderPatch 是界面上的修改：nil 的项不改
type ReminderPatch struct {
	Text, At *string
	Repeat   *ReminderRepeat
	Targets  *[]string
	Paused   *bool
}

// ReminderOptions 是编辑框要知道的：提醒开没开、QQ 能不能选、默认发到哪
type ReminderOptions struct {
	Enabled, AllowQQ bool
	DefaultTargets   []string
}

// ReminderControl 是桌面可选的提醒入口（设置页）。k 是这个角色的本地私聊；身份固定是主人。
// 她建的和主人建的按钮一样，都能改、取消、删（规格-提醒 第九节）。
type ReminderControl interface {
	ReminderOptions() ReminderOptions
	CreateReminder(context.Context, ConversationKey, ReminderDraft) error
	UpdateReminder(context.Context, ConversationKey, int, ReminderPatch) error
	FinishReminder(ctx context.Context, k ConversationKey, id int, done bool) error
	DeleteReminder(context.Context, ConversationKey, int) error
}

// GoalInfo 是一个目标的详情。预算 0 表示不限；LastReason 是裁判最近一次的理由（没判过为空），
// 每轮的理由后台没有逐轮存，只有最近一次。
type GoalInfo struct {
	Text, State, PauseReason string // State：active、paused
	Rounds, MaxRounds        int    // 已经跑完的轮数、轮数上限（0 不限）
	TokensUsed, MaxTokens    int64
	StartedAt                int64 // Unix 毫秒
	LastReason               string
	LastAchieved, LastFailed bool  // 裁判最近一次：判了达成、裁判自己出错
	LastAt                   int64 // Unix 毫秒，0 表示没判过
}

// LoopInfo 是一个定期任务。时间都是 Unix 毫秒，0 表示没有；IntervalSec 为 0 表示由她自己定节奏。
// Status：running、paused（停了的不在这里）。LastResult：content（有内容）、quiet（无事）、error（出错，LastNote 是原因），空是还没跑过。
type LoopInfo struct {
	ID          int
	Prompt      string
	Status      string
	IntervalSec int64
	NextRun     int64
	NextReason  string
	Runs        int
	LastRun     int64
	LastResult  string
	LastNote    string
	Expires     int64
	// 日程：Kind 是 interval / daily / weekly / self；At 是 "15:04"，Days 0 是周日（只有每天、每周几有）
	Kind    string
	At      string
	Days    []int
	Notify  string    // content（有内容和出错都提醒）、errors（只提醒出错）、none（不提醒）
	NewChat bool      // 每一轮在新开的话题里跑
	History []LoopRun // 最近的运行记录，旧的在前
}

// LoopSpec 是定期任务页表单填出来的一个任务（新建和修改共用）。Kind 为 interval 时看 IntervalSec，
// daily 看 At，weekly 看 At 和 Days，self 是让她自己定下次。Notify 空等于 content。
type LoopSpec struct {
	Prompt      string
	Kind        string
	IntervalSec int64
	At          string
	Days        []int
	Notify      string
	NewChat     bool
}

// LoopRun 是定期任务的一次运行记录。
type LoopRun struct {
	At     int64
	Result string // content、quiet、error
	Note   string // 出错原因、“错过了 X 次”
	Missed int
}

// LoopControl 是桌面可选的定期任务控制入口（定期任务页的暂停、继续、删除）。
// 会话由调用方给，只认本地私聊；暂停、继续、删除和输入框里的 /loop pause、/loop resume、/loop stop 同一套逻辑，
// 删除就是停止（任务留在表里，列表里不再出现）。不新增权限：它不让她多做任何事，只是停下或放开原有的任务。
type LoopControl interface {
	PauseLoop(context.Context, ConversationKey, int) error
	ResumeLoop(context.Context, ConversationKey, int) error
	RemoveLoop(context.Context, ConversationKey, int) error
	// AddLoop 在这个会话里新建定期任务，返回编号；EditLoop 把任务改成 spec（没变的日程不重算下次时间，
	// 暂停中的仍是暂停）。校验和 /loop 命令同一套，写法不对返回能直接显示的说明。
	AddLoop(context.Context, ConversationKey, LoopSpec) (int, error)
	EditLoop(context.Context, ConversationKey, int, LoopSpec) error
}

// LocalStatusReader 是桌面可选的读接口；有目标、定期任务或自动轮的会话才出现在订阅里，其余会话没有这一项。
type LocalStatusReader interface {
	ReadLocalStatus(context.Context, ConversationKey) (LocalStatus, error)
	WatchLocalStatus(context.Context) (Subscription[[]LocalStatus], error)
}
