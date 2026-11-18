package api

// SlashCommand 是一条可以在本地私聊输入框里用的斜杠命令，给输入框做补全用（只列名字和一句说明）。
type SlashCommand struct {
	Name    string // 不带斜杠
	Summary string
}

// SlashLister 是可选扩展：列出本地私聊认的斜杠命令。名单从后台的命令登记表现取，界面不另存一份。
// 按名字排好序；没有登记表（只看历史）时返回空。
type SlashLister interface {
	SlashCommands() []SlashCommand
}
