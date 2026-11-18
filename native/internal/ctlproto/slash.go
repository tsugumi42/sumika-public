package ctlproto

// CmdGoal、CmdLoop 是 /goal 和 /loop 在命令行里的写法（规格-本机控制接口第十节）。
// 它们和主窗口输入框里的斜杠命令走同一套后台逻辑；会话由后台按角色拼成自检话题，请求里没有会话 id。
//
//	goal show|set <目标>|edit <目标>|pause|resume|clear
//	loop list|add [间隔] <任务>|edit <编号> [间隔] [新任务文字]|pause <编号>|resume <编号>|remove <编号>
//
// 角色用请求的 character 字段（空 = 主窗口当前角色）。
const (
	CmdGoal = "goal"
	CmdLoop = "loop"
	// CmdTrust 是 /trust：项目说明（AGENTS.md 等）拒绝过的，想改主意就让她下一次重新问（不带子命令，只认 --character）。
	// 不当场替主人信任：信任只能是主人在审批卡上点的。
	CmdTrust = "trust"
)

// GoalSubs、LoopSubs 是两条命令认的子命令（服务端校验用，别处不要另写一份）
var (
	GoalSubs = []string{"show", "set", "edit", "pause", "resume", "clear"}
	LoopSubs = []string{"list", "add", "edit", "pause", "resume", "remove"}
)

// GoalState 是一个会话里目标的现状
type GoalState struct {
	Text        string `json:"text"`
	Status      string `json:"status"` // active / paused
	Rounds      int    `json:"rounds"`
	MaxRounds   int    `json:"maxRounds"` // 0 = 不限
	TokensUsed  int64  `json:"tokensUsed"`
	PauseReason string `json:"pauseReason,omitempty"`
	LastJudge   string `json:"lastJudge,omitempty"` // 裁判上一次给的理由
	Line        string `json:"line"`                // 界面上那一行状态
}

// LoopTask 是一个定期任务（和 /loop 列表里的一行同一份）
type LoopTask struct {
	ID          int    `json:"id"`
	Status      string `json:"status"` // running / paused / stopped
	Prompt      string `json:"prompt"`
	IntervalSec int64  `json:"intervalSec"` // 0 = 她自己定节奏
	Runs        int    `json:"runs"`
	NextRun     string `json:"nextRun,omitempty"` // RFC 3339；没在等下一次是空
	Line        string `json:"line"`
	// 日程和选项：Kind 是 interval / daily / weekly / self；At 是 "15:04"；Days 周一是 1、周日是 7（和 --weekly 同一写法）
	Kind    string `json:"kind"`
	At      string `json:"at,omitempty"`
	Days    []int  `json:"days,omitempty"`
	Notify  string `json:"notify"` // content / errors / none
	NewChat bool   `json:"newChat"`
}

// LoopArgs 是 loop add|edit 的选项（和定期任务页的表单是同一批字段）。日程四选一：Every 固定间隔（30m、2h、1d）、
// Daily 每天某时（15:04）、Weekly 每周几（1,3,5，周一是 1、周日是 7）加 At 几点、Self 让她自己定下次；都不写 add 就是自己定，
// edit 就是日程不改。NewChat 指针区分没写（edit 不改）、每次新聊天（true）、同一个聊天（false）。Notify 空是默认（add）或不改（edit）。
type LoopArgs struct {
	Every   string `json:"every,omitempty"`
	Daily   string `json:"daily,omitempty"`
	Weekly  string `json:"weekly,omitempty"`
	At      string `json:"at,omitempty"`
	Self    bool   `json:"self,omitempty"`
	NewChat *bool  `json:"newChat,omitempty"`
	Notify  string `json:"notify,omitempty"`
}

// LoopPatch 是服务端校验、换算好的 LoopArgs：Kind 空表示日程不改；Days 已换成周日是 0。
type LoopPatch struct {
	Kind        string
	IntervalSec int64
	At          string
	Days        []int
	NewChat     *bool
	Notify      string
}

// SlashResult 是 goal、loop 的 data。Reply 是这条命令给主人看的回复（和输入框里发同一条命令得到的系统行同一句话）；
// Started 为真是这条命令让她排上了一轮（设目标、继续目标、新建定期任务）；一轮的结果用 chat 看不到，看会话或 goal show。
type SlashResult struct {
	Character    string     `json:"character"`
	Topic        string     `json:"topic"`
	Conversation string     `json:"conversation"`
	Reply        string     `json:"reply"`
	Started      bool       `json:"started"`
	Goal         *GoalState `json:"goal"`  // 这个会话现在的目标；没有是 null
	Loops        []LoopTask `json:"loops"` // 这个会话的定期任务（含已停止、还没清掉的）；goal 命令里是空数组
}
