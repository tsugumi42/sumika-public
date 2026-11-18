package store

// 这一份是唯一登记"渠道 → 平台""会话 → 平台"的地方（规格-聊天记录按平台分库 第二节）。
// 别处不许自己拆会话串选库：手上有渠道的（中间件的 Conv.Channel、内核的 Audience.Channel）用 PlatformOf，
// 只有会话串的（压缩、插话、停止、补读、记忆的事件来源）用 PlatformOfConversation。

import (
	"fmt"
	"sort"
	"strings"
)

// Platform 是登记过的一个平台：渠道名、平台名（库文件的名字 paths.ChatDB，也对应 migrations/ 下的同名目录）、
// 只读打开历史时是不是必须有这个库
type Platform struct {
	Channel  string
	Name     string
	Required bool
}

// Registry 是一份"渠道 → 平台"的登记（规格-多平台装配第 2 步）。生产只用 DefaultRegistry；测试要加虚构平台就自己造一份
// 交给 OpenGoWith、OpenGoReadOnlyWith，不往全局表里加东西。零值什么都没登记，用它查渠道一律报错
type Registry struct{ list []Platform }

// DefaultRegistry 是现在的两个平台
var DefaultRegistry = mustRegistry(
	Platform{Channel: "console", Name: "local", Required: true}, // 主窗口、本地群聊、本地话题：凡是 Audience.Channel == "console" 的都算本地
	Platform{Channel: "qq", Name: "qq", Required: true},
)

// NewRegistry 校验并造一份登记：渠道名不能空，平台名要过 ValidPlatform，渠道和平台都不能重复
func NewRegistry(ps ...Platform) (Registry, error) {
	channels, names := map[string]bool{}, map[string]bool{}
	for _, p := range ps {
		switch {
		case p.Channel == "":
			return Registry{}, fmt.Errorf("平台 %q 的渠道名是空的", p.Name)
		case !ValidPlatform(p.Name):
			return Registry{}, fmt.Errorf("平台名 %q 不能用：只认小写字母、数字、连字符，1 到 32 个字", p.Name)
		case channels[p.Channel]:
			return Registry{}, fmt.Errorf("渠道 %q 登记了两遍", p.Channel)
		case names[p.Name]:
			return Registry{}, fmt.Errorf("平台 %q 登记了两遍", p.Name)
		}
		channels[p.Channel], names[p.Name] = true, true
	}
	return Registry{list: append([]Platform(nil), ps...)}, nil
}

func mustRegistry(ps ...Platform) Registry {
	r, err := NewRegistry(ps...)
	if err != nil {
		panic(err)
	}
	return r
}

// Platforms 是登记过的平台，照登记的先后
func (r Registry) Platforms() []Platform { return append([]Platform(nil), r.list...) }

// PlatformOf 是渠道名对应的平台。没登记的渠道报错，不落进别的库（规格-聊天记录按平台分库第六节：不许"取不到就落到本地库"）
func (r Registry) PlatformOf(channel string) (string, error) {
	for _, p := range r.list {
		if p.Channel == channel {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("渠道 %q 没有登记平台（登记过的只有 %s）", channel, strings.Join(r.channelNames(), "、"))
}

// PlatformOfConversation 从一个会话串取平台：第一个 ":" 前面那段是渠道（types.Conversation.String：群 "渠道:group:群号"，
// 私聊 "渠道:private:她的号:对方的号"，主窗口是 "console:" 开头），再交给 PlatformOf
func (r Registry) PlatformOfConversation(conversation string) (string, error) {
	i := strings.IndexByte(conversation, ':')
	switch {
	case i < 0:
		return "", fmt.Errorf("会话 %q 里没有渠道（要 \"渠道:...\"）：不知道进哪个库", conversation)
	case i == 0:
		return "", fmt.Errorf("会话 %q 的渠道是空的：不知道进哪个库", conversation)
	}
	return r.PlatformOf(conversation[:i])
}

// PlatformOf 照默认登记取渠道对应的平台
func PlatformOf(channel string) (string, error) { return DefaultRegistry.PlatformOf(channel) }

// PlatformOfConversation 照默认登记从会话串取平台
func PlatformOfConversation(conversation string) (string, error) {
	return DefaultRegistry.PlatformOfConversation(conversation)
}

// reservedPlatforms 不能当平台名：chat、events、trace 是 migrations/ 下已有的目录，拿来当平台名会跑错迁移；
// 其余是 Windows 的设备名，nul.db 这种文件名在 Windows 上打不开或写进空设备
var reservedPlatforms = map[string]bool{
	"chat": true, "events": true, "trace": true,
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidPlatform 查一个平台名能不能当库文件的名字：只认小写字母、数字、连字符，1 到 32 个字
// （大写、点、斜杠、".." 都不行：平台名会直接拼进文件路径），也不能是 reservedPlatforms 里的
func ValidPlatform(name string) bool {
	if len(name) < 1 || len(name) > 32 || reservedPlatforms[name] {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// channelNames 是登记过的渠道，排好序（错误文字里用）
func (r Registry) channelNames() []string {
	out := make([]string, 0, len(r.list))
	for _, p := range r.list {
		out = append(out, p.Channel)
	}
	sort.Strings(out)
	return out
}
