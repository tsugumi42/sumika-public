package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	backend "sumika-native/internal/app"
	"sumika-native/internal/llm/llmtest"
)

type shown struct{ id, title, body string }

// 主窗口不在前台就弹，在前台且正看着这个会话不弹；弹不出来只记日志，不 panic；通知的 ID 带前缀，按会话和种类分开。
func TestAttentionNotifiesOnlyWhenWindowNotForeground(t *testing.T) {
	t.Parallel()
	var got []shown
	var fail error
	s := &shell{a: &app{}}
	s.a.rm.view.Store("console:example") // 正摆着的就是这个会话
	s.showNotice = func(id, title, body string) error {
		got = append(got, shown{id, title, body})
		return fail
	}
	ev := backend.Attention{Kind: backend.AttentionGoalDone, Conversation: "console:example", Character: "example", Title: "目标达成", Body: "做完了"}

	s.a.rm.fg.Store(true)
	s.attention(ev)
	if len(got) != 0 {
		t.Fatalf("窗口在前台且正看着这个会话不该弹：%+v", got)
	}
	s.a.rm.fg.Store(false)
	s.attention(ev)
	if len(got) != 1 || got[0].title != "目标达成" || got[0].body != "做完了" || !strings.HasPrefix(got[0].id, attentionIDPrefix) || !strings.Contains(got[0].id, "console:example") {
		t.Fatalf("窗口不在前台应弹一条：%+v", got)
	}
	ev.Kind = backend.AttentionLoop
	s.attention(ev)
	if got[1].id == got[0].id {
		t.Fatalf("不同种类的通知 ID 要不同，免得互相顶掉：%q", got[1].id)
	}
	fail = errors.New("系统拒绝")
	s.attention(ev) // 只记日志
	if len(got) != 3 {
		t.Fatalf("弹失败也算试过一次：%d", len(got))
	}
	var nilShell = &shell{}
	nilShell.attention(ev) // 外壳还没建好界面：不 panic、不弹
}

// 弹不弹只看"窗口在前台 且 正摆着的是这个会话"：在前台但看的是别的会话、停在设置页、窗口在后台，都弹；
// 看着的会话换成通知说的那个，就不弹。走真的根视图，验证每帧确实把"正摆着哪个会话"交了出来。
func TestAttentionSkipsOnlyWhenWatchingThatConversation(t *testing.T) {
	a := shellApp()
	a.rail, a.sel = 0, "local:r1"
	tt := shellTester(a, 2048, 1030)
	var got []shown
	s := &shell{a: a}
	s.showNotice = func(id, title, body string) error { got = append(got, shown{id, title, body}); return nil }
	ev := func(conv string) backend.Attention {
		return backend.Attention{Kind: backend.AttentionLoop, Conversation: conv, Title: "定期任务 #1 有新消息", Body: "内容"}
	}
	a.rm.fg.Store(true)

	s.attention(ev("local:r1"))
	if len(got) != 0 {
		t.Fatalf("前台且正看着 local:r1，不该弹：%+v", got)
	}
	s.attention(ev("home:g1"))
	if len(got) != 1 {
		t.Fatalf("前台但看的是别的会话，该弹：%+v", got)
	}
	a.rail = 4 // 切去设置页：会话还选中着，但眼前不是它
	tt.Frame()
	s.attention(ev("local:r1"))
	if len(got) != 2 {
		t.Fatalf("前台但停在设置页，该弹：%+v", got)
	}
	a.rail = 0
	tt.Frame()
	s.attention(ev("local:r1"))
	if len(got) != 2 {
		t.Fatalf("回到聊天页又看着它，不该弹：%+v", got)
	}
	a.rm.fg.Store(false) // 窗口在后台（最小化、失去焦点同此）
	s.attention(ev("local:r1"))
	if len(got) != 3 {
		t.Fatalf("窗口在后台，哪怕选中的是它也该弹：%+v", got)
	}
	s.attention(backend.Attention{Kind: backend.AttentionLoop, Title: "无会话"}) // 会话为空：前台时不当成"正看着"
	a.rm.fg.Store(true)
	a.sel, a.rail = "", 0
	tt.Frame()
	s.attention(backend.Attention{Kind: backend.AttentionLoop, Conversation: "", Title: "无会话"})
	if len(got) != 5 {
		t.Fatalf("会话为空的事不能被空选中吞掉：%d", len(got))
	}
}

// 通知 ID 里能取回会话；别人的通知、没带会话的不乱认。
func TestAttentionConversationFromID(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]struct {
		conv string
		ok   bool
	}{
		attentionIDPrefix + "goal-done|console:char00":   {"console:char00", true},
		attentionIDPrefix + "reminder:12|qq:private:1:2": {"qq:private:1:2", true},
		attentionIDPrefix + "loop":                       {"", true},
		"notify|loop|console:char00":                     {"", false},
		"":                                               {"", false},
	} {
		conv, ok := attentionConversation(id)
		if conv != want.conv || ok != want.ok {
			t.Errorf("%q → (%q,%v)，应为 (%q,%v)", id, conv, ok, want.conv, want.ok)
		}
	}
}

// 点通知：窗口叫出来（这里没有真窗口，只验界面状态）、切到通知说的会话并滚到最新一条；
// 已经选中它就不重订阅只滚到底；会话不在列表里不乱动；别的通知不管。走真后端和真会话表。
func TestNotificationClickOpensThatConversation(t *testing.T) {
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编造回复")}, nil)
	s := &shell{a: h.a}
	if h.a.sel != "console:char00" {
		t.Fatalf("起点应是 char00：%q", h.a.sel)
	}
	h.until(t, "第二个本地会话在列表里", func() bool { return h.a.convByID("console:char01") != nil })
	h.a.rail = 3 // 人在"运行"页，会话页不在眼前
	s.notificationClicked(attentionIDPrefix + backend.AttentionGoalDone + "|console:char01")
	h.until(t, "切到 char01", func() bool { return h.a.sel == "console:char01" && h.a.firstDone })
	if h.a.rail != 0 {
		t.Fatalf("应回到本地页：rail=%d", h.a.rail)
	}
	if v, _ := h.a.rm.view.Load().(string); v != "console:char01" {
		t.Fatalf("界面应已摆着 char01：%q", v)
	}
	h.a.list.ScrollTo(0, ui.Start)
	h.tt.Frame()
	gen := h.a.gen
	s.notificationClicked(attentionIDPrefix + backend.AttentionLoop + "|console:char01")
	h.until(t, "滚到底", func() bool { return h.a.list.AtEnd() })
	if h.a.gen != gen {
		t.Fatal("已经选中就不该重新订阅")
	}
	s.notificationClicked(attentionIDPrefix + backend.AttentionLoop + "|console:不存在")
	s.notificationClicked("别人的通知|console:char00")
	h.tt.Frame()
	if h.a.sel != "console:char01" {
		t.Fatalf("不存在的会话、别人的通知都不该切走：%q", h.a.sel)
	}
}
