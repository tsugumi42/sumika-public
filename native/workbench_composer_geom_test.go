package main

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

func composerGeomApp(width int) (*app, *ui.Tester) {
	now := time.Now().UnixMilli()
	a := &app{sel: "c", demoMode: true, msgs: []*msg{
		{id: "p", who: "她", actor: "r1", text: "对方说的一句", blocks: segview.TextBlocks("对方说的一句"), at: now},
		{id: "s", who: "我", mine: true, text: "我说的一句", blocks: segview.TextBlocks("我说的一句"), at: now},
	}}
	a.list = a.newList()
	a.composerOf("c").Main.Text = "测试文字"
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, width, 700)
	tt.Frame()
	tt.Frame()
	return a, tt
}

// 输入框和消息列同宽、同一条左右边线：默认全宽，宽窄窗口都随窗口。
// 字到左、上边缘，发送键到右、下边缘，四个距离相等（都是内距）。
func TestWorkbenchComposerAlignsWithMessageColumn(t *testing.T) {
	for _, width := range []int{1370, 900, 480} {
		a, tt := composerGeomApp(width)
		set := chatui.DefaultSettings()
		set.Text = a.legacyTextSize() // 和界面画的同一个字号档（默认 15，ADR-350）
		tok := chatui.Derive(set)
		pad := chatui.ComposerPad(tok)
		box, ok := tt.Find("输入区")
		if !ok {
			t.Fatalf("宽 %d：没找到输入区：%v", width, tt.Texts())
		}
		want := float32(width - 32) // 默认全宽，两边各留 16（ADR-326）
		if box.W < want-1 || box.W > want+1 {
			t.Fatalf("宽 %d：输入框宽 %.1f，应为消息列宽 %.0f", width, box.W, want)
		}
		// 对方的气泡贴列的左边，自己的气泡贴列的右边：和输入框的左右边线对齐
		var left, right float32 = 1e9, -1e9
		for _, label := range []string{"消息：p", "消息：s"} {
			r, ok := tt.Find(label)
			if !ok {
				t.Fatalf("宽 %d：没找到 %s", width, label)
			}
			left, right = min(left, r.X), max(right, r.X+r.W)
		}
		if abs32f(left-box.X) > 1 || abs32f(right-(box.X+box.W)) > 1 {
			t.Fatalf("宽 %d：消息列 [%.1f, %.1f] 和输入框 [%.1f, %.1f] 没对齐", width, left, right, box.X, box.X+box.W)
		}
		text, ok := tt.Find("发一条消息") // 输入框自己的框（文字从它的左上角起排）
		if !ok {
			t.Fatalf("宽 %d：没找到输入框：%v", width, tt.Texts())
		}
		send, ok := tt.Find("发送")
		if !ok {
			t.Fatalf("宽 %d：没找到发送键", width)
		}
		dl, dt := text.X-box.X, text.Y-box.Y
		dr, db := box.X+box.W-(send.X+send.W), box.Y+box.H-(send.Y+send.H)
		t.Logf("宽 %d：左 %.1f 上 %.1f 右 %.1f 下 %.1f 内距 %.1f，输入框 %.0f×%.0f，发送键 %.1f", width, dl, dt, dr, db, pad, box.W, box.H, send.W)
		// 胶囊（ADR-351）：发送键离上、右、下一样远（和外框同心）；文字左边留 ComposerPad + 描边，单行控件左右可以比上下宽
		st := send.Y - box.Y
		if abs32f(dr-db) > 1 || abs32f(st-db) > 1 || abs32f(dl-(pad+1)) > 1 {
			t.Fatalf("宽 %d：左 %.1f（应 %.1f）、发送键上 %.1f 右 %.1f 下 %.1f（应一样）", width, dl, pad+1, st, dr, db)
		}
		_ = dt
		if send.W < 32 || abs32f(send.W-send.H) > 0.5 {
			t.Fatalf("宽 %d：发送键 %.1f×%.1f，应是不小于 32 的圆", width, send.W, send.H)
		}
		if box.H < chatui.ComposerMinHeight(tok)-1 {
			t.Fatalf("宽 %d：输入框高 %.1f，小于胶囊高 %.1f", width, box.H, chatui.ComposerMinHeight(tok))
		}
		if got := chatui.ComposerRadius(tok) - pad; abs32f(got-chatui.ComposerSend(tok)/2) > 0.01 {
			t.Fatalf("键圆角 %.2f 和外框同心应是外框圆角 − 内距 = %.2f", chatui.ComposerSend(tok)/2, got)
		}
		if chatui.ComposerSendIcon(tok) < 20 || chatui.ComposerSend(tok) < 32 {
			t.Fatalf("发送图标 %.1f、键 %.1f 太小", chatui.ComposerSendIcon(tok), chatui.ComposerSend(tok))
		}
	}
}

func abs32f(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func TestShotWorkbenchComposer(t *testing.T) {
	if os.Getenv("NEWUI_SHOT") == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	a, tt := composerGeomApp(1370)
	a.composerOf("c").Main.Text = ""
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	cropShot(t, tt, "输入区", "8-composer.png")
}
