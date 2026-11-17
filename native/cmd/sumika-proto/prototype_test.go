package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"math"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

func tester(a *app) *ui.Tester { tt := ui.NewTester(a.view, 2048, 1030); tt.SetScale(1.25); return tt }
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}
func typeMessage(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	click(t, tt, "消息")
	tt.Type(text)
}
func TestSendStreamRetryStop(t *testing.T) {
	a := newApp()
	tt := tester(a)
	cv := a.E.conv("A")
	typeMessage(t, tt, "沿着河边走走。")
	click(t, tt, "发送")
	if len(cv.Messages) != 4 || cv.Messages[3].State != "sending" {
		t.Fatalf("未进入假后端：%+v", cv.Messages)
	}
	tt.Advance(200 * time.Millisecond)
	if cv.Messages[3].State != "sent" || cv.Draft != "" {
		t.Fatal("确认后未清草稿或未送达", cv.Draft)
	}
	tt.Advance(400 * time.Millisecond)
	if len(cv.Messages) != 5 || cv.Messages[4].State != "streaming" || cv.Messages[4].Text == "" || !tt.HasText(cv.Messages[4].Text) {
		t.Fatal("没有显示中间流块")
	}
	click(t, tt, "停止")
	partial := cv.Messages[4].Text
	tt.Advance(3 * time.Second)
	if cv.Messages[4].State != "interrupted" || cv.Messages[4].Text != partial || a.E.active("A") {
		t.Fatal("停止后回复复活")
	}
	cv.FailNext = true
	typeMessage(t, tt, "再问一句。")
	click(t, tt, "发送")
	tt.Advance(200 * time.Millisecond)
	failed := cv.Messages[len(cv.Messages)-1]
	id := failed.ID
	if failed.State != "failed" || cv.Draft != "再问一句。" {
		t.Fatal("失败未保留草稿", failed, cv.Draft)
	}
	click(t, tt, "发送失败，重试")
	tt.Advance(4 * time.Second)
	if failed.ID != id || failed.State != "sent" || failed.Attempt != 2 {
		t.Fatal("重试不是同一消息", failed)
	}
	self := 0
	for _, m := range cv.Messages {
		if m.ID == id {
			self++
		}
	}
	if self != 1 || cv.Messages[len(cv.Messages)-1].State != "complete" {
		t.Fatal("重试重复或没有完成回复")
	}
	t.Logf("发送→确认→流式→停止→失败→同ID重试通过；保留字数=%d", len([]rune(partial)))
}
func TestFollowingAndDetached(t *testing.T) {
	a := newApp()
	cv := a.E.conv("A")
	a.E.history(cv, 160)
	tt := tester(a)
	r := a.read("A")
	typeMessage(t, tt, "讲一段更长的话。")
	click(t, tt, "发送")
	tt.Advance(900 * time.Millisecond)
	if !r.List.AtEnd() {
		t.Fatal("流式时未跟随末尾")
	}
	last := cv.Messages[len(cv.Messages)-1]
	rect, ok := tt.Find("消息：" + last.ID)
	composer, _ := tt.Find("消息")
	if !ok || rect.Y+rect.H > composer.Y-spacePanel+1 {
		t.Fatalf("末条被输入挡住：%+v composer=%+v", rect, composer)
	}
	tt.Scroll(1100, 400, 0, -650)
	if r.List.AtEnd() {
		t.Fatal("上翻没有脱离底部")
	}
	first, _ := r.List.Visible()
	anchor := cv.Messages[first].ID
	before, ok := tt.Find("消息：" + anchor)
	if !ok {
		t.Fatal("没有锚点", first)
	}
	tt.Advance(3 * time.Second)
	after, ok := tt.Find("消息：" + anchor)
	if !ok || math.Abs(float64(after.Y-before.Y)) > 1 {
		t.Fatalf("上翻后拉回：%+v → %+v", before, after)
	}
	click(t, tt, "回到最新")
	if !r.List.AtEnd() {
		t.Fatal("回最新没有恢复跟随")
	}
	t.Logf("锚点=%s 位移=%.3f DIP，末帧构建行数=%d/总数=%d", anchor, after.Y-before.Y, r.Rows, len(cv.Messages))
}
func TestInterleavingAndIsolation(t *testing.T) {
	a := newApp()
	a.Selected["local"] = "B"
	a.Group = 1
	tt := tester(a)
	cv := a.E.conv("B")
	base := len(cv.Messages) + 1 // 发出去的那条之后（初始消息有几条都不影响）
	typeMessage(t, tt, "把两个人想到的都说说。")
	click(t, tt, "发送")
	tt.Advance(900 * time.Millisecond)
	if len(cv.Messages) != base+2 || cv.Messages[base].Actor != "r1" || cv.Messages[base+1].Actor != "r2" || cv.Messages[base].State != "streaming" || cv.Messages[base+1].State != "streaming" {
		t.Fatal("未出现两个角色交错中间态", len(cv.Messages))
	}
	tt.Advance(5 * time.Second)
	if len(cv.Messages) != base+4 {
		t.Fatalf("没有分成四条消息：%d", len(cv.Messages))
	}
	for _, m := range cv.Messages[base:] {
		if m.State != "complete" {
			t.Fatal("分段没有结束", m)
		}
	}
	typeMessage(t, tt, "留在群里的草稿")
	click(t, tt, "私聊")
	click(t, tt, "晴枝")
	if a.cv().ID != "A" || a.cv().Draft != "" {
		t.Fatal("会话未切换或草稿串了")
	}
	a.Scenario.Target = "B"
	a.Scenario.Actor = "r2"
	a.scenarioRun(false, false)
	tt.Frame()
	tt.Advance(4 * time.Second)
	if a.cv().ID != "A" || len(a.E.conv("A").Messages) != 3 || cv.Draft != "留在群里的草稿" || cv.Unread != 1 {
		t.Fatal("后台回复串到当前会话", a.cv().ID, cv.Unread)
	}
	click(t, tt, "群聊")
	click(t, tt, "纸灯小屋")
	if a.cv().Draft != "留在群里的草稿" {
		t.Fatal("群草稿丢了")
	}
	t.Log("r1/r2同时流式，随后各自第二条；后台B不抢A焦点和草稿")
}
func TestScriptClockAndBoundaries(t *testing.T) {
	a := newApp()
	cv := a.E.conv("A")
	s := replyScript(cv, "run.start", false, true)
	source := scriptJSON(s)
	for name, text := range map[string]string{"version": strings.Replace(source, "\"version\": 1", "\"version\": 2", 1), "unknown": strings.Replace(source, "\"seed\": 42", "\"seed\": 42, \"exec\": true", 1), "target": strings.Replace(source, "\"conversation\": \"A\"", "\"conversation\": \"B\"", 1), "actor": strings.Replace(source, "\"actor\": \"r1\"", "\"actor\": \"self\"", 1), "await": strings.Replace(source, "\"ref\": \"read\"", "\"ref\": \"missing\"", 1)} {
		if _, err := parseScript(text, a.E); err == nil {
			t.Errorf("%s被接收", name)
		}
	}
	parsed, err := parseScript(source, a.E)
	if err != nil {
		t.Fatal(err)
	}
	a.E.start(parsed, "run.start", "")
	a.E.Paused = true
	tt := tester(a)
	tt.Advance(10 * time.Second)
	click(t, tt, "场景")
	if a.E.Now != 0 || len(cv.Messages) != 3 {
		t.Fatal("暂停没有冻结")
	}
	a.E.next()
	if a.E.Now != 300*time.Millisecond {
		t.Fatal("单步没有推进下一事件")
	}
	a.E.Paused = false
	tt.Frame()
	tt.Advance(4 * time.Second)
	// 读搜进了这一轮的过程消息（两步都完成），回复排在它后面
	if a.E.ReadCount["A"] != 1 || a.E.SearchCount["A"] != 1 || len(cv.Messages) != 5 || len(cv.Messages[3].Steps) != 2 || cv.Messages[3].Steps[1].State != chatui.StepDone || cv.Messages[4].State != "complete" || !tt.HasText("读了 1 份资料，搜了 1 次") {
		t.Fatal("await未串起读搜和回复")
	}
	all := replyScript(cv, "message.accepted", false, false)
	other := replyScript(a.E.conv("B"), "message.accepted", false, false)
	all.Tracks = append(all.Tracks, other.Tracks...)
	a.E.start(all, "message.accepted", "A")
	if a.E.active("B") {
		t.Fatal("输入事件没有按target过滤")
	}
	a.E.stop("A")
	now := a.E.Now
	a.E.advance(now + time.Minute)
	if a.E.active("A") {
		t.Fatal("停止没有终结轨道")
	}
	withoutAwait := replyScript(cv, "run.start", false, false)
	withoutAwait.Tracks[0].Steps = []step{{Op: "trigger", Trigger: "M06", ID: "unawaited", Args: args{Text: "还会说完。", Chunk: "grapheme", IntervalMS: 50}}}
	a.E.start(withoutAwait, "run.start", "")
	a.E.advance(a.E.Now + time.Second)
	if m := cv.Messages[len(cv.Messages)-1]; m.Text != "还会说完。" || m.State != "complete" || a.E.active("A") {
		t.Fatal("轨道结束丢弃了已派出的回复", m)
	}
	a.Scenario.Count = "0"
	click(t, tt, "让她回一句")
	if len(a.E.Events) != 0 {
		t.Fatal("0次触发仍派事件")
	}
	a.Scenario.Count = "-1"
	click(t, tt, "让她回一句")
	if !tt.HasText("次数必须是") || len(a.E.Events) != 0 {
		t.Fatal("非法次数未在面板报错")
	}
	a.E.schedule(0, "", func() { t.Error("旧run事件执行") })
	a.E.Run++
	a.E.advance(a.E.Now)
	t.Log("拒绝未知字段/版本/目标/角色/引用；暂停、单步、await、停止、旧run丢弃通过")
}
func TestResponsiveAndLargeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("压力和规模测试，放在每日整套")
	}
	for _, w := range []float32{2048, breakpointWide + 1, breakpointWide, breakpointWide - 1, breakpointNarrow + 1, breakpointNarrow, breakpointNarrow - 1, 480} {
		for _, dark := range []bool{false, true} {
			a := newApp()
			a.Dark = dark
			a.Layer = 1
			a.E.history(a.E.conv("A"), 10000)
			tt := ui.NewTester(a.view, int(w), 560)
			tt.SetScale(1.25)
			if _, ok := tt.Find("发送"); !ok {
				t.Fatalf("%v dark=%v没有发送", w, dark)
			}
			typeMessage(t, tt, strings.Repeat("很长的中文草稿，发送后输入区应自动收回。\n", 8))
			height := a.read("A").Height
			if height <= composerMin+spacePanel {
				t.Fatalf("%v长草稿没有实际撑高输入区：%.2f", w, height)
			}
			click(t, tt, "发送")
			tt.Advance(220 * time.Millisecond)
			if a.read("A").Height >= height {
				t.Fatalf("%v草稿清空未自动回填 %.2f→%.2f", w, height, a.read("A").Height)
			}
			if n := a.read("A").Rows; n > 70 {
				t.Fatalf("万条历史构建了%d行", n)
			}
			send, ok := tt.Find("发送")
			if !ok || send.W < 24 || send.H < 24 || send.X < 0 || send.X+send.W > w {
				t.Fatalf("%v发送框越界：%+v", w, send)
			}
			t.Logf("宽=%.0f 深=%v 行数=%d/10002 composer=%.2f→%.2f", w, dark, a.read("A").Rows, height, a.read("A").Height)
		}
	}
	a := newApp()
	a.Layer = 0
	tt := ui.NewTester(a.view, 480, 560)
	click(t, tt, "晴枝")
	if a.Layer != 1 {
		t.Fatal("第一层进聊天失败")
	}
	typeMessage(t, tt, "窄窗草稿")
	click(t, tt, "资料与活动")
	if a.Layer != 2 {
		t.Fatal("聊天进资料失败")
	}
	click(t, tt, "场景")
	if !a.Scenes {
		t.Fatal("资料层没有直接打开场景面板")
	}
	click(t, tt, "收起")
	click(t, tt, "返回聊天")
	if a.Layer != 1 || a.cv().Draft != "窄窗草稿" {
		t.Fatal("返回聊天丢状态")
	}
	click(t, tt, "返回")
	if a.Layer != 0 {
		t.Fatal("没有逐层退回")
	}
}
func TestIMEAndQueuedMessages(t *testing.T) {
	a := newApp()
	tt := tester(a)
	cv := a.E.conv("A")
	typeMessage(t, tt, "还在组词")
	tt.Compose("输入", 2)
	tt.Key(0, ui.KeyEnter)
	if len(cv.Messages) != 3 {
		t.Fatal("输入法确认误发送")
	}
	tt.Compose("", 0)
	tt.Type("确认")
	tt.Key(ui.Shift, ui.KeyEnter)
	if !strings.Contains(cv.Draft, "\n") {
		t.Fatal("Shift+Enter没有换行")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Advance(600 * time.Millisecond)
	typeMessage(t, tt, "下一句")
	tt.Key(0, ui.KeyEnter)
	if len(cv.Queued) != 1 {
		t.Fatal("运行时没有排队", cv.Queued)
	}
	tt.Advance(8 * time.Second)
	if len(cv.Queued) != 0 {
		t.Fatal("没有继续队列")
	}
	count := 0
	for _, m := range cv.Messages {
		if m.Text == "下一句" && m.Actor == "self" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("排队消息没有恰好发送一次", count)
	}
	t.Log(fmt.Sprint("IME和多行、运行时排队通过"))
}
