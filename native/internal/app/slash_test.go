package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/slash"
)

// kinds 把会话里的行按先后列成 "kind/actor_kind/原文"，看顺序和类型。
func slashRows(t *testing.T, a *App, conv string) []string {
	t.Helper()
	st, err := a.set.ForConversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Events.Query(`SELECT kind, actor_kind, origin, text FROM events WHERE conversation=? ORDER BY seq`, conv)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, actor, origin, text string
		if err := rows.Scan(&kind, &actor, &origin, &text); err != nil {
			t.Fatal(err)
		}
		out = append(out, kind+"/"+actor+"/"+origin+"/"+text)
	}
	return out
}

func TestSlashUnknownCommandIsSentVerbatimWithNotice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好的")})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "/nosuch 你好", nil); err != nil {
		t.Fatal(err)
	}
	rows := slashRows(t, a, key.Conversation)
	if len(rows) != 3 || rows[0] != "message/owner/owner//nosuch 你好" || !strings.HasPrefix(rows[1], "notice/system/system:slash/没有这个命令：/nosuch") || rows[2] != "message/character/character/好的" {
		t.Fatalf("未知命令应照原文发出，系统行排在主人消息后面：%q", rows)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bodies) != 1 || !strings.Contains(e.bodies[0], "/nosuch 你好") {
		t.Fatalf("原文没有发给模型：%d", len(e.bodies))
	}
	// 系统行不进她的历史
	if strings.Contains(e.bodies[0], "没有这个命令") {
		t.Fatal("系统行被发给了模型")
	}
}

func TestSlashRegisteredCommandIsHandledNotSent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("收到")})
	key := localKey("console:" + testChar)
	var got slashCall
	a.slashes.add(slash.Command{Name: "echo"}, func(_ context.Context, c slashCall) (slashResult, error) {
		got = c
		if c.Inv.Arg == "改写" {
			return slashResult{Reply: "已设定", Send: "改写后的话", Source: noticeGoal}, nil
		}
		if c.Inv.Arg == "出错" {
			return slashResult{}, errors.New("写法不对")
		}
		return slashResult{Reply: "回声：" + c.Inv.Arg}, nil
	})
	ctx := context.Background()
	if err := d.Reader.SendLocal(ctx, key, "/echo 你好", nil); err != nil {
		t.Fatal(err)
	}
	if e.llmCalls() != 0 {
		t.Fatalf("命令不该发给模型：%d 次", e.llmCalls())
	}
	if got.Conv != key.Conversation || got.Character != testChar || got.Text != "/echo 你好" {
		t.Fatalf("命令拿到的上下文：%+v", got)
	}
	if err := d.Reader.SendLocal(ctx, key, "/echo 出错", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(ctx, key, "/echo 改写", nil); err != nil {
		t.Fatal(err)
	}
	rows := slashRows(t, a, key.Conversation)
	want := []string{
		"notice/system/system:slash/回声：你好",
		"notice/system/system:slash/写法不对",
		"message/owner/owner/改写后的话",
		"notice/system/system:goal/已设定",
		"message/character/character/收到",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("会话记录：\n%s\n想要：\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	if e.llmCalls() != 1 {
		t.Fatalf("只有改写后的那句该发给模型：%d 次", e.llmCalls())
	}
	// 路径里的斜杠和不合法的名字不是命令
	for _, text := range []string{"/usr/bin 在哪", "/ 单独一个斜杠", "说到 /echo 不在开头"} {
		if _, handled, done, err := a.localSlash(ctx, key, testChar, text); handled || err != nil {
			t.Fatalf("%q 不该当命令：handled=%v err=%v", text, handled, err)
		} else {
			done()
		}
	}
}

// 斜杠命令只在本地私聊认：QQ 私聊、QQ 群、本地群聊的键直接进入命令表也不处理，不写任何记录。
func TestSlashOnlyEligibleInLocalPrivate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	called := false
	a.slashes.add(slash.Command{Name: "echo"}, func(context.Context, slashCall) (slashResult, error) {
		called = true
		return slashResult{Reply: "不该出现"}, nil
	})
	ctx := context.Background()
	for _, k := range []api.ConversationKey{
		{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:100:200"},
		{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:300"},
		{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:x"},
		{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + testChar + ":group:x"},
		{Platform: "local", Channel: "qq", Scene: "private", Conversation: "console:" + testChar},
	} {
		send, handled, done, err := a.localSlash(ctx, k, testChar, "/echo 你好")
		done()
		if handled || err != nil || send != "/echo 你好" {
			t.Fatalf("%+v 不是本地私聊却处理了命令：handled=%v err=%v", k, handled, err)
		}
	}
	if called {
		t.Fatal("命令处理函数被非本地私聊调到了")
	}
	// 走正式入口：QQ 键本来就发不了；命令不处理
	if err := d.Reader.SendLocal(ctx, api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:100:200"}, "/echo 你好", nil); err == nil {
		t.Fatal("QQ 键放行了本地发送")
	}
	if called {
		t.Fatal("命令处理函数被调到了")
	}
}

// 命令在她正在说话时也能用（/goal clear、/loop stop 要能打断自动流程），不会得到“正在生成”。
func TestSlashRunsWhileBusy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	a.slashes.add(slash.Command{Name: "echo"}, func(context.Context, slashCall) (slashResult, error) {
		return slashResult{Reply: "忙着也能用"}, nil
	})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	drafts := make(chan api.LocalDraft, 4)
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "说点什么", func(v api.LocalDraft) { drafts <- v }) }()
	localRecv(t, drafts)
	if err := d.Reader.SendLocal(ctx, key, "/echo", nil); err != nil {
		t.Fatalf("忙时命令：%v", err)
	}
	if err := d.Reader.SendLocal(ctx, key, "普通话", nil); !errors.Is(err, api.ErrLocalChatBusy) {
		t.Fatalf("忙时普通话该返回 busy：%v", err)
	}
	if err := d.Reader.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
}

// 分块输入：只有一个没有引用、没有附件的普通文字块才当命令；带引用块或附件的不当命令。
func TestSlashWithParts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("收到")})
	calls := 0
	a.slashes.add(slash.Command{Name: "echo"}, func(context.Context, slashCall) (slashResult, error) {
		calls++
		return slashResult{Reply: "回声"}, nil
	})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if _, ok := plainText("", []api.LocalPart{{Text: "/echo", Quote: &api.LocalQuote{}}}); ok {
		t.Fatal("带引用块的不该当普通文字")
	}
	if _, ok := plainText("", []api.LocalPart{{Text: "/echo", Attachments: []api.LocalAttachment{{}}}}); ok {
		t.Fatal("带附件的不该当普通文字")
	}
	if _, ok := plainText("", []api.LocalPart{{Text: "/echo"}, {Text: "再来一块"}}); ok {
		t.Fatal("多块的不该当命令")
	}
	// 单个普通文字块：SendLocalParts 和 SubmitLocal 都当命令
	if err := d.Reader.SendLocalParts(ctx, key, []api.LocalPart{{Text: "/echo 一"}}, nil); err != nil {
		t.Fatal(err)
	}
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Parts: []api.LocalPart{{Text: "/echo 二"}}, Mode: "start", ClientKey: "k1"})
	if err != nil || rec.State != "command" || calls != 2 || e.llmCalls() != 0 {
		t.Fatalf("%+v %v calls=%d 请求=%d", rec, err, calls, e.llmCalls())
	}
	// 带引用块的：不当命令（引用出处是假的，被核对拒绝，命令处理函数没被调到）
	bad := []api.LocalPart{{Text: "/echo 三", Quote: &api.LocalQuote{Ref: api.EventRef{Conversation: key}}}}
	if err := d.Reader.SendLocalParts(ctx, key, bad, nil); err == nil || calls != 2 {
		t.Fatalf("带引用的不该走命令：%v calls=%d", err, calls)
	}
}
