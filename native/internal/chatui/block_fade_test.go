package chatui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 整块出现的表格、代码块（规格-过渡动画 铁律 3）：流式输出中冒出来时只淡入一次，不位移、不动高度；
// 历史消息、滚出去又回来的行不播（规格 C1 的“已播过”）。

const (
	blockText  = "先说一句话。"
	codeAdd    = "\n\n```go\nfmt.Println(1)\nfmt.Println(2)\n```"
	tableAdd   = "\n\n| 名称 | 数量 |\n|---|---|\n| 纸灯 | 3 |\n| 笔记 | 12 |"
	mdTestFont = ""
)

// pixelAt 取窗口里 (x, y)（逻辑像素）那一点的颜色，各通道一个数。
func pixelAt(tt *ui.Tester, x, y float32) [3]int {
	img := tt.Image()
	i := (int(y)*img.Stride + int(x)*4)
	return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
}

func pixDist(a, b [3]int) int {
	d := 0
	for i := range a {
		if v := a[i] - b[i]; v < 0 {
			d -= v
		} else {
			d += v
		}
	}
	return d
}

type blockRig struct {
	tt   *ui.Tester
	m    *Msg
	show bool
	h    Host
}

func newBlockRig(t *testing.T, reduce bool, text string, state State) *blockRig {
	t.Helper()
	r := &blockRig{show: true, h: Host{T: Derive(DefaultSettings()), Column: 620, NoEnter: true, SelfChar: CharColors[0], MD: NewMD(), Mono: mdTestFont}}
	r.m = &Msg{ID: "m", Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 23, 0, 0, 0, time.Local), Text: text, Markdown: true, State: state}
	r.tt = ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.RGB(255, 255, 255))
		ui.Column(c.Key("list")).Width(r.h.Column).Children(func() {
			if r.show {
				r.h.Message(c, r.m, nil, nil)
			}
		})
	}, 700, 700)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

// probe 是这一帧里那一块（key）的框、整行的框和块里一个取样点的颜色。
func (r *blockRig) probe(t *testing.T, key string, dx, dy float32) (box, row ui.Rect, pix [3]int) {
	t.Helper()
	n := tree(r.tt).find("m").find(key)
	if n == nil {
		t.Fatalf("找不到 %q", key)
	}
	rn := tree(r.tt).find("m")
	x, y := n.Box.X+dx, n.Box.Y+dy
	if dx < 0 {
		x = n.Box.X + n.Box.W + dx
	}
	if dy < 0 {
		y = n.Box.Y + n.Box.H + dy
	}
	return n.Box, rn.Box, pixelAt(r.tt, x, y)
}

// 只淡入：块的框和整行的框在淡入的每一帧一动不动（块出现那一帧高度一次到位，之后不再变）；取样点的颜色从气泡底逐步变到终点，单调；
// 放完不再要帧。减少动态效果下各测一次（淡入还在）。
func TestBlocksFadeInOnlyWhenTheyAppear(t *testing.T) {
	cases := []struct {
		name, add, key string
		dx, dy         float32 // 取样点：块框内；负数从右 / 下边量
	}{
		{"代码块", codeAdd, "code", 4, -3},
		{"表格", tableAdd, "table", -4, 4},
	}
	for _, tc := range cases {
		for _, reduce := range []bool{false, true} {
			t.Run(tc.name+"/"+reduceName(reduce), func(t *testing.T) {
				r := newBlockRig(t, reduce, blockText, Streaming)
				settle(t, r.tt, nil)
				r.m.Text = blockText + tc.add
				// 块出现的那一帧：markdown 自己的逐字出现要先走到块那里（减少动态时一次出完），找到的第一帧就是它
				for i := 0; tree(r.tt).find("m").find(tc.key) == nil; i++ {
					if i > 200 {
						t.Fatal("块一直没出现")
					}
					r.tt.Advance(16 * time.Millisecond)
					r.tt.Frame()
				}
				box0, row0, p0 := r.probe(t, tc.key, tc.dx, tc.dy)
				var pix [][3]int
				pix = append(pix, p0)
				settle(t, r.tt, func(int) {
					box, row, p := r.probe(t, tc.key, tc.dx, tc.dy)
					// 淡入自己不位移：块和整行的左上角一动不动。流式正常速度下 markdown 一行行把块里的内容放出来，块会随内容变高（那是出字，不是淡入）：
					// 只许单调变高；减少动态效果时整块一次出完，高度也一动不动
					if box.X != box0.X || box.Y != box0.Y || row.X != row0.X || row.Y != row0.Y {
						t.Fatalf("淡入期间位置动了：块 %v → %v，整行 %v → %v", box0, box, row0, row)
					}
					if reduce && (box != box0 || row != row0) {
						t.Fatalf("减少动态：整块一次出完，淡入期间框不该变：块 %v → %v，整行 %v → %v", box0, box, row0, row)
					}
					if box.H < box0.H-0.01 || row.H < row0.H-0.01 {
						t.Fatalf("块变矮了：块 %v → %v，整行 %v → %v", box0, box, row0, row)
					}
					box0, row0 = box, row
					pix = append(pix, p)
				})
				end := pix[len(pix)-1]
				if pixDist(p0, end) < 20 {
					t.Fatalf("出现那一帧应接近透明（和终点差得远）：%v → %v", p0, end)
				}
				mid := 0
				for i := 1; i < len(pix); i++ {
					if pixDist(pix[i], end) > pixDist(pix[i-1], end) {
						t.Fatalf("第 %d 帧颜色离终点更远了：%v → %v", i, pix[i-1], pix[i])
					}
					if d := pixDist(pix[i], end); d > 0 && d < pixDist(p0, end) {
						mid++
					}
				}
				if mid < 2 {
					t.Errorf("淡入中途只有 %d 帧：%v", mid, pix)
				}
			})
		}
	}
}

// 历史消息第一次画就带着块：直接是终点，不淡入；滚出去又回来（行摘掉再画）也不播。
func TestBlocksDoNotFadeForHistoryOrRemount(t *testing.T) {
	for _, add := range []struct{ name, text, key string }{{"代码块", codeAdd, "code"}, {"表格", tableAdd, "table"}} {
		t.Run(add.name, func(t *testing.T) {
			r := newBlockRig(t, false, blockText+add.text, Sent)
			if r.tt.FramePending() {
				settle(t, r.tt, nil) // 字的淡入（markdown 自己的）放完再比
			}
			_, _, end := r.probe(t, add.key, 4, -3)
			if add.key == "table" {
				_, _, end = r.probe(t, add.key, -4, 4)
			}
			// 重新挂上
			r.show = false
			r.tt.Frame()
			r.show = true
			r.tt.Frame()
			var p [3]int
			if add.key == "table" {
				_, _, p = r.probe(t, add.key, -4, 4)
			} else {
				_, _, p = r.probe(t, add.key, 4, -3)
			}
			if pixDist(p, end) != 0 {
				t.Errorf("重新挂上的块不该再淡入：%v → %v", end, p)
			}
		})
	}
	// 流式里先在场的块：中途换宽度、重画也不会再播
	r := newBlockRig(t, false, blockText+codeAdd, Streaming)
	settle(t, r.tt, nil)
	_, _, end := r.probe(t, "code", 4, -3)
	r.m.Text += "\n\n后面接着写了几个字。"
	r.tt.Frame()
	_, _, p := r.probe(t, "code", 4, -3)
	if pixDist(p, end) != 0 {
		t.Errorf("已经在场的块在后面接着出字时不该再淡入：%v → %v", end, p)
	}
	_ = fmt.Sprint
}
