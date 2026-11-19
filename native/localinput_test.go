package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// 草稿编辑器跨发送/停止切换保持身份；禁用的未实现动作不能夺走发送职责。
func TestLocalInputKeyboardAndGenerationGeometry(t *testing.T) {
	draft := ""
	busy, stopping := false, false
	sent, stops := "", 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Justify(ui.End).Children(func() {
			localInput(c, &draft, busy, stopping, false, func(text string) { sent = text; draft = ""; busy = true }, func() { stops++; stopping = true })
		})
	}, 380, 480)
	tt.SetScale(1.25)
	tt.Frame()
	if err := tt.Click("发一条本地消息"); err != nil {
		t.Fatal(err)
	}
	tt.Type("编造第一行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第二行🙂 English 123")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第三行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第四行")
	tt.Frame()
	before, ok := tt.Find("发一条本地消息")
	if !ok || before.H <= theme.Light().Metrics().Compose().FieldMinHeight() {
		t.Fatalf("多行未增高：%+v", before)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if sent != "编造第一行\n第二行🙂 English 123\n第三行\n第四行" || draft != "" || !busy {
		t.Fatalf("多行提交或清稿错误：sent=%q draft=%q busy=%v", sent, draft, busy)
	}
	field, _ := tt.Find("发一条本地消息")
	// 新回合空稿后字段变矮，但提交槽替换不能改变字段的横向几何。
	if field.X != before.X || field.W != before.W {
		t.Fatalf("停止替换使字段横移：%+v→%+v", before, field)
	}
	tt.Type("生成中不该写入")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if draft != "" {
		t.Fatalf("生成中只读字段被改动：%q", draft)
	}
	if err := tt.Click("停止生成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	_ = tt.Click("停止生成")
	tt.Frame()
	if stops != 1 {
		t.Fatalf("停止在途重复提交：%d", stops)
	}
	busy, stopping = false, false
	tt.Frame()
	if err := tt.Click("发一条本地消息"); err != nil {
		t.Fatal(err)
	}
	tt.Type("停止后继续")
	tt.Frame()
	if draft != "停止后继续" {
		t.Fatalf("停止后编辑未恢复：%q", draft)
	}
	tt.Advance(time.Second)
	for _, label := range []string{"附件输入尚未接入", "表情选择尚未接入"} {
		_ = tt.Click(label)
		tt.Frame()
		if sent != "编造第一行\n第二行🙂 English 123\n第三行\n第四行" || stops != 1 {
			t.Fatal("禁用动作执行了提交")
		}
	}
}
