package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func questionTester(h *Host, q QuestionInfo, width int) *ui.Tester {
	view := func(c *ui.Context) {
		c.Root().Background(h.T.C.ChatBg)
		ui.Column(c).Gap(20).Padding(20).Children(func() {
			ui.TextInputBase(c.Key("input"), new(string)).Label("输入框").Width(120).Height(30)
			h.QuestionCard(c, q, h.T.C.Line, h.T.C.Surface)
		})
	}
	tt := ui.NewTester(view, width, 700)
	tt.Frame()
	tt.Frame()
	return tt
}

func testQuestion(shown time.Time) QuestionInfo {
	return QuestionInfo{ID: "q1", Text: "编的问题：先备份还是直接改？", Shown: shown,
		Options: []QuestionOption{{Label: "先备份", Detail: "多占一点空间"}, {Label: "直接改"}}}
}

// 点选项回调下标；点“先不答”回调 QuestionSkip；提交中按钮禁用。
func TestQuestionCardClicks(t *testing.T) {
	var got []string
	h := &Host{T: Derive(DefaultSettings()), Column: 600, OnAnswer: func(id string, o int) { got = append(got, fmt.Sprint(id, ":", o)) }}
	tt := questionTester(h, testQuestion(time.Time{}), 700)
	for _, want := range []string{"先备份", "直接改", "先不答", "多占一点空间", "也可以直接在下面打字回她"} {
		if !tt.HasText(want) {
			t.Fatalf("缺 %q：%v", want, tt.Texts())
		}
	}
	for _, label := range []string{"直接改", "先备份", "先不答"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if fmt.Sprint(got) != "[q1:1 q1:0 q1:-1]" {
		t.Fatalf("回调：%v", got)
	}
	got = nil
	busy := testQuestion(time.Time{})
	busy.Busy = true
	tt = questionTester(h, busy, 700)
	_ = tt.Click("先备份")
	_ = tt.Click("先不答")
	tt.Frame()
	if len(got) != 0 {
		t.Fatalf("提交中还能点：%v", got)
	}
}

// 键盘：焦点在卡上 Esc = 先不答；焦点在卡上 Enter 不替她选；刚出现 500ms 内按钮上的 Enter 不算；焦点在输入框里都不算。
func TestQuestionCardKeyboard(t *testing.T) {
	var got []string
	h := &Host{T: Derive(DefaultSettings()), Column: 600, OnAnswer: func(id string, o int) { got = append(got, fmt.Sprint(o)) }}
	focusCard := func(tt *ui.Tester) {
		r, ok := tt.Find("编的问题：先备份还是直接改？")
		if !ok {
			t.Fatal("找不到问题正文")
		}
		tt.ClickAt(r.X+2, r.Y+r.H/2)
		tt.Frame()
	}
	tt := questionTester(h, testQuestion(time.Time{}), 700)
	focusCard(tt)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(got) != 0 {
		t.Fatalf("焦点在卡上 Enter 不该替她选：%v", got)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if fmt.Sprint(got) != "[-1]" {
		t.Fatalf("Esc：%v，期望先不答", got)
	}
	// 焦点移到第一个选项：Enter 选它
	got = nil
	tt = questionTester(h, testQuestion(time.Time{}), 700)
	focusCard(tt)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if fmt.Sprint(got) != "[0]" {
		t.Fatalf("焦点在第一个选项上 Enter：%v", got)
	}
	// 刚出现：Enter 不算（出现时刻在“现在”之后）
	got = nil
	tt = questionTester(h, testQuestion(time.Now().Add(time.Hour)), 700)
	focusCard(tt)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(got) != 0 {
		t.Fatalf("刚出现就认了 Enter：%v", got)
	}
	// 焦点在输入框里：Enter、Esc 都不归卡片
	got = nil
	tt = questionTester(h, testQuestion(time.Time{}), 700)
	if err := tt.Click("输入框"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(got) != 0 {
		t.Fatalf("焦点在输入框里也触发了回答：%v", got)
	}
}

// 失败写原因、按钮仍可再点；窄窗口、长正文、很长的选项名都不溢出外框。
func TestQuestionCardErrorAndNarrow(t *testing.T) {
	var got []int
	h := &Host{T: Derive(DefaultSettings()), Column: 600, OnAnswer: func(_ string, o int) { got = append(got, o) }}
	q := testQuestion(time.Time{})
	q.Error = "编的失败原因"
	q.Text = strings.Repeat("很长的问题正文，", 40)
	q.Options[0].Label = strings.Repeat("很长的选项名", 6)
	for _, w := range []float32{700, 360, 280} {
		tt := questionTester(h, q, int(w))
		if !tt.HasText("编的失败原因") {
			t.Fatalf("宽 %v：失败原因没显示", w)
		}
		for _, text := range tt.Texts() {
			if r, ok := tt.Find(text); ok && (r.X < 0 || r.X+r.W > w+0.5) {
				t.Errorf("宽 %v：%q 溢出：%+v", w, text, r)
			}
		}
	}
	tt := questionTester(h, q, 700)
	_ = tt.Click("直接改")
	tt.Frame()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("失败后按钮不能再点：%v", got)
	}
}
