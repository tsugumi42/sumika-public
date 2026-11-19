package chatui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 2026-11-15 Markdown 表格重排（table_md.go）：宽度按内容、列宽按内容分、数字列右对齐、放不下横向滚动、风格同别的块。

func TestTableWidths(t *testing.T) {
	for _, c := range []struct {
		name         string
		natural      []float32
		avail, floor float32
		want         []float32
	}{
		{"放得下：全是自然宽", []float32{60, 100, 200}, 500, 50, []float32{60, 100, 200}},
		{"短列紧凑，长列拿剩下的", []float32{60, 100, 800}, 400, 50, []float32{60, 100, 240}},
		{"两个长列均分剩下的", []float32{60, 600, 600}, 400, 50, []float32{60, 170, 170}},
		{"各列都到最窄还放不下：不再压（交给横向滚动）", []float32{120, 130, 140}, 200, 120, []float32{120, 120, 120}},
		{"最窄不超过自然宽", []float32{30, 800}, 100, 120, []float32{30, 120}},
	} {
		got := tableWidths(c.natural, c.avail, c.floor)
		for k := range got {
			if d := got[k] - c.want[k]; d > 1 || d < -1 {
				t.Errorf("%s：%v，期望 %v", c.name, got, c.want)
				break
			}
		}
	}
}

func tableMsg(src string) *Msg {
	return &Msg{ID: "m", Sender: "r1", Name: "晴枝", Char: CharColors[0], Text: src, Markdown: true}
}

type tableShot struct {
	tt                  *ui.Tester
	frame, grid, bubble *node
	avail               float32
}

func renderTable(t *testing.T, src string, column float32) tableShot {
	t.Helper()
	h := Host{T: Derive(DefaultSettings()), Column: column, NoEnter: true, MD: NewMD(), SelfChar: CharColors[0]}
	m := tableMsg(src)
	var lay layout
	tt := ui.NewTester(func(c *ui.Context) {
		lay = h.measure(c, m, nil, nil)
		ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
	}, int(column)+40, 1200)
	tt.Frame()
	tt.Frame()
	root := tree(tt)
	s := tableShot{tt: tt, frame: root.find("table"), grid: root.find("grid"), bubble: root.find("m").find("bubble"), avail: lay.ContentW}
	if s.frame == nil || s.grid == nil || s.bubble == nil {
		t.Fatalf("没画出表格：%v", tt.Texts())
	}
	return s
}

const (
	shortTable = "| 项目 | 状态 |\n|---|---|\n| 纸灯笔记 | 已补完 |\n| 窗边那段 | 进行中 |\n"
	longTable  = "| 文件 | 数量 | 说明 |\n|:--|--:|---|\n| `notes/a.md` | 12 | 把昨天说到一半的窗边那段补完整，再把灯罩换成和纸的那条也记进去，免得下次又忘了从哪里接着写 |\n| `todo.md` | 128 | **全部**重新排过一遍 |\n"
	wideTable  = "| 日期 | 早 | 午 | 晚 | 睡前 | 步数 | 心率 | 体重 | 睡眠 | 备注 |\n|---|--:|--:|--:|--:|--:|--:|--:|--:|---|\n| 11-10 | 6:40 | 12:10 | 19:30 | 23:15 | 8230 | 72 | 58.4 | 7.5 | 晴 |\n| 11-11 | 6:55 | 12:30 | 19:10 | 23:40 | 10412 | 70 | 58.2 | 6.8 | 下雨 |\n"
)

// 内容窄时按内容宽，不硬撑满气泡；框和里面的表差一圈边。
func TestTableShortKeepsContentWidth(t *testing.T) {
	s := renderTable(t, shortTable, 720)
	if s.frame.Box.W > s.avail/2 {
		t.Errorf("短表框宽 %.1f 撑得太宽（可用 %.1f）", s.frame.Box.W, s.avail)
	}
	if !near(s.frame.Box.W, s.grid.Box.W+2) {
		t.Errorf("框宽 %.1f 应是表宽 %.1f 加两边的 1 像素边", s.frame.Box.W, s.grid.Box.W)
	}
}

// 内容多时最宽到气泡可用宽、不撑破气泡；长文字列拿剩余宽并换行（它那行比别行高），短列（数字）保持紧凑、右对齐。
func TestTableLongFillsAvailAndWraps(t *testing.T) {
	s := renderTable(t, longTable, 720)
	if !near(s.frame.Box.W, s.avail) {
		t.Errorf("长表框宽 %.1f，应排满可用宽 %.1f", s.frame.Box.W, s.avail)
	}
	if s.frame.Box.X+s.frame.Box.W > s.bubble.Box.X+s.bubble.Box.W {
		t.Error("表格越出了气泡")
	}
	if !near(s.grid.Box.W, s.frame.Box.W-2) {
		t.Errorf("放得下时不该滚动：表宽 %.1f，框内宽 %.1f", s.grid.Box.W, s.frame.Box.W-2)
	}
	row1, row2 := s.grid.find("row-1"), s.grid.find("row-2")
	if row1.Box.H <= row2.Box.H+1 {
		t.Errorf("说明列该换行：第一行高 %.1f、第二行高 %.1f", row1.Box.H, row2.Box.H)
	}
	num, text := row1.find("cell-1"), row1.find("cell-2")
	if num.Box.W > text.Box.W/2 {
		t.Errorf("数字列 %.1f 该比说明列 %.1f 紧凑得多", num.Box.W, text.Box.W)
	}
	// 数字列右对齐：格里内容的右边贴着格右边的内距
	padX, _ := Host{T: Derive(DefaultSettings())}.tablePad()
	md := num.find("md-1-1")
	if md == nil || !near(md.Box.X+md.Box.W, num.Box.X+num.Box.W-padX) {
		t.Errorf("数字列没有右对齐：%+v 格 %+v", md, num.Box)
	}
	// 表头一行：凹块底色那档（读不到背景，查它是表的第一行）和半粗字在 shot 里目测；这里查格子之间没有竖线：格子紧挨着
	c0, c1 := row1.find("cell-0"), row1.find("cell-1")
	if !near(c0.Box.X+c0.Box.W, c1.Box.X) {
		t.Errorf("列之间不该有边：%.1f 到 %.1f", c0.Box.X+c0.Box.W, c1.Box.X)
	}
}

// 圆角和代码块同一档（气泡圆角 − 横内边距，同心）。
func TestTableRadiusMatchesCodeBlock(t *testing.T) {
	tk := Derive(DefaultSettings())
	s := renderTable(t, shortTable, 720)
	want := max(tk.Inner(tk.R1, tk.BubblePadH), tk.RInnerMin)
	for _, r := range s.frame.Radius {
		if !near(r, want) {
			t.Fatalf("表格圆角 %v，应是 %.1f", s.frame.Radius, want)
		}
	}
}

// 列很多：表自己横向滚动，框不超过可用宽，气泡不被撑宽；每列不窄于最窄。
func TestTableManyColumnsScrolls(t *testing.T) {
	s := renderTable(t, wideTable, 520)
	if s.frame.Box.W > s.avail+0.5 {
		t.Errorf("框宽 %.1f 超过可用宽 %.1f", s.frame.Box.W, s.avail)
	}
	if s.grid.Box.W <= s.frame.Box.W {
		t.Errorf("列多时表 %.1f 该比框 %.1f 宽、在框里滚动", s.grid.Box.W, s.frame.Box.W)
	}
	if s.bubble.Box.W > Derive(DefaultSettings()).BubbleShare*520+1 {
		t.Errorf("气泡被表格撑宽了：%.1f", s.bubble.Box.W)
	}
}

// 对齐标记：:-- 左、--: 右、:-: 居中；没写、全是数字的列右对齐，文字列左对齐。
func TestTableAlignMarks(t *testing.T) {
	src := "| 左 | 中 | 右 | 数 | 字 |\n|:--|:-:|--:|---|---|\n| aaa | bbb | ccc | 1,200 | 文字 |\n| a | b | c | 35% | 更长的文字 |\n"
	s := renderTable(t, src, 720)
	row := s.grid.find("row-1")
	padX, _ := Host{T: Derive(DefaultSettings())}.tablePad()
	at := func(k int) (l, r float32) {
		cell := row.find(fmt.Sprintf("cell-%d", k))
		md := cell.find(fmt.Sprintf("md-1-%d", k))
		return md.Box.X - cell.Box.X, cell.Box.X + cell.Box.W - (md.Box.X + md.Box.W)
	}
	if l, _ := at(0); !near(l, padX) {
		t.Errorf("左对齐列：内容左边距 %.1f，应是 %.1f", l, padX)
	}
	if l, r := at(1); !near(l, r) {
		t.Errorf("居中列：左 %.1f 右 %.1f 应相等", l, r)
	}
	if _, r := at(2); !near(r, padX) {
		t.Errorf("右对齐列：内容右边距 %.1f，应是 %.1f", r, padX)
	}
	if _, r := at(3); !near(r, padX) {
		t.Errorf("全是数字的列没写标记也该右对齐：右边距 %.1f", r)
	}
	if l, _ := at(4); !near(l, padX) {
		t.Errorf("文字列该左对齐：左边距 %.1f", l)
	}
}

// 数字格的判定：带符号、千分位、小数、百分号、短单位算数字；一般文字、日期不算。
func TestNumericCell(t *testing.T) {
	for s, want := range map[string]bool{"12": true, "-3.5": true, "1,200": true, "35%": true, "128ms": true, "¥99.5": true, "+2": true, "12万": true, "6:40": false, "11-10": false, "已补完": false, "v1.2.3 beta": false, "": false} {
		if got := numericCell.MatchString(s); got != want {
			t.Errorf("%q 数字格判定 %v，期望 %v", s, got, want)
		}
	}
}

// 流式时列宽只增不减：后来的文字更短（或表被改写）时，列宽不往回缩。
func TestTableStreamingWidthsOnlyGrow(t *testing.T) {
	long := "| 名称 | 说明 |" + "\n" + "|---|---|" + "\n" + "| a | 一句稍微长一点的说明 |" + "\n"
	short := "| 名称 | 说明 |" + "\n" + "|---|---|" + "\n" + "| a | 短 |" + "\n"
	h := Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, MD: NewMD()}
	m := tableMsg(long)
	m.State = Streaming
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
	}, 800, 600)
	width := func() float32 {
		tt.Frame()
		tt.Advance(2 * time.Second)
		g := tree(tt).find("grid")
		if g == nil {
			t.Fatalf("没画出表格：%v", tt.Texts())
		}
		return g.Box.W
	}
	w1 := width()
	m.Text = short
	w2 := width()
	if w2 < w1-0.5 {
		t.Errorf("流式时列宽往回缩了：%.1f -> %.1f", w1, w2)
	}
}

// 表头底色是凹块灰里混进一点角色色；表头和第一行之间是 2 像素的深线，行间仍是 1 像素的细线（主人 2026-11-15）。
func TestTableHeadTintAndRule(t *testing.T) {
	for _, dark := range []bool{false, true} {
		st := DefaultSettings()
		st.Dark = dark
		tk := Derive(st)
		h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD(), SelfChar: CharColors[0]}
		m := tableMsg(longTable + "| `x` | 1 | 第三行 |\n")
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
		}, 760, 1200)
		tt.Frame()
		tt.Frame()
		img := tt.Image()
		grid := tree(tt).find("grid")
		row0, row1, row2 := grid.find("row-0"), grid.find("row-1"), grid.find("row-2")
		px := func(y float32) ui.Color {
			x := int(row0.Box.X) + 2 // 格子左内距里，没有字
			c := img.RGBAAt(x, int(y))
			return ui.RGB(c.R, c.G, c.B)
		}
		near3 := func(a, b ui.Color) bool {
			d := func(x, y uint8) int { return int(x) - int(y) }
			return max(d(a.R, b.R), -d(a.R, b.R)) <= 2 && max(d(a.G, b.G), -d(a.G, b.G)) <= 2 && max(d(a.B, b.B), -d(a.B, b.B)) <= 2
		}
		want := tk.tableHeadFill(tk.AccentOf(h.SelfChar))
		plain := tk.C.Bubble.Mix(tk.C.Hover, float32(tk.C.Hover.A)/255) // 没混角色色时的灰
		if got := px(row0.Box.Y + row0.Box.H/2); !near3(got, want) {
			t.Errorf("深=%v 表头底色 %v，应是凹块灰混角色色 %v", dark, got, want)
		}
		if near3(want, plain) {
			t.Errorf("深=%v 表头底色和纯中性的 %v 看不出差别", dark, plain)
		}
		// 在表头下面和行间各数一数：该色的像素有几行（线画在行的上沿，位置按像素取整，所以数个数不数位置）
		count := func(y0 float32, want ui.Color) int {
			n := 0
			for y := int(y0) - 2; y <= int(y0)+4; y++ {
				if near3(px(float32(y)), want) {
					n++
				}
			}
			return n
		}
		if n := count(row1.Box.Y, tk.C.LineStrong); n != int(tableHeadRule) {
			t.Errorf("深=%v 表头下的线有 %d 像素厚，应是 %v（LineStrong）", dark, n, tableHeadRule)
		}
		if n := count(row2.Box.Y, tk.C.Line); n != 1 {
			t.Errorf("深=%v 行间的线有 %d 像素厚，应是 1（Line）", dark, n)
		}
	}
}
