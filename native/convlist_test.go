package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"sumika-native/internal/api"
)

func listConv(id, name, text string) conv {
	return conv{Key: demoKey(id), Title: name, Last: lastLine{Text: text, CreatedAt: t0}}
}

func TestConvListSurvivesEmptyAndUnnamed(t *testing.T) {
	a := &app{rail: 1, convs: []conv{
		listConv("qq:private:80001:90001", "编造的朋友", "在吗"),
		{Key: demoKey("qq:private:80001:90002"), Title: "没说过话的"},
		listConv("qq:private:80001:90003", " ", "嗯"),
	}}
	a.convs[2].Unread = -1
	tt := testerOf(400, 400, a.convList)
	if !tt.HasText(unnamedConv) || !tt.HasText("编造的朋友") || tt.HasText("没说过话的") {
		t.Fatalf("可见条目=%q", tt.Texts())
	}
	a.rail = 0
	a.local = []conv{{Key: demoKey("local:blank"), Title: "空日常"}}
	tt.Frame()
	if !tt.HasText("空日常") || !tt.HasText("还没有消息") || tt.HasText("00:00") {
		t.Fatalf("空本地日常被隐藏或伪造时间: %q", tt.Texts())
	}
}

func TestConvListLongGroupNameStaysInside(t *testing.T) {
	names := []string{strings.Repeat("宽", 400), strings.Repeat("W", 400), "短群名"}
	a := &app{rail: 1}
	for i, n := range names {
		a.convs = append(a.convs, listConv(fmt.Sprintf("qq:group:%d", 90000+i), n, "最后一句"))
	}
	tt := testerOf(300, 600, a.convList)
	for _, n := range names {
		r, ok := tt.Find(n)
		if !ok || r.X != 0 || r.W != 300 || r.H != 62 {
			t.Fatalf("会话行越界或被长名撑高: %+v exists=%v", r, ok)
		}
	}
}

func TestConvListBigUnreadBadgeFits(t *testing.T) {
	counts := []int{0, 5, 99, 100, 5000, 99999, 1234567}
	a := &app{rail: 1}
	for i, n := range counts {
		cv := listConv(fmt.Sprintf("qq:group:%d", 90000+i), fmt.Sprintf("编造的群%d", i), "最后一句")
		cv.Unread = n
		a.convs = append(a.convs, cv)
	}
	tt := testerOf(300, 700, a.convList)
	for i, n := range counts {
		row, ok := tt.Find(fmt.Sprintf("编造的群%d", i))
		if !ok || row.W != 300 || row.H != 62 {
			t.Fatalf("未读%d撑大行: %+v", n, row)
		}
		if n == 0 {
			continue
		}
		badge, ok := tt.Find(fmt.Sprint(n))
		if !ok || badge.X+badge.W > row.X+row.W || badge.X < row.X+row.W/2 {
			t.Fatalf("未读%d越界: row=%+v badge=%+v exists=%v", n, row, badge, ok)
		}
	}
}

func TestConvListSearch(t *testing.T) {
	a := &app{rail: 1, convs: []conv{listConv("qq:group:90001", "Alpha 小组", "周末见"), listConv("qq:private:80001:90002", "编造乙", "Meeting tomorrow"), listConv("qq:group:90003", "编造丙", "其他消息"), listConv("qq:group:90004", " ", "无名消息")}, local: []conv{listConv("local:a", "本地 Alpha", "本地消息")}, homes: []conv{listConv("home:a", "本地小屋", "Meeting today")}}
	tt := testerOf(280, 600, a.convList)
	search := func(text string) { click(t, tt, "搜索会话"); tt.Type(text); tt.Frame() }
	search("  aLPHa  ")
	if !tt.HasText("Alpha 小组") || tt.HasText("编造乙") {
		t.Fatal("名字搜索未去空白/忽略大小写")
	}
	click(t, tt, "清空会话搜索")
	if !tt.Focused("搜索会话") {
		t.Fatal("清空后丢失输入焦点")
	}
	tt.Type("MEETING")
	tt.Frame()
	if !tt.HasText("编造乙") || tt.HasText("Alpha 小组") {
		t.Fatal("没有按预览过滤")
	}
	click(t, tt, "清空会话搜索")
	search("未命名")
	if !tt.HasText(unnamedConv) {
		t.Fatal("无名会话不能搜索")
	}
	click(t, tt, "清空会话搜索")
	search("找不到")
	if !tt.HasText("没有匹配的会话") {
		t.Fatal("缺少无匹配状态")
	}
	a.rail = 0
	tt.Frame()
	if !tt.HasText("本地 Alpha") || !tt.HasText("本地小屋") {
		t.Fatal("平台搜索词混用")
	}
	search("meeting")
	if !tt.HasText("本地小屋") || tt.HasText("本地 Alpha") {
		t.Fatal("本地群预览搜索失败")
	}
	a.rail = 1
	tt.Frame()
	if !tt.HasText("没有匹配的会话") {
		t.Fatal("QQ搜索词没有保留")
	}
}

func TestConvListSearchInDragZone(t *testing.T) {
	a := panesApp()
	tt := barTester(a, 2048, 1030)
	field, ok := tt.Find("搜索会话")
	if !ok || field.Y < 32 || field.H != 35 {
		t.Fatalf("搜索框=%+v", field)
	}
	click(t, tt, "搜索会话")
	tt.Type("小桃")
	tt.Frame()
	if !tt.HasText("小桃") || tt.HasText("小葵") {
		t.Fatal("原生标题带下搜索不能过滤")
	}
	click(t, tt, labelListFold)
	if _, ok := tt.Find("搜索会话"); ok {
		t.Fatal("头像栏仍有搜索框")
	}
	if !a.pn.layout.Compact {
		t.Fatal("收起键失效")
	}
}

func TestConvListSearchWidths(t *testing.T) {
	for _, width := range []int{260, 280, 540} {
		for _, query := range []string{"", strings.Repeat("很长的搜索词", 40)} {
			a := &app{rail: 1}
			a.search[1] = query
			tt := testerOf(width, 600, a.convList)
			tt.SetScale(1.25)
			field, fok := tt.Find("搜索会话")
			fold, bok := tt.Find(labelListFold)
			if !fok || !bok || field.X < 0 || field.W <= 0 || field.X+field.W > fold.X || fold.X+fold.W > float32(width) || field.H != 35 || fold.W < 32 || fold.H < 32 {
				t.Fatalf("width=%d field=%+v fold=%+v", width, field, fold)
			}
		}
	}
}

func TestConvListSelectionFollowsIDAfterFilterAndOverview(t *testing.T) {
	a := panesApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	a.reader = demoReader{}
	a.keys = map[string]api.ConversationKey{}
	for _, cv := range a.local {
		a.keys[cv.Key.Conversation] = cv.Key
	}
	tt := paneTester(a, 2048, 1030)
	click(t, tt, "小桃")
	selected := a.dialogs[0].state.SelectedID
	if a.sel != "console:c1" || selected != dialogID(a.local[1].Key) {
		t.Fatal("选择没有按完整ID打开")
	}
	// 同样三条，换序并更新预览，模拟概览快照。
	a.local[0], a.local[1] = a.local[1], a.local[0]
	a.local[0].Last.Text = "新的预览"
	a.dialogRevision++
	tt.Frame()
	if a.dialogs[0].state.SelectedID != selected || !tt.HasText("新的预览") {
		t.Fatal("概览重排串了选择或没有更新预览")
	}
	click(t, tt, "搜索会话")
	tt.Type("小葵")
	tt.Frame()
	if a.sel != "console:c1" || a.dialogs[0].state.SelectedID != selected {
		t.Fatal("选中会话被过滤后身份改变")
	}
	click(t, tt, "清空会话搜索")
	if a.dialogs[0].state.SelectedID != selected {
		t.Fatal("清空筛选后丢了选中身份")
	}
	// 输入焦点里打方向键不能更换会话；列表键盘选择仍可用。
	click(t, tt, "会话条目")
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.sel == "console:c1" {
		t.Fatal("列表方向键没有选择下一会话")
	}
}

func TestConvListCurrentConversationReopensInNarrowList(t *testing.T) {
	for _, width := range []int{380, 451, 711} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := panesApp()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.ctx, a.reader = ctx, demoReader{}
			a.keys = map[string]api.ConversationKey{}
			for _, cv := range a.local {
				a.keys[cv.Key.Conversation] = cv.Key
			}
			selected := a.sel
			tt := paneTester(a, width, 1030)
			for range 2 {
				click(t, tt, "返回会话列表")
				click(t, tt, a.local[0].Title)
				if chat, ok := tt.Find("聊天窗口"); !ok || chat.W <= 0 || chat.H <= 0 || a.pn.layout.Active != layout.Chat {
					t.Fatal("single-pane-reopen: 返回列表后单击当前会话没有进入聊天")
				}
				if a.sel != selected {
					t.Fatalf("重新进入同一会话改变身份: %q", a.sel)
				}
			}
			tt.SetSize(2048, 1030)
			tt.Frame()
			if a.dialogs[0].state.SelectedID != dialogID(a.local[0].Key) {
				t.Fatal("放宽窗口后没有恢复当前会话高亮")
			}
		})
	}
}

func TestConvListThousandRowsAndCompactSelection(t *testing.T) {
	a := panesApp()
	a.rail = 1
	a.convs = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	a.reader = demoReader{}
	for i := range 1000 {
		a.convs = append(a.convs, listConv(fmt.Sprintf("qq:group:%d", 90000+i), fmt.Sprintf("虚拟会话%04d", i), "最后一句"))
	}
	tt := paneTester(a, 2048, 1030)
	count := func() int {
		rows := map[string]bool{}
		for _, s := range tt.Texts() {
			if strings.HasPrefix(s, "虚拟会话") {
				rows[s] = true
			}
		}
		return len(rows)
	}
	if n := count(); n > 20 || n < 10 {
		t.Fatalf("1000会话实际构建行=%d", n)
	}
	click(t, tt, labelListFold)
	tt.Advance(time.Second)
	if l, _, _ := paneWidths(tt); l != 66 {
		t.Fatalf("头像栏宽=%v", l)
	}
	click(t, tt, "虚拟会话0001")
	if a.sel != "qq:group:90001" {
		t.Fatalf("头像窄栏选择=%q", a.sel)
	}
	if n := count(); n > 20 {
		t.Fatalf("头像栏没有虚拟化: %d", n)
	}
}
