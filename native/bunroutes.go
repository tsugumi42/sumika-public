package main

// 桥的方法分流表（规格-本地私聊切到Bun 第四节）。backend.local 是 bun 时，桥持两个客户端：
// 到 Go 的进程内管道，和到 Bun 子进程的连接；每个协议方法归哪一边写死在这里，不靠名字猜，也不靠“表外默认走 Go”。
// 规则：会写本地数据（local.db、本地状态文件）的一律走 Bun；读本地数据的可以走 Go（Go 只读打开 local.db）；QQ 全部走 Go。
// go 模式（默认）桥只有一个客户端，这张表不起作用。
//
// 表要覆盖 rpcwire.Methods 的每一个方法（bunroutes_test.go 对着生成的清单查）：协议里新加了方法而这里没归类，测试就红。
// 没归类的方法在运行时也不放行（via 回错），免得一个新的写方法默认走 Go 去写本地。

import "sumika-native/internal/rpcwire"

type route int

const (
	routeUnset  route = iota // 没归类：不许走
	routeGo                  // Go：读方法、QQ
	routeBun                 // Bun：会写本地数据的
	routeByKey               // 看会话键的平台：local 走 Bun，别的走 Go（read_mark.set）
	routeMerged              // 两边都订，桥把两份拼起来（local_status 的订阅：Bun 管本地的状态，Go 管 QQ 的“正在输入”）
)

var routes = buildRoutes()

func buildRoutes() map[string]route {
	m := map[string]route{}
	for _, name := range []string{
		// 握手、运行状态、概览、消息、会话订阅、媒体、表情、设置：Go 读（local.db 只读打开，Bun 写了靠 data_version 叫醒）
		rpcwire.MethodInitialize,
		rpcwire.MethodRuntimeRead, rpcwire.MethodRuntimeSubscribe, rpcwire.MethodRuntimeUnsubscribe, rpcwire.MethodRuntimeSnapshot,
		rpcwire.MethodOverviewRead, rpcwire.MethodOverviewSubscribe, rpcwire.MethodOverviewUnsubscribe, rpcwire.MethodOverviewSnapshot,
		rpcwire.MethodMessagesRead,
		rpcwire.MethodConversationSubscribe, rpcwire.MethodConversationUnsubscribe, rpcwire.MethodConversationSnapshot,
		rpcwire.MethodMediaRead, rpcwire.MethodFacesIndex, rpcwire.MethodFacesPNG, rpcwire.MethodSettingsRead,
		rpcwire.MethodAvatarRead, rpcwire.MethodForwardRead, rpcwire.MethodVideoPrepare,
	} {
		m[name] = routeGo
	}
	for _, name := range []string{
		// 本地私聊：发送、草稿、停止、图片、审批、提交、运行、放行、撤回、提问
		rpcwire.MethodLocalSend, rpcwire.MethodLocalSendImages, rpcwire.MethodLocalSendParts, rpcwire.MethodLocalDraft, rpcwire.MethodLocalSaveImage,
		rpcwire.MethodLocalStop, rpcwire.MethodLocalApprovalsSubscribe, rpcwire.MethodLocalApprovalsUnsubscribe, rpcwire.MethodLocalApprovalsSnapshot,
		rpcwire.MethodLocalDecide, rpcwire.MethodLocalSubmit, rpcwire.MethodLocalStopRun,
		rpcwire.MethodLocalRunSubscribe, rpcwire.MethodLocalRunUnsubscribe, rpcwire.MethodLocalRunSnapshot,
		rpcwire.MethodHeldList, rpcwire.MethodHeldRelease, rpcwire.MethodHeldDiscard, rpcwire.MethodLocalWithdraw,
		rpcwire.MethodLocalQuestionsSubscribe, rpcwire.MethodLocalQuestionsUnsubscribe, rpcwire.MethodLocalQuestionsSnapshot, rpcwire.MethodLocalAnswer,
		// 本地群聊、悄悄话和它的日志
		rpcwire.MethodLocalGroupCreate, rpcwire.MethodLocalGroupSend, rpcwire.MethodLocalGroupProcess, rpcwire.MethodLocalGroupStop,
		rpcwire.MethodWhisperSend, rpcwire.MethodWhisperLogRead, rpcwire.MethodWhisperLogEntry, rpcwire.MethodWhisperLogSubscribe,
		rpcwire.MethodWhisperLogUnsubscribe, rpcwire.MethodWhisperLogSnapshot, rpcwire.MethodWhisperLogCancel,
		// 目标、定期任务、提醒的状态和控制（状态订阅在桥里和 Go 的 QQ“正在输入”合并）
		rpcwire.MethodLocalStatusRead,
		rpcwire.MethodLoopAdd, rpcwire.MethodLoopEdit, rpcwire.MethodLoopPause, rpcwire.MethodLoopResume, rpcwire.MethodLoopRemove,
		rpcwire.MethodReminderOptions, rpcwire.MethodReminderCreate, rpcwire.MethodReminderUpdate, rpcwire.MethodReminderFinish, rpcwire.MethodReminderDelete,
		// 放行规则、会话审批档、本地工作区、检查点
		rpcwire.MethodApprovalRulesList, rpcwire.MethodApprovalRulesRevoke, rpcwire.MethodApprovalRulesAdd,
		rpcwire.MethodConversationApprovalRead, rpcwire.MethodConversationApprovalSet,
		rpcwire.MethodWorkspaceRead, rpcwire.MethodWorkspaceSet, rpcwire.MethodWorkspaceClear, rpcwire.MethodWorkspaceRecent,
		rpcwire.MethodCheckpointsList, rpcwire.MethodCheckpointsPreview, rpcwire.MethodCheckpointsRestore, rpcwire.MethodCheckpointsProgress, rpcwire.MethodCheckpointsCancel,
		rpcwire.MethodCheckpointProgressSubscribe, rpcwire.MethodCheckpointProgressUnsubscribe, rpcwire.MethodCheckpointProgressSnapshot,
		// sumika-ctl 的自检对话、目标和定期任务、悄悄话（规格-本地私聊切到Bun 第十一节）：执行体在 Bun。界面不调，ctl 经 ctl_bun.go 直接用 bunLink
		rpcwire.MethodSelftestChat, rpcwire.MethodSelftestSlash, rpcwire.MethodSelftestWhisper, rpcwire.MethodSelftestWhisperStatus, rpcwire.MethodSelftestWhisperCancel,
	} {
		m[name] = routeBun
	}
	m[rpcwire.MethodReadMarkSet] = routeByKey
	for _, name := range []string{rpcwire.MethodLocalStatusSubscribe, rpcwire.MethodLocalStatusUnsubscribe, rpcwire.MethodLocalStatusSnapshot} {
		m[name] = routeMerged
	}
	return m
}
