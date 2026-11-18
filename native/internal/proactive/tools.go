package proactive

import "sumika-native/internal/tools"

// 话题线里的工具声明（规格-主动联系 第八、九节）：声明在这里，谁能看见、怎么执行由宿主定（app/proactivethread.go）。
// 参数都是对象；线里没有执行工具，这几个只碰线自己的状态和"发给主人"这一件事。
const (
	ToolOwnerPresence   = "owner_presence"
	ToolThreadSend      = "thread_send"
	ToolStayQuiet       = "stay_quiet"
	ToolEndThread       = "end_thread"
	ToolHandBack        = "hand_back"
	ToolProactiveSnooze = "proactive_snooze"
)

// ToolNames 是这几个工具的名字，policy.Specs 登记和改坏检查都用它
var ToolNames = []string{ToolOwnerPresence, ToolThreadSend, ToolStayQuiet, ToolEndThread, ToolHandBack, ToolProactiveSnooze}

const emptyParams = `{"type":"object","properties":{},"additionalProperties":false}`

// Decls 是各工具给模型看的声明
var Decls = map[string]tools.Declaration{
	ToolOwnerPresence: {
		Name:        ToolOwnerPresence,
		Description: "看所有者最近在哪里活跃、哪里发得出去。返回可去的地方，每个带一个句柄（t1、t2……，只在这条线里有效），以及他最后一次在那里说话是多久以前。想找他之前先看一眼；不会列出群友或别人。",
		Parameters:  emptyParams,
	},
	ToolThreadSend: {
		Name:        ToolThreadSend,
		Description: "把一句话发给所有者。target 是 owner_presence 给的句柄，text 是要说的话（短一点，像平时发消息），reason 用一句话写你为什么现在找他（第一次发的时候写，界面上他能看到）。这是他收到话的唯一办法，你直接写在这里的字他看不到。返回会告诉你有没有发出去、为什么发不出去。",
		Parameters:  `{"type":"object","required":["target","text"],"properties":{"target":{"type":"string","description":"owner_presence 返回的句柄，例如 t1"},"text":{"type":"string","description":"要发给所有者的话"},"reason":{"type":"string","description":"一句话：为什么现在找他"}},"additionalProperties":false}`,
	},
	ToolStayQuiet: {
		Name:        ToolStayQuiet,
		Description: "这次不找他。没有由头、他看起来不方便、或者刚找过，就用它收住；reason 用一句话写原因，只记在本机日志里。",
		Parameters:  `{"type":"object","properties":{"reason":{"type":"string","description":"一句话：为什么这次不找他"}},"additionalProperties":false}`,
	},
	ToolEndThread: {
		Name:        ToolEndThread,
		Description: "收掉这条线。聊完了、或者不想再等了，写一句结论（发生了什么、有什么要记得的），之后这条线不再醒。",
		Parameters:  `{"type":"object","required":["conclusion"],"properties":{"conclusion":{"type":"string","description":"一句话结论"}},"additionalProperties":false}`,
	},
	ToolHandBack: {
		Name:        ToolHandBack,
		Description: "他刚回的话和这条线要说的事无关时，把这句交回给平时的对话去回，这条线里不再接那个地方的话。只在转述来的所有者消息上用。",
		Parameters:  emptyParams,
	},
	ToolProactiveSnooze: {
		Name:        ToolProactiveSnooze,
		Description: "所有者说别烦、今天别来找他时用：到时间之前不再主动联系他，并收掉开着的线。hours 是多少小时（不填用默认，有上限），reason 写他是怎么说的。",
		Parameters:  `{"type":"object","properties":{"hours":{"type":"number","description":"多少小时内不再找他"},"reason":{"type":"string","description":"一句话：他怎么说的"}},"additionalProperties":false}`,
	},
}
