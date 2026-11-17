package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/llm/llmtest"
)

// 她向主人提问（规格-向主人提问 第八节）：完整路径走真后台，界面里点选项、打字、先不答。

const uiAskArgs = `{"question":"编的问题：先做哪个","options":[{"label":"方案甲","detail":"快一点"},{"label":"方案乙"}]}`

// shotComposerRegion 把输入区上方到底部的组件区域存成 PNG；只在设了 SUMIKA_SHOT_DIR 时写（截图只给人看，不进测试断言）。
func (h *localUIHarness) shotComposerRegion(t *testing.T, name string) {
	dir := os.Getenv("SUMIKA_SHOT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r, ok := h.tt.Find("提问：编的问题：先做哪个")
	if !ok {
		t.Fatal("找不到问题卡")
	}
	img := h.tt.Image()
	s := 1.25
	crop := img.SubImage(image.Rect(int((float64(r.X)-24)*s), int((float64(r.Y)-24)*s), int((float64(r.X+r.W)+24)*s), min(img.Bounds().Dy(), int((float64(r.Y+r.H)+190)*s)))).(*image.RGBA)
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, crop); err != nil {
		t.Fatal(err)
	}
	t.Logf("截图 %s：%dx%d 像素", name, crop.Bounds().Dx(), crop.Bounds().Dy())
}

func (h *localUIHarness) askCard(t *testing.T) {
	t.Helper()
	h.until(t, "问题卡出现", func() bool { return h.tt.HasText("方案甲") && h.tt.HasText("也可以直接在下面打字回她") })
}

func TestAskOwnerCardClickOption(t *testing.T) {
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", uiAskArgs), llmtest.Text("好的，按甲做")}}, nil)
	h.wbSend(t, "编的任务")
	h.askCard(t)
	h.until(t, "标题栏说在等回答", func() bool { return h.a.wbSubtitle() == "在等你回答" })
	if !h.tt.HasText("先不答") || !h.a.localRun("console:char00").busy {
		t.Fatalf("卡片或忙碌状态不对：%v", h.tt.Texts())
	}
	h.shotComposerRegion(t, "question-card-light.png")
	h.tt.SetDark(true)
	h.tt.Frame()
	h.shotComposerRegion(t, "question-card-dark.png")
	h.tt.SetDark(false)
	h.tt.Frame()
	h.click(t, "方案甲")
	h.until(t, "答完说完", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("好的，按甲做") })
	if h.tt.HasText("方案乙") || h.tt.HasText("先不答") {
		t.Fatalf("答完卡片还在：%v", h.tt.Texts())
	}
	if !h.tt.HasText("方案甲") { // 主人的回答是聊天里的一条消息
		t.Fatalf("回答没进聊天：%v", h.tt.Texts())
	}
}

func TestAskOwnerCardSkip(t *testing.T) {
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", uiAskArgs), llmtest.Text("那我自己定了")}}, nil)
	h.wbSend(t, "编的任务")
	h.askCard(t)
	h.click(t, "先不答")
	h.until(t, "先不答后说完", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("那我自己定了") })
	if h.tt.HasText("方案乙") {
		t.Fatal("卡片还在")
	}
	if body := h.requests(); len(body) != 2 || !strings.Contains(body[1], "先不答") {
		t.Fatalf("先不答没进下一次请求：%d", len(body))
	}
}

// 打字就是回答：卡片开着、输入框有字时发送键回来；发出去结束等待，同一轮接着做，不重复开监听。
// 窗口级 Esc 在卡片开着时不停止。
func TestAskOwnerTypedAnswer(t *testing.T) {
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", uiAskArgs), llmtest.Text("收到，先做丙")}}, nil)
	h.wbSend(t, "编的任务")
	h.askCard(t)
	run := h.a.localRun("console:char00")
	runID := run.runID
	// 输入框没字：还是停止键
	if !h.tt.HasText("停止生成") {
		t.Fatalf("没字时应是停止键：%v", h.tt.Texts())
	}
	// 卡片开着时按 Esc 不停止
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if run.stopping || run.stopped || !h.tt.HasText("方案甲") {
		t.Fatal("Esc 停了这一轮或收了卡片")
	}
	h.click(t, "发一条消息")
	h.tt.Type("都不要，先做丙")
	h.tt.Frame()
	if h.tt.HasText("停止生成") {
		t.Fatalf("有字时发送键应换回来：%v", h.tt.Texts())
	}
	if !h.tt.HasText("发送") {
		t.Fatalf("没有发送键：%v", h.tt.Texts())
	}
	h.shotComposerRegion(t, "question-card-typing-light.png")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until(t, "打字回答后说完", func() bool { return !run.busy && h.tt.HasText("收到，先做丙") })
	if run.runID != "" && run.runID != runID {
		t.Fatalf("应插进同一轮：%q → %q", runID, run.runID)
	}
	if h.tt.HasText("先不答") || h.a.hasQuestion(h.a.keyOf(h.a.sel)) {
		t.Fatal("答完卡片还在")
	}
	if !h.tt.HasText("都不要，先做丙") {
		t.Fatalf("主人的话没进聊天：%v", h.tt.Texts())
	}
	if body := h.requests(); len(body) != 2 || !strings.Contains(body[1], "都不要，先做丙") {
		t.Fatalf("主人的话没进下一次请求：%d", len(body))
	}
}

// 卡片开着时 Alt+回车（排队）不算回答：什么都不发，卡片还在，草稿留着（规格-向主人提问 第四节第 7 条）。
func TestAskOwnerAltEnterDoesNotAnswer(t *testing.T) {
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", uiAskArgs), llmtest.Text("收到")}}, nil)
	h.wbSend(t, "编的任务")
	h.askCard(t)
	run := h.a.localRun("console:char00")
	h.click(t, "发一条消息")
	h.tt.Type("先排着")
	h.tt.Frame()
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.tt.Frame()
	if !h.a.hasQuestion(h.a.keyOf(h.a.sel)) || !run.busy || run.submitting {
		t.Fatal("Alt+回车被当成了回答")
	}
	if len(h.requests()) != 1 {
		t.Fatalf("排队不该发出任何请求：%d", len(h.requests()))
	}
}

// 旧界面：同样的卡片（点选项、先不答）；旧输入框等待时只读，打字回答只在新界面。
func TestAskOwnerCardLegacyView(t *testing.T) {
	h := newLocalUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", uiAskArgs), llmtest.Text("好的，按乙做")}}, nil, 0)
	h.send(t, "编的任务")
	h.askCard(t)
	h.click(t, "方案乙")
	h.until(t, "答完说完", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("好的，按乙做") })
	if h.tt.HasText("先不答") {
		t.Fatal("答完卡片还在")
	}
}
