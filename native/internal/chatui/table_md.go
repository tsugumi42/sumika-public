package chatui

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// Markdown 表格（主人 2026-11-15："不会自动排满窗口，也不考虑每个元素的宽度，风格也不统一"）。
// mygo 自带的表格每列最宽 24em、每格四边画框、外圈圆角写死，没有可调的口子，所以走 Options.Block 自己排，
// 格子里的字仍交给 markdown 画（链接、行内代码、加粗照常，选区和复制跟外面那一份）。
//
// 排法：
//   - 每列先量各格单行排下去的自然宽（表头按半粗字量）+ 左右内距；
//   - 全部放得下就按自然宽排，表比气泡窄就是窄的，不硬撑满；
//   - 放不下：从最窄的列起，每列取"自然宽"和"剩下的宽按剩下几列均分"里小的那个——短列（数字、单位、状态）保持紧凑，
//     长文字的列拿到剩余宽并换行；每列不窄于 minTableCol 个正文字宽（自然宽更小的不受限）；
//   - 各列都到了最窄还放不下（列很多）：表自己横向滚动，不撑破气泡。
//   - 数字列右对齐（Markdown 里写了对齐标记就照标记）。
//
// 风格和气泡里别的块一样：外圈圆角同代码块（同心），表头一行用凹块底色加半粗字，行与行之间一条细线，格子之间不画线。

const (
	minTableCol = 6 // 每列最窄几个正文字宽（不含内距）

	// 表头底色是凹块灰里混进角色色的这么一点（主人 2026-11-15："在灰色基础上偏主题色一点点"）：浅深色都看得出和表身分开，又不抢眼。
	tableHeadTint float32 = .14
	// 表头和第一行之间的分隔线粗，比行间的细线（1）粗一档。
	tableHeadRule float32 = 2
)

// tableHeadFill 是表头底色：气泡底上盖一层凹块的悬停灰（原来表头的底，半透明所以先合成成不透明的），再混进 tableHeadTint 的角色色。
func (t Tokens) tableHeadFill(acc Accent) ui.Color {
	neutral := t.C.Bubble.Mix(t.C.Hover, float32(t.C.Hover.A)/255)
	return neutral.Mix(acc.Base, tableHeadTint)
}

// numericCell 认"数字格"：可带正负号、币种、千分位、小数，后面可跟百分号或一个短单位。
var numericCell = regexp.MustCompile(`^[+\-−~≈]?[¥$€£]?[0-9][0-9,，.]*` + `\s*(%|‰|[A-Za-zµ/]{1,6}|[万亿元秒分个条次天时])?$`)

// hasTable 粗看一段 Markdown 里可能有没有表格（有竖线又有 -- ），决定要不要接管表格块；认错只会多包一层，不会画错。
func hasTable(src string) bool { return strings.Contains(src, "|") && strings.Contains(src, "--") }

// tableMeasure 是一张表各列量出来的结果，按表的源码缓存。
type tableMeasure struct {
	natural []float32 // 每列自然宽，含左右内距
	numeric []bool    // 每列是不是数字列
}

// tableBlock 是 markdown.Options.Block：只接管表格块。
func (h Host) tableBlock(e *mdEntry) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		if d.Block(i).Kind != parser.Table {
			return false
		}
		h.table(c, e, d, i)
		return true
	}
}

func (h Host) tablePad() (x, y float32) { return h.T.S3, h.T.S1 }

// measureTable 量各列的自然宽、判断数字列；同一份源码、同一套字体字号只量一次。
func (h Host) measureTable(e *mdEntry, d *parser.Document, i int) tableMeasure {
	t := h.T
	b := d.Block(i)
	key := fmt.Sprintf("%v|%v|%v|%v|%s", t.Body, h.Font, h.Mono, t.Weight[2], d.Source()[b.Start:b.End])
	if m, ok := e.tables[key]; ok {
		return m
	}
	cols := len(b.Align)
	padX, _ := h.tablePad()
	m := tableMeasure{natural: make([]float32, cols), numeric: make([]bool, cols)}
	seen := make([]bool, cols) // 这一列有没有出现过非空的表身格
	for k := range m.numeric {
		m.numeric[k] = true
	}
	font := ui.Font{Family: h.Font, Size: t.Body, LineHeight: t.BodyLeading()}
	for r := i + 1; r < b.Next; r = d.Block(r).Next {
		row := d.Block(r)
		k := 0
		for cell := r + 1; cell < row.Next; cell = d.Block(cell).Next {
			if k >= cols {
				break
			}
			in := d.Inlines(cell)
			spans := h.mdSpans(in, func(string) int { return 0 })
			if row.Header {
				for s := range spans {
					if spans[s].Weight == 0 {
						spans[s].Weight = t.Weight[2]
					}
				}
			}
			w := float32(0)
			if len(spans) > 0 {
				for _, ln := range ui.ShapeRichText(spans, font, 1e5).Lines() {
					w = max(w, ln.Rect.W)
				}
			}
			m.natural[k] = max(m.natural[k], float32(math.Ceil(float64(w)))+2*padX+1)
			if !row.Header {
				txt := strings.TrimSpace(parser.PlainText(in))
				switch txt {
				case "", "-", "—", "–":
				default:
					seen[k] = true
					if !numericCell.MatchString(txt) {
						m.numeric[k] = false
					}
				}
			}
			k++
		}
	}
	for k := range m.numeric {
		m.numeric[k] = m.numeric[k] && seen[k]
	}
	if len(e.tables) >= 8 {
		clear(e.tables) // 流式时源码每帧都变，只留最近几份
	}
	if e.tables == nil {
		e.tables = map[string]tableMeasure{}
	}
	e.tables[key] = m
	return m
}

// tableWidths 把各列的自然宽排进 avail：见本文件开头的"排法"。floor 是每列最窄。
func tableWidths(natural []float32, avail, floor float32) []float32 {
	order := make([]int, len(natural))
	for k := range order {
		order[k] = k
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case natural[a] < natural[b]:
			return -1
		case natural[a] > natural[b]:
			return 1
		}
		return 0
	})
	out := make([]float32, len(natural))
	left := avail
	for n, k := range order {
		share := left / float32(len(order)-n)
		w := min(natural[k], max(share, min(natural[k], floor)))
		out[k] = float32(math.Floor(float64(w)))
		if natural[k] <= share { // 放得下就是自然宽（不向下取整，免得字被挤得折行）
			out[k] = natural[k]
		}
		left -= out[k]
	}
	return out
}

// table 画一张表。
func (h Host) table(c *ui.Context, e *mdEntry, d *parser.Document, i int) {
	t := h.T
	b := d.Block(i)
	cols := len(b.Align)
	if cols == 0 {
		return
	}
	m := h.measureTable(e, d, i)
	natural := slices.Clone(m.natural)
	// 流式时列宽只增不减，列不随格子慢慢写满而来回跳（和 mygo 自带的一样）
	if prev := e.tablePrev[b.Start]; b.Open && len(prev) == cols {
		for k := range natural {
			natural[k] = max(natural[k], prev[k])
		}
	}
	if e.tablePrev == nil {
		e.tablePrev = map[int][]float32{}
	}
	e.tablePrev[b.Start] = natural
	padX, padY := h.tablePad()
	avail := e.width
	if avail <= 0 {
		avail = 40 * t.Body
	}
	const border = 1
	widths := tableWidths(natural, avail-2*border, minTableCol*t.Body+2*padX)
	var total float32
	for _, w := range widths {
		total += w
	}
	frameW := min(total+2*border, avail)
	r := max(t.Inner(t.R1, t.BubblePadH), t.RInnerMin) // 同代码块：气泡圆角 − 横内边距
	// 选项：格子里的字交给 markdown 画（不再接管表格，不另开选区）
	o := &e.cell
	o.Theme, o.Extensions, o.OnLink, o.ShareSelection = e.opts.Theme, chatMarkdown, h.OnLink, true
	e.cellReveal = markdown.Reveal{Unit: markdown.AtOnce}
	o.Reveal = &e.cellReveal
	frame := ui.Column(c.Key("table")).Label("表格").Role(ui.RoleTable).Width(frameW).Shrink(0).Radius(r).Border(border, t.C.Line).Clip()
	e.fadeIn(t, frame, "table", i)
	frame.Children(func() {
		scroll := ui.ScrollHorizontal(c.Key("table-scroll")).FillWidth()
		scroll.Children(func() {
			grid := ui.Column(c.Key("grid")).Width(total).Shrink(0)
			grid.Children(func() {
				n := 0
				for r := i + 1; r < b.Next; r = d.Block(r).Next {
					row := d.Block(r)
					rowEl := ui.Row(c.Key(fmt.Sprintf("row-%d", n))).Role(ui.RoleRow).AlignItems(ui.Stretch).FillWidth()
					if row.Header {
						rowEl.Background(t.tableHeadFill(t.AccentOf(h.SelfChar)))
					}
					switch {
					case n == 1: // 表头下面：粗一点的深线
						rowEl.BorderWidth(tableHeadRule, 0, 0, 0).BorderColor(t.C.LineStrong)
					case n > 1:
						rowEl.BorderWidth(border, 0, 0, 0).BorderColor(t.C.Line)
					}
					rowEl.Children(func() {
						k := 0
						for cell := r + 1; cell < row.Next; cell = d.Block(cell).Next {
							if k >= cols {
								break
							}
							role := ui.RoleCell
							if row.Header {
								role = ui.RoleColumnHeader
							}
							box := ui.Column(c.Key(fmt.Sprintf("cell-%d", k))).Width(widths[k]).Shrink(0).Padding(padY, padX).Role(role)
							switch {
							case b.Align[k] == parser.AlignCenter:
								box.AlignItems(ui.Center)
							case b.Align[k] == parser.AlignRight || (b.Align[k] == parser.AlignNone && m.numeric[k]):
								box.AlignItems(ui.End)
							}
							if row.Header {
								box.FontWeight(t.Weight[2])
							}
							src := d.Source()[d.Block(cell).Start:d.Block(cell).End]
							box.Children(func() {
								if strings.TrimSpace(src) != "" {
									markdown.View(c.Key(fmt.Sprintf("md-%d-%d", n, k)), src, o)
								} else {
									ui.Box(c.Key("empty")).Height(t.BodyLine)
								}
							})
							k++
						}
					})
					n++
				}
			})
		})
	})
}
