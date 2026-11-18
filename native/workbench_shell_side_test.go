package main

import (
	"slices"
	"testing"

	"sumika-native/internal/api"
)

// G：右栏的资料：成员来自后台给的“看到这个会话的角色”，写名称和场合；不放活动占位。旧说明：不造数。
func TestShellSideMembersAndActivity(t *testing.T) {
	a := shellApp()
	a.sel, a.title = "home:g1", "纸灯小屋"
	a.workbench.shell.seg[0] = segGroup
	a.workbench.shell.st.Panes.SideOpen = true
	tt := shellTester(a, 2048, 1030)
	side := boxOf(t, tt, "side")
	for _, n := range []string{"白羽", "夜莺"} {
		r, ok := tt.Find("头像：member-" + n)
		if !ok || r.X < side.Box.X {
			t.Fatalf("成员 %s 没在右栏里：%v %v", n, r, ok)
		}
	}
	if !tt.HasText("本地群聊") || !tt.HasText("纸灯小屋") {
		t.Fatalf("资料要写名称和场合：%v", tt.Texts())
	}
	if hasKey(tt, "activity-section") {
		t.Fatal("资料里不再放活动的占位，活动有自己的标签")
	}
	for _, s := range tt.Texts() {
		if len(s) > 3 && (s[:3] == "读了" || s[:3] == "搜了") {
			t.Fatalf("右栏活动里出现了编的数：%q", s)
		}
	}
	// 没有角色的会话：写空态
	a.sel, a.title = "qq:group:90002", "编造的群"
	a.rail = 1
	tt.Frame()
	tt.Frame()
	if !tt.HasText("还没有角色在看这个会话") {
		t.Fatalf("没有成员时没写空态：%v", tt.Texts())
	}
}

// 真实的本地私聊后台不给观察者，角色在会话键 console:<角色>[:topic:<id>] 里；别的场合不靠会话键猜。
func TestConvFromViewLocalPrivateMember(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  api.ConversationKey
		obs  []string
		want []string
	}{
		{api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:lingyin"}, nil, []string{"lingyin"}},
		{api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:lingyin:topic:t1"}, nil, []string{"lingyin"}},
		{api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:lingyin"}, []string{"别的"}, []string{"别的"}},
		{api.ConversationKey{Platform: "local", Scene: "group", Conversation: "home:g1"}, nil, nil},
		{api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:90002"}, nil, nil},
	}
	for _, tc := range cases {
		if got := convFromView(api.ConversationView{Key: tc.key}).Observers; tc.obs == nil && !slices.Equal(got, tc.want) {
			t.Errorf("%v：%v，应为 %v", tc.key, got, tc.want)
		}
		if tc.obs != nil {
			if got := convFromView(api.ConversationView{Key: tc.key, Observers: tc.obs}).Observers; !slices.Equal(got, tc.want) {
				t.Errorf("%v 有观察者时不该改：%v", tc.key, got)
			}
		}
	}
}
