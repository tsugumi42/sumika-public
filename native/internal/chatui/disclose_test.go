package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 第 2 批过渡动画（规格-过渡动画 C5～C8、铁律 3、4、5）：审批决定后卡自己收成一行、过程气泡和工具卡的展开收起、表格代码块整块出现。
// 逐帧测：Advance 推时钟加 Frame 取一帧；终点不靠固定毫秒数等，一直推到 FramePending 为假（上限只防死循环）。

const maxFrames = 600 // 每帧 16 毫秒，约 10 秒；只是防死循环的上限

// settle 一帧一帧推到不再要帧，每帧后调 each；返回推了几帧。到上限还要帧就是动画没收尾。
func settle(t *testing.T, tt *ui.Tester, each func(i int)) int {
	t.Helper()
	for i := 0; i < maxFrames; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		if each != nil {
			each(i)
		}
		if !tt.FramePending() {
			return i + 1
		}
	}
	t.Fatalf("推了 %d 帧还在要帧：动画没收尾", maxFrames)
	return maxFrames
}

func bubbleH(tt *ui.Tester, id string) float32 { return tree(tt).find(id).find("bubble").Box.H }

func reduceName(reduce bool) string {
	return map[bool]string{false: "正常", true: "减少动态"}[reduce]
}

// 审批决定后：同一张卡的高度用“利落”弹簧收到结果那一行，单调减少、不过冲、终点等于一开始就是结果行的高度；
// 按钮、凹块先淡出，结果的字淡入，中途只有一个结果状态。动画放完不再要帧。
func TestAskCardCollapsesInPlace(t *testing.T) {
	results := map[ApprovalState]string{ApprovalAllowed: "已允许", ApprovalDenied: "已拒绝", ApprovalExpired: "超时", ApprovalRevoked: "已撤回"}
	for _, reduce := range []bool{false, true} {
		for state, word := range results {
			t.Run(fmt.Sprintf("%s/%s", reduceName(reduce), word), func(t *testing.T) {
				var called []string
				h := askHost(func(string, ApprovalScope) { called = append(called, "允许") }, func(string, string) { called = append(called, "拒绝") })
				st := askStep(time.Time{})
				tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
				tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
				tt.Frame()
				tt.Frame()
				start := bubbleH(tt, "p")

				// 一开始就是结果行的冷渲染：终点高度
				cold := askStep(time.Time{})
				cold.Approval.State = state
				cold.State = StepDone
				ct := msgsTester(h, []*Msg{procMsg("p", 0, cold)}, 900)
				end := bubbleH(ct, "p")
				if !(end < start-20) {
					t.Fatalf("结果行高 %.1f 应明显比等批准的卡 %.1f 矮", end, start)
				}

				st.Approval.State = state
				st.State = StepDone
				var hs []float32
				both, wrong := false, ""
				record := func() {
					hs = append(hs, bubbleH(tt, "p"))
					seen := 0
					for _, w := range results {
						if countText(tt, w) > 0 {
							seen++
							if w != word {
								wrong = w
							}
						}
					}
					if seen > 1 {
						wrong = "同时出现了不止一个结果"
					}
					if countText(tt, "允许一次") > 0 && countText(tt, word) > 0 {
						both = true
					}
				}
				tt.Frame() // 决定那一帧，时钟还没动
				record()
				n := settle(t, tt, func(int) { record() })
				if wrong != "" {
					t.Errorf("中途出现了别的结果状态：%s", wrong)
				}
				if !near(hs[len(hs)-1], end) {
					t.Errorf("终点高 %.2f，应等于一开始就是结果行的 %.2f", hs[len(hs)-1], end)
				}
				for i := 1; i < len(hs); i++ {
					if hs[i] > hs[i-1]+0.01 {
						t.Fatalf("第 %d 帧高度回升 %.2f → %.2f（%v）", i, hs[i-1], hs[i], hs)
					}
					if hs[i] < end-0.01 {
						t.Fatalf("第 %d 帧高度 %.2f 低于终点 %.2f：过冲", i, hs[i], end)
					}
				}
				if reduce {
					// 减少动态效果：高度直接到位（不播位移尺寸），结果的字仍然淡入
					if !near(hs[0], end) {
						t.Errorf("减少动态：决定那一帧高度应已是终点：%.1f，终点 %.1f", hs[0], end)
					}
				} else {
					mid := 0
					for _, v := range hs {
						if v > end+0.5 && v < start-0.5 {
							mid++
						}
					}
					if mid < 3 {
						t.Errorf("高度中途只有 %d 帧，应是逐帧收起：%v", mid, hs)
					}
					if !both {
						t.Errorf("中途应有一帧“允许一次”按钮和结果字同时在（同一张卡变形）")
					}
				}
				if countText(tt, "想运行命令") != 0 {
					t.Errorf("收完以后摘要行该换成普通的收起摘要：%v", tt.Texts())
				}
				if len(called) != 0 {
					t.Errorf("收起中的按钮不该再算决定：%v", called)
				}
				t.Logf("%d 帧收完：%.1f → %.1f", n, start, end)
			})
		}
	}
}

// 收起期间点“允许一次”“拒绝”、按 Esc 都不再算（决定已经做完）。
func TestAskCardFrozenWhileCollapsing(t *testing.T) {
	var called []string
	h := askHost(func(string, ApprovalScope) { called = append(called, "允许") }, func(string, string) { called = append(called, "拒绝") })
	st := askStep(time.Time{})
	tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
	r, ok := tt.Find("允许一次")
	if !ok {
		t.Fatal("找不到按钮")
	}
	st.Approval.State, st.State = ApprovalDenied, StepDenied
	tt.Frame()
	tt.Advance(30 * time.Millisecond)
	tt.Frame()
	if countText(tt, "允许一次") == 0 {
		t.Skip("30 毫秒时按钮已经摘掉，没有可点的")
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(called) != 0 {
		t.Errorf("收起中点了按钮或按了 Esc，不该再有决定：%v", called)
	}
}

// 从历史里读出来就是已决定的：直接是一行，不播收起；滚出去又回来也不播。
func TestAskCardHistoryIsOneRow(t *testing.T) {
	h := askHost(nil, nil)
	st := askStep(time.Time{})
	st.Approval.State, st.State = ApprovalAllowed, StepDone
	tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
	if tt.FramePending() {
		t.Errorf("历史里已决定的行第一帧后不该再要帧")
	}
	end := bubbleH(tt, "p")
	if end > h.T.BodyLine+2*h.T.BubblePadV+1 {
		t.Errorf("历史里已决定的应是一行：高 %.1f", end)
	}
	if !tt.HasText("已允许") || tt.HasText("允许一次") {
		t.Errorf("应只有结果：%v", tt.Texts())
	}
}

// ---- 列表里的展开收起 ----

// listRig 把几条消息放进 ui.List（高 viewH），宿主按正式聊天区的写法给 FollowingEnd、ViewRect。
type listRig struct {
	tt    *ui.Tester
	st    *ui.ListState
	msgs  []*Msg
	open  map[string]*bool
	viewH float32
	h     Host
}

func newListRig(t *testing.T, follow, reduce bool, msgs []*Msg, motion bool) *listRig {
	t.Helper()
	r := &listRig{msgs: msgs, open: map[string]*bool{}, viewH: 520, st: &ui.ListState{FollowEnd: follow}}
	r.st.Key = func(i int) any { return r.msgs[i].ID }
	r.h = Host{T: Derive(DefaultSettings()), Column: 620, NoEnter: true, SelfChar: CharColors[0],
		StepOpen: func(id string) *bool {
			if r.open[id] == nil {
				r.open[id] = new(bool)
			}
			return r.open[id]
		}}
	if motion {
		r.h.FollowingEnd = func() bool { return r.st.FollowEnd && r.st.AtEnd() }
		r.h.ViewRect = func() ui.Rect { return ui.Rect{X: 0, Y: 0, W: 700, H: r.viewH} }
	}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("stack")).Size(700, r.viewH).Children(func() {
			ui.List(c.Key("list"), r.st, len(r.msgs), func(i int) {
				var prev, next *Msg
				if i > 0 {
					prev = r.msgs[i-1]
				}
				if i+1 < len(r.msgs) {
					next = r.msgs[i+1]
				}
				r.h.Message(c, r.msgs[i], prev, next)
			}).Fill()
		})
	}, 700, int(r.viewH))
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
	for range 4 {
		r.tt.Frame()
	}
	return r
}

// y 是消息行 id 现在的顶；没画出来是 -1。
func (r *listRig) y(id string) float32 {
	if n := tree(r.tt).find(id); n != nil {
		return n.Box.Y
	}
	return -1
}

// thoughtMsg：一条只有一段思考的过程消息，正文很长，展开后明显变高。
func thoughtMsg(id string, lines int) *Msg {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "第 %d 步：把笔记里的几处写法对一遍。\n", i+1)
	}
	return procMsg(id, 0, &Step{ID: id + "-t", Kind: StepThink, State: StepDone, Seconds: 4, Thought: strings.TrimRight(b.String(), "\n")})
}

func fillerMsgs(prefix string, n int) []*Msg {
	var out []*Msg
	for i := 0; i < n; i++ {
		out = append(out, &Msg{ID: fmt.Sprintf("%s%d", prefix, i), Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 23, 0, i, 0, time.Local), Text: fmt.Sprintf("第 %d 句闲话。", i)})
	}
	return out
}

// 过程气泡在列表里展开：行顶可见、列表没贴底时，展开行上方的行一帧都不动，下方的行只往下走；
// 高度单调增加、不过冲、终点等于直接展开的高度；放完不再要帧。每种都在减少动态效果下再测一次。
func TestProcessBubbleExpandInList(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		t.Run(reduceName(reduce), func(t *testing.T) {
			msgs := append(append(fillerMsgs("a", 3), thoughtMsg("p", 14)), fillerMsgs("b", 14)...)
			r := newListRig(t, false, reduce, msgs, true)
			if r.st.AtEnd() {
				t.Fatal("场景要求列表不在末尾")
			}
			r.tt.Frame()
			startH := bubbleH(r.tt, "p")
			above, below := r.y("a2"), r.y("b0")

			// 直接展开的终点：另起一个、一开始就展开的
			cold := newListRig(t, false, reduce, append(append(fillerMsgs("a", 3), thoughtMsg("p", 14)), fillerMsgs("b", 14)...), true)
			*cold.open["p"] = true
			cold.tt.Frame()
			settle(t, cold.tt, nil)
			end := bubbleH(cold.tt, "p")
			if !(end > startH+40) {
				t.Fatalf("展开后应明显变高：%.1f → %.1f", startH, end)
			}

			*r.open["p"] = true
			var hs, belowY []float32
			each := func(int) {
				hs = append(hs, bubbleH(r.tt, "p"))
				belowY = append(belowY, r.y("b0"))
				if y := r.y("a2"); !near(y, above) {
					t.Fatalf("展开行上方那一行动了：%.2f → %.2f", above, y)
				}
			}
			r.tt.Frame()
			each(0)
			settle(t, r.tt, each)
			if !near(hs[len(hs)-1], end) {
				t.Errorf("终点高 %.2f，应等于直接展开的 %.2f", hs[len(hs)-1], end)
			}
			for i := 1; i < len(hs); i++ {
				if hs[i] < hs[i-1]-0.01 || hs[i] > end+0.01 {
					t.Fatalf("第 %d 帧高度 %.2f（前一帧 %.2f，终点 %.2f）：不单调或过冲 %v", i, hs[i], hs[i-1], end, hs)
				}
				if belowY[i] < belowY[i-1]-0.01 {
					t.Fatalf("第 %d 帧下面的行往回走了：%.2f → %.2f", i, belowY[i-1], belowY[i])
				}
			}
			mid := 0
			for _, v := range hs {
				if v > startH+0.5 && v < end-0.5 {
					mid++
				}
			}
			if reduce {
				if mid != 0 {
					t.Errorf("减少动态：高度应直接到位，中途却有 %d 帧", mid)
				}
			} else if mid < 3 {
				t.Errorf("高度中途只有 %d 帧，应是逐帧长出来：%v", mid, hs)
			}
			if belowY[len(belowY)-1] <= below {
				t.Errorf("下面的行应被推下去：%.1f → %.1f", below, belowY[len(belowY)-1])
			}

			// 收回去：同样单调、行上方不动
			*r.open["p"] = false
			hs = hs[:0]
			r.tt.Frame()
			each(0)
			settle(t, r.tt, each)
			if !near(hs[len(hs)-1], startH) {
				t.Errorf("收回后高 %.2f，应回到 %.2f", hs[len(hs)-1], startH)
			}
			for i := 1; i < len(hs); i++ {
				if hs[i] > hs[i-1]+0.01 || hs[i] < startH-0.01 {
					t.Fatalf("收回第 %d 帧高度 %.2f（前一帧 %.2f，终点 %.2f）：不单调或过冲 %v", i, hs[i], hs[i-1], startH, hs)
				}
			}
		})
	}
}

// 列表贴着末尾跟随时，或宿主没给条件（默认）、或这一行的顶部不在可见范围里：高度直接切换，内容照样淡入。
func TestProcessBubbleExpandHardCutCases(t *testing.T) {
	type tcase struct {
		name   string
		follow bool
		motion bool
		hide   bool // ViewRect 挪到这一行下面：行顶不可见
	}
	for _, tc := range []tcase{
		{"贴底跟随", true, true, false},
		{"宿主没给条件", false, false, false},
		{"行顶不可见", false, true, true},
	} {
		for _, reduce := range []bool{false, true} {
			t.Run(tc.name+"/"+reduceName(reduce), func(t *testing.T) {
				msgs := append(fillerMsgs("a", 3), thoughtMsg("p", 14))
				if !tc.follow {
					msgs = append(msgs, fillerMsgs("b", 14)...)
				}
				r := newListRig(t, tc.follow, reduce, msgs, tc.motion)
				if tc.follow && !r.st.AtEnd() {
					t.Fatal("场景要求列表在末尾")
				}
				if tc.hide {
					r.h.ViewRect = func() ui.Rect { return ui.Rect{X: 0, Y: r.y("p") + 1000, W: 700, H: 400} }
				}
				r.tt.Frame()
				startH := bubbleH(r.tt, "p")
				*r.open["p"] = true
				r.tt.Frame()
				r.tt.Frame()
				after := bubbleH(r.tt, "p")
				var last float32
				for i := 0; i < 20 && r.tt.FramePending(); i++ {
					r.tt.Advance(16 * time.Millisecond)
					r.tt.Frame()
				}
				last = bubbleH(r.tt, "p")
				if !(last > startH+40) {
					t.Fatalf("展开后应变高：%.1f → %.1f", startH, last)
				}
				if !near(after, last) {
					t.Errorf("该硬切：翻转后第二帧高 %.1f 应已是终点 %.1f", after, last)
				}
			})
		}
	}
}

// 内容淡入：硬切时高度到位了，字仍从淡到浓（减少动态效果下也是）。
func TestProcessBubbleHardCutContentFadesIn(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		r := newListRig(t, true, reduce, append(fillerMsgs("a", 3), thoughtMsg("p", 14)), true)
		before := darkness(r.tt)
		*r.open["p"] = true
		r.tt.Frame()
		d1 := darkness(r.tt)
		stepBy(r.tt, 40*time.Millisecond)
		dm := darkness(r.tt)
		settle(t, r.tt, nil)
		dEnd := darkness(r.tt)
		if !(before < d1 && d1 < dm && dm < dEnd) {
			t.Errorf("reduce=%v 展开内容的深色量 %d → %d → %d → %d，应逐步变浓", reduce, before, d1, dm, dEnd)
		}
	}
}

func twoStepMsg(id string) *Msg {
	var out strings.Builder
	for i := 0; i < 11; i++ {
		fmt.Fprintf(&out, "第 %d 行输出\n", i+1)
	}
	return procMsg(id,
		0,
		&Step{ID: id + "-t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: strings.Repeat("再对一遍这几处的写法。\n", 9)},
		&Step{ID: id + "-c", Kind: StepCommand, State: StepDone, Target: "go test ./...", Output: out.String(), Seconds: 2},
	)
}

// 过程气泡里面的思考行、命令卡展开收起（C6、C7）：同样的规矩——单调、不过冲、上方不动、终点一致、放完不再要帧；减少动态直接到位。
func TestInnerRowsExpandInList(t *testing.T) {
	cases := []struct{ name, toggle string }{{"思考行", "q-t"}, {"命令卡", "q-c"}}
	for _, tc := range cases {
		for _, reduce := range []bool{false, true} {
			t.Run(tc.name+"/"+reduceName(reduce), func(t *testing.T) {
				mk := func() *listRig {
					msgs := append(append(fillerMsgs("a", 3), twoStepMsg("q")), fillerMsgs("b", 14)...)
					r := newListRig(t, false, reduce, msgs, true)
					*r.open["q"] = true // 先把整个过程气泡展开，放完
					r.tt.Frame()
					settle(t, r.tt, nil)
					return r
				}
				r := mk()
				startH := bubbleH(r.tt, "q")
				above, below := r.y("a2"), r.y("b0")

				cold := mk()
				*cold.open[tc.toggle] = true
				cold.tt.Frame()
				settle(t, cold.tt, nil)
				end := bubbleH(cold.tt, "q")
				if !(end > startH+30) {
					t.Fatalf("展开后应明显变高：%.1f → %.1f", startH, end)
				}

				*r.open[tc.toggle] = true
				var hs []float32
				belowY, dir := below, float32(1) // 展开：下面的行只往下走；收回：只往上走
				each := func(int) {
					hs = append(hs, bubbleH(r.tt, "q"))
					if y := r.y("a2"); !near(y, above) {
						t.Fatalf("上方的行动了：%.2f → %.2f", above, y)
					}
					y := r.y("b0")
					if (y-belowY)*dir < -0.01 {
						t.Fatalf("下面的行走反了（方向 %v）：%.2f → %.2f", dir, belowY, y)
					}
					belowY = y
				}
				r.tt.Frame()
				each(0)
				settle(t, r.tt, each)
				if !near(hs[len(hs)-1], end) {
					t.Errorf("终点高 %.2f，应等于直接展开的 %.2f", hs[len(hs)-1], end)
				}
				mid := 0
				for i := 1; i < len(hs); i++ {
					if hs[i] < hs[i-1]-0.01 || hs[i] > end+0.01 {
						t.Fatalf("第 %d 帧高度 %.2f（前一帧 %.2f，终点 %.2f）：不单调或过冲 %v", i, hs[i], hs[i-1], end, hs)
					}
					if hs[i] > startH+0.5 && hs[i] < end-0.5 {
						mid++
					}
				}
				if reduce == (mid > 0) || (!reduce && mid < 3) {
					t.Errorf("reduce=%v 中途 %d 帧：%v", reduce, mid, hs)
				}
				// 收回
				*r.open[tc.toggle] = false
				dir = -1
				hs = hs[:0]
				r.tt.Frame()
				each(0)
				settle(t, r.tt, each)
				if !near(hs[len(hs)-1], startH) {
					t.Errorf("收回后高 %.2f，应回到 %.2f", hs[len(hs)-1], startH)
				}
			})
		}
	}
}

func askListMsg(state ApprovalState) *Msg {
	st := askStep(time.Time{})
	st.Approval.State = state
	if state != ApprovalPending {
		st.State = StepDone
	}
	return procMsg("ask", 0, st)
}

// 审批卡在列表里收起：不管贴不贴底都动（它就是要看着变的那一张卡）。
// 贴底时卡在最下面，上面的行只被带着往下走（单调，没有自己的动画）；不贴底时上方一动不动、下方的行只往上走。
func TestAskCardCollapsesInList(t *testing.T) {
	for _, follow := range []bool{true, false} {
		for _, reduce := range []bool{false, true} {
			t.Run(fmt.Sprintf("贴底=%v/%s", follow, reduceName(reduce)), func(t *testing.T) {
				build := func(state ApprovalState) *listRig {
					msgs := append(fillerMsgs("a", 9), askListMsg(state))
					if !follow {
						msgs = append(msgs, fillerMsgs("b", 14)...)
					}
					return newListRig(t, follow, reduce, msgs, true)
				}
				r := build(ApprovalPending)
				if follow && !r.st.AtEnd() {
					t.Fatal("场景要求贴底")
				}
				startH := bubbleH(r.tt, "ask")
				cold := build(ApprovalDenied)
				end := bubbleH(cold.tt, "ask")
				if !(end < startH-20) {
					t.Fatalf("结果行 %.1f 应比卡 %.1f 矮", end, startH)
				}
				// 贴底：看最底下的 a8；不贴底：看上方的 a2 不动、下方的 b0 往上走
				watch := "a8"
				if !follow {
					watch = "b0"
				}
				ask := r.st // 同一个列表状态
				_ = ask
				ap := r.msgs[len(r.msgs)-1]
				if !follow {
					ap = r.msgs[9]
				}
				ap.Steps[0].Approval.State, ap.Steps[0].State = ApprovalDenied, StepDenied
				top := r.y("a0")
				prev := r.y(watch)
				var hs []float32
				each := func(int) {
					hs = append(hs, bubbleH(r.tt, "ask"))
					y := r.y(watch)
					if follow {
						// 贴底：末尾对齐，行被往下带；不许往回走
						if y < prev-0.01 {
							t.Fatalf("%s 往回走了：%.2f → %.2f", watch, prev, y)
						}
					} else {
						if y > prev+0.01 {
							t.Fatalf("%s 应只往上走：%.2f → %.2f", watch, prev, y)
						}
						if a := r.y("a0"); !near(a, top) {
							t.Fatalf("上方的行动了：%.2f → %.2f", top, a)
						}
					}
					prev = y
				}
				r.tt.Frame()
				each(0)
				settle(t, r.tt, each)
				if !near(hs[len(hs)-1], end) {
					t.Errorf("终点高 %.2f，应等于 %.2f", hs[len(hs)-1], end)
				}
				for i := 1; i < len(hs); i++ {
					if hs[i] > hs[i-1]+0.01 || hs[i] < end-0.01 {
						t.Fatalf("第 %d 帧高度不单调或过冲：%v", i, hs)
					}
				}
			})
		}
	}
}
