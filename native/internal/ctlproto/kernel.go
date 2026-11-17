package ctlproto

// CmdKernel 是回合内核的故障在命令行里的写法（质检 X-07、后续路线 P0-4）：
//
//	kernel faults                  列出现在没解除的故障（哪个会话、什么时候、什么原因、重试了几次）。只读
//	kernel retry [<角色> <会话>]   重试故障：重写没写成的写入并读回确认，成功就恢复受理；不写角色和会话 = 全部故障
//
// 故障是内核记下的“这个会话（或整个内核）的状态写不进记录库，暂停受理新输入”。重试是主人和运维的操作，不在模型能调的工具里。
// 没恢复就如实失败（退出码 1），回复里带着还没恢复的那些；不会把失败说成成功。
const CmdKernel = "kernel"

// 子命令
const (
	KernelFaultsCmd = "faults"
	KernelRetryCmd  = "retry"
)

// KernelFault 是一条故障。时间是毫秒时间戳；Reason 是日常说法，Detail 是最近一次的原始错误（只当数据看）。
type KernelFault struct {
	Scope        string `json:"scope"` // kernel（整个内核）| conversation（这个会话）
	Character    string `json:"character,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Kind         string `json:"kind"`
	Reason       string `json:"reason"`
	Detail       string `json:"detail,omitempty"`
	At           int64  `json:"at"`
	Tries        int    `json:"tries"`
}

// KernelFaults 是 kernel faults 的 data
type KernelFaults struct {
	Faults []KernelFault `json:"faults"`
	// StrayEnds 是没人认领的活动退出记录条数（压缩、检查点、记忆这类不属于前台运行的活动没记进库的退出）；不挡受理，kernel retry 全部时一起重写
	StrayEnds int `json:"strayEnds,omitempty"`
}

// KernelRetry 是 kernel retry 的 data：Cleared 是这次解除的，Remaining 是还没恢复的（有就是失败）
type KernelRetry struct {
	Cleared   []KernelFault `json:"cleared"`
	Remaining []KernelFault `json:"remaining"`
	StrayEnds int           `json:"strayEnds,omitempty"` // 重试后仍没写成的“没人认领的活动退出记录”条数
}
