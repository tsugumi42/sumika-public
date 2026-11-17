package main

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/llm/llmtest"
)

// 模型药丸真点：点开菜单、选另一个模型，配置跟着变，药丸显示新值。
func TestComposerModelPillChangesModel(t *testing.T) {
	a := shellApp()
	a.workbench.shell.st.Panes.SideOpen, a.workbench.shell.sideTab = true, 1 // 药丸在右栏“上下文”栏顶部（ADR-351）
	posted := make(chan func(), 16)
	a.postFn = func(fn func()) { posted <- fn }
	tt := shellTester(a, 2048, 1030)
	settle := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !done(); tt.Frame() {
			select {
			case fn := <-posted:
				fn()
			case <-time.After(5 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s：busy=%v 备注=%v", what, a.models.busy, a.models.notes)
			}
		}
		tt.Frame()
	}
	settle("模型设置没读到", func() bool { return a.models.loaded })
	s := &a.models
	current := ""
	for _, ch := range s.view.Characters {
		if ch.ID == "r1" {
			current = ch.ConfiguredModel
		}
	}
	if current == "" || len(s.view.Models) < 2 {
		t.Fatalf("演示数据不够：当前 %q，可选 %d", current, len(s.view.Models))
	}
	other := s.view.Groups[0].Models[0] // 第一组在菜单最上面，不用滚
	if other.ID == current {
		other = s.view.Groups[0].Models[1]
	}
	if err := tt.Click("这个角色用的模型"); err != nil {
		t.Fatalf("药丸点不了：%v，%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click(other.Name); err != nil {
		t.Fatalf("菜单里没有 %s：%v", other.Name, err)
	}
	settle("模型没改成", func() bool { return !s.busy && s.view.Characters[0].ConfiguredModel != current })
	if !tt.HasText(other.Name) {
		t.Fatalf("药丸没显示新模型 %s：%q", other.Name, tt.Texts())
	}
}

// 胶囊输入框（ADR-351）：输入框里没有药丸；发送键离外框上、右、下一样远（和外框同心），文字离左边是 ComposerPad + 描边。
func TestComposerTextAlignsWithPills(t *testing.T) {
	a := shellApp()
	a.chars[0].Model = "demo/model-00"
	tt := shellTester(a, 2048, 1030)
	box := boxOf(t, tt, "composer")
	if _, ok := tt.Find("这个角色用的模型"); ok {
		t.Fatal("输入框里不该再有模型药丸（搬到右栏了）")
	}
	text, ok := tt.Find("发一条消息")
	if !ok {
		t.Fatalf("没有输入框：%q", tt.Texts())
	}
	if want := chatui.ComposerPad(a.workbench.shell.kit.T) + 1; !closeTo(text.X-box.Box.X, want) {
		t.Fatalf("文字离外框左边 %v，应为 %v", text.X-box.Box.X, want)
	}
	send := boxOf(t, tt, "send")
	top, right, bottom := send.Box.Y-box.Box.Y, box.Box.X+box.Box.W-send.Box.X-send.Box.W, box.Box.Y+box.Box.H-send.Box.Y-send.Box.H
	if !closeTo(top, right) || !closeTo(right, bottom) {
		t.Fatalf("发送键离外框上 %v、右 %v、下 %v，应一样远", top, right, bottom)
	}
}

// 审批药丸：真后台读这个会话的档；点开选"免审批"只改这个会话、当场生效，全局默认不变；"跟随默认"撤回。
func TestComposerApprovalPillSwitchesThisConversation(t *testing.T) {
	h := wbUI(t, llmtest.Script{}, nil)
	st := &h.a.workbench.shell.tools
	h.a.workbench.shell.st.Panes.SideOpen, h.a.workbench.shell.sideTab = true, 1 // 药丸在右栏“上下文”栏顶部（ADR-351），画出来才去读
	h.until(t, "审批方式读到", func() bool { return st.approval != "" })
	h.tt.Frame()
	if st.approval != "manual" || st.approvalOverride || !st.approvalEditable {
		t.Fatalf("起始状态：%+v", st)
	}
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	reg := h.a.reader.(api.ConversationApproval)
	h.click(t, "这个对话的审批方式（跟随默认）")
	h.tt.Frame()
	h.click(t, "免审批")
	h.until(t, "切到免审批", func() bool { return st.approval == "yolo" && !st.approvalBusy })
	got, err := reg.ConversationApprovalMode(context.Background(), key)
	if err != nil || got.Mode != "yolo" || got.Default != "manual" || !got.Override {
		t.Fatalf("后台状态：%+v %v", got, err)
	}
	h.tt.Frame()
	h.click(t, "这个对话的审批方式")
	h.tt.Frame()
	h.click(t, "跟随默认：手动批准")
	h.until(t, "回到默认", func() bool { return st.approval == "manual" && !st.approvalOverride && !st.approvalBusy })
	if got, _ = reg.ConversationApprovalMode(context.Background(), key); got.Override || got.Mode != "manual" {
		t.Fatalf("撤回后：%+v", got)
	}
}

// 切导航时聊天区跟着换：不能点了 QQ 还停在本地的会话上（2026-11-09 真窗口查出来的）。
func TestNavSwitchMovesConversation(t *testing.T) {
	h := wbUI(t, llmtest.Script{}, nil)
	before := h.a.sel
	if before == "" {
		t.Fatal("本地应该有选中的会话")
	}
	h.click(t, "QQ")
	h.tt.Frame()
	if h.a.sel != "" {
		t.Fatalf("切到 QQ 后聊天区还停在 %q", h.a.sel)
	}
	if _, ok := h.tt.Find("发一条消息"); ok {
		t.Fatal("QQ 页没选会话，不该有本地的输入框")
	}
	h.click(t, "本地")
	h.tt.Frame()
	if h.a.sel != before {
		t.Fatalf("回到本地应回到上次看的会话 %q，现在是 %q", before, h.a.sel)
	}
}
