package api

import "context"

// WorkspaceInfo 是一个本地私聊会话现在用的工作目录（按会话选工作目录，规格-按会话选工作目录）。
// QQ、本地群聊没有这个概念：接口回 store.ErrDisplayScope，不返回任何路径。
type WorkspaceInfo struct {
	CWD, Root string
	// Name 是项目名（项目根的文件夹名），界面药丸和会话列表显示它
	Name string
	// Bound 为假表示这个会话没选目录，用的是全局默认（设置里的 tools.workdir）
	Bound bool
	// Problem 不空表示目录现在用不了（不存在、不是文件夹、没权限、没响应……），原因带路径；这个会话发消息会被拒，不会退到别的目录
	Problem string
}

// LocalWorkspaces 是桌面可选的"这个会话的工作目录"入口；只有主人在本地私聊能改，模型没有对应工具。
// 改完从下一轮生效，在跑的一轮不受影响。
type LocalWorkspaces interface {
	ReadLocalWorkspace(context.Context, ConversationKey) (WorkspaceInfo, error)
	// SetLocalWorkspace 的 dir 要是绝对路径，已存在；不合格原样报错，不建、不改
	SetLocalWorkspace(context.Context, ConversationKey, string) (WorkspaceInfo, error)
	// ClearLocalWorkspace 恢复全局默认
	ClearLocalWorkspace(context.Context, ConversationKey) (WorkspaceInfo, error)
	// RecentWorkspaces 是最近用过的目录（新的在前，最多 8 个），选目录的菜单用
	RecentWorkspaces(context.Context) ([]string, error)
}
