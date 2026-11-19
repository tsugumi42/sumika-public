package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/llm/llmtest"
)

// wbUI 用真后端（编造的模型、临时数据目录）开新界面，走和旧界面测试同一套夹具。
func wbUI(t *testing.T, script llmtest.Script, gate <-chan struct{}) *localUIHarness {
	t.Helper()
	h := newLocalUI(t, script, gate, 0)
	h.a.mainView, h.a.mainViewLocked = desksettings.MainViewWorkbench, true
	h.tt.Frame()
	if _, ok := h.tt.Find("发一条消息"); !ok {
		t.Fatalf("新界面没有输入框：%v", h.tt.Texts())
	}
	return h
}

func (h *localUIHarness) wbSend(t *testing.T, text string) {
	t.Helper()
	h.click(t, "发一条消息")
	h.tt.Type(text)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
}

func TestWorkbenchComposerSendsStreamsAndStops(t *testing.T) {
	release := make(chan struct{})
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("前半句", "，完整回复"), Hold: func(n int) int {
		if n == 2 {
			return 1
		}
		return -1
	}}, release)
	h.wbSend(t, "编的输入")
	st := h.a.localRun("console:char00")
	h.until(t, "流式前半句", func() bool { return h.tt.HasText("前半句") && st.busy })
	if got := h.a.composerOf("console:char00").Main.Text; got != "" {
		t.Fatalf("收下之后输入框没清：%q", got)
	}
	stop, ok := h.tt.Find("停止生成")
	if !ok || stop.W < 24 || stop.H < 24 {
		t.Fatalf("停止键不可达或小于 24：%+v %v", stop, ok)
	}
	close(release)
	h.until(t, "完整回复落入历史", func() bool { return !st.busy && h.tt.HasText("前半句，完整回复") })
	if !h.tt.HasText("编的输入") {
		t.Fatal("发出去的话没有出现在记录里")
	}
	h.wbSend(t, "第二条")
	h.until(t, "第二轮开始", func() bool { return st.busy })
	h.click(t, "停止生成")
	h.until(t, "停止后收尾", func() bool { return !st.busy })
	if st.err != "" {
		t.Fatalf("主动停止被显示成失败：%s", st.err)
	}
}

func TestWorkbenchComposerApproval(t *testing.T) {
	for _, choice := range []string{"deny", "approve"} {
		t.Run(choice, func(t *testing.T) {
			allow := choice == "approve"
			name := "wb-approved.txt"
			args, _ := json.Marshal(map[string]string{"purpose": "写界面测试文件", "path": name, "content": "真实工具写的编造内容"})
			h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("wb-write", "write", string(args)), llmtest.Text("审批处理完成")}}, nil)
			target := filepath.Join(h.workdir, name)
			h.wbSend(t, "写这个临时文件")
			h.until(t, "审批卡", func() bool { return h.tt.HasText("允许一次") })
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("点击前已执行：%v", err)
			}
			if _, ok := h.tt.Find("更多选择"); !ok {
				t.Fatal("工作目录里写文件的卡该有“这个对话”“记住”（后台给了这两档）")
			}
			h.click(t, map[bool]string{true: "允许一次", false: "拒绝"}[allow])
			h.until(t, "审批后回复", func() bool {
				return !h.a.localRun("console:char00").busy && h.tt.HasText("审批处理完成")
			})
			b, err := os.ReadFile(target)
			if allow != (err == nil && string(b) == "真实工具写的编造内容") {
				t.Fatalf("审批结果和文件对不上：allow=%v %q %v", allow, b, err)
			}
			time.Sleep(10 * time.Millisecond)
			h.tt.Frame()
			if h.tt.HasText("允许一次") {
				t.Fatal("已结束的审批卡还在")
			}
		})
	}
}

// 十个角色、一万条历史的真后台：引用一条历史消息发出去，读回来按块画，点引用滚回原消息；
// 顺带量一下新界面这几步的帧耗时和元素数（ADR-056），数字写进日志。
func TestWorkbenchComposerQuoteEndToEndWithTenThousandMessages(t *testing.T) {
	// 合并前的快测用 1000 条历史，整套里跑一万条（ADR-056 的规模数）
	history := 10000
	if testing.Short() {
		history = 1000
	}
	h := newLocalUI(t, llmtest.Script{Reply: llmtest.Text("收到引用")}, nil, history)
	h.a.mainView, h.a.mainViewLocked = desksettings.MainViewWorkbench, true
	h.tt.Frame()
	if len(h.a.chars) != 10 || len(h.a.msgs) != 200 {
		t.Fatalf("十倍数据没走正常分页：角色 %d，消息 %d", len(h.a.chars), len(h.a.msgs))
	}
	target := h.a.msgs[len(h.a.msgs)-1]
	p := h.a.composerOf("console:char00")
	card := p.AddQuote(chatui.Quoted{MessageID: target.id, Name: "我", Text: fmt.Sprintf("历史 %d", history-1)}, false)
	card.Text, p.Main.Text = "这条是什么意思", "另外一句"
	h.tt.Frame()
	h.click(t, "发送")
	st := h.a.localRun("console:char00")
	h.until(t, "发出去并收到回复", func() bool { return !st.busy && !st.submitting && h.tt.HasText("收到引用") })
	if st.err != "" || len(p.Cards) != 0 || p.Main.Text != "" {
		t.Fatalf("后台收下后输入区该清空：err %q 卡 %d 主 %q", st.err, len(p.Cards), p.Main.Text)
	}
	var sent *msg
	for _, m := range h.a.msgs {
		if len(m.chatParts) == 2 {
			sent = m
		}
	}
	if sent == nil {
		t.Fatalf("读回来的消息没有分块：%v", h.tt.Texts())
	}
	q := sent.chatParts[0].Quote
	if q == nil || q.Gone || q.Target != target.id || q.Snippet != fmt.Sprintf("历史 %d", history-1) || sent.chatParts[0].Text != "这条是什么意思" || sent.chatParts[1].Quote != nil || sent.chatParts[1].Text != "另外一句" {
		t.Fatalf("分块读回来不对：%+v %+v", sent.chatParts[0], sent.chatParts[1])
	}
	if !h.tt.HasText("这条是什么意思") || !h.tt.HasText("另外一句") {
		t.Fatalf("气泡没按块画：%v", h.tt.Texts())
	}
	h.a.list.ScrollToEnd()
	h.tt.Frame()
	label := "引用 " + q.Name + "：点一下回到原消息"
	if err := h.tt.Click(label); err != nil {
		t.Fatalf("点引用块：%v", err)
	}
	h.until(t, "滚回被引的消息", func() bool {
		first, last := h.a.list.Visible()
		i := h.a.indexOf(target.id)
		return i >= first && i <= last && h.a.quoteHL.Text == fmt.Sprintf("历史 %d", history-1)
	})
	t.Logf("10 角色、%d 历史，驻留消息 %d；界面文字 %d 项；Tester 构建帧最大 %.2f ms", history, len(h.a.msgs), len(h.tt.Texts()), float64(h.maxFrame)/float64(time.Millisecond))
}
