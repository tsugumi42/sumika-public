package chatui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 输入框 ↑ 回忆历史：草稿恢复、多行不抢光标、Esc 退出。

func focusedRecallRig(t *testing.T, past ...string) *composerRig {
	t.Helper()
	r := newComposerRig(t)
	r.host.Past = func() []string { return past }
	if err := r.tester.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tester.Frame()
	return r
}

func (r *composerRig) key(mods ui.Modifiers, k ui.Key) {
	r.tester.Key(mods, k)
	r.tester.Frame()
}

func TestComposerUpRecallsAndDownRestoresDraft(t *testing.T) {
	r := focusedRecallRig(t, "第一句", "第二句")
	r.key(0, ui.KeyUp)
	if got := r.p.Main.Text; got != "第二句" {
		t.Fatalf("空输入框按 ↑ 应取最近一条，实际 %q", got)
	}
	r.key(0, ui.KeyUp)
	if got := r.p.Main.Text; got != "第一句" {
		t.Fatalf("继续 ↑ 应往前一条，实际 %q", got)
	}
	r.key(0, ui.KeyUp) // 已到最早：不变
	if got := r.p.Main.Text; got != "第一句" {
		t.Fatalf("到头后不该变，实际 %q", got)
	}
	r.key(0, ui.KeyDown)
	if got := r.p.Main.Text; got != "第二句" {
		t.Fatalf("↓ 应往回一条，实际 %q", got)
	}
	r.key(0, ui.KeyDown)
	if got := r.p.Main.Text; got != "" {
		t.Fatalf("回到最新应还回空草稿，实际 %q", got)
	}
}

func TestComposerRecallKeepsTypedDraft(t *testing.T) {
	r := focusedRecallRig(t, "旧的话")
	r.tester.Type("写到一半")
	r.tester.Frame()
	r.key(0, ui.KeyUp) // 光标在末尾、有草稿：不劫持，按键只是移动光标
	if got := r.p.Main.Text; got != "写到一半" {
		t.Fatalf("打到一半的草稿不能被 ↑ 换掉，实际 %q", got)
	}
	r.key(0, ui.KeyHome)
	r.key(0, ui.KeyUp) // 光标在最开头：才翻历史，草稿先存起来
	if got := r.p.Main.Text; got != "旧的话" {
		t.Fatalf("光标在开头按 ↑ 应取历史，实际 %q", got)
	}
	r.key(0, ui.KeyDown)
	if got := r.p.Main.Text; got != "写到一半" {
		t.Fatalf("翻回最新应原样还回草稿，实际 %q", got)
	}
}

func TestComposerEscEndsRecallAndRestoresDraft(t *testing.T) {
	r := focusedRecallRig(t, "旧的话")
	r.tester.Type("草稿")
	r.tester.Frame()
	r.key(0, ui.KeyHome)
	r.key(0, ui.KeyUp)
	if r.p.Main.Text != "旧的话" {
		t.Fatalf("没翻出历史：%q", r.p.Main.Text)
	}
	r.key(0, ui.KeyEscape)
	if got := r.p.Main.Text; got != "草稿" {
		t.Fatalf("Esc 应还回草稿，实际 %q", got)
	}
	r.key(0, ui.KeyEscape) // 没在翻了：Esc 回到原来的行为，不改文字
	if got := r.p.Main.Text; got != "草稿" {
		t.Fatalf("Esc 不该动文字，实际 %q", got)
	}
}

func TestComposerUpInMultiLineMovesCaret(t *testing.T) {
	r := focusedRecallRig(t, "历史")
	r.tester.Type("第一行")
	r.tester.Key(ui.Shift, ui.KeyEnter)
	r.tester.Type("第二行")
	r.tester.Frame()
	r.key(0, ui.KeyUp) // 光标在第二行末尾：只是上移到第一行
	if got := r.p.Main.Text; got != "第一行\n第二行" {
		t.Fatalf("多行文字中间按 ↑ 应移动光标，实际 %q", got)
	}
	r.key(0, ui.KeyUp) // 现在在第一行但不在开头：仍然只移动（到开头）
	if got := r.p.Main.Text; got != "第一行\n第二行" {
		t.Fatalf("第一行中间按 ↑ 不该翻历史，实际 %q", got)
	}
	r.key(0, ui.KeyHome)
	r.key(0, ui.KeyUp)
	if got := r.p.Main.Text; got != "历史" {
		t.Fatalf("开头按 ↑ 才翻历史，实际 %q", got)
	}
	r.key(0, ui.KeyDown)
	if got := r.p.Main.Text; got != "第一行\n第二行" {
		t.Fatalf("多行草稿应原样还回，实际 %q", got)
	}
}

func TestComposerEditedRecallBecomesNewDraft(t *testing.T) {
	r := focusedRecallRig(t, "甲", "乙")
	r.key(0, ui.KeyUp)
	r.tester.Type("改")
	r.tester.Frame()
	r.key(0, ui.KeyDown) // 翻出来的话被改过：当成新草稿，↓ 不能把它换掉
	if got := r.p.Main.Text; got != "改乙" {
		t.Fatalf("改过的内容不能被覆盖，实际 %q", got)
	}
}

func TestComposerHistoryMergesAndDedupes(t *testing.T) {
	p := NewComposer()
	p.Remember("  新发的 ")
	p.Remember("   ")
	p.Remember("老的")
	h := ComposerHost{Past: func() []string { return []string{"老的", "更老", "新发的"} }}
	got := p.history(h)
	want := []string{"更老", "新发的", "老的"} // 同一句只留最新的一次
	if len(got) != len(want) {
		t.Fatalf("历史 %q，想要 %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("历史 %q，想要 %q", got, want)
		}
	}
}

func TestComposerSendResetsRecall(t *testing.T) {
	r := focusedRecallRig(t, "旧")
	r.key(0, ui.KeyUp)
	r.p.Remember("旧")
	r.p.Accept([]*Block{r.p.Main})
	if r.p.recall.on {
		t.Fatal("发送后还停在回忆状态")
	}
}
