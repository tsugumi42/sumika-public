package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

func localKey(id string) api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: id}
}

func localDesktop(t *testing.T, e *testEnv, script llmtest.Script) (*App, *Desktop) {
	t.Helper()
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	return a, d
}

func localRecv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("本地聊天未在 10 秒内返回")
		var z T
		return z
	}
}

// 四种场合、身份字段及生命周期都从正式写接口进；不启动任何协议端。
func TestDesktopLocalChatBoundaryAndPersistence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("本地回复")})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	invalid := []api.ConversationKey{{}, localKey(""), localKey("console:"), localKey("console:owner"), localKey("console:" + testChar + ":group:x"), localKey("console:" + testChar + ":topic:foreign"), {Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:100:200"}, {Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:300"}, {Platform: "qq", Channel: "console", Scene: "private", Conversation: key.Conversation}, {Platform: "local", Channel: "qq", Scene: "private", Conversation: key.Conversation}, {Platform: "local", Channel: "console", Scene: "group", Conversation: key.Conversation}}
	st, _ := a.set.For("console")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('foreign','invented',1),('owned',?,1)`, testChar); err != nil {
		t.Fatal(err)
	}
	for _, k := range invalid {
		if err := d.Reader.SendLocal(ctx, k, "冒充主人", nil); err == nil {
			t.Fatalf("发送放行了伪造会话 %+v", k)
		}
		if err := d.Reader.StopLocal(ctx, k); err == nil {
			t.Fatalf("停止放行了伪造会话 %+v", k)
		}
		if err := d.Reader.DecideLocal(ctx, k, "invented-card", api.ApproveOnce); err == nil {
			t.Fatalf("审批放行了伪造会话 %+v", k)
		}
	}
	for _, text := range []string{"", " \r\n\t"} {
		if err := d.Reader.SendLocal(ctx, key, text, nil); err == nil {
			t.Fatal("收了空消息")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := d.Reader.SendLocal(cancelled, key, "已取消", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后发送：%v", err)
	}
	for _, k := range []api.ConversationKey{key, localKey(key.Conversation + ":topic:owned")} {
		if err := d.Reader.SendLocal(ctx, k, "编的输入", nil); err != nil {
			t.Fatal(err)
		}
		page, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: k, Limit: 20}, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Value.Items) != 2 || page.Value.Items[0].ActorKind != "owner" || page.Value.Items[0].ActorID != "owner" || page.Value.Items[0].Text != "编的输入" || page.Value.Items[1].Text != "本地回复" {
			t.Fatalf("正式内核落库结果：%+v", page.Value.Items)
		}
	}
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='owner'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("拒绝的输入也写入了本地库：%d %v", n, err)
	}
	qq, _ := a.set.For("qq")
	if err := qq.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("本地发送动了 QQ 库：%d %v", n, err)
	}
	d.Close()
	if err := d.Reader.SendLocal(ctx, key, "关闭后", nil); err == nil {
		t.Fatal("关闭入口还接消息")
	}
	opt := archiveFixture(t, "local-chat-archive")
	archive, err := Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	ad := archive.Desktop()
	defer ad.Close()
	if err := ad.Reader.SendLocal(ctx, key, "不写库", nil); !errors.Is(err, api.ErrLocalChatUnsupported) {
		t.Fatalf("archive 没拒绝写入：%v", err)
	}
}

func TestDesktopLocalChatStreamingStopAndIsolation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	st, _ := a.set.For("console")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('parallel',?,1)`, testChar); err != nil {
		t.Fatal(err)
	}
	other := localKey(key.Conversation + ":topic:parallel")
	drafts := make(chan api.LocalDraft, 10)
	second := make(chan api.LocalDraft, 10)
	done := make(chan error, 1)
	otherDone := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "第一条", func(v api.LocalDraft) { drafts <- v }) }()
	first := localRecv(t, drafts)
	if first.Text != "前半句" || first.TurnID == "" {
		t.Fatalf("没有完整草稿和回合身份：%+v", first)
	}
	if err := d.Reader.SendLocal(ctx, key, "重复发送", nil); !errors.Is(err, api.ErrLocalChatBusy) {
		t.Fatalf("重复发送：%v", err)
	}
	go func() {
		otherDone <- d.Reader.SendLocal(ctx, other, "另一话题", func(v api.LocalDraft) { second <- v })
	}()
	next := localRecv(t, second)
	if next.Text != "前半句" || next.TurnID == first.TurnID {
		t.Fatalf("并行会话串了回合：%+v %+v", first, next)
	}
	if err := d.Reader.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := localRecv(t, done); err != nil && !strings.Contains(err.Error(), "aborted") {
		t.Fatal(err)
	}
	select {
	case err := <-otherDone:
		t.Fatalf("停止串到了另一个话题：%v", err)
	default:
	}
	d.Close()
	localRecv(t, otherDone)
	var text, segments string
	if err := st.Events.QueryRow(`SELECT text,segments_json FROM events WHERE conversation=? AND actor_kind='character'`, key.Conversation).Scan(&text, &segments); err != nil {
		t.Fatal(err)
	}
	if text != "前半句" || !strings.Contains(segments, `"aborted":true`) {
		t.Fatalf("停止丢了半句或状态：%q %s", text, segments)
	}
}

func TestDesktopLocalChatApprovalDecisions(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{"approve", "deny", "expire", "stop"} {
		t.Run(choice, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			target := "编的文件.txt"
			args, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": target, "content": "获批才写"})
			a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("local-write", "write", string(args)), llmtest.Text("处理结束")}})
			target = filepath.Join(a.workdir(), target)
			if choice == "expire" {
				a.approvalGate.ApprovalMinutes = 0.002
			}
			ctx := context.Background()
			key := localKey("console:" + testChar)
			var sub api.Subscription[[]api.LocalApproval]
			if choice != "expire" {
				var err error
				sub, err = d.Reader.WatchLocalApprovals(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer sub.Close()
			}
			done := make(chan error, 1)
			go func() { done <- d.Reader.SendLocal(ctx, key, "写编的文件", nil) }()
			var card api.LocalApproval
			if choice == "expire" {
				// 没有界面订阅也按期限到期；允许通知合并，不能依赖短暂待批卡一定被读到。
				localRecv(t, done)
				states := a.approvals.Snapshot()
				if len(states) != 1 {
					t.Fatalf("到期后的审批记录：%+v", states)
				}
				r := states[0].Request
				card = api.LocalApproval{ID: r.ID, CharacterID: r.CharacterID, Key: localKey(r.Conversation), Tool: r.Tool, Subject: r.Subject}
			} else {
				for card.ID == "" {
					snap := localRecv(t, sub.Updates())
					if len(snap.Value) > 0 {
						card = snap.Value[0]
					}
				}
			}
			if card.Key != key || card.CharacterID != testChar || card.Tool != "write" || !strings.Contains(card.Subject, target) {
				t.Fatalf("审批身份或动作丢失：%+v", card)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("审批前已执行：%v", err)
			}
			if err := d.Reader.DecideLocal(ctx, key, "", api.ApproveOnce); !errors.Is(err, api.ErrApprovalGone) {
				t.Fatalf("空 ID：%v", err)
			}
			// 工作目录里的写文件卡给"这个对话""记住"（范围档照卡上的 Options，受保护路径的见 desktop_scope_test.go）
			if choice != "expire" && (!hasChoice(card.Options, api.ApproveConversation) || !hasChoice(card.Options, api.ApproveAlways)) {
				t.Fatalf("写文件的本地卡该带范围档：%v", card.Options)
			}
			foreign := localKey(key.Conversation + ":topic:another")
			st, _ := a.set.For("console")
			if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('another',?,1)`, testChar); err != nil {
				t.Fatal(err)
			}
			if err := d.Reader.DecideLocal(ctx, foreign, card.ID, api.ApproveOnce); !errors.Is(err, api.ErrApprovalGone) {
				t.Fatalf("跨会话批准：%v", err)
			}
			switch choice {
			case "approve", "deny":
				answer := api.ApproveDeny
				if choice == "approve" {
					answer = api.ApproveOnce
				}
				if err := d.Reader.DecideLocal(ctx, key, card.ID, answer); err != nil {
					t.Fatal(err)
				}
			case "stop":
				if err := d.Reader.StopLocal(ctx, key); err != nil {
					t.Fatal(err)
				}
			}
			if choice != "expire" {
				localRecv(t, done)
			}
			if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveOnce); !errors.Is(err, api.ErrApprovalGone) {
				t.Fatalf("迟到批准：%v", err)
			}
			b, readErr := os.ReadFile(target)
			if choice == "approve" {
				if readErr != nil || string(b) != "获批才写" {
					t.Fatalf("批准没执行：%q %v", b, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("%s 仍执行了：%q %v", choice, b, readErr)
			}
			var status string
			if err := a.set.Trace().QueryRow(`SELECT json_extract(approval_json,'$.decision') FROM steps WHERE provider_call_id='local-write'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"approve": "approved", "deny": "denied", "expire": "expired", "stop": "cancelled"}[choice]
			if status != expected {
				t.Fatalf("审批持久化=%q，期望 %s", status, expected)
			}
			e.mu.Lock()
			bodies := append([]string(nil), e.bodies...)
			e.mu.Unlock()
			if !strings.Contains(bodies[0], `"name":"write"`) {
				t.Fatal("本地回合没有把写工具交给模型")
			}
			if choice != "stop" && (len(bodies) != 2 || !strings.Contains(bodies[1], `"tool_call_id":"local-write"`)) {
				t.Fatalf("工具结果没进入下一次模型请求：%v", bodies)
			}
			t.Logf("%s：审批 %s，文件写入=%t", choice, status, readErr == nil)
		})
	}
}

// 在登记活动回合之前按停止：取消信号仍须带进真正的模型路径。
func TestDesktopLocalChatStopBeforeModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("不应请求")})
	entered, release := make(chan struct{}), make(chan struct{})
	a.hub.Cfg.BeforeTurn = func() { close(entered); <-release }
	done := make(chan error, 1)
	key := localKey("console:" + testChar)
	go func() { done <- d.Reader.SendLocal(context.Background(), key, "马上停止", nil) }()
	localRecv(t, entered)
	if err := d.Reader.StopLocal(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	close(release)
	localRecv(t, done)
	st, _ := a.set.For("console")
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='character'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("停止前排队的回复仍产生输出：%d %v", n, err)
	}
	var outcome string
	if err := a.set.Trace().QueryRow(`SELECT outcome FROM turns ORDER BY started_at DESC LIMIT 1`).Scan(&outcome); err != nil || outcome != "stopped" {
		t.Fatalf("请求前停止未收尾：%q %v", outcome, err)
	}
}

func hasChoice(options []api.ApprovalChoice, c api.ApprovalChoice) bool {
	for _, o := range options {
		if o == c {
			return true
		}
	}
	return false
}
