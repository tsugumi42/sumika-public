package catalog

import (
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

// 消息和审批条目：点“允许”，审批卡收成一行结果（只有一个结果字、没有“允许一次”按钮了）；“重新来”换一张新的；
// 点“追加表格”，消息里多一个表格；点“清空”回到只有那句话。
func TestMessageEntry(t *testing.T) {
	font := look()
	e, ok := Find("message")
	if !ok {
		t.Fatal("没有登记 message 条目")
	}
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	if !tt.HasText("想运行命令") || !tt.HasText("允许一次") {
		t.Fatalf("起点应是一张等批准的卡：%v", tt.Texts())
	}
	if err := tt.Click("允许"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for i := 0; i < 600 && tt.FramePending(); i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Error("卡收完以后不该再要帧")
	}
	if !tt.HasText("已允许") || tt.HasText("允许一次") {
		t.Errorf("收完应只剩结果：%v", tt.Texts())
	}
	if err := tt.Click("重新来"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for i := 0; i < 600 && tt.FramePending(); i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		if i > 40 {
			break // 倒计时每秒要一帧，不会停
		}
	}
	if !tt.HasText("允许一次") {
		t.Errorf("重新来后应是新的一张等批准的卡：%v", tt.Texts())
	}

	if err := tt.Click("追加表格"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for i := 0; i < 40; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if !tt.HasText("纸灯.md") {
		t.Errorf("追加后消息里应有表格：%v", tt.Texts())
	}
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("纸灯.md") {
		t.Errorf("清空后表格应不见了")
	}
}
