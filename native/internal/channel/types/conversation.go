// Package types 是渠道中间件和平台适配器之间说的"普通话"（规格-渠道中间件 第三、五节）：会话、段、入站事件、通知、
// 连接状态、要发的段、回执，加上适配器、接收端、出口三个接口。
//
// 只放类型、常量和几个纯函数的小方法，不放实现：中间件（internal/channel）、OneBot 适配器（internal/onebot）、
// 平台插件（internal/channel/qq）、复读插件都引它，谁也不用认识谁。这里不出现任何协议的字段名（post_type、sub_type、at.qq 这类），
// 也不替哪个平台做策略（认人、判闸门、写库）——那是中间件的事（ADR-177 的四层）。
//
// 号码（群号、用户号、平台消息号）一律是字符串，不转数字：平台的号会超过 2^53，转成浮点会往返变样、把不同的消息当成同一条（清单⑪）。
package types

import (
	"errors"
	"fmt"
	"strings"
)

// 形式（词汇表"形式"）的两个值；和 audience.Audience.Scene 是同样的字符串，直接比较
const (
	ScenePrivate = "private"
	SceneGroup   = "group"
)

// Conversation 是一个会话（词汇表"会话"）。结构体是真身，字符串只用来存库和交给内核（turn.Input.Conversation）：
// 判断场合只看 Channel 和 Scene，不拆字符串、不看前缀（清单⑤：Bun 的 console.ts:224 就是靠前缀认渠道的，换平台要改一圈）
type Conversation struct {
	Channel string // 渠道名，等于适配器的 Name()
	Scene   string // ScenePrivate / SceneGroup
	ID      string // 平台会话号：群里是群号，私聊是对方的号
	Self    string // 她的号：只有私聊带（主人同时私聊两个号是两个会话）；群不带（一个群一份历史，角色的分开靠内核的实例）
}

// String 是会话的字符串形式，和 Bun 一样（旧库的 session_id 不重算，规格 第四节、第十八节 ⑭）：
// 群 "渠道:group:群号"，私聊 "渠道:private:她的号:对方的号"。不合法的会话也能出字符串，是不是合法用 Valid 查，别拿字符串猜
func (c Conversation) String() string {
	if c.Scene == SceneGroup {
		return c.Channel + ":group:" + c.ID
	}
	return c.Channel + ":private:" + c.Self + ":" + c.ID
}

// Valid 查一个会话是不是填对了：适配器交上来的、插件拼的，进库之前都过一遍。
// 私聊没带她的号、群带了她的号都算错——前者会让两个号的私聊串成一个，后者会让一个群分裂成两份历史。
// 渠道、会话号、她的号都不许带冒号：String 用冒号拼、ParseConversation 用冒号拆，带了就拆不回原样（Valid 通过的一定能往返）
func (c Conversation) Valid() error {
	switch {
	case c.Channel == "":
		return errors.New("会话没有渠道")
	case c.Scene != ScenePrivate && c.Scene != SceneGroup:
		return fmt.Errorf("会话的形式 %q 不是 private 或 group", c.Scene)
	case c.ID == "":
		return errors.New("会话没有平台会话号")
	case c.Scene == ScenePrivate && c.Self == "":
		return errors.New("私聊会话要带她的号（同一个人私聊两个号是两个会话）")
	case c.Scene == SceneGroup && c.Self != "":
		return errors.New("群会话不带她的号（一个群一份历史）")
	case strings.Contains(c.Channel, ":"):
		return fmt.Errorf("会话的渠道 %q 带冒号，会话串拆不回来", c.Channel)
	case strings.Contains(c.ID, ":"):
		return fmt.Errorf("会话的平台会话号 %q 带冒号，会话串拆不回来", c.ID)
	case strings.Contains(c.Self, ":"):
		return fmt.Errorf("会话里她的号 %q 带冒号，会话串拆不回来", c.Self)
	}
	return nil
}

// ParseConversation 是 String 的反过程：把库里存的会话串（"渠道:group:群号"、"渠道:private:她的号:对方的号"）还原成结构，
// 还原出来的要过 Valid。只给"手里只有库里的会话串、又要问协议端"的地方用（取一条消息的新图片地址）；判断场合仍然不拆字符串
func ParseConversation(s string) (Conversation, error) {
	parts := strings.Split(s, ":")
	switch {
	case len(parts) == 3 && parts[1] == SceneGroup:
		c := Conversation{Channel: parts[0], Scene: SceneGroup, ID: parts[2]}
		return c, c.Valid()
	case len(parts) == 4 && parts[1] == ScenePrivate:
		c := Conversation{Channel: parts[0], Scene: ScenePrivate, Self: parts[2], ID: parts[3]}
		return c, c.Valid()
	}
	return Conversation{}, fmt.Errorf("会话串 %q 不是 渠道:group:群号 或 渠道:private:她的号:对方的号", s)
}
