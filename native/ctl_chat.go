package main

// 自检对话（sumika-ctl chat，规格-本机控制接口第八节）接进桌面程序：走桌面本地私聊同一条写路径（DesktopReader.SelftestChat），
// 不另写一套；会话由后台按角色拼成自检话题，控制接口收不到任何会话 id。

import (
	"context"
	"errors"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.ChatBackend = (*ctlBackend)(nil)

func (b *ctlBackend) Chat(ctx context.Context, req ctlserver.ChatRequest) (ctlproto.ChatResult, error) {
	r, err := b.checkpointReader() // 取可写桌面的读写入口：没有可写后台（只看历史）回 readonly，演示模式回 demo
	if err != nil {
		return ctlproto.ChatResult{}, err
	}
	if link, archive, on := b.bunMode(); on { // 本地私聊交给了 Bun：执行体在 Bun，这里只翻译（规格-本地私聊切到Bun 第十一节）；绝不调 Go 的 SelftestChat
		return b.chatViaBun(ctx, r, link, archive, req)
	}
	return chatReply(r.SelftestChat(ctx, backend.SelftestRequest{Character: req.Character, New: req.New, Text: req.Text, Timeout: req.Timeout}))
}

// chatReply 把后台的自检结果翻成协议的结果（go 模式和 bun 模式共用这一份）
func chatReply(res backend.SelftestResult, err error) (ctlproto.ChatResult, error) {
	switch {
	case errors.Is(err, api.ErrLocalChatUnsupported):
		return ctlproto.ChatResult{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式不能对话，请先开始接收")
	case errors.Is(err, backend.ErrSelftestCharacter):
		return ctlproto.ChatResult{}, ctlproto.Errorf(ctlproto.CodeInvalid, "没有这个角色")
	case err != nil:
		return ctlproto.ChatResult{}, err
	}
	out := ctlproto.ChatResult{Status: res.Status, Character: res.Character, Topic: res.Topic, Conversation: res.Conversation, TurnID: res.TurnID,
		Reply: res.Reply, Draft: res.Draft, Tools: make([]ctlproto.ChatTool, len(res.Tools)), Model: res.Model, Route: res.Route, ElapsedMS: res.ElapsedMS, Error: res.Error}
	for i, t := range res.Tools {
		out.Tools[i] = ctlproto.ChatTool{Name: t.Name, Args: t.Args, Result: t.Result, Status: t.Status, Error: t.Error}
	}
	if a := res.Approval; a != nil {
		out.Approval = &ctlproto.ChatApproval{ID: a.ID, Title: a.Title, Tool: a.Tool, Subject: a.Subject, Why: a.Why, Purpose: a.Purpose, Preview: a.Preview, Notes: a.Notes}
	}
	switch res.Status {
	case backend.SelftestDone:
		return out, nil
	case backend.SelftestTimeout:
		return out, &ctlproto.Error{Code: ctlproto.CodeTimeout, Msg: "到了时限，这一轮已取消", Data: out}
	case backend.SelftestWaitingApproval:
		return out, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: "停在审批上了：没有替用户批，请用户在界面上批准或拒绝", Data: out}
	}
	msg := res.Error
	if msg == "" {
		msg = "这一轮没有正常结束（" + res.Status + "）"
	}
	return out, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: msg, Data: out}
}
