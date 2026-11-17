package chatui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 自动召回行（主人 2026-11-09："工具调用能不能补上 memory 给它提供的具体信息，自动召回和主动应该分开"）：
// 这一轮真正注入给模型的记忆，每条写正文、相关度分数、类型来源和记录时间，方便看召回得准不准。
// 它不是工具调用：图标是书签（工具是扳手），摘要里不算“调用了”。模型主动查记忆（memory_search）仍是工具卡，展开看返回。

// RecallItem 是注入的一条记忆。分数没有的是 0，不写；宿主按后台的明细原样转过来，不另算。
type RecallItem struct {
	Text, Kind, Source string
	At                 int64 // 记下的时间，Unix 毫秒
	Pinned, Hearsay    bool  // 固定要点；待确认
	Score, Rerank, Sim float64
	Lexical, Vector    int           // 词法、向量那一路的名次，0 没中
	Previous           []RecallPrior // 被这条取代的旧版本，新的在前（模型读到的“取代了旧记录”）
}

// RecallPrior 是一条被取代的旧记录。
type RecallPrior struct {
	Text string
	At   int64 // 记下的时间，Unix 毫秒
}

// recallPriorLine 是记忆正文下面的“取代了”小字，没有旧版本返回空。
func recallPriorLine(it RecallItem) string {
	parts := make([]string, 0, len(it.Previous))
	for _, p := range it.Previous {
		parts = append(parts, recallTime(p.At)+" "+p.Text)
	}
	if len(parts) == 0 {
		return ""
	}
	return "取代了：" + strings.Join(parts, "；")
}

// 单条正文超过这么多字先折起来；整块最高这么多行，再多在里面滚。
const (
	recallClipRunes = 120
	recallMaxLines  = 16
)

var recallKindName = map[string]string{"fact": "事实", "promise": "约定", "preference": "偏好", "boundary": "边界", "episode": "片段"}

// recallMeta 是一条记忆下面那行小字：类型、待确认、记录时间、来源、各路分数。只写真有的。
func recallMeta(it RecallItem) string {
	var p []string
	switch {
	case it.Pinned:
		p = append(p, "固定要点")
	case recallKindName[it.Kind] != "":
		p = append(p, recallKindName[it.Kind])
	case it.Kind != "":
		p = append(p, it.Kind)
	}
	if it.Hearsay {
		p = append(p, "待确认")
	}
	if it.At > 0 {
		p = append(p, recallTime(it.At))
	}
	src := strings.TrimSpace(it.Source)
	if src == "" {
		src = "来源未核实"
	}
	if r := []rune(src); len(r) > 40 {
		src = string(r[:40]) + "…"
	}
	p = append(p, src)
	if it.Rerank != 0 {
		p = append(p, fmt.Sprintf("重排 %.2f", it.Rerank))
	}
	if it.Vector > 0 {
		p = append(p, fmt.Sprintf("向量 %.2f 第 %d 名", it.Sim, it.Vector))
	}
	if it.Lexical > 0 {
		p = append(p, fmt.Sprintf("词法第 %d 名", it.Lexical))
	}
	if it.Score != 0 {
		p = append(p, fmt.Sprintf("综合 %.3f", it.Score))
	}
	return strings.Join(p, " · ")
}

func recallTime(ms int64) string {
	t := time.UnixMilli(ms).Local()
	if t.Year() == time.Now().Year() {
		return t.Format("1月2日 15:04")
	}
	return t.Format("2006年1月2日 15:04")
}

// recallCard 是自动召回行：头部"召回了 N 条记忆"，点开是每条的明细（最高 16 行、能滚；单条太长先折，点"展开"看全文）。
// 做法和工具卡同一族（头部、凹块、圆角都走 card、block），没有内容就不显示这一行（调用方不会给空的）。
func (h Host) recallCard(c *ui.Context, s *Step, o *bool, char OKLCH) {
	t := h.T
	h.card(c, "recall-"+s.ID, s, o, func() { h.cardIcon(c, "bookmark", char) }, func() {
		h.rowText(c, ui.Span{Text: fmt.Sprintf("召回了 %d 条记忆", len(s.Recall)), Features: Tabular}).Grow(1).MinWidth(0)
	}, func(add func(ui.Element) ui.Element) {
		line := h.rowLine()
		ui.Scroll(c.Key("recall-scroll")).FillWidth().MaxHeight(recallMaxLines * line).Children(func() {
			ui.Column(c.Key("recall-list")).FillWidth().Gap(t.BubblePadV).Children(func() {
				for i, it := range s.Recall {
					h.recallItem(c, s, i, it)
				}
			})
		})
	})
}

func (h Host) recallItem(c *ui.Context, s *Step, i int, it RecallItem) {
	t := h.T
	line := h.rowLine()
	text, long := it.Text, len([]rune(it.Text)) > recallClipRunes
	var all *bool
	if long && h.StepOpen != nil {
		all = h.StepOpen(fmt.Sprintf("%s#记忆%d全文", s.ID, i))
	}
	if long && (all == nil || !*all) {
		text = string([]rune(text)[:recallClipRunes]) + "…"
	}
	h.block(c, fmt.Sprintf("recall-item-%d", i)).Label("召回的记忆").Gap(t.S1).Children(func() {
		ui.Text(c.Key("text"), text).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(line).TextColor(t.C.Text).Wrap().Selectable()
		if prior := recallPriorLine(it); prior != "" {
			ui.Text(c.Key("prior"), prior).Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(h.monoLine()).TextColor(t.C.Text3).Wrap().Selectable()
		}
		ui.Text(c.Key("meta"), recallMeta(it)).Font(h.Font).FontSize(t.FS[1]).FontFeatures(Tabular).FixedLineHeight(h.monoLine()).TextColor(t.C.Text3).Wrap()
		if all != nil {
			label := "展开全文"
			if *all {
				label = "收起"
			}
			b := ui.ButtonBase(c.Key("more")).Label(label).Radius(t.RInnerMin).Padding(0, t.S1).OnClick(func() { *all = !*all })
			bg := ui.Transparent
			if b.Hovered() {
				bg = t.C.Hover
			}
			b.Background(t.Fill(b, bg))
			b.Children(func() {
				ui.Text(c.Key("label"), label).Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(h.monoLine()).TextColor(t.AccentOf(h.procChar).Text).SingleLine()
			})
		}
	})
}
