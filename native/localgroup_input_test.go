package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/llm/llmtest"
)

func localUIGroup(t *testing.T, h *localUIHarness, name string) api.ConversationKey {
	t.Helper()
	g, err := h.a.reader.(api.LocalGroupWriter).CreateLocalGroup(context.Background(), api.LocalGroupCreate{Name: name, Characters: []string{"char00", "char01"}})
	if err != nil {
		t.Fatal(err)
	}
	h.a.keys[g.Key.Conversation] = g.Key
	return g.Key
}

func TestLocalGroupComposerBoundaries(t *testing.T) {
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("编造回复")}, nil, 0)
	original := h.a.reader
	group := localUIGroup(t, h, "编造边界群")
	private := h.a.keyOf("console:char00")
	for _, tc := range []struct {
		name            string
		key             api.ConversationKey
		mode            backend.Mode
		reader          api.Reader
		input, approval bool
		message         string
	}{
		{"本地私聊", private, "receive", original, true, true, ""},
		{"本地群聊", group, "receive", original, true, false, ""},
		{"QQ私聊", api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:invented"}, "receive", original, false, false, "QQ 发送需走平台发送规则"},
		{"QQ群", api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:invented"}, "receive", original, false, false, "QQ 发送需走平台发送规则"},
		{"archive群", group, "archive", original, false, false, "请先启动后台"},
		{"无群写接口", group, "receive", struct{ api.Reader }{original}, false, false, api.ErrLocalGroupUnsupported.Error()},
		{"伪造渠道", api.ConversationKey{Platform: "local", Channel: "qq", Scene: "group", Conversation: group.Conversation}, "receive", original, false, false, "仅查看记录"},
		{"未知场合", api.ConversationKey{Platform: "local", Channel: "console", Scene: "invented", Conversation: group.Conversation}, "receive", original, false, false, "仅查看记录"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.a.reader, h.a.host.Mode = tc.reader, tc.mode
			h.a.sel = tc.key.Conversation
			h.a.keys[h.a.sel] = tc.key
			// 即使错误数据把审批放在群键下，群也不能绘制可操作的审批卡。
			h.a.approvals = []api.LocalApproval{{ID: "invented-approval", Key: tc.key, Tool: "write", Subject: "编造的临时文件", Title: "编造审批"}}
			tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { h.a.localComposer(c) }) }, 800, 300)
			tt.Frame()
			if _, ok := tt.Find("发一条本地消息"); ok != tc.input {
				t.Fatalf("输入框=%v，期望=%v：%v", ok, tc.input, tt.Texts())
			}
			if tt.HasText("允许一次") != tc.approval {
				t.Fatalf("审批卡越过场合边界：%v", tt.Texts())
			}
			if tc.message != "" && !tt.HasText(tc.message) {
				t.Fatalf("缺少只读原因 %q：%v", tc.message, tt.Texts())
			}
		})
	}
}

// 记录真实写入口的请求；只有收尾错误是注入的，输入和回复仍须由真实后台落库并订阅到 UI。
type observedGroupWriter struct {
	api.Reader
	api.LocalGroupWriter
	sent      chan api.LocalGroupSend
	stopped   chan api.ConversationKey
	finishErr error
}

func (w *observedGroupWriter) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	w.sent <- req
	if err := w.LocalGroupWriter.SendLocalGroup(ctx, req); err != nil {
		return err
	}
	return w.finishErr
}
func (w *observedGroupWriter) StopLocalGroup(ctx context.Context, k api.ConversationKey) error {
	w.stopped <- k
	return w.LocalGroupWriter.StopLocalGroup(ctx, k)
}
func observeGroup(h *localUIHarness) *observedGroupWriter {
	w := &observedGroupWriter{Reader: h.a.reader, LocalGroupWriter: h.a.reader.(api.LocalGroupWriter), sent: make(chan api.LocalGroupSend, 8), stopped: make(chan api.ConversationKey, 8)}
	h.a.reader = w
	return w
}

func TestLocalGroupComposerDraftsSendStopAndSwitch(t *testing.T) {
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("编造群回复"), Hold: func(int) int { return 0 }}, nil, 0)
	g1, g2 := localUIGroup(t, h, "编造甲群"), localUIGroup(t, h, "编造乙群")
	w := observeGroup(h)
	h.a.open(g1.Conversation)
	h.until(t, "甲群打开", func() bool { return h.a.firstDone })
	h.click(t, "发一条本地消息")
	h.tt.Type("@char00 甲群草稿")
	h.tt.Frame()
	h.a.open(g2.Conversation)
	h.until(t, "乙群打开", func() bool { return h.a.firstDone })
	h.click(t, "发一条本地消息")
	h.tt.Type("乙群草稿")
	h.tt.Frame()
	h.a.open(g1.Conversation)
	h.until(t, "甲群草稿恢复", func() bool { return h.a.firstDone })
	h.click(t, "发一条本地消息")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Key(ui.Cmd, ui.KeyC)
	if h.tt.Clipboard() != "@char00 甲群草稿" {
		t.Fatalf("甲群编辑器未恢复草稿：%q", h.tt.Clipboard())
	}
	if h.a.localRun(g2.Conversation).input != "乙群草稿" {
		t.Fatal("两个群的输入草稿串了")
	}
	h.click(t, "发送")
	h.until(t, "群请求进入模型且主人消息已可见", func() bool {
		return len(h.requests()) > 0 && h.tt.HasText("@char00 甲群草稿") && h.tt.HasText("正在回复")
	})
	req := <-w.sent
	if req.Key != g1 || req.Text != "@char00 甲群草稿" || len(req.Mentions) != 0 {
		t.Fatalf("发送路由或正文点名契约错误：%+v", req)
	}
	// busy 时 Enter 不能追加主人消息；切走不取消整轮，乙群仍可编辑。
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.a.open(g2.Conversation)
	h.until(t, "乙群仍保有草稿", func() bool { return h.a.firstDone })
	h.click(t, "发一条本地消息")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Key(ui.Cmd, ui.KeyC)
	if h.tt.Clipboard() != "乙群草稿" {
		t.Fatalf("乙群编辑器草稿串了：%q", h.tt.Clipboard())
	}
	if h.tt.HasText("正在回复") || !h.a.localRun(g1.Conversation).busy {
		t.Fatal("切会话串了 busy 或取消了甲群")
	}
	h.a.open(g1.Conversation)
	h.until(t, "切回仍可停止", func() bool { return h.a.firstDone && h.tt.HasText("正在回复") })
	h.click(t, "停止生成")
	h.until(t, "群停止和发送均收尾", func() bool { return !h.a.localRun(g1.Conversation).busy })
	select {
	case k := <-w.stopped:
		if k != g1 {
			t.Fatalf("停止串群：%+v", k)
		}
	default:
		t.Fatal("没有调用群停止写接口")
	}
	if h.a.localRun(g1.Conversation).err != "" || h.tt.HasText("正在回复") {
		t.Fatalf("主动停止应恢复输入且不报失败：%v", h.tt.Texts())
	}
	page, err := w.ReadMessages(context.Background(), api.ConversationRequest{Key: g1, Limit: 20}, "")
	if err != nil || len(page.Value.Items) != 1 || page.Value.Items[0].Text != req.Text {
		t.Fatalf("停止或重复 Enter 丢失/重复了已发输入：%+v %v", page.Value.Items, err)
	}
	var body struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(h.requests()[0]), &body); err != nil {
		t.Fatal(err)
	}
	for _, tool := range body.Tools {
		switch tool.Function.Name {
		case "read", "ls", "grep", "find", "write", "edit", "powershell", "todo_write":
			t.Fatalf("群模型请求带了本地执行工具：%s", tool.Function.Name)
		}
	}
	h.click(t, "发一条本地消息")
	h.tt.Type("停止后仍可输入")
	h.tt.Frame()
	if h.a.localRun(g1.Conversation).input != "停止后仍可输入" {
		t.Fatal("停止后输入未恢复")
	}
}

func TestLocalGroupComposerFailurePreservesHistoryAndRecovers(t *testing.T) {
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("编造的已落库回复")}, nil, 0)
	g := localUIGroup(t, h, "编造失败群")
	w := observeGroup(h)
	w.finishErr = errors.New("编造的收尾失败")
	h.a.open(g.Conversation)
	h.until(t, "群打开", func() bool { return h.a.firstDone })
	h.send(t, "@char00 已发送的一句")
	h.until(t, "失败显示且历史保留", func() bool {
		return !h.a.localRun(g.Conversation).busy && h.tt.HasText("发送失败：编造的收尾失败") && h.tt.HasText("@char00 已发送的一句") && h.tt.HasText("编造的已落库回复")
	})
	w.finishErr = nil
	h.send(t, "@char00 失败后新输入")
	h.until(t, "失败后能再发送成功", func() bool {
		return !h.a.localRun(g.Conversation).busy && h.tt.HasText("@char00 失败后新输入") && len(h.requests()) == 2
	})
	if err := h.a.localRun(g.Conversation).err; err != "" || h.tt.HasText("编造的收尾失败") {
		t.Fatalf("新发送未清除上轮错误：%s；%v", err, h.tt.Texts())
	}
	page, err := w.ReadMessages(context.Background(), api.ConversationRequest{Key: g, Limit: 20}, "")
	if err != nil || len(page.Value.Items) != 4 {
		t.Fatalf("失败重试不应抹掉或重发前一句：%+v %v", page.Value.Items, err)
	}
	var texts []string
	for _, m := range page.Value.Items {
		texts = append(texts, m.Text)
	}
	if strings.Join(texts, "|") != "@char00 已发送的一句|编造的已落库回复|@char00 失败后新输入|编造的已落库回复" {
		t.Fatalf("群订阅历史顺序错误：%v", texts)
	}
}
