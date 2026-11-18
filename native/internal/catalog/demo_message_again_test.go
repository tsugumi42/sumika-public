package catalog

import (
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

// 主人 2026-11-16：画廊里点“重新来”以后，再点“允许”，审批卡就直接变成结果、没有收起的动画了。
// 每一次决定（第一次和重新来之后）都要有几帧“允许一次”按钮和“已允许”同时在（同一张卡在变）。
func TestMessageEntryCollapsesAgain(t *testing.T) {
	font := look()
	e, ok := Find("message")
	if !ok {
		t.Fatal("没有登记 message 条目")
	}
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	decide := func(round int) {
		if err := tt.Click("允许"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		both := 0
		for i := 0; i < 120; i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			if tt.HasText("允许一次") && tt.HasText("已允许") {
				both++
			}
			if !tt.HasText("允许一次") && i > 5 {
				break
			}
		}
		if both < 2 {
			t.Errorf("第 %d 次决定：只有 %d 帧按钮和结果同时在，卡是直接换成结果的，没有收起", round, both)
		}
		for i := 0; i < 120 && tt.HasText("允许一次"); i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	}
	decide(1)
	for round := 2; round <= 3; round++ {
		if err := tt.Click("重新来"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 40; i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
		if !tt.HasText("允许一次") {
			t.Fatalf("重新来后应是新的一张等批准的卡：%v", tt.Texts())
		}
		decide(round)
	}
}
