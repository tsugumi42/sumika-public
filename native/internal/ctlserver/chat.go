package ctlserver

import (
	"context"
	"time"

	"sumika-native/internal/ctlproto"
)

// ChatRequest 是交给后端的一次自检对话；到后端手里，字段都是本层校验过的：
// Character 空或形状合格、Text 合格、Timeout 在 (0, MaxWait]。没有会话 id：会话由后端按角色拼，只会是本地私聊的自检话题
type ChatRequest struct {
	Character string
	New       bool
	Text      string
	Timeout   time.Duration
}

// ChatBackend 是可选扩展，沿用 CheckpointBackend 的做法：没接可写桌面的宿主不实现它，回 readonly，不落到别的路径
type ChatBackend interface {
	Chat(ctx context.Context, req ChatRequest) (ctlproto.ChatResult, error)
}

// chat 校验 chat 请求。这里是"谁能以主人身份说话"的唯一关口：只收一句话、可选的角色和 new、时限，别的参数一律 invalid
func (s *Server) chat(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 0 {
		return invalid("chat 只接一句话（写成一个参数，带空格的用引号），多余的参数不收")
	}
	if !ctlproto.ValidChatText(req.Text) {
		return invalid("要说的话不能是空的，最多 %d 字，也不能带控制字符", ctlproto.MaxChatText)
	}
	if req.Character != "" && !ctlproto.ValidCharacter(req.Character) {
		return invalid("角色 id 不能带空白、冒号、斜杠")
	}
	timeout := ctlproto.DefaultChatTTL
	if req.Wait != "" {
		d, err := time.ParseDuration(req.Wait)
		if err != nil || d <= 0 || d > ctlproto.MaxWait {
			return invalid("--timeout 要写成大于 0、不超过 %v 的时长，比如 120s", ctlproto.MaxWait)
		}
		timeout = d
	}
	if r, ok := s.demo("chat"); ok {
		return r, nil
	}
	b, ok := s.b.(ChatBackend)
	if !ok {
		return fail(ctlproto.CodeReadOnly, "自检对话接口不可用"), nil
	}
	d, err := b.Chat(s.ctx, ChatRequest{Character: req.Character, New: req.New, Text: req.Text, Timeout: timeout})
	return s.result(cleanChat(d), err), nil
}

// cleanChat 外面来的文字（模型的话、工具参数和结果、审批卡）出去前过一遍：回复保留换行，别的单行字段不许带换行和终端控制序列
func cleanChat(c ctlproto.ChatResult) ctlproto.ChatResult {
	c.Reply = ctlproto.CleanText(ctlproto.Truncate(c.Reply, ctlproto.MaxChatReply))
	c.Draft = ctlproto.CleanText(ctlproto.Truncate(c.Draft, ctlproto.MaxChatReply))
	c.Error = ctlproto.Clean(c.Error)
	c.Model, c.Route = ctlproto.Clean(c.Model), ctlproto.Clean(c.Route)
	c.Character, c.Topic, c.Conversation, c.TurnID = ctlproto.Clean(c.Character), ctlproto.Clean(c.Topic), ctlproto.Clean(c.Conversation), ctlproto.Clean(c.TurnID)
	tools := make([]ctlproto.ChatTool, len(c.Tools)) // 空也给 [] 不给 null
	for i, t := range c.Tools {
		tools[i] = ctlproto.ChatTool{Name: ctlproto.Clean(t.Name), Args: ctlproto.Clean(t.Args), Result: ctlproto.Clean(t.Result), Status: ctlproto.Clean(t.Status), Error: t.Error}
	}
	c.Tools = tools
	if a := c.Approval; a != nil {
		v := *a
		v.ID, v.Title, v.Tool, v.Subject = ctlproto.Clean(v.ID), ctlproto.Clean(v.Title), ctlproto.Clean(v.Tool), ctlproto.Clean(v.Subject)
		v.Why, v.Purpose, v.Preview = ctlproto.Clean(v.Why), ctlproto.Clean(v.Purpose), ctlproto.CleanText(v.Preview)
		v.Notes = nil
		for _, n := range a.Notes {
			v.Notes = append(v.Notes, ctlproto.Clean(n))
		}
		c.Approval = &v
	}
	return c
}
