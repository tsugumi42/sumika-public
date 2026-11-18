package catalog

import (
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

// 菜单条目：三种菜单都能打开、选一项、结果写到下面那行；右键菜单从“右键这里”那块打开。
func TestMenusEntry(t *testing.T) {
	font := look()
	e, ok := Find("menus")
	if !ok {
		t.Fatal("没有登记 menus 条目")
	}
	tt := openEntry(t, e, chatui.DefaultSettings(), font)
	wait := func() {
		for i := 0; i < 120 && tt.FramePending(); i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
		tt.Frame()
	}
	cases := []struct {
		open  func() error
		item  string
		after string
	}{
		{func() error { return tt.Click("换个角色回复") }, "雾栞", "刚才：换成雾栞。"},
		{func() error { return tt.Click("回复用的模型") }, "Agnes 2.5 Pro", "刚才：选了 Agnes 2.5 Pro。"},
		{func() error { return tt.RightClick("右键这里") }, "标为已读", "刚才：标为已读。"},
	}
	for _, c := range cases {
		if err := c.open(); err != nil {
			t.Fatal(err)
		}
		wait()
		if err := tt.Click(c.item); err != nil {
			t.Fatalf("打开以后点不到 %q：%v %v", c.item, err, tt.Texts())
		}
		wait()
		if !tt.HasText(c.after) {
			t.Errorf("选了 %q 以后应写 %q：%v", c.item, c.after, tt.Texts())
		}
	}
}
