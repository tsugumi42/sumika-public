package snowluma

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

func forwardParams(t *testing.T, p types.Part, self string) map[string]json.RawMessage {
	t.Helper()
	action, params, reason, ok := forwardAction(p, sendAccount, self, false)
	if !ok {
		t.Fatalf("组不出动作：%s", reason)
	}
	if action != "send_group_forward_msg" && action != "send_private_forward_msg" {
		t.Fatalf("动作 %s", action)
	}
	raw, _ := json.Marshal(params)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func nodeNames(t *testing.T, m map[string]json.RawMessage) (names, uins []string) {
	t.Helper()
	var nodes []struct{ Data struct{ Name, Uin string } }
	if err := json.Unmarshal(m["messages"], &nodes); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		names, uins = append(names, n.Data.Name), append(uins, n.Data.Uin)
	}
	return
}

// 卡片文字：有的项才传，没有的项不出现在参数里（让 QQ 用默认）；node 的号永远是连接自己的
func TestForwardActionCarriesCardOnlyWhenSet(t *testing.T) {
	base := types.Part{ID: "编的计划:0", Conv: groupConv(), Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "退路名", Body: types.TextBody("编的正文")}}}
	m := forwardParams(t, base, "")
	for _, k := range []string{"source", "news", "summary", "prompt"} {
		if _, has := m[k]; has {
			t.Fatalf("没设卡片不该传 %s", k)
		}
	}
	card := base
	card.Card = &types.ForwardCard{Source: "编的标题", News: []string{"共 12 字", "3 张图"}, Prompt: "编的外显"}
	m = forwardParams(t, card, "")
	var src, prompt string
	_ = json.Unmarshal(m["source"], &src)
	_ = json.Unmarshal(m["prompt"], &prompt)
	if src != "编的标题" || prompt != "编的外显" {
		t.Fatalf("source/prompt：%s", m)
	}
	if _, has := m["summary"]; has {
		t.Fatal("空的 summary 不该传")
	}
	if string(m["news"]) != `[{"text":"共 12 字"},{"text":"3 张图"}]` {
		t.Fatalf("news 要是 [{text}]：%s", m["news"])
	}
	empty := base
	empty.Card = &types.ForwardCard{}
	if _, has := forwardParams(t, empty, "")["source"]; has {
		t.Fatal("空卡片不该传任何项")
	}
}

func TestForwardActionNodeNameChoice(t *testing.T) {
	p := types.Part{ID: "编的计划:0", Conv: privateConv(), Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "退路名", Self: true, Body: types.TextBody("甲")},
		{Name: "写死的名", Body: types.TextBody("乙")},
	}}
	names, uins := nodeNames(t, forwardParams(t, p, "编的昵称"))
	if names[0] != "编的昵称" || names[1] != "写死的名" {
		t.Fatalf("有昵称：Self 的用昵称，写死的不动：%q", names)
	}
	names, _ = nodeNames(t, forwardParams(t, p, ""))
	if names[0] != "退路名" {
		t.Fatalf("取不到昵称退回原名：%q", names)
	}
	if uins[0] != sendAccount || uins[1] != sendAccount {
		t.Fatalf("节点的号必须是连接自己的：%q", uins)
	}
}

func TestLoginNickname(t *testing.T) {
	for in, want := range map[string]string{
		`{"status":"ok","retcode":0,"data":{"user_id":90001,"nickname":"编的昵称"}}`:                            "编的昵称",
		`{"status":"ok","retcode":0,"data":{"user_id":90001,"nickname":"甲` + string(rune(92)) + `u000a乙"}}`: "甲乙",
		`{"status":"ok","retcode":0,"data":{"user_id":90001}}`:                                              "",
		`{"status":"ok","retcode":0,"data":{"user_id":90001,"nickname":7}}`:                                 "",
		`{"status":"ok","retcode":0,"data":null}`:                                                           "",
		`不是 JSON`: "",
	} {
		if got := loginNickname([]byte(in)); got != want {
			t.Errorf("%s：%q，要 %q", in, got, want)
		}
	}
}

// 走完整的适配器：核验时记下昵称，Send 转发时节点名用它
func TestSendForwardUsesLoginNickname(t *testing.T) {
	e := newSendEnvSetup(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-90010}}`), func(p *actionPeer) {
		p.setLoginBody(`{"status":"ok","retcode":0,"data":{"user_id":` + sendAccount + `,"nickname":"编的昵称"}}`)
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.ad.mu.Lock()
		name := e.ad.loginName
		e.ad.mu.Unlock()
		if name != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("核验没有记下昵称")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p := types.Part{ID: "编的计划:0", Conv: groupConv(), Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "退路名", Self: true, Body: types.TextBody("甲")}}, Card: &types.ForwardCard{Source: "编的标题"}}
	done := make(chan error, 1)
	go func() { _, err := e.ad.Send(context.Background(), sendAccount, p); done <- err }()
	act := e.peer.waitActions(t, 1)[0]
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var nodes []struct{ Data struct{ Name string } }
	if err := json.Unmarshal(act.Params["messages"], &nodes); err != nil || len(nodes) != 1 || nodes[0].Data.Name != "编的昵称" {
		t.Fatalf("节点名：%s %v", act.Raw, err)
	}
	if string(act.Params["source"]) != `"编的标题"` {
		t.Fatalf("source：%s", act.Raw)
	}
}
