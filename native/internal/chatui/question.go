package chatui

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

// 问题卡（docs/规格-向主人提问.md 第八节）：她在一轮中间问主人时挂在输入区上方，和审批卡并排。
// 外框和块的几何照审批卡；语义分开：选项是回答，不是“允许 / 拒绝”。

// QuestionSkip 是 OnAnswer 里“先不答”的下标。
const QuestionSkip = -1

// QuestionOption 是问题卡上的一个选项。
type QuestionOption struct{ Label, Detail string }

// QuestionInfo 是一张问题卡的内容（宿主从 api.LocalQuestion 转来，chatui 不认后台的类型）。
type QuestionInfo struct {
	ID, Text string
	Options  []QuestionOption
	Shown    time.Time // 卡片第一次出现的时刻（出现后 500ms 内焦点按钮上的回车不算）
	Busy     bool      // 回答已发出、等后台确认：按钮禁用
	Error    string    // 上一次回答没成功的原因；有就写在按钮上面，按钮可以再点
}

const questionTextLines = 4

// QuestionCard 画一张问题卡：问题正文（最多 4 行，全文在聊天气泡里）、每个选项一行整宽按钮、
// 最下面一行左边写“也可以直接在下面打字回她”、右边“先不答”。
// 键盘只在焦点在卡里时有效：焦点在选项按钮上 Enter 选它，块里任何地方 Esc = 先不答；新卡片不抢焦点。
func (h Host) QuestionCard(c *ui.Context, q QuestionInfo, line, fill ui.Color) {
	t := h.T
	card := ui.Column(c.Key(q.ID)).Label("提问："+q.Text).FillWidth().Shrink(0).Padding(t.BubblePadV).Gap(t.S2).Radius(t.R1).Background(fill).Border(1, line)
	enter := false
	answer := func(option int) {
		if h.OnAnswer != nil && !q.Busy {
			h.OnAnswer(q.ID, option)
		}
	}
	if !q.Busy {
		card.Focusable()
		ready := q.Shown.IsZero() || c.Now().Sub(q.Shown) >= approvalEnterGuard
		enter = card.Shortcut(0, ui.KeyEnter) && ready
		if card.Shortcut(0, ui.KeyEscape) {
			answer(QuestionSkip)
		}
	}
	// 选项按钮和外框同心：圆角 = 外框圆角 − 内距
	r := max(t.Inner(t.R1, t.BubblePadV), t.RInnerMin)
	card.Children(func() {
		ui.Text(c.Key("text"), q.Text).Font(h.Font).FontSize(t.FS[3]).FontWeight(t.Weight[2]).TextColor(t.C.Text).Wrap().FillWidth().MaxLines(questionTextLines)
		ui.Column(c.Key("options")).FillWidth().Gap(t.S1).Children(func() {
			for i, o := range q.Options {
				b := ui.ButtonBase(c.Key("option-"+strconv.Itoa(i))).Label(o.Label).FillWidth().MinHeight(t.HSm).Padding(t.S2, t.S3).Radius(r).Disabled(q.Busy).OnClick(func() { answer(i) })
				bg := t.C.Surface
				if !q.Busy && b.Pressed() {
					bg = bg.Mix(t.C.Text, 0.11)
				} else if !q.Busy && b.Hovered() {
					bg = bg.Mix(t.C.Text, 0.06)
				}
				if enter && b.Focused() {
					answer(i)
				}
				b.Background(t.Fill(b, bg)).TextColor(t.C.Text).Children(func() {
					ui.Column(c).FillWidth().Gap(0).Children(func() {
						ui.Text(c.Key("label"), o.Label).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).Wrap().FillWidth()
						if o.Detail != "" {
							ui.Text(c.Key("detail"), o.Detail).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).Wrap().FillWidth()
						}
					})
				})
			}
		})
		if q.Error != "" {
			ui.Text(c.Key("error"), q.Error).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Danger).Wrap().FillWidth()
		}
		// 窄窗口：提示和按钮换行，不挤出外框
		ui.Row(c.Key("foot")).FillWidth().Gap(t.S2).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c.Key("hint"), "也可以直接在下面打字回她").Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).Grow(1)
			skip := ui.ButtonBase(c.Key("skip")).Label("先不答").Height(t.HSm).Padding(0, t.S2).Radius(r).Disabled(q.Busy).OnClick(func() { answer(QuestionSkip) })
			if enter && skip.Focused() {
				answer(QuestionSkip)
			}
			skip.TextColor(t.C.Text2).Children(func() { ui.Text(c, "先不答").Font(h.Font).FontSize(t.FS[2]) })
		})
	})
}
