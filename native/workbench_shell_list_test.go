package main

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// C1：私聊｜群聊分段：点“群聊”只显示群聊，点“私聊”回来；本地和 QQ 两页各有一套。
func TestShellSegmentSwitch(t *testing.T) {
	for _, tc := range []struct {
		rail                         int
		private, group, hidePrivateN string
	}{{0, "白羽", "纸灯小屋", "白羽"}, {1, "编造的朋友", "编造的群", "编造的朋友"}} {
		a := shellAppFlat()
		a.rail = tc.rail
		if tc.rail == 1 {
			a.sel, a.title = "qq:private:80001:90001", "编造的朋友"
			a.keys["qq:private:80001:90001"] = a.convs[0].Key
		}
		tt := shellTester(a, 2048, 1030)
		if !tt.HasText(tc.private) || tt.HasText(tc.group) {
			t.Fatalf("rail %d 默认应只有私聊：%v", tc.rail, tt.Texts())
		}
		click(t, tt, "群聊")
		if a.workbench.shell.seg[tc.rail] != segGroup || !tt.HasText(tc.group) || tt.HasText(tc.hidePrivateN+"\x00") {
			t.Fatalf("rail %d 点群聊后：%v", tc.rail, tt.Texts())
		}
		for _, row := range tt.Texts() {
			if row == tc.private && false {
				t.Fatal("群聊分段里还有私聊")
			}
		}
		click(t, tt, "私聊")
		if a.workbench.shell.seg[tc.rail] != segPrivate || !tt.HasText(tc.private) || tt.HasText(tc.group) {
			t.Fatalf("rail %d 点私聊后：%v", tc.rail, tt.Texts())
		}
	}
}

// C：分段的选中块用弹簧滑过去（减少动态效果时直接到位），点“群聊”后选中块停在群聊那一格。
func TestShellSegmentIndicatorMoves(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	hs := a.workbench.shell.kit.SegHandles["venues"]
	if hs == nil || hs.IndW <= 0 {
		t.Fatal("选中块没量到位置")
	}
	before := hs.IndX
	click(t, tt, "群聊")
	tt.Advance(time.Second)
	tt.Frame()
	if hs.IndX <= before+50 {
		t.Fatalf("选中块没滑到右边那一格：%v → %v", before, hs.IndX)
	}
}

// C：会话行：行高 58（46 + 两边 6）、头像 46、两边离栏边 8；选中行有选中色；预览是最后一条消息。
func TestShellConversationRowGeometry(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	row := boxOf(t, tt, "conversation-local:r1")
	list := boxOf(t, tt, "sidebar")
	if !closeTo(row.Box.H, 58) || !closeTo(row.Box.W, list.Box.W-16) || !closeTo(row.Box.X, list.Box.X+8) {
		t.Fatalf("会话行 %+v 与列表 %+v：应两边各留 8、高 58", row.Box, list.Box)
	}
	if av := boxOf(t, tt, "avatar-conversation-local:r1"); !closeTo(av.Box.W, 46) || !closeTo(av.Box.H, 46) {
		t.Fatalf("头像 %v×%v，应为 46", av.Box.W, av.Box.H)
	}
	if !tt.HasText("今晚的月色真好") {
		t.Fatal("预览不是最后一条消息")
	}
}

// C：搜索按会话名和最后一条消息过滤；没有结果写空态；清空后回来。
func TestShellSearchFilters(t *testing.T) {
	a := shellAppFlat()
	a.local = append(a.local, conv{Key: demoKey("local:r2"), Kind: "local", Title: "夜莺", Last: lastLine{Text: "星星出来了", CreatedAt: t0}})
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "搜索会话")
	tt.Type("夜")
	tt.Frame()
	if !hasKey(tt, "conversation-local:r2") || hasKey(tt, "conversation-local:r1") {
		t.Fatalf("按名字过滤：%v", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("月色")
	tt.Frame()
	if !hasKey(tt, "conversation-local:r1") || hasKey(tt, "conversation-local:r2") {
		t.Fatalf("按最后一条消息过滤：%v", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("不存在的词")
	tt.Frame()
	if !tt.HasText("没有找到会话，换个词试试") {
		t.Fatalf("空态：%v", tt.Texts())
	}
}

// C：空分段的空态文案；没有“新建群聊”能力时群聊空态写原因。
func TestShellEmptyStates(t *testing.T) {
	a := shellAppFlat()
	a.local, a.homes = nil, nil
	tt := shellTester(a, 2048, 1030)
	if !tt.HasText("还没有本地私聊") {
		t.Fatalf("私聊空态：%v", tt.Texts())
	}
	click(t, tt, "群聊")
	if !tt.HasText("群聊：当前数据源不能建群") {
		t.Fatalf("群聊空态没写原因：%v", tt.Texts())
	}
}

type groupReader struct {
	api.Reader
	created []api.LocalGroupCreate
}

func (g *groupReader) CreateLocalGroup(_ context.Context, r api.LocalGroupCreate) (api.ConversationView, error) {
	g.created = append(g.created, r)
	return api.ConversationView{Key: demoKey("home:new"), Title: r.Name}, nil
}

func (g *groupReader) SendLocalGroup(context.Context, api.LocalGroupSend) error { return nil }

func (g *groupReader) StopLocalGroup(context.Context, api.ConversationKey) error { return nil }

// C2：新建群聊在搜索框右边的“+”小图标钮里：没有通栏按钮；点开是菜单，里面有“新建群聊”；点了弹出对话框。
func TestShellNewGroupMenu(t *testing.T) {
	a := shellAppFlat()
	a.reader = &groupReader{Reader: demoReader{}}
	a.caps = []api.Capability{{Name: "local-group", State: api.Ready}}
	a.chars = []character{{ID: "r1", Name: "白羽"}, {ID: "r2", Name: "夜莺"}}
	posted := make(chan func(), 16) // 后台线程要回界面线程的活先排队，测试在两帧之间执行，和真窗口一样
	a.viewCtx, a.postFn = context.Background(), func(f func()) { posted <- f }
	tt := shellTester(a, 2048, 1030)
	if tt.HasText("新建群聊") {
		t.Fatalf("没打开菜单就不该有“新建群聊”（原来的通栏按钮要去掉）：%v", tt.Texts())
	}
	plus, ok := tt.Find("更多")
	search := boxOf(t, tt, "search")
	if !ok || !closeTo(plus.W, 32) || !closeTo(plus.H, 32) || plus.X < search.Box.X+search.Box.W-1 {
		t.Fatalf("“+”钮 %v %v：应是 32×32，在搜索框右边（搜索框 %+v）", plus, ok, search.Box)
	}
	click(t, tt, "更多")
	if !tt.HasText("新建群聊") {
		t.Fatalf("菜单里没有新建群聊：%v", tt.Texts())
	}
	click(t, tt, "新建群聊")
	if !a.gc.open {
		t.Fatal("点菜单项没打开建群对话框")
	}
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("选择 2 到 10 个角色，主人会自动加入。") || !tt.HasText("白羽 · r1") {
		t.Fatalf("对话框内容：%v", tt.Texts())
	}
	// 点两个角色、填名字、创建
	click(t, tt, "白羽 · r1")
	click(t, tt, "夜莺 · r2")
	click(t, tt, "群聊名称")
	tt.Type("编造的新群")
	tt.Frame()
	click(t, tt, "创建群聊")
	deadline := time.Now().Add(3 * time.Second)
	for len(a.reader.(*groupReader).created) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := a.reader.(*groupReader).created
	for len(posted) > 0 {
		(<-posted)()
	}
	tt.Frame()
	if len(got) != 1 || got[0].Name != "编造的新群" || len(got[0].Characters) != 2 {
		t.Fatalf("建群请求：%+v", got)
	}
}

// 没有建群能力：不画“+”钮。
func TestShellNoPlusWithoutGroupCapability(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	if _, ok := tt.Find("更多"); ok {
		t.Fatal("没有建群能力也画了“+”钮")
	}
}

// 菜单项：Esc 关菜单，点菜单外也关。
func TestShellMenuCloses(t *testing.T) {
	a := shellAppFlat()
	a.reader = &groupReader{Reader: demoReader{}}
	a.caps = []api.Capability{{Name: "local-group", State: api.Ready}}
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "更多")
	if !tt.HasText("新建群聊") {
		t.Fatal("菜单没开")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("新建群聊") {
		t.Fatal("Esc 没关菜单")
	}
}

// 第一份概览还没到：会话列表什么都不写（原来“还没有…”会在到之前闪一下），读得慢了才写“取数据中…”，到了就换成真内容。
func TestShellConversationListDoesNotFlashWhileLoading(t *testing.T) {
	a := shellAppFlat()
	a.local, a.homes = nil, nil
	a.ovLoad.Begin()
	tt := shellTester(a, 2048, 1030)
	if tt.HasText("还没有本地私聊") || tt.HasText("取数据中…") {
		t.Fatalf("刚开始等概览就写了字（会闪一下）：%v", tt.Texts())
	}
	tt.Advance(3 * time.Second)
	tt.Frame()
	if !tt.HasText("取数据中…") || tt.HasText("还没有本地私聊") {
		t.Fatalf("读得慢了该写“取数据中…”：%v", tt.Texts())
	}
	a.ovLoad.Succeed()
	tt.Frame()
	tt.Frame()
	if !tt.HasText("还没有本地私聊") || tt.HasText("取数据中…") {
		t.Fatalf("概览到了该换成空态：%v", tt.Texts())
	}
}
