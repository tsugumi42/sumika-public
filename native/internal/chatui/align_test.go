package chatui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 对齐自动检查（主人 2026-11-06："这个没对齐已经好几次了，应该自动检查出来"）：她这一侧的消息列里，
// 每一行开头的图标格子（Key "lead"）中心在同一条竖线上，图标后面的字从同一条竖线开始；
// 图标列的左缘和气泡正文、群聊名字的左缘是同一条线（ADR-287 后过程是气泡，摘要行和气泡里的字同一个起点）。
// 覆盖读搜、思考、命令卡、改文件卡、审批块的计时 / 四种结果、自动放行和气泡，三档外观、私聊群聊。
// 新加一种带图标的行没走 lead、或者自己拼间距，这里会红。
func TestLeadColumnAligned(t *testing.T) {
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.Local)
	diff := "-a\n+b\n"
	her := func(id string, sec int, text string, steps ...*Step) *Msg {
		return &Msg{ID: id, Sender: "r1", Name: "晴枝", Char: CharColors[1], At: at.Add(time.Duration(sec) * time.Second), Text: text, Steps: steps, Markdown: true}
	}
	msgs := []*Msg{
		her("p1", 0, "",
			&Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 2, Thought: "想过"},
			&Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"},
			&Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm x", Approval: &Approval{State: ApprovalPending, Reason: "清理", Remaining: time.Minute}},
			&Step{ID: "e", Kind: StepEdit, State: StepRunning, Target: "a.md", Diff: diff, Approval: &Approval{State: ApprovalPending, Reason: "补一行", Remaining: time.Minute}},
			&Step{ID: "c1", Kind: StepCommand, State: StepDone, Target: "ls", Approval: &Approval{State: ApprovalAllowed}},
			&Step{ID: "c2", Kind: StepCommand, State: StepDenied, Target: "rm /", Approval: &Approval{State: ApprovalDenied}},
			&Step{ID: "c3", Kind: StepCommand, State: StepStopped, Target: "make", Approval: &Approval{State: ApprovalExpired}},
			&Step{ID: "e2", Kind: StepEdit, State: StepStopped, Target: "b.md", Approval: &Approval{State: ApprovalRevoked}},
			&Step{ID: "c4", Kind: StepCommand, State: StepDone, Target: "ls", Output: "x", Approval: &Approval{State: ApprovalAuto, AutoReason: "只读"}},
			&Step{ID: "th", Kind: StepThink, State: StepRunning, Thought: "还在想"}),
		her("m", 2, "好了，我们接着聊。"),
	}
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		for _, group := range []bool{false, true} {
			tk := Derive(s)
			h := Host{T: tk, Column: 720, NoEnter: true, Group: group, MD: NewMD()}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("list")).Width(h.Column).Children(func() {
					for i, m := range msgs {
						var prev, next *Msg
						if i > 0 {
							prev = msgs[i-1]
						}
						if i+1 < len(msgs) {
							next = msgs[i+1]
						}
						h.Message(c, m, prev, next)
					}
				})
			}, 900, 3000)
			tt.Frame()
			tt.Frame()
			root := tree(tt)
			name := fmt.Sprintf("外观 %v/%v/%v 群=%v", s.Text, s.Radius, s.Density, group)
			var leads []*node
			var walk func(n *node)
			walk = func(n *node) {
				if n.Key == "lead" {
					leads = append(leads, n)
				}
				for _, ch := range n.Children {
					walk(ch)
				}
			}
			walk(root.find("p1"))
			if len(leads) < 12 {
				t.Fatalf("%s 只找到 %d 个图标格子，场景里的行没画全", name, len(leads))
			}
			center := leads[0].Box.X + leads[0].Box.W/2
			var textX float32 = -1
			for _, l := range leads {
				if c := l.Box.X + l.Box.W/2; math.Abs(float64(c-center)) > 0.5 {
					t.Errorf("%s 图标格子中心 %.2f，和第一行的 %.2f 不在一条线上（%s 下面）", name, c, center, l.Parent.Key)
				}
				// 图标后面的字：同一行里图标格子后面的下一个元素
				sib := l.Parent.Children
				for i, ch := range sib {
					if ch == l && i+1 < len(sib) {
						x := sib[i+1].Box.X
						if textX < 0 {
							textX = x
						} else if math.Abs(float64(x-textX)) > 0.5 {
							t.Errorf("%s 图标后面的字从 %.2f 开始，别的行从 %.2f（%s 下面）", name, x, textX, l.Parent.Key)
						}
					}
				}
			}
			colLeft := center - h.rowLine()/2
			side := tk.BubblePadH - tk.BubblePadV
			if txt := root.find("m").find("text"); txt == nil || math.Abs(float64(txt.Box.X+side-colLeft)) > 0.5 {
				t.Errorf("%s 气泡正文左缘 %v，图标列左缘 %.2f", name, txt.Box.X+side, colLeft)
			}
			if group {
				if nm := root.find("p1").find("name"); nm == nil || math.Abs(float64(nm.Box.X+nm.Pad[3]-colLeft)) > 0.5 {
					t.Errorf("%s 名字左缘不在图标列左缘 %.2f 上", name, colLeft)
				}
			}
		}
	}
}
