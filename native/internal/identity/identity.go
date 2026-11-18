// Package identity 是给模型看的消息头、聊天记录里固定的身份词：括号里的身份、她自己的行、她的号上别人发的。
// 卡上的 ownerName 不能占用它们，否则所有者和那一类人的行头会一样，模型分不清谁是谁。
// 渠道中间件、记忆整理和角色卡校验都从这里取，别各写一份。
package identity

const (
	OwnerDefault = "用户"  // 卡上没写称呼时，所有者那一行的身份
	Self         = "你"   // 她自己的行："铃音（你）"
	SelfAccount  = "你的号" // 她的号上别人发的
	GroupPeer    = "群友"
	Friend       = "好友"
	Character    = "角色" // 别的角色
)

// Reserved 是 ownerName 不能用的词：消息头和聊天记录里写死的身份词。
// 加新的固定身份词只改这里，角色卡校验跟着变。
var Reserved = []string{OwnerDefault, Self, SelfAccount, GroupPeer, Friend, Character}
