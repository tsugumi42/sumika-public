package api

import (
	"context"
	"errors"
)

// 发送分块消息被后台拒收的两类原因；错误文字以"第 N 块："开头，界面据此标在出错的那一块上。
var (
	// ErrQuoteTarget 是引用的那条消息找不到：不存在、在别的会话、她看不到、或不是聊天消息，几种情形不区分。
	ErrQuoteTarget = errors.New("引用的消息不存在，或在这个会话里看不到")
	// ErrPart 是某一块本身不合格：空块、附件缺来源或种类不对、块太多。
	ErrPart = errors.New("消息的某一块不合格")
)

// LocalPart 是主人在本地私聊里一条消息的一个部分（规格-引用）。一条消息由有序的部分组成，顺序有意义：
// Quote 非 nil 是"引用回复块"：被引用的出处和选中的片段，加上针对这一段的回复文字和这一块自己的附件；
// Quote 为 nil 是"普通文字块"：不属于任何引用的话，也可以带附件。每块至少要有文字或附件。
type LocalPart struct {
	Quote       *LocalQuote
	Text        string
	Attachments []LocalAttachment
	// Mentions、All 只有悄悄话用（规格-群聊引用和路由）：这一块（引用卡）指给哪些角色（角色 ID），All 是“所有人”。
	// 本地私聊的分块消息不带这两项。
	Mentions []string
	All      bool
}

// LocalQuote 是引用回复块的出处。后台核对：消息必须在这个会话里并且她看得到；选中的文字必须真出现在原文里。
type LocalQuote struct {
	Ref EventRef // 被引用的那条；Conversation 必须等于发送的会话，Event.EventID 是事件 id
	// Text 是选中的文字；空白表示引用整条。
	Text string
	// Start、End 是选区在原文（MessageView.Text）里的字符（rune，不是字节）偏移 [Start, End)，可选：
	// 对得上 Text 就直接用，对不上或没给就在原文里找 Text 的第一处。
	Start, End int
}

// LocalAttachment 是块上的附件，现在只有数据结构：Kind 为 image 时要给 Media（媒体库里已有的图，后台核对凭据可见）
// 或 Path（本地文件路径）；Kind 为 file 时给 Path。Name 是显示名，可选。
// 真图怎么交给模型由 feat/real-images 那条线接；现在给模型的是文字占位（规格-引用第五节）。
type LocalAttachment struct {
	Kind  string // image | file
	Media *MediaRef
	Path  string
	Name  string
	// Width、Height 是图片的像素宽高（已按 EXIF 转正）：只在读回来时由后台给，发送时不用填；读不出来是 0。
	// 界面靠它在图加载前就占好位置，加载完不跳。
	Width, Height int
}

// MessagePart 是读回来的一块：HasQuote 为 true 时 Ref、Quote 是出处（和 MessageView.ReplyTo、Reply 同一套语义：
// 被引用的那条后来看不到时 Quote.State 为 Unsupported，片段仍在 Quote 之外的存档里不交出）。
type MessagePart struct {
	HasQuote    bool
	Ref         *EventRef
	Quote       Result[QuoteView]
	Text        string
	Attachments []LocalAttachment
}

// LocalPartsChat 是 LocalChat 的可选扩展：发送分块消息。只有一个普通文字块、没有附件，和 SendLocal 完全一样。
type LocalPartsChat interface {
	SendLocalParts(ctx context.Context, key ConversationKey, parts []LocalPart, draft func(LocalDraft)) error
}

// WhisperChat 是桌面入口的可选扩展：把几块引用卡作为悄悄话交给被指的角色（规格-群聊引用和路由）。
// key 是引用所在的来源会话（本地群聊、QQ 私聊、QQ 群；本地私聊的引用是带引用的回复，不走这里）。
// 悄悄话不产生任何对外消息，也不触发来源会话的回合；任何一块不合格整条拒绝，错误以“第 N 块：”开头。
type WhisperChat interface {
	SendWhisper(ctx context.Context, key ConversationKey, parts []LocalPart) (WhisperReceipt, error)
}

// WhisperReceipt 是发出的悄悄话：批号和被唤起的角色（按第一次出现排序）。
type WhisperReceipt struct {
	Batch      string
	Characters []string
}

// LocalImageSaver 是可选扩展：把主人粘贴进输入框的 PNG 存进媒体库，返回可放进 LocalPart.Attachments 的附件
// （Kind 为 image、Path 指向媒体库里的文件）。只收本地私聊的会话键；不是 PNG 或太大会报错。
type LocalImageSaver interface {
	SaveLocalImage(ctx context.Context, key ConversationKey, png []byte) (LocalAttachment, error)
}
