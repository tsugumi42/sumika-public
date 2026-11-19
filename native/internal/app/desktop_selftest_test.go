package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/store/memsource"
)

func selftestReq(text string) SelftestRequest {
	return SelftestRequest{Text: text, Timeout: 20 * time.Second}
}

// 等这个会话上的一轮跑完（desk.runs 里没有它）
func waitIdle(t *testing.T, d *Desktop, conv string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d.mu.Lock()
		_, running := d.runs[conv]
		d.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("这一轮没在 10 秒内结束")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 一句话进自检话题：回复、模型（trace 里带服务商 ID：magpie/模型）、会话 id 对；库里主人的话和她的回复都带 selftest 标记，记忆的事件视图读出来是自检；
// QQ 库一行没多；界面的会话列表里这个话题标题是"自检"；别的会话没被碰
func TestSelftestChatNormalSentence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("自检回复")})
	ctx := context.Background()
	res, err := d.Reader.SelftestChat(ctx, selftestReq("自检的一句"))
	if err != nil {
		t.Fatal(err)
	}
	conv := "console:" + testChar + ":topic:selftest-" + testChar
	if res.Status != SelftestDone || res.Reply != "自检回复" || res.Conversation != conv || res.Character != testChar || res.TurnID == "" || res.Model != "magpie/"+testModel || res.Error != "" {
		t.Fatalf("结果不对：%+v", res)
	}
	st, _ := a.set.For("console")
	src, err := memsource.New(st, testChar)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := src.Events(ctx, conv, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].ActorKind != "owner" || evs[0].Text != "自检的一句" || evs[1].ActorKind != "character" {
		t.Fatalf("库里的两条：%+v", evs)
	}
	for _, ev := range evs {
		if !ev.Selftest {
			t.Fatalf("没标成自检：%+v", ev)
		}
	}
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation<>?`, conv).Scan(&n); err != nil || n != 0 {
		t.Fatalf("自检碰了别的会话：%d %v", n, err)
	}
	qq, _ := a.set.For("qq")
	if err := qq.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("自检动了 QQ 库：%d %v", n, err)
	}
	if err := qq.Events.QueryRow(`SELECT count(*) FROM outbound_plans`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("自检排了往外发的计划：%d %v", n, err)
	}
	list, err := a.set.ReadDisplayConversations(ctx, []string{testChar}, testChar, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range list.Items {
		if v.Conversation == conv {
			found = true
			if !strings.HasPrefix(v.Title, "自检") {
				t.Fatalf("会话列表里标题不是自检：%q", v.Title)
			}
		}
	}
	if !found {
		t.Fatalf("会话列表里没有自检话题：%+v", list.Items)
	}
}

// 默认继续同一个自检话题；--new 开新的（上下文清空，新话题只有这一轮），之后不带 --new 的接着用最新的
func TestSelftestChatNewTopic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	say := func(req SelftestRequest) SelftestResult {
		t.Helper()
		res, err := d.Reader.SelftestChat(ctx, req)
		if err != nil || res.Status != SelftestDone {
			t.Fatalf("%+v %v", res, err)
		}
		return res
	}
	first, again := say(selftestReq("一")), say(selftestReq("二"))
	if first.Conversation != again.Conversation {
		t.Fatalf("默认该继续同一个话题：%s %s", first.Conversation, again.Conversation)
	}
	req := selftestReq("三")
	req.New = true
	fresh := say(req)
	req.Text = "四"
	fresh2 := say(req)
	if fresh.Conversation == first.Conversation || fresh2.Conversation == fresh.Conversation || !audience.IsSelftest(fresh.Conversation) || !audience.IsSelftest(fresh2.Conversation) {
		t.Fatalf("--new 该每次开一个新的自检话题：%s %s %s", first.Conversation, fresh.Conversation, fresh2.Conversation)
	}
	if next := say(selftestReq("五")); next.Conversation != fresh2.Conversation {
		t.Fatalf("不带 --new 该接着最新的话题：%s %s", next.Conversation, fresh2.Conversation)
	}
	page, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: localKey(fresh.Conversation), Limit: 20}, "")
	if err != nil || len(page.Value.Items) != 2 {
		t.Fatalf("新话题里只该有这一轮的两条：%+v %v", page.Value.Items, err)
	}
}

// 被拒的形状：空、全空白、超长、零时限、不存在的角色、带冒号的角色；都不建话题、不写库、不惊动模型
func TestSelftestChatRejections(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("不该发出去")})
	ctx := context.Background()
	for name, req := range map[string]SelftestRequest{
		"空":      selftestReq(""),
		"全空白":    selftestReq(" \r\n\t"),
		"超长":     selftestReq(strings.Repeat("长", MaxSelftestText+1)),
		"零时限":    {Text: "你好"},
		"不存在角色":  {Text: "你好", Character: "nobody", Timeout: time.Second},
		"带冒号的角色": {Text: "你好", Character: testChar + ":topic:x", Timeout: time.Second},
	} {
		if res, err := d.Reader.SelftestChat(ctx, req); err == nil {
			t.Fatalf("%s：没拒绝：%+v", name, res)
		}
	}
	st, _ := a.set.For("console")
	var n int
	if err := st.Events.QueryRow(`SELECT (SELECT count(*) FROM events)+(SELECT count(*) FROM local_topics)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("被拒的请求动了库：%d %v", n, err)
	}
	e.mu.Lock()
	calls := len(e.bodies)
	e.mu.Unlock()
	if calls != 0 {
		t.Fatalf("被拒的请求惊动了模型：%d", calls)
	}
}

// 只看历史模式：先拒再碰库
func TestSelftestChatArchiveMode(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "selftest-archive")
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	if _, err := d.Reader.SelftestChat(context.Background(), selftestReq("你好")); !errors.Is(err, api.ErrLocalChatUnsupported) {
		t.Fatalf("archive 没拒绝：%v", err)
	}
}

// 工具调用取自运行记录：名字、参数、结果摘要
func TestSelftestChatRecordsTools(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("st-ls", "ls", `{"path":"."}`), llmtest.Text("看完了")}})
	res, err := d.Reader.SelftestChat(context.Background(), selftestReq("看看目录"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != SelftestDone || res.Reply != "看完了" || len(res.Tools) != 1 || res.Tools[0].Name != "ls" || !strings.Contains(res.Tools[0].Args, `"path"`) || res.Tools[0].Status != "done" || res.Tools[0].Error {
		t.Fatalf("工具记录不对：%+v", res)
	}
}

// 正在跑时再发一条：不发、不取消正在跑的那一轮，回 busy；正在跑的那一轮到时限后取消、回半截，话题随后空出来
func TestSelftestChatBusyAndTimeout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	ctx := context.Background()
	first := make(chan SelftestResult, 1)
	go func() {
		r, _ := d.Reader.SelftestChat(ctx, SelftestRequest{Text: "第一句", Timeout: 3 * time.Second})
		first <- r
	}()
	conv := "console:" + testChar + ":topic:selftest-" + testChar
	deadline := time.Now().Add(10 * time.Second)
	for {
		d.mu.Lock()
		_, running := d.runs[conv]
		d.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("第一句没跑起来")
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, err := d.Reader.SelftestChat(ctx, selftestReq("第二句"))
	if err != nil || res.Status != SelftestBusy {
		t.Fatalf("该回 busy：%+v %v", res, err)
	}
	st, _ := a.set.For("console")
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='owner'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("busy 的那句不该落库：%d %v", n, err)
	}
	r := localRecv(t, first)
	if r.Status != SelftestTimeout || r.Draft != "前半句" {
		t.Fatalf("时限：%+v", r)
	}
	waitIdle(t, d, conv)
}

// 停在审批卡上：回 waiting_approval 带卡片内容；没替用户批（文件没写）、这一轮没取消；用户在界面上批了才写，回合才结束
func TestSelftestChatWaitingApproval(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	target := "自检写的文件.txt"
	args, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": target, "content": "获批才写"})
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("st-write", "write", string(args)), llmtest.Text("写好了")}})
	target = filepath.Join(a.workdir(), target)
	ctx := context.Background()
	res, err := d.Reader.SelftestChat(ctx, selftestReq("写个文件"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != SelftestWaitingApproval || res.Approval == nil || res.Approval.Tool != "write" || !strings.Contains(res.Approval.Subject, target) || res.Approval.ID == "" {
		t.Fatalf("该停在 write 的审批卡上：%+v", res)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没批就写了：%v", err)
	}
	// 第二条：卡还在、这一轮没取消，回 busy 并带着这张卡
	again, err := d.Reader.SelftestChat(ctx, selftestReq("再说一句"))
	if err != nil || again.Status != SelftestBusy || again.Approval == nil || again.Approval.ID != res.Approval.ID {
		t.Fatalf("卡还在时再发：%+v %v", again, err)
	}
	if err := d.Reader.DecideLocal(ctx, localKey(res.Conversation), res.Approval.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, d, res.Conversation)
	if b, err := os.ReadFile(target); err != nil || string(b) != "获批才写" {
		t.Fatalf("批准后该写入：%q %v", b, err)
	}
}

// 模型报错：回 error 带原因，不当成功
func TestSelftestChatModelError(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	e.llm.Close() // 网关没了：这一轮的模型请求必然失败
	res, err := d.Reader.SelftestChat(context.Background(), SelftestRequest{Text: "你好", Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != SelftestError || res.Error == "" || res.Reply != "" {
		t.Fatalf("模型报错该回 error：%+v", res)
	}
}
