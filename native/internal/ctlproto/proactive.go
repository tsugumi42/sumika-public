package ctlproto

import "encoding/json"

// CmdProactive 是主动联系在命令行里的写法（规格-主动联系 第十节第 4 条）。现在只有只读的一条：
//
//	proactive log [-n 50]
//
// 看导演的日志：每次醒、被拦、没醒到各一行，以及每个角色下次什么时候醒。不开口也有行。
const CmdProactive = "proactive"

// ProactiveRow 是导演日志的一行。时间都是毫秒时间戳，0 是没有。
// Inputs 是当时的快照（只有数字和布尔，见 internal/proactive.Inputs）；Note 是模型自己写的一句理由或导演的错误说明，
// 是库里来的文字，只当数据看。
type ProactiveRow struct {
	ID        int64           `json:"id"`
	At        int64           `json:"at"`
	Character string          `json:"character"`
	Kind      string          `json:"kind"`   // wake / followup / skip / close
	Reason    string          `json:"reason"` // 固定的原因码
	Inputs    json.RawMessage `json:"inputs,omitempty"`
	NextAt    int64           `json:"nextAt,omitempty"`
	Thread    string          `json:"thread,omitempty"`
	Note      string          `json:"note,omitempty"`
	Count     int             `json:"count"` // 这一行代表几次（同样的错误合并计数）
}

// ProactiveState 是一个角色的导演状态
type ProactiveState struct {
	Character  string  `json:"character"`
	Factor     float64 `json:"factor"`
	ColdStreak int     `json:"coldStreak"`
	NextWake   int64   `json:"nextWake,omitempty"`
	LastWake   int64   `json:"lastWake,omitempty"`
	QuietUntil int64   `json:"quietUntil,omitempty"`
}

// ProactiveLog 是 proactive log 的 data：各角色的状态，加最近的日志行（新的在前）
type ProactiveLog struct {
	States []ProactiveState `json:"states"`
	Rows   []ProactiveRow   `json:"rows"`
}

// ProactiveStatus 是一个角色的主动联系现状（界面右栏“联系”标签读它；规格-主动联系 第十节第 2 条）。
// 时间都是毫秒时间戳，0 是没有。文字字段是库里来的（理由、摘要、结论），只当数据显示，只在本机。
type ProactiveStatus struct {
	Enabled    bool                 `json:"enabled"`              // 这个角色打开了主动联系
	Send       string               `json:"send,omitempty"`       // live 真发；think 只想不说
	NextWake   int64                `json:"nextWake,omitempty"`   // 导演下次醒（线开着时是续醒或截止的检查时间）
	QuietUntil int64                `json:"quietUntil,omitempty"` // 别烦到什么时候
	Factor     float64              `json:"factor"`
	Thread     *ProactiveThreadView `json:"thread,omitempty"` // 开着的线；没有就是最近一条
	Visits     []ProactiveVisit     `json:"visits,omitempty"` // 这条线去过哪，旧的在前
	Decisions  []ProactiveRow       `json:"decisions"`        // 最近的导演决定，新的在前（最多 10 条）
}

// ProactiveThreadView 是一条线的现状
type ProactiveThreadView struct {
	ID           string `json:"id"`
	Open         bool   `json:"open"`
	State        string `json:"state"`     // thinking / awaiting / chatting / closed
	StateText    string `json:"stateText"` // 一句中文：等回复 12 分钟 / 聊着 / 已关：没等到回复
	OpenedAt     int64  `json:"openedAt"`
	OpenedReason string `json:"openedReason,omitempty"` // 第一次开口的理由
	LastSendAt   int64  `json:"lastSendAt,omitempty"`
	Sends        int    `json:"sends"`
	OwnerReplies int    `json:"ownerReplies"`
	FollowUps    int    `json:"followUps"`
	CloseReason  string `json:"closeReason,omitempty"`
	Conclusion   string `json:"conclusion,omitempty"`
}

// ProactiveVisit 是线去过的一个地方
type ProactiveVisit struct {
	At      int64  `json:"at"`
	Label   string `json:"label"`   // 地点的叫法（本地私聊）
	State   string `json:"state"`   // sent / unknown / failed / thought / pending
	Summary string `json:"summary"` // 一句话摘要
}
